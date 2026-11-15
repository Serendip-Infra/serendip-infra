package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/provider"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
)

// Spec 023 T009: edge cases of the observability routes (empty packages,
// a provider going not ready mid-request, per-caller caps) on spec 022's
// world with fault injection. Cursor paging is tested where it lives:
// core/reconcile and providers/observability/openchoreo.

// ctl023 wraps the world's fake observability capability so a test can make
// it fail, block, or answer nothing, and see how often it was asked.
type ctl023 struct {
	*obs023
	cmu sync.Mutex
	// err is returned by every call when set.
	err error
	// gate, when set, blocks every call until it's closed or the call's
	// context ends.
	gate chan struct{}
	// onCall runs at the start of every call, before err is considered.
	onCall func()
	// emptyMetrics makes Metrics answer no samples at all.
	emptyMetrics bool
	calls        int
	// entered gets a value as each call starts.
	entered chan struct{}
}

func (c *ctl023) enter(ctx context.Context) error {
	c.cmu.Lock()
	err, gate, hook := c.err, c.gate, c.onCall
	c.calls++
	c.cmu.Unlock()
	if hook != nil {
		hook()
	}
	select {
	case c.entered <- struct{}{}:
	default:
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (c *ctl023) set(f func(*ctl023)) {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	f(c)
}

func (c *ctl023) callCount() int {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	return c.calls
}

func (c *ctl023) SearchLogs(ctx context.Context, ts []reconcile.LogTarget, q reconcile.LogQuery) (reconcile.LogPage, error) {
	if err := c.enter(ctx); err != nil {
		return reconcile.LogPage{}, err
	}
	return c.obs023.SearchLogs(ctx, ts, q)
}

func (c *ctl023) BuildLogs(ctx context.Context, b resource.Build, q reconcile.LogQuery) (reconcile.LogPage, error) {
	if err := c.enter(ctx); err != nil {
		return reconcile.LogPage{}, err
	}
	return c.obs023.BuildLogs(ctx, b, q)
}

func (c *ctl023) Metrics(ctx context.Context, t reconcile.LogTarget, q reconcile.MetricsQuery) (reconcile.Metrics, error) {
	if err := c.enter(ctx); err != nil {
		return reconcile.Metrics{}, err
	}
	c.cmu.Lock()
	empty := c.emptyMetrics
	c.cmu.Unlock()
	if empty {
		return reconcile.Metrics{}, nil
	}
	return c.obs023.Metrics(ctx, t, q)
}

func newCtlWorld(t *testing.T) (*world023, *ctl023) {
	t.Helper()
	var c *ctl023
	w := newWorld023Wrapped(t, func(f *obs023) reconcile.ObservabilityProvider {
		c = &ctl023{obs023: f, entered: make(chan struct{}, 64)}
		return c
	})
	w.obsReady()
	return w, c
}

// noGoroutineLeaks fails the test if goroutines started after this call
// are still running when it ends.
func noGoroutineLeaks(t *testing.T) {
	t.Helper()
	base := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for runtime.NumGoroutine() > base && time.Now().Before(deadline) {
			runtime.Gosched()
		}
		if n := runtime.NumGoroutine(); n > base {
			buf := make([]byte, 1<<16)
			buf = buf[:runtime.Stack(buf, true)]
			t.Errorf("%d goroutines running, %d at the start:\n%s", n, base, buf)
		}
	})
}

type obsRoute struct{ name, path string }

func (w *world023) obsRoutes() []obsRoute {
	return []obsRoute{
		{"deployment search", "/v1/deployments/" + w.webDev + "/logs/search"},
		{"application search", "/v1/applications/" + w.app + "/logs/search?environment=" + w.dev},
		{"metrics", "/v1/deployments/" + w.webDev + "/metrics"},
	}
}

// get serves a GET as token, under ctx, on the caller's goroutine.
func (w *world023) get(ctx context.Context, token, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	return rec
}

func rawBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return m
}

