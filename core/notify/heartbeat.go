package notify

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/serendip-infra/serendip-infra/core/resource"
)

// HeartbeatStale is how recent the evaluator's tick and the delivery
// loop's run must be for a heartbeat to go (decision 15).
const HeartbeatStale = 2 * time.Minute

// Health is what the heartbeat proves: that Serendip can alert. Each
// func is set by serendipd's wiring.
type Health struct {
	// EvaluatorTick is when the evaluator last completed a tick.
	EvaluatorTick func() time.Time
	// DeliveryRun is when the delivery loop last ran.
	DeliveryRun func() time.Time
	// StoreOK reads the store.
	StoreOK func(ctx context.Context) error
}

// Paused reasons, shown on the Heartbeat's Sending condition.
const (
	PausedEvaluator = "EvaluatorStalled"
	PausedDelivery  = "DeliveryStalled"
	PausedStore     = "StoreUnavailable"
)

// paused is why Serendip can't alert now, or "".
func (h Health) paused(ctx context.Context, now time.Time) string {
	switch {
	case h.EvaluatorTick == nil || now.Sub(h.EvaluatorTick()) > HeartbeatStale:
		return PausedEvaluator
	case h.DeliveryRun == nil || now.Sub(h.DeliveryRun()) > HeartbeatStale:
		return PausedDelivery
	case h.StoreOK == nil || h.StoreOK(ctx) != nil:
		return PausedStore
	}
	return ""
}

// HeartbeatResult is one beat's outcome. Paused (with nothing sent) means
// Serendip can't alert, so the outside monitor's own timeout raises the
// alarm: no "fail" signal is ever sent, since sending one would depend on
// the same machine still working.
type HeartbeatResult struct {
	Sent   bool
	Paused string
	Err    *SendError
}

// Beat sends one heartbeat if Serendip can alert. force skips the health
// gate, for a person's test send.
func (c *Client) Beat(ctx context.Context, health Health, spec resource.HeartbeatSpec, secrets SecretReader, force bool) HeartbeatResult {
	if !force {
		if p := health.paused(ctx, c.now()); p != "" {
			return HeartbeatResult{Paused: p}
		}
	}
	host := hostOnly(spec.Host)
	if secrets == nil {
		return HeartbeatResult{Err: &SendError{Host: host, Msg: "no Secrets reader"}}
	}
	raw, err := secrets(ctx, spec.URLSecretID)
	if err != nil || raw == "" {
		return HeartbeatResult{Err: &SendError{Host: host, Retryable: err != nil, Msg: "the Secret " + spec.URLSecretID + " couldn't be read"}}
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Hostname(), host) {
		return HeartbeatResult{Err: &SendError{Host: host, Msg: "the URL's Secret points at a different host than the heartbeat shows"}}
	}
	method := http.MethodGet
	if spec.Method == http.MethodPost {
		method = http.MethodPost
	}
	if _, se := c.do(ctx, method, raw, nil, nil, spec.AllowPrivateDestination != nil, false, raw); se != nil {
		se.Host = host
		return HeartbeatResult{Err: se}
	}
	return HeartbeatResult{Sent: true}
}
