package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/audit"
	"github.com/serendip-infra/serendip-infra/core/provider"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/providers/application/openchoreo"
	"github.com/serendip-infra/serendip-infra/providers/networking/cilium"
	obsplane "github.com/serendip-infra/serendip-infra/providers/observability/openchoreo"
)

// The observability package parses the records the networking provider
// points it at (contract-guardian, T006).
func TestFlowFormatsMatch(t *testing.T) {
	if obsplane.FlowFormat != cilium.FlowFormat {
		t.Errorf("observability parses %q, cilium writes %q", obsplane.FlowFormat, cilium.FlowFormat)
	}
}

// The node guard exempts the namespace the observability package's
// Prometheus runs in, or the guard would cut its scrape (RP-091, T014).
func TestNodeGuardExemptsPrometheus(t *testing.T) {
	if !slices.Contains(cilium.MetricsReaders(), obsplane.PlaneNamespace) {
		t.Errorf("node guard exempts %v, not the observability plane %q", cilium.MetricsReaders(), obsplane.PlaneNamespace)
	}
}

type factsStub struct {
	f   reconcile.NetworkFacts
	err error
}

func (s *factsStub) Facts(context.Context) (reconcile.NetworkFacts, error) { return s.f, s.err }

type visibilityStub struct {
	rep     cilium.Report
	targets []cilium.Target
}

func (v *visibilityStub) EnsureVisibility(_ context.Context, targets []cilium.Target) (cilium.Report, error) {
	v.targets = targets
	return v.rep, nil
}

type flowStub struct{ src reconcile.FlowSource }

func (f *flowStub) FlowSource(context.Context) (reconcile.FlowSource, error) { return f.src, nil }

type workloadsStub []openchoreo.HTTPWorkload

func (w workloadsStub) HTTPWorkloads(context.Context) ([]openchoreo.HTTPWorkload, error) {
	return w, nil
}

type auditRow struct {
	kind, id, actor, iface string
	after                  json.RawMessage
}

func newTestNetwork(t *testing.T) (*networkState, *[]auditRow, *[]reconcile.FlowSource) {
	t.Helper()
	n := newNetworkState(provider.NewRegistry(), nil, time.Hour)
	var rows []auditRow
	var sets []reconcile.FlowSource
	n.record = func(ctx context.Context, kind, id string, after json.RawMessage) error {
		rows = append(rows, auditRow{kind, id, audit.ActorFrom(ctx), audit.InterfaceFrom(ctx), after})
		return nil
	}
	n.setFlowSource = func(src reconcile.FlowSource) error {
		sets = append(sets, src)
		if src.Format != "" && src.Format != cilium.FlowFormat {
			return errors.New("unknown format")
		}
		return nil
	}
	return n, &rows, &sets
}

func TestNetworkRequestMetrics(t *testing.T) {
	n, _, _ := newTestNetwork(t)
	if ok, why := n.RequestMetrics(context.Background()); ok || why == "" {
		t.Errorf("unread: %v %q, want not available with a reason", ok, why)
	}
	for _, tc := range []struct {
		name    string
		f       reconcile.NetworkFacts
		ok      bool
		wantWhy string
	}{
		{"available", reconcile.NetworkFacts{RequestMetrics: true, Unavailable: []string{"requests: a note"}}, true, ""},
		{"reason", reconcile.NetworkFacts{Unavailable: []string{"flows: x", "requests: Hubble is turned off"}}, false, "Hubble is turned off"},
		{"no reason", reconcile.NetworkFacts{}, false, "the cluster's network layer doesn't record HTTP requests"},
	} {
		d := detectedNetwork{name: "x", facts: &factsStub{f: tc.f}}
		n.pass(context.Background(), d)
		ok, why := n.RequestMetrics(context.Background())
		if ok != tc.ok || why != tc.wantWhy {
			t.Errorf("%s: %v %q, want %v %q", tc.name, ok, why, tc.ok, tc.wantWhy)
		}
	}
}