// TestObservabilityEmptyReadyPackage: a ready package with nothing stored
// answers 200 with empty, not null, collections.
func TestObservabilityEmptyReadyPackage(t *testing.T) {
	w, c := newCtlWorld(t)
	w.obs.mu.Lock()
	w.obs.lines = map[string][]reconcile.StoredLogLine{}
	w.obs.mu.Unlock()
	routes := w.obsRoutes()

	t.Run("a Deployment search has lines [] and no next", func(t *testing.T) {
		rec := w.get(context.Background(), "tok-owner", routes[0].path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		m := rawBody(t, rec)
		if string(m["lines"]) != "[]" {
			t.Errorf("lines = %s", m["lines"])
		}
		for _, k := range []string{"next", "omitted", "truncated"} {
			if v, ok := m[k]; ok {
				t.Errorf("%s = %s, want it left out", k, v)
			}
		}
	})

	t.Run("an Application search has lines [] and omitted 0", func(t *testing.T) {
		rec := w.get(context.Background(), "tok-owner", routes[1].path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		m := rawBody(t, rec)
		if string(m["lines"]) != "[]" || string(m["omitted"]) != "0" {
			t.Errorf("lines = %s, omitted = %s", m["lines"], m["omitted"])
		}
		if _, ok := m["next"]; ok {
			t.Errorf("next = %s", m["next"])
		}
	})

	t.Run("an Application with no Deployments in the Environment answers 200 without asking the provider", func(t *testing.T) {
		before := c.callCount()
		// staging has no Deployments of the Application.
		rec := w.get(context.Background(), "tok-owner", "/v1/applications/"+w.app+"/logs/search?environment="+w.staging)
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		m := rawBody(t, rec)
		if string(m["lines"]) != "[]" || string(m["omitted"]) != "0" {
			t.Errorf("lines = %s, omitted = %s", m["lines"], m["omitted"])
		}
		if _, ok := m["next"]; ok {
			t.Errorf("next = %s", m["next"])
		}
		if got := c.callCount(); got != before {
			t.Errorf("the provider was asked %d times for no targets", got-before)
		}
	})

	t.Run("a search matching nothing is the same empty page", func(t *testing.T) {
		rec := w.get(context.Background(), "tok-owner", routes[0].path+"?q=no-such-phrase")
		if m := rawBody(t, rec); rec.Code != http.StatusOK || string(m["lines"]) != "[]" {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
	})

	t.Run("metrics with no samples have empty series and an empty summary", func(t *testing.T) {
		c.set(func(c *ctl023) { c.emptyMetrics = true })
		for _, brief := range []string{"", "?summary=true"} {
			rec := w.get(context.Background(), "tok-owner", routes[2].path+brief)
			if rec.Code != http.StatusOK {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			m := rawBody(t, rec)
			if string(m["cpu"]) != "[]" || string(m["memory"]) != "[]" || string(m["summary"]) != "{}" {
				t.Errorf("summary=%q: cpu %s, memory %s, summary %s", brief, m["cpu"], m["memory"], m["summary"])
			}
		}
	})
}

// TestObservabilityProviderNotReadyMidRequest: the provider says it isn't
// ready while the registry still says it is, or the registry flips between
// requests. Both are 503 with Retry-After, never 502.
func TestObservabilityProviderNotReadyMidRequest(t *testing.T) {
	w, c := newCtlWorld(t)

	t.Run("ErrProviderNotReady from the provider is 503 on every route", func(t *testing.T) {
		for _, err := range []error{reconcile.ErrProviderNotReady, fmt.Errorf("openchoreo observability: %w", reconcile.ErrProviderNotReady)} {
			c.set(func(c *ctl023) { c.err = err })
			for _, r := range w.obsRoutes() {
				rec := w.get(context.Background(), "tok-owner", r.path)
				if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
					t.Errorf("%s with %q: %d, Retry-After %q (%s)", r.name, err, rec.Code, rec.Header().Get("Retry-After"), rec.Body)
				}
				if rec.Code == http.StatusBadGateway {
					t.Errorf("%s: a not-ready provider is a 502", r.name)
				}
			}
		}
		c.set(func(c *ctl023) { c.err = nil })
	})

	t.Run("the registry flipping between requests names the package", func(t *testing.T) {
		for _, tc := range []struct{ name, reason, want string }{
			{"not installed", reconcile.ReasonNotInstalled, "isn't installed"},
			{"no reason at all", "", "isn't installed"},
			{"installing", reconcile.ReasonInstalling, "installing"},
			{"removing", reconcile.ReasonRemoving, "being removed"},
			{"installed but unhealthy", "Degraded", "not ready (Degraded)"},
		} {
			for _, r := range w.obsRoutes() {
				w.obsReady()
				if rec := w.get(context.Background(), "tok-owner", r.path); rec.Code != http.StatusOK {
					t.Fatalf("%s %s while ready: %d %s", tc.name, r.name, rec.Code, rec.Body)
				}
				w.registry.SetReadiness("observability", provider.Readiness{Ready: false, Reason: tc.reason})
				asked := c.callCount()
				rec := w.get(context.Background(), "tok-owner", r.path)
				if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" ||
					!strings.Contains(rec.Body.String(), obsPackage) || !strings.Contains(rec.Body.String(), tc.want) {
					t.Errorf("%s %s: %d, Retry-After %q (%s)", tc.name, r.name, rec.Code, rec.Header().Get("Retry-After"), rec.Body)
				}
				if got := c.callCount(); got != asked {
					t.Errorf("%s %s: the provider was asked while not ready", tc.name, r.name)
				}
			}
		}
		w.obsReady()
		w.get(context.Background(), "tok-owner", w.obsRoutes()[0].path) // serves again
	})

	t.Run("flipping during the call answers with the registry's reason", func(t *testing.T) {
		for _, r := range w.obsRoutes() {
			w.obsReady()
			c.set(func(c *ctl023) {
				c.err = reconcile.ErrProviderNotReady
				c.onCall = func() {
					w.registry.SetReadiness("observability", provider.Readiness{Ready: false, Reason: "Degraded"})
				}
			})
			rec := w.get(context.Background(), "tok-owner", r.path)
			if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" ||
				!strings.Contains(rec.Body.String(), obsPackage) || !strings.Contains(rec.Body.String(), "Degraded") {
				t.Errorf("%s: %d (%s)", r.name, rec.Code, rec.Body)
			}
		}
		c.set(func(c *ctl023) { c.err, c.onCall = nil, nil })
		w.obsReady()
	})
}

// TestObservabilityBuildLogsFallbackNotReady: stored build logs that can't
// be read because the package isn't ready are "logs expired" (410), as when
// there's no package; never a 502.
func TestObservabilityBuildLogsFallbackNotReady(t *testing.T) {
	w, c := newCtlWorld(t)
	w.packageReady()
	comp := w.createSource(t, "tok-devops", map[string]any{"repository": "https://example.com/shop/api"})
	b := w.succeed(t, w.trigger(t, comp.Metadata.ID, "").Metadata.ID)
	w.builder.RemoveBuild(context.Background(), b)
	logs := "/v1/builds/" + b.Metadata.ID + "/logs"
	srv := httptest.NewServer(w.h)
	defer srv.Close()

	// Control: with the package ready the expired run reads from the store.
	if rec := w.get(context.Background(), "tok-logreader", logs); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"stored":true`) {
		t.Fatalf("control: %d %s", rec.Code, rec.Body)
	}

	for _, tc := range []struct {
		name  string
		query string
		err   error
	}{
		{"not ready from the read", "", reconcile.ErrProviderNotReady},
		{"not ready, wrapped", "", fmt.Errorf("openchoreo observability: %w", reconcile.ErrProviderNotReady)},
		{"not ready while following", "?follow=true", reconcile.ErrProviderNotReady},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c.set(func(c *ctl023) { c.err = tc.err })
			defer c.set(func(c *ctl023) { c.err = nil })
			// A real connection, so following can stream.
			req, _ := http.NewRequest(http.MethodGet, srv.URL+logs+tc.query, nil)
			req.Header.Set("Authorization", "Bearer tok-logreader")
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusGone || !strings.Contains(string(body), "logs expired") {
				t.Errorf("%d %s", resp.StatusCode, body)
			}
		})
	}

	t.Run("the registry going not ready is 410 without reading", func(t *testing.T) {
		w.registry.SetReadiness("observability", provider.Readiness{Ready: false, Reason: reconcile.ReasonInstalling})
		defer w.obsReady()
		asked := c.callCount()
		rec := w.get(context.Background(), "tok-logreader", logs)
		if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), "logs expired") {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
		if got := c.callCount(); got != asked {
			t.Error("the store was read while the package wasn't ready")
		}
	})

	t.Run("another failure is still a 502", func(t *testing.T) {
		c.set(func(c *ctl023) { c.err = errors.New("backend exploded: secret detail") })
		defer c.set(func(c *ctl023) { c.err = nil })
		rec := w.get(context.Background(), "tok-logreader", logs)
		if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "secret detail") {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
	})
}

// held is a request blocked inside the provider.
type held struct {
	cancel context.CancelFunc
	done   chan *httptest.ResponseRecorder
}

// hold starts a request and returns once it's inside the provider (so it
// has its slot).
func (w *world023) hold(t *testing.T, c *ctl023, token, path string) held {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h := held{cancel: cancel, done: make(chan *httptest.ResponseRecorder, 1)}
	go func() { h.done <- w.get(ctx, token, path) }()
	<-c.entered
	return h
}

func (c *ctl023) block() chan struct{} {
	// Forget the starts of earlier calls, so hold waits for its own.
	for len(c.entered) > 0 {
		<-c.entered
	}
	g := make(chan struct{})
	c.set(func(c *ctl023) { c.gate = g })
	return g
}

func (c *ctl023) unblock() { c.set(func(c *ctl023) { c.gate = nil }) }

// TestObservabilityConcurrencyCaps: 3 in-flight queries a caller, shared by
// the three routes; the 4th gets 429 with Retry-After while another caller
// still gets through.
func TestObservabilityConcurrencyCaps(t *testing.T) {
	w, c := newCtlWorld(t)
	noGoroutineLeaks(t)
	routes := w.obsRoutes()

	for _, fourth := range routes {
		t.Run("4th is a "+fourth.name, func(t *testing.T) {
			gate := c.block()
			var holds []held
			for _, r := range []obsRoute{routes[0], routes[1], routes[2]} {
				holds = append(holds, w.hold(t, c, "tok-owner", r.path))
			}
			rec := w.get(context.Background(), "tok-owner", fourth.path)
			if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
				t.Errorf("the 4th query: %d, Retry-After %q (%s)", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
			}
			if len(c.entered) != 0 {
				t.Error("the refused query reached the provider")
			}
			// Another caller is unaffected.
			other := w.hold(t, c, "tok-logreader", routes[0].path)

			close(gate)
			c.unblock()
			for k, h := range append(holds, other) {
				if rec := <-h.done; rec.Code != http.StatusOK {
					t.Errorf("held query %d: %d %s", k, rec.Code, rec.Body)
				}
				h.cancel()
			}
			// All slots came back.
			for range 4 {
				if rec := w.get(context.Background(), "tok-owner", fourth.path); rec.Code != http.StatusOK {
					t.Errorf("after release: %d %s", rec.Code, rec.Body)
				}
			}
		})
	}
}

// TestObservabilitySlotsReleased: a slot comes back after success, after an
// error, after not-ready, and after the client goes away, so more than
// three queries in a row, and a full set again, succeed.
func TestObservabilitySlotsReleased(t *testing.T) {
	w, c := newCtlWorld(t)
	noGoroutineLeaks(t)
	path := w.obsRoutes()[0].path

	// fillAndRefuse proves all 3 slots are free: it takes 3, is refused a
	// 4th, and lets go.
	fillAndRefuse := func(t *testing.T) {
		t.Helper()
		gate := c.block()
		var holds []held
		for range 3 {
			holds = append(holds, w.hold(t, c, "tok-owner", path))
		}
		if rec := w.get(context.Background(), "tok-owner", path); rec.Code != http.StatusTooManyRequests {
			t.Errorf("4th: %d, want 429 (a slot was lost earlier?)", rec.Code)
		}
		close(gate)
		c.unblock()
		for _, h := range holds {
			if rec := <-h.done; rec.Code != http.StatusOK {
				t.Errorf("held: %d %s", rec.Code, rec.Body)
			}
			h.cancel()
		}
	}

	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, http.StatusOK},
		{"a provider error", errors.New("boom"), http.StatusBadGateway},
		{"a not-ready provider", reconcile.ErrProviderNotReady, http.StatusServiceUnavailable},
		{"a bad cursor", reconcile.ErrBadCursor, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c.set(func(c *ctl023) { c.err = tc.err })
			for k := range 7 {
				if rec := w.get(context.Background(), "tok-owner", path); rec.Code != tc.want {
					t.Fatalf("query %d: %d, want %d (%s)", k, rec.Code, tc.want, rec.Body)
				}
			}
			c.set(func(c *ctl023) { c.err = nil })
			fillAndRefuse(t)
		})
	}

	t.Run("a client that goes away", func(t *testing.T) {
		for round := range 3 {
			c.block()
			var holds []held
			for range 3 {
				holds = append(holds, w.hold(t, c, "tok-owner", path))
			}
			for _, h := range holds {
				h.cancel()
			}
			for _, h := range holds {
				<-h.done // the handler returned
			}
			c.unblock()
			if rec := w.get(context.Background(), "tok-owner", path); rec.Code != http.StatusOK {
				t.Fatalf("round %d after the cancels: %d %s", round, rec.Code, rec.Body)
			}
		}
		fillAndRefuse(t)
	})

	t.Run("a query validation error takes no slot", func(t *testing.T) {
		for range 7 {
			if rec := w.get(context.Background(), "tok-owner", path+"?limit=0"); rec.Code != http.StatusBadRequest {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		}
		fillAndRefuse(t)
	})
}
