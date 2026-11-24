package openbao

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

// unsealEvery bounds how often the Provider tries to unseal OpenBao: a
// sealed instance is noticed by every Secret's resync, and one attempt
// covers them all (spec 026 plan decision 13).
const unsealEvery = 10 * time.Second

// unsealer unseals OpenBao again after its pod restarts, with the key
// Bootstrap kept at credentialsPath (ADR 0009's custody). Bootstrap
// unseals only at serendipd's start; without this, a restarted OpenBao
// stayed sealed until serendipd restarted too (RP-094).
type unsealer struct {
	credentialsPath string
	now             func() time.Time

	mu       sync.Mutex
	lastTry  time.Time
	warnedAt time.Time // the last failure logged: at most one an hour
}

// warn logs a failure at most once an hour, so a sealed OpenBao that
// can't be unsealed doesn't fill the log every 10 s.
func (u *unsealer) warn(format string, args ...any) {
	if now := u.now(); u.warnedAt.IsZero() || now.Sub(u.warnedAt) >= time.Hour {
		u.warnedAt = now
		log.Printf(format, args...)
	}
}

// WithUnseal lets the Provider unseal OpenBao when a call finds it
// sealed, reading the unseal key from credentialsPath for each attempt
// (it isn't kept in memory). Without it, a sealed OpenBao stays not
// ready until serendipd's next start.
func (p *Provider) WithUnseal(credentialsPath string) *Provider {
	p.unseal = &unsealer{credentialsPath: credentialsPath, now: time.Now}
	return p
}

// recovered reports whether a call that found OpenBao unavailable should
// be tried again: OpenBao is unsealed now, by this attempt or another
// one. It never initializes an uninitialized OpenBao: that means its
// storage was lost, and a silent Init would hide that every value is
// gone.
func (p *Provider) recovered(ctx context.Context) bool {
	u := p.unseal
	if u == nil {
		return false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	h, err := p.client.Health(ctx)
	switch {
	case err != nil:
		return false // no pod, or not answering: nothing to unseal yet
	case !h.Initialized:
		u.warn("openbao: the instance is uninitialized (its storage was lost?); not initializing it again, so Secrets stay unavailable")
		return false
	case !h.Sealed:
		return true // unsealed by another call meanwhile
	}
	now := u.now()
	if !u.lastTry.IsZero() && now.Sub(u.lastTry) < unsealEvery {
		return false
	}
	u.lastTry = now
	creds, err := readCredentials(u.credentialsPath)
	if err != nil {
		u.warn("openbao: sealed, and the stored credentials can't be read: %v", err)
		return false
	}
	sealed, err := p.client.Unseal(ctx, creds.UnsealKey)
	if err != nil || sealed {
		// Still sealed with no error would mean more than one key share,
		// which Bootstrap never makes (1-of-1, ADR 0009).
		u.warn("openbao: sealed, and unsealing with the stored key didn't work (still sealed: %t): %v", sealed, err)
		return false
	}
	u.warnedAt = time.Time{} // a later failure is news again
	log.Print("openbao: was sealed (its pod restarted?); unsealed it with the stored key")
	return true
}

// retried runs call, and once more after unsealing when it found OpenBao
// unavailable and the unseal worked.
func (p *Provider) retried(ctx context.Context, call func() error) error {
	err := call()
	if errors.Is(err, ErrUnavailable) && p.recovered(ctx) {
		err = call()
	}
	return notReady(err)
}