func TestNetworkClusterRanges(t *testing.T) {
	n, _, _ := newTestNetwork(t)
	pool := netip.MustParsePrefix("10.0.0.0/8")
	node := netip.MustParsePrefix("172.18.0.2/32")
	n.pass(context.Background(), detectedNetwork{facts: &factsStub{f: reconcile.NetworkFacts{PodRanges: []netip.Prefix{pool, node}}}})
	boom := errors.New("partial")
	got, err := n.clusterRanges(func(context.Context) ([]netip.Prefix, error) { return []netip.Prefix{node}, boom })(context.Background())
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the read's", err)
	}
	if want := []netip.Prefix{node, pool}; !slices.Equal(got, want) {
		t.Errorf("ranges = %v, want %v", got, want)
	}
}

func TestNetworkFlowSource(t *testing.T) {
	prev := flowSourceWait
	flowSourceWait = 10 * time.Millisecond
	t.Cleanup(func() { flowSourceWait = prev })

	n, _, sets := newTestNetwork(t)
	if err := n.waitFlowSource(context.Background()); !errors.Is(err, reconcile.ErrProviderNotReady) {
		t.Fatalf("before any pass: %v, want ErrProviderNotReady", err)
	}
	good := reconcile.FlowSource{HostPath: "/var/run/cilium/hubble", File: "events.log", Format: cilium.FlowFormat}
	flows := &flowStub{src: good}
	d := detectedNetwork{facts: &factsStub{}, flows: flows}
	n.pass(context.Background(), d)
	if err := n.waitFlowSource(context.Background()); err != nil {
		t.Fatalf("after a pass: %v", err)
	}
	n.pass(context.Background(), d) // unchanged: not set again
	flows.src = reconcile.FlowSource{HostPath: "/x", File: "y", Format: "other"}
	n.pass(context.Background(), d) // refused: falls back to none
	want := []reconcile.FlowSource{good, flows.src, {}}
	if !slices.Equal(*sets, want) {
		t.Errorf("sets = %v, want %v", *sets, want)
	}
}

func TestNetworkVisibilityAudit(t *testing.T) {
	n, rows, _ := newTestNetwork(t)
	v := &visibilityStub{}
	d := detectedNetwork{facts: &factsStub{}, visibility: v, flows: &flowStub{}}

	n.pass(context.Background(), d)
	if v.targets != nil || len(*rows) != 0 {
		t.Fatalf("before the platform: targets %v, rows %v; want no pass", v.targets, *rows)
	}

	n.platformReady(workloadsStub{{Namespace: "dp-a", Policy: "srv-np", Ports: []int{8080}}})
	if len(n.kick) != 1 {
		t.Error("platformReady didn't ask for a pass")
	}
	n.pass(context.Background(), d)
	if want := []cilium.Target{{Namespace: "dp-a", Policy: "srv-np", HTTPPorts: []int{8080}}}; !slices.EqualFunc(v.targets, want, func(a, b cilium.Target) bool {
		return a.Namespace == b.Namespace && a.Policy == b.Policy && slices.Equal(a.HTTPPorts, b.HTTPPorts)
	}) {
		t.Errorf("targets = %v, want %v", v.targets, want)
	}
	if len(*rows) != 0 {
		t.Errorf("a pass with no writes left %d audit rows", len(*rows))
	}

	v.rep = cilium.Report{Applied: []string{"dp-a/serendip-l7-srv-np"}, Deleted: []string{"dp-b/serendip-l7-old"}, Unchanged: 3}
	n.pass(context.Background(), d)
	if len(*rows) != 1 {
		t.Fatalf("a pass that wrote left %d audit rows, want 1", len(*rows))
	}
	r := (*rows)[0]
	if r.kind != visibilityAuditKind || r.id != visibilityAuditID || r.actor != reconcile.ActorName || r.iface != reconcile.InterfaceName {
		t.Errorf("row = %+v", r)
	}
	if want := `{"applied":["dp-a/serendip-l7-srv-np"],"deleted":["dp-b/serendip-l7-old"]}`; string(r.after) != want {
		t.Errorf("after = %s, want %s", r.after, want)
	}
}

