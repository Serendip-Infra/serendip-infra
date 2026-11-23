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
		{"no reason", reconcile.NetworkFacts{}, false, ""},
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
	p := &tracingPackage{setEndpoint: func(e string) { endpoints = append(endpoints, e) }, obsRemoving: removing}
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
	// Not installable until T013 measures it.
	if err := p.Installable(); err == nil {
		t.Error("the tracing package is installable without measured requirements")
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
