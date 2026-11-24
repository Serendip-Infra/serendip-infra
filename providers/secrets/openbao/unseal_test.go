package openbao

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
)

// fakeBao is a sealed-or-not OpenBao: KV calls answer 503 while sealed,
// /v1/sys/unseal with the right key unseals it.
type fakeBao struct {
	mu          sync.Mutex
	initialized bool
	sealed      bool
	down        bool
	unsealOK    bool // whether an unseal call takes
	unseals     int
	inits       int
}

func (f *fakeBao) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/v1/sys/health":
			json.NewEncoder(w).Encode(HealthStatus{Initialized: f.initialized, Sealed: f.sealed})
		case "/v1/sys/init":
			f.inits++
			w.WriteHeader(http.StatusBadRequest)
		case "/v1/sys/unseal":
			f.unseals++
			var body unsealRequest
			json.NewDecoder(r.Body).Decode(&body)
			if body.Key != "the-unseal-key" {
				t.Errorf("unseal key = %q", body.Key)
			}
			if f.unsealOK {
				f.sealed = false
			}
			json.NewEncoder(w).Encode(unsealResponse{Sealed: f.sealed})
		default:
			if f.sealed || !f.initialized {
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte(`{"errors":["Vault is sealed"]}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": map[string]any{"k": "v"}}})
		}
	}
}

func unsealProvider(t *testing.T, f *fakeBao) (*Provider, *time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "openbao-init.json")
	if err := writeCredentials(path, credentials{RootToken: "root", UnsealKey: "the-unseal-key"}); err != nil {
		t.Fatal(err)
	}
	client, _ := newTestServerClient(t, f.handler(t))
	p := NewProvider(client).WithUnseal(path)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	p.unseal.now = func() time.Time { return now }
	return p, &now
}

// A restarted OpenBao comes back sealed: the call unseals it with the
// stored key and goes through (spec 026 plan decision 13).
func TestUnseal_SealedThenUnsealedAndRetried(t *testing.T) {
	f := &fakeBao{initialized: true, sealed: true, unsealOK: true}
	p, _ := unsealProvider(t, f)
	v, err := p.ReadValue(context.Background(), newTestSecret("p"))
	if err != nil || v["k"] != "v" {
		t.Fatalf("ReadValue = %v, %v", v, err)
	}
	if f.unseals != 1 || f.sealed {
		t.Fatalf("unseals = %d, sealed = %t", f.unseals, f.sealed)
	}
	// Every operation recovers the same way.
	f.sealed = true
	*p.unseal = unsealer{credentialsPath: p.unseal.credentialsPath, now: p.unseal.now}
	if err := p.WriteValue(context.Background(), newTestSecret("p"), map[string]any{"k": "v"}); err != nil {
		t.Fatalf("WriteValue after a reseal = %v", err)
	}
}

// The status read recovers OpenBao too, so /v1/secrets/status is ready
// again without a value call.
func TestUnseal_EncryptionAtRestUnseals(t *testing.T) {
	f := &fakeBao{initialized: true, sealed: true, unsealOK: true}
	p, _ := unsealProvider(t, f)
	if _, err := p.EncryptionAtRest(context.Background()); err != nil {
		t.Fatalf("EncryptionAtRest = %v, want unsealed and answered", err)
	}
	if f.unseals != 1 {
		t.Fatalf("unseals = %d", f.unseals)
	}
}

// Unsealed by another call meanwhile: just retry, no second unseal.
func TestUnseal_AlreadyUnsealedRetriesWithoutUnsealing(t *testing.T) {
	f := &fakeBao{initialized: true, sealed: false}
	p, _ := unsealProvider(t, f)
	if !p.recovered(context.Background()) || f.unseals != 0 {
		t.Fatalf("recovered = false or unseals = %d", f.unseals)
	}
}

// No pod behind the Service: nothing to unseal, still not ready.
func TestUnseal_UnreachableStaysNotReady(t *testing.T) {
	f := &fakeBao{down: true}
	p, _ := unsealProvider(t, f)
	_, err := p.ReadValue(context.Background(), newTestSecret("p"))
	if !errors.Is(err, reconcile.ErrProviderNotReady) || f.unseals != 0 {
		t.Fatalf("err = %v, unseals = %d", err, f.unseals)
	}
}

// Uninitialized means lost storage: never Init, never unseal.
func TestUnseal_UninitializedIsNeverInitialized(t *testing.T) {
	f := &fakeBao{initialized: false, sealed: true, unsealOK: true}
	p, _ := unsealProvider(t, f)
	_, err := p.ReadValue(context.Background(), newTestSecret("p"))
	if !errors.Is(err, reconcile.ErrProviderNotReady) || f.inits != 0 || f.unseals != 0 {
		t.Fatalf("err = %v, inits = %d, unseals = %d", err, f.inits, f.unseals)
	}
	if _, err := p.EncryptionAtRest(context.Background()); !errors.Is(err, reconcile.ErrProviderNotReady) {
		t.Fatalf("EncryptionAtRest = %v", err)
	}
}

// One attempt every 10 s, however many calls find it sealed.
func TestUnseal_RateLimited(t *testing.T) {
	f := &fakeBao{initialized: true, sealed: true, unsealOK: false}
	p, now := unsealProvider(t, f)
	for range 5 {
		if _, err := p.ReadValue(context.Background(), newTestSecret("p")); !errors.Is(err, reconcile.ErrProviderNotReady) {
			t.Fatalf("err = %v", err)
		}
	}
	if f.unseals != 1 {
		t.Fatalf("unseals = %d in the same 10 s, want 1", f.unseals)
	}
	*now = now.Add(unsealEvery)
	f.unsealOK = true
	if _, err := p.ReadValue(context.Background(), newTestSecret("p")); err != nil || f.unseals != 2 {
		t.Fatalf("after 10 s: err = %v, unseals = %d", err, f.unseals)
	}
}

// Without WithUnseal (no credentials file configured), a sealed OpenBao
// stays not ready, as before.
func TestUnseal_NotConfigured(t *testing.T) {
	f := &fakeBao{initialized: true, sealed: true, unsealOK: true}
	client, _ := newTestServerClient(t, f.handler(t))
	_, err := NewProvider(client).ReadValue(context.Background(), newTestSecret("p"))
	if !errors.Is(err, reconcile.ErrProviderNotReady) || f.unseals != 0 {
		t.Fatalf("err = %v, unseals = %d", err, f.unseals)
	}
}

// A missing or unreadable credentials file is logged, and the call stays
// not ready.
func TestUnseal_CredentialsUnreadable(t *testing.T) {
	f := &fakeBao{initialized: true, sealed: true, unsealOK: true}
	p, _ := unsealProvider(t, f)
	p.unseal.credentialsPath = filepath.Join(t.TempDir(), "missing.json")
	if _, err := p.ReadValue(context.Background(), newTestSecret("p")); !errors.Is(err, reconcile.ErrProviderNotReady) || f.unseals != 0 {
		t.Fatalf("err = %v, unseals = %d", err, f.unseals)
	}
}