func TestNetworkRunDetects(t *testing.T) {
	registry := provider.NewRegistry()
	calls := 0
	n := newNetworkState(registry, func(context.Context) (detectedNetwork, error) {
		calls++
		if calls == 1 {
			return detectedNetwork{}, errors.New("cluster unreachable")
		}
		return detectedNetwork{name: "external", facts: &factsStub{f: reconcile.NetworkFacts{Plugin: "kindnet"}}}, nil
	}, time.Hour)
	n.retry = backoff{initial: time.Millisecond, max: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { n.run(ctx); close(done) }()
	deadline := time.After(5 * time.Second)
	for {
		if f, ok := n.Facts(); ok {
			if f.Plugin != "kindnet" {
				t.Errorf("plugin = %q", f.Plugin)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("no facts read")
		case <-time.After(5 * time.Millisecond):
		}
	}
	// An external network layer has no flow source: known, and zero.
	if err := n.waitFlowSource(ctx); err != nil {
		t.Errorf("waitFlowSource: %v", err)
	}
	if !registry.Readiness()[capNetworking].Ready {
		t.Errorf("readiness = %+v", registry.Readiness()[capNetworking])
	}
	if _, ok := registry.Lookup(capNetworking); !ok {
		t.Error("the networking provider isn't registered")
	}
	cancel()
	<-done
}

func TestTracingPackageEndpoint(t *testing.T) {
	var endpoints []string
	removing := &atomic.Bool{}
	removing.Store(true)
	p := &tracingPackage{setEndpoint: func(e string) { endpoints = append(endpoints, e) }, obsRemoving: removing, ops: newOpLock()}
	// While the observability package is being removed, nothing installs,
	// but the endpoint stays: the instance exists.
	st, err := p.Install(context.Background(), resource.ProviderInstance{})
	if err != nil || st.Ready || st.Message == "" {
		t.Errorf("Install = %+v, %v; want a waiting message", st, err)
	}
	if !slices.Equal(endpoints, []string{obsplane.OTLPEndpoint}) {
		t.Errorf("endpoints = %v", endpoints)
	}
}

func TestTraceEndpointAttach(t *testing.T) {
	e := &traceEndpoint{}
	e.set(obsplane.OTLPEndpoint)
	p := openchoreo.NewProvider(nil, reconcile.GatewayEndpoint{})
	e.attach(p)
	if got := p.TraceEndpoint(); got != obsplane.OTLPEndpoint {
		t.Errorf("after attach: %q", got)
	}
	e.set("")
	if got := p.TraceEndpoint(); got != "" {
		t.Errorf("after clearing: %q", got)
	}
}

func TestTracingCatalogueEntry(t *testing.T) {
	c := packageCatalogue(nil, nil, &tracingPackage{})
	p, ok := c["openchoreo-tracing"]
	if !ok || p.Capability != capTracing || p.Removal == "" {
		t.Fatalf("entry = %+v", p)
	}
	// Installable with the cost T013 measured, stated as the package
	// states it.
	if err := p.Installable(); err != nil {
		t.Errorf("Installable() = %v", err)
	}
	if p.Requirements != obsplane.TracingRequirements {
		t.Errorf("Requirements = %+v, want the package's", p.Requirements)
	}
	if err := obsplane.TracingRequirements.Validate(); err != nil {
		t.Errorf("TracingRequirements: %v", err)
	}
}

type failingFlows struct{}

func (failingFlows) FlowSource(context.Context) (reconcile.FlowSource, error) {
	return reconcile.FlowSource{}, errors.New("cilium-config unreadable")
}

// A flow source that can't be read doesn't hold the observability
// package off for ever (contract-guardian, T008).
func TestNetworkFlowSourceUnreadable(t *testing.T) {
	n, _, sets := newTestNetwork(t)
	d := detectedNetwork{facts: &factsStub{}, flows: failingFlows{}}
	n.pass(context.Background(), d)
	select {
	case <-n.flowKnown:
		t.Fatal("one failed read made the flow source known")
	default:
	}
	n.pass(context.Background(), d)
	if err := n.waitFlowSource(context.Background()); err != nil {
		t.Fatalf("after %d failed reads: %v", maxFlowFails, err)
	}
	if want := []reconcile.FlowSource{{}}; !slices.Equal(*sets, want) {
		t.Errorf("sets = %v, want %v", *sets, want)
	}
}

// A visibility pass whose audit row can't be written keeps it, and the
// next pass writes it (go-reviewer, T008).
func TestNetworkVisibilityAuditRetried(t *testing.T) {
	n, rows, _ := newTestNetwork(t)
	record := n.record
	fail := true
	n.record = func(ctx context.Context, kind, id string, after json.RawMessage) error {
		if fail {
			return errors.New("database is locked")
		}
		return record(ctx, kind, id, after)
	}
	v := &visibilityStub{rep: cilium.Report{Applied: []string{"dp-a/p"}}}
	d := detectedNetwork{facts: &factsStub{}, visibility: v, flows: &flowStub{}}
	n.platformReady(workloadsStub{{Namespace: "dp-a", Policy: "p", Ports: []int{80}}})
	n.pass(context.Background(), d)
	if len(*rows) != 0 || len(n.pending) != 1 {
		t.Fatalf("after a failed write: rows %d, pending %d; want 0, 1", len(*rows), len(n.pending))
	}
	fail = false
	v.rep = cilium.Report{Unchanged: 1}
	n.pass(context.Background(), d)
	if len(*rows) != 1 || len(n.pending) != 0 {
		t.Fatalf("next pass: rows %d, pending %d; want 1, 0", len(*rows), len(n.pending))
	}
	if want := `{"applied":["dp-a/p"]}`; string((*rows)[0].after) != want {
		t.Errorf("after = %s, want %s", (*rows)[0].after, want)
	}
}

func TestOpLock(t *testing.T) {
	t.Run("lock then unlock then lock again", func(t *testing.T) {
		l := newOpLock()
		for range 2 {
			if err := l.lock(context.Background()); err != nil {
				t.Fatal(err)
			}
			l.unlock()
		}
	})
	t.Run("a held lock gives up when ctx ends", func(t *testing.T) {
		l := newOpLock()
		if err := l.lock(context.Background()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if err := l.lock(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("lock under a held lock = %v, want DeadlineExceeded", err)
		}
		// The give-up took nothing: one unlock frees it.
		l.unlock()
		if err := l.lock(context.Background()); err != nil {
			t.Errorf("lock after unlock = %v", err)
		}
	})
	t.Run("a cancelled ctx on a held lock returns Canceled", func(t *testing.T) {
		l := newOpLock()
		_ = l.lock(context.Background())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := l.lock(ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("lock = %v, want Canceled", err)
		}
	})
	t.Run("a waiting lock proceeds when unlocked", func(t *testing.T) {
		l := newOpLock()
		_ = l.lock(context.Background())
		got := make(chan error, 1)
		go func() { got <- l.lock(context.Background()) }()
		select {
		case err := <-got:
			t.Fatalf("lock returned while held: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
		l.unlock()
		select {
		case err := <-got:
			if err != nil {
				t.Errorf("lock = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("lock didn't proceed after unlock")
		}
	})
}

// A tracingPackage install checks obsRemoving under the lock: one that
// waited out a held lock sees a removal that began meanwhile, and doesn't
// reach the installer (nil here: reaching it would panic).
func TestTracingPackageInstall_ChecksRemovingUnderLock(t *testing.T) {
	tests := []struct {
		name         string
		holdCtx      time.Duration // ctx of the install; 0 = none
		setRemoving  bool          // set while the lock is held
		wantErr      error
		wantWaitMsg  bool
		wantEndpoint int
	}{
		{name: "removal set while it waited: the waiting message, no install", setRemoving: true, wantWaitMsg: true, wantEndpoint: 1},
		{name: "ctx ends while it waits: the ctx error", holdCtx: 30 * time.Millisecond, wantErr: context.DeadlineExceeded, wantEndpoint: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var endpoints []string
			removing := &atomic.Bool{}
			ops := newOpLock()
			p := &tracingPackage{setEndpoint: func(e string) { endpoints = append(endpoints, e) }, obsRemoving: removing, ops: ops}
			if err := ops.lock(context.Background()); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if tc.holdCtx > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.holdCtx)
				defer cancel()
			}
			type result struct {
				st  reconcile.PackageState
				err error
			}
			done := make(chan result, 1)
			go func() {
				st, err := p.Install(ctx, resource.ProviderInstance{})
				done <- result{st, err}
			}()
			if tc.holdCtx == 0 {
				select {
				case r := <-done:
					t.Fatalf("Install returned while the lock was held: %+v", r)
				case <-time.After(30 * time.Millisecond):
				}
				// The removal starts while the install waits.
				removing.Store(tc.setRemoving)
				ops.unlock()
			}
			var r result
			select {
			case r = <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("Install didn't return")
			}
			if !errors.Is(r.err, tc.wantErr) {
				t.Errorf("err = %v, want %v", r.err, tc.wantErr)
			}
			if tc.wantWaitMsg && (r.st.Ready || r.st.Message == "" || r.st.Applied != "") {
				t.Errorf("state = %+v, want a waiting message, not Ready, nothing applied", r.st)
			}
			if len(endpoints) != tc.wantEndpoint {
				t.Errorf("endpoint set %d times, want %d", len(endpoints), tc.wantEndpoint)
			}
			// The lock is free again afterwards (unless the test still holds it).
			if tc.holdCtx == 0 {
				if err := ops.lock(context.Background()); err != nil {
					t.Errorf("lock left held: %v", err)
				}
			}
		})
	}
}

// A failed trace-collector removal is returned, and the observability
// installer is not reached (nil here: reaching it would panic).
func TestObservabilityUninstall_RemoveTracingFails(t *testing.T) {
	boom := errors.New("collector removal failed")
	tests := []struct {
		name    string
		remove  func(context.Context) error
		ctx     func() (context.Context, context.CancelFunc)
		holdOps bool
		wantErr error
	}{
		{name: "removeTracing error is returned", remove: func(context.Context) error { return boom },
			ctx: func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }, wantErr: boom},
		{name: "lock not obtained before ctx ends: ctx error, removeTracing not called", holdOps: true,
			remove: func(context.Context) error { t.Error("removeTracing called without the lock"); return nil },
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 20*time.Millisecond)
			}, wantErr: context.DeadlineExceeded},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			removing := &atomic.Bool{}
			ops := newOpLock()
			if tc.holdOps {
				_ = ops.lock(context.Background())
			}
			p := &observabilityPackage{removing: removing, removeTracing: tc.remove, tracingOps: ops}
			ctx, cancel := tc.ctx()
			defer cancel()
			if err := p.Uninstall(ctx, "inst"); !errors.Is(err, tc.wantErr) {
				t.Errorf("Uninstall = %v, want %v", err, tc.wantErr)
			}
			if !removing.Load() {
				t.Error("removing wasn't set: tracing installs would go ahead")
			}
			if !tc.holdOps {
				// The lock was released after the failed removal.
				if err := ops.lock(context.Background()); err != nil {
					t.Errorf("lock left held: %v", err)
				}
			}
		})
	}
}
