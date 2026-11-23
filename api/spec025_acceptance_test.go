package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/api"
	"github.com/serendip-infra/serendip-infra/core/identity"
	"github.com/serendip-infra/serendip-infra/core/policy"
	"github.com/serendip-infra/serendip-infra/core/provider"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
)

// This file proves specs/025-networking's API side (T009): criteria 2,
// 5, 6 and 9, and the traces action, in process over spec 023's world:
// Components web and api in Application shop, deployed to dev in Project
// A, and ledger in Project B's Environment elsewhere. The fakes name every
// end and edge they're given, whoever may read it, so what's asserted is
// the API's own scoping, not a well-behaved provider's.

// end025 is a flow end by Component and Environment.
type end025 struct{ comp, env string }

// obs025 adds flows and traffic to obs023, recording what it was asked.
type obs025 struct {
	*obs023
	mu      sync.Mutex
	flows   []struct{ src, dst end025 }
	edges   []reconcile.TrafficEdge
	targets [][]string // Deployment IDs, per call
	peers   [][]string
	err     error // answered by Flows and Traffic when set
}

func ids025(ts []reconcile.LogTarget) []string {
	out := []string{}
	for _, t := range ts {
		out = append(out, t.Deployment.Metadata.ID)
	}
	return out
}

// Flows answers every stored record with an end among targets, naming
// both ends whatever they are (a leaky provider).
func (f *obs025) Flows(_ context.Context, targets []reconcile.LogTarget, q reconcile.FlowQuery) (reconcile.FlowPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets, f.peers = append(f.targets, ids025(targets)), append(f.peers, ids025(q.Peers))
	if f.err != nil {
		return reconcile.FlowPage{}, f.err
	}
	in := map[end025]bool{}
	for _, t := range targets {
		in[end025{t.Component.Metadata.ID, t.Environment.Metadata.ID}] = true
	}
	page := reconcile.FlowPage{}
	for i, r := range f.flows {
		if !in[r.src] && !in[r.dst] {
			continue
		}
		v := reconcile.FlowForwarded
		reason := ""
		if i%2 == 1 {
			v, reason = reconcile.FlowDropped, "refused by a network policy"
		}
		if q.DroppedOnly && v != reconcile.FlowDropped {
			continue
		}
		page.Flows = append(page.Flows, reconcile.Flow{Time: time.Now().Add(-time.Duration(i) * time.Minute), Verdict: v, Reason: reason,
			Source:      reconcile.FlowEnd{Component: r.src.comp, Environment: r.src.env},
			Destination: reconcile.FlowEnd{Component: r.dst.comp, Environment: r.dst.env}, Protocol: "TCP", Port: 8080, Count: 1})
	}
	return page, nil
}

// Traffic answers every stored edge (a leaky provider).
func (f *obs025) Traffic(_ context.Context, targets []reconcile.LogTarget, _, _ time.Time) ([]reconcile.TrafficEdge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = append(f.targets, ids025(targets))
	if f.err != nil {
		return nil, f.err
	}
	return slices.Clone(f.edges), nil
}

func (f *obs025) last() (targets, peers []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.targets) > 0 {
		targets = f.targets[len(f.targets)-1]
	}
	if len(f.peers) > 0 {
		peers = f.peers[len(f.peers)-1]
	}
	return targets, peers
}

// tracing025 holds spans by trace ID and answers as the contract says:
// a target's own spans, the rest counted in Hidden, none not found.
type tracing025 struct {
	mu    sync.Mutex
	spans map[string][]reconcile.Span
	calls [][]string // Deployment IDs asked, per Trace call
	lists [][]string
	err   error // answered by every read when set
	// maxSpans, when set, cuts each answer to that many spans in start
	// order, marking it Truncated.
	maxSpans int
}

func (f *tracing025) SearchTraces(_ context.Context, targets []reconcile.LogTarget, _ reconcile.TraceQuery) (reconcile.TracePage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists = append(f.lists, ids025(targets))
	if f.err != nil {
		return reconcile.TracePage{}, f.err
	}
	return reconcile.TracePage{Traces: []reconcile.TraceSummary{{TraceID: strings.Repeat("ab", 16), Start: time.Now(), Spans: 1}}}, nil
}

func (f *tracing025) Trace(_ context.Context, targets []reconcile.LogTarget, id string) (reconcile.Trace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, ids025(targets))
	if f.err != nil {
		return reconcile.Trace{}, f.err
	}
	in := map[end025]bool{}
	for _, t := range targets {
		in[end025{t.Component.Metadata.ID, t.Environment.Metadata.ID}] = true
	}
	out := reconcile.Trace{TraceID: id}
	for _, s := range f.spans[id] {
		if in[end025{s.Component, s.Environment}] {
			out.Spans = append(out.Spans, s)
		} else {
			out.Hidden++
		}
	}
	if len(out.Spans) == 0 {
		return reconcile.Trace{}, reconcile.ErrTraceNotFound
	}
	if f.maxSpans > 0 && len(out.Spans) > f.maxSpans {
		out.Spans = slices.Clone(out.Spans)
		slices.SortFunc(out.Spans, func(a, b reconcile.Span) int { return a.Start.Compare(b.Start) })
		out.Spans, out.Truncated = out.Spans[:f.maxSpans], true
	}
	return out, nil
}

func (*tracing025) EvaluateCondition(context.Context, reconcile.LogTarget, resource.AlertCondition, time.Duration) (reconcile.ConditionValue, error) {
	return reconcile.ConditionValue{}, reconcile.ErrNoData
}

// facts025 is the networking capability's cache, set by the test.
type facts025 struct {
	mu    sync.Mutex
	facts reconcile.NetworkFacts
	read  bool
}

func (f *facts025) get() (reconcile.NetworkFacts, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.facts, f.read
}

func (f *facts025) set(nf reconcile.NetworkFacts) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.facts, f.read = nf, true
}

var cilium025 = reconcile.NetworkFacts{Plugin: "cilium", Version: "1.20.5", PolicyEnforcement: reconcile.PolicyEnforced,
	Flows: true, RequestMetrics: true, PodRanges: []netip.Prefix{netip.MustParsePrefix("10.244.0.0/16")}}

var kindnet025 = reconcile.NetworkFacts{Plugin: "kindnet", PolicyEnforcement: reconcile.PolicyEnforced,
	Unavailable: []string{"flows: the cluster's CNI doesn't record connections", "requests: the cluster's CNI doesn't provide request metrics"}}

type world025 struct {
	*world023
	obs25   *obs025
	tracing *tracing025
	facts   *facts025
}

func newWorld025(t *testing.T) *world025 {
	t.Helper()
	tr := &tracing025{spans: map[string][]reconcile.Span{}}
	fc := &facts025{}
	var o *obs025
	w023 := newWorld023With(t, func(f *obs023) reconcile.ObservabilityProvider {
		o = &obs025{obs023: f}
		return o
	}, api.WithTracing(tr), api.WithNetworking(fc.get))
	w := &world025{world023: w023, obs25: o, tracing: tr, facts: fc}
	w.obsReady()
	w.registry.SetReadiness("tracing", provider.Readiness{Ready: true, Reason: reconcile.ReasonInstalled})
	return w
}

func (w *world025) web() end025    { return end025{w.world023.web, w.dev} }
func (w *world025) api() end025    { return end025{w.world023.api, w.dev} }
func (w *world025) ledger() end025 { return end025{w.otherComp, w.elsewhere} }

type flows025 struct {
	Flows []reconcile.Flow `json:"flows"`
}

func (w *world025) flows(t *testing.T, token, dep string, want int) flows025 {
	t.Helper()
	var out flows025
	body := w.call(t, token, http.MethodGet, "/v1/deployments/"+dep+"/flows", nil, want)
	if want == http.StatusOK {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
	}
	return out
}

// TestSpec025Networking: the facts are served under platform read, 503
// before they're read; on a network layer without flows or request
// metrics, those routes say so with the reason (criterion 2), never an
// empty list.
func TestSpec025Networking(t *testing.T) {
	w := newWorld025(t)
	w.call(t, "tok-owner", http.MethodGet, "/v1/networking", nil, http.StatusServiceUnavailable)
	w.call(t, "tok-owner", http.MethodGet, "/v1/deployments/"+w.webDev+"/flows", nil, http.StatusServiceUnavailable)

	w.facts.set(cilium025)
	var got reconcile.NetworkFacts
	json.Unmarshal(w.call(t, "tok-owner", http.MethodGet, "/v1/networking", nil, http.StatusOK), &got)
	if got.Plugin != "cilium" || got.Version != "1.20.5" || got.PolicyEnforcement != reconcile.PolicyEnforced || len(got.PodRanges) != 1 {
		t.Errorf("facts: %+v", got)
	}
	if body := w.call(t, "tok-reader", http.MethodGet, "/v1/networking", nil, http.StatusForbidden); !strings.Contains(string(body), policy.ActionPlatformRead) {
		t.Errorf("a Project's Reader: %s", body)
	}

	w.facts.set(kindnet025)
	for path, want := range map[string]string{
		"/v1/deployments/" + w.webDev + "/flows":                      "the cluster's CNI doesn't record connections",
		"/v1/applications/" + w.app + "/traffic?environment=" + w.dev: "the cluster's CNI doesn't provide request metrics",
	} {
		if body := w.call(t, "tok-owner", http.MethodGet, path, nil, http.StatusConflict); !strings.Contains(string(body), want) {
			t.Errorf("%s: %s", path, body)
		}
	}
	if targets, _ := w.obs25.last(); targets != nil {
		t.Errorf("the provider was asked %v on a network layer without them", targets)
	}
}

// TestSpec025Criterion6_Flows: a reader of Project A sees A's flows to B
// with B's end unnamed, A's other Component named, and no flow between B
// Components; the provider is asked for the Deployment's flows only, and
// told only A's other Deployment as a peer.
func TestSpec025Criterion6_Flows(t *testing.T) {
	w := newWorld025(t)
	w.facts.set(cilium025)
	ledger2 := end025{"ledger-2", w.elsewhere}
	w.obs25.flows = []struct{ src, dst end025 }{
		{w.web(), w.ledger()}, {w.api(), w.web()}, {w.ledger(), ledger2}, {w.web(), w.api()},
	}
	got := w.flows(t, "tok-reader", w.webDev, http.StatusOK)
	if len(got.Flows) != 3 {
		t.Fatalf("flows: %+v", got.Flows)
	}
	for _, f := range got.Flows {
		for _, e := range []reconcile.FlowEnd{f.Source, f.Destination} {
			if e.Component == w.otherComp || e.Component == "ledger-2" || e.Environment == w.elsewhere {
				t.Errorf("Project B named: %+v", f)
			}
		}
		if f.Destination.Outside && f.Source.Component != w.world023.web {
			t.Errorf("an outside end on a flow not from web: %+v", f)
		}
	}
	if got.Flows[0].Destination != (reconcile.FlowEnd{Outside: true}) {
		t.Errorf("web→ledger: %+v", got.Flows[0].Destination)
	}
	if got.Flows[1].Source.Component != w.world023.api || got.Flows[1].Verdict != reconcile.FlowDropped || got.Flows[1].Reason == "" {
		t.Errorf("api→web, dropped: %+v", got.Flows[1])
	}
	targets, peers := w.obs25.last()
	if !slices.Equal(targets, []string{w.webDev}) || !slices.Equal(peers, []string{w.apiDev}) {
		t.Errorf("the provider was asked targets %v, peers %v", targets, peers)
	}

	// Dropped only; B's flows are B's readers'.
	w.call(t, "tok-reader", http.MethodGet, "/v1/deployments/"+w.webDev+"/flows?verdict=dropped", nil, http.StatusOK)
	w.call(t, "tok-reader", http.MethodGet, "/v1/deployments/"+w.webDev+"/flows?verdict=bogus", nil, http.StatusBadRequest)
	w.call(t, "tok-reader", http.MethodGet, "/v1/deployments/"+w.otherDep+"/flows", nil, http.StatusForbidden)
}

// TestSpec025Criterion5_Traffic: only edges whose two ends the caller can
// read are shown, and a caller with one readable end sees none.
func TestSpec025Criterion5_Traffic(t *testing.T) {
	people["tok-oneobs"] = identity.Principal{Subject: "sub-oneobs", Name: "oneobs"}
	t.Cleanup(func() { delete(people, "tok-oneobs") })
	w := newWorld025(t)
	w.facts.set(cilium025)
	web, apiC := w.world023.web, w.world023.api
	w.obs25.edges = []reconcile.TrafficEdge{{From: web, To: apiC, Rate: 4, ErrorRate: 0.4}, {From: apiC, To: web, Rate: 1}, {From: web, To: w.otherComp, Rate: 2}}

	var got struct {
		Edges   []reconcile.TrafficEdge `json:"edges"`
		Omitted int                     `json:"omitted"`
	}
	traffic := "/v1/applications/" + w.app + "/traffic?environment=" + w.dev
	json.Unmarshal(w.call(t, "tok-reader", http.MethodGet, traffic, nil, http.StatusOK), &got)
	if len(got.Edges) != 2 || got.Omitted != 0 {
		t.Errorf("a Project reader's edges: %+v", got)
	}
	if targets, _ := w.obs25.last(); !slices.Equal(slices.Sorted(slices.Values(targets)), slices.Sorted(slices.Values([]string{w.webDev, w.apiDev}))) {
		t.Errorf("the provider was asked %v", targets)
	}

	// Browsing rights on the Project, metrics on web's Deployment only.
	var role resource.RoleDefinition
	json.Unmarshal(w.call(t, "tok-owner", http.MethodPost, "/v1/authorization/roleDefinitions", map[string]any{"spec": map[string]any{
		"roleName":         "Browser",
		"permissions":      []resource.Permission{{Actions: []string{"serendip/applications/read", "serendip/environments/read"}}},
		"assignableScopes": []resource.Scope{{Kind: resource.KindProject, ID: w.proj}},
	}}, http.StatusCreated), &role)
	one := map[string]any{"type": "person", "id": "sub-oneobs"}
	w.assign(t, one, role.Metadata.ID, resource.Scope{Kind: resource.KindProject, ID: w.proj})
	w.assign(t, one, policy.ReaderRoleID, resource.Scope{Kind: resource.KindDeployment, ID: w.webDev})
	before := len(w.obs25.targets)
	json.Unmarshal(w.call(t, "tok-oneobs", http.MethodGet, traffic, nil, http.StatusOK), &got)
	if len(got.Edges) != 0 || got.Omitted != 1 || len(w.obs25.targets) != before {
		t.Errorf("one readable end: %+v, provider asked %d times more", got, len(w.obs25.targets)-before)
	}
	w.call(t, "tok-reader", http.MethodGet, "/v1/applications/"+w.app+"/traffic", nil, http.StatusBadRequest)
	w.call(t, "tok-reader", http.MethodGet, "/v1/applications/"+w.app+"/traffic?environment="+w.elsewhere, nil, http.StatusNotFound)
}

// TestSpec025Criterion9_Traces: a Reader without the traces action gets
// 403; a guessed trace ID of an unreadable Component answers 404; a trace
// shows the spans of every Deployment in its Environment the caller may
// read, and says partial for the rest without counting them.
func TestSpec025Criterion9_Traces(t *testing.T) {
	people["tok-onetrace"] = identity.Principal{Subject: "sub-onetrace", Name: "onetrace"}
	t.Cleanup(func() { delete(people, "tok-onetrace") })
	w := newWorld025(t)
	at := time.Now().Add(-10 * time.Minute)
	span := func(id string, e end025, off time.Duration) reconcile.Span {
		return reconcile.Span{SpanID: id, Name: id, Start: at.Add(off), Duration: time.Millisecond, Component: e.comp, Environment: e.env}
	}
	shared, theirs, own := strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32)
	w.tracing.spans[shared] = []reconcile.Span{span("c", w.ledger(), 3), span("b", w.api(), 2), span("a", w.web(), 1)}
	w.tracing.spans[theirs] = []reconcile.Span{span("x", w.ledger(), 0)}
	w.tracing.spans[own] = []reconcile.Span{span("o", w.web(), 0)}
	trace := func(token, dep, id string, want int) (out struct {
		Spans   []reconcile.Span `json:"spans"`
		Partial bool             `json:"partial"`
		Hidden  *int             `json:"hidden"`
	}) {
		t.Helper()
		body := w.call(t, token, http.MethodGet, "/v1/deployments/"+dep+"/traces/"+id, nil, want)
		if want == http.StatusOK {
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatalf("decode %s: %v", body, err)
			}
		}
		return out
	}

	for _, path := range []string{"/v1/deployments/" + w.webDev + "/traces", "/v1/deployments/" + w.webDev + "/traces/" + own} {
		if body := w.call(t, "tok-reader", http.MethodGet, path, nil, http.StatusForbidden); !strings.Contains(string(body), policy.ActionComponentTracesRead) {
			t.Errorf("Reader %s: %s", path, body)
		}
	}
	w.call(t, "tok-logreader", http.MethodGet, "/v1/deployments/"+w.webDev+"/traces", nil, http.StatusOK)
	if got := w.tracing.lists[len(w.tracing.lists)-1]; !slices.Equal(got, []string{w.webDev}) {
		t.Errorf("list asked for %v", got)
	}

	// The Log Reader at Project A: web and api's spans, ledger's hidden.
	got := trace("tok-logreader", w.webDev, shared, http.StatusOK)
	if len(got.Spans) != 2 || got.Spans[0].SpanID != "a" || got.Spans[1].SpanID != "b" || !got.Partial || got.Hidden != nil {
		t.Errorf("shared trace: %+v", got)
	}
	// The same trace from api's Deployment.
	if got := trace("tok-logreader", w.apiDev, shared, http.StatusOK); len(got.Spans) != 2 || !got.Partial {
		t.Errorf("shared trace from api: %+v", got)
	}
	// Guessing B's trace, from either readable Deployment, or by B's own.
	trace("tok-logreader", w.webDev, theirs, http.StatusNotFound)
	trace("tok-logreader", w.apiDev, theirs, http.StatusNotFound)
	w.call(t, "tok-logreader", http.MethodGet, "/v1/deployments/"+w.otherDep+"/traces/"+theirs, nil, http.StatusForbidden)
	if got := trace("tok-owner", w.otherDep, theirs, http.StatusOK); len(got.Spans) != 1 || got.Partial {
		t.Errorf("B's trace for the Owner: %+v", got)
	}
	// A trace of web's alone: whole. One call per trace, naming this
	// Deployment first and the Environment's other readable one.
	calls := len(w.tracing.calls)
	if got := trace("tok-logreader", w.webDev, own, http.StatusOK); len(got.Spans) != 1 || got.Partial {
		t.Errorf("web's own trace: %+v", got)
	}
	if n := len(w.tracing.calls) - calls; n != 1 || !slices.Equal(w.tracing.calls[calls], []string{w.webDev, w.apiDev}) {
		t.Errorf("web's own trace: %d calls, %v", n, w.tracing.calls[calls:])
	}
	// ... but not under api, which has no span in it, though web's spans
	// are readable (criterion 8: T012).
	trace("tok-logreader", w.apiDev, own, http.StatusNotFound)
	trace("tok-owner", w.apiDev, own, http.StatusNotFound)

	// A truncated trace: the cut dropped api's span, so api's own are
	// asked for alone (T014). One with none of api's is still 404.
	big, webOnly := strings.Repeat("4", 32), strings.Repeat("5", 32)
	w.tracing.spans[big] = []reconcile.Span{span("w1", w.web(), 0), span("w2", w.web(), 1), span("a1", w.api(), 2)}
	w.tracing.spans[webOnly] = []reconcile.Span{span("w1", w.web(), 0), span("w2", w.web(), 1), span("w3", w.web(), 2)}
	w.tracing.maxSpans = 2
	calls = len(w.tracing.calls)
	if got := trace("tok-logreader", w.apiDev, big, http.StatusOK); len(got.Spans) != 2 {
		t.Errorf("truncated trace with api's span: %+v", got)
	}
	if n := len(w.tracing.calls) - calls; n != 2 || !slices.Equal(w.tracing.calls[calls+1], []string{w.apiDev}) {
		t.Errorf("truncated trace: %d calls, %v", n, w.tracing.calls[calls:])
	}
	trace("tok-logreader", w.apiDev, webOnly, http.StatusNotFound)
	trace("tok-owner", w.apiDev, webOnly, http.StatusNotFound)
	trace("tok-logreader", w.webDev, webOnly, http.StatusOK)
	w.tracing.maxSpans = 0

	// Traces of web's Deployment only: api's spans are hidden too.
	one := map[string]any{"type": "person", "id": "sub-onetrace"}
	w.assign(t, one, policy.LogReaderRoleID, resource.Scope{Kind: resource.KindDeployment, ID: w.webDev})
	if got := trace("tok-onetrace", w.webDev, shared, http.StatusOK); len(got.Spans) != 1 || got.Spans[0].SpanID != "a" || !got.Partial {
		t.Errorf("one readable Deployment: %+v", got)
	}

	// Malformed IDs never reach the provider.
	calls = len(w.tracing.calls)
	for _, id := range []string{"xyz", strings.Repeat("0", 32), strings.Repeat("g", 32), strings.Repeat("a", 33)} {
		trace("tok-logreader", w.webDev, id, http.StatusBadRequest)
	}
	if len(w.tracing.calls) != calls {
		t.Error("a malformed trace ID reached the provider")
	}
	for _, q := range []string{"minDuration=-1s", "minDuration=2h", "limit=201", "errorsOnly=maybe", "before=nope"} {
		w.call(t, "tok-logreader", http.MethodGet, "/v1/deployments/"+w.webDev+"/traces?"+q, nil, http.StatusBadRequest)
	}

	// Not installable yet (its cost unmeasured), and not installed: 503.
	w.registry.SetReadiness("tracing", provider.Readiness{Ready: false, Reason: reconcile.ReasonUnavailable, Message: "not installable until its resource cost is measured"})
	if body := w.call(t, "tok-logreader", http.MethodGet, "/v1/deployments/"+w.webDev+"/traces", nil, http.StatusServiceUnavailable); !strings.Contains(string(body), "can't be installed in this build: not installable until") {
		t.Errorf("tracing unavailable: %s", body)
	}
	// Without the tracing package, 503.
	w.registry.SetReadiness("tracing", provider.Readiness{Ready: false, Reason: reconcile.ReasonNotInstalled})
	if body := w.call(t, "tok-logreader", http.MethodGet, "/v1/deployments/"+w.webDev+"/traces", nil, http.StatusServiceUnavailable); !strings.Contains(string(body), "tracing package") {
		t.Errorf("without tracing: %s", body)
	}
}

// TestSpec025Metrics_Requests: the request series come through, are
// summarised, and NaN samples are dropped.
func TestSpec025Metrics_Requests(t *testing.T) {
	w := newWorld025(t)
	w.obs.requests = &reconcile.RequestSeries{
		Rate:      []reconcile.MetricPoint{{Time: time.Now().Add(-time.Minute), Value: 3}, {Time: time.Now(), Value: nan025()}},
		ErrorRate: []reconcile.MetricPoint{{Time: time.Now(), Value: 0.3}},
	}
	var got struct {
		Requests *reconcile.RequestSeries `json:"requests"`
		Summary  map[string]struct {
			Max float64 `json:"max"`
		} `json:"summary"`
	}
	json.Unmarshal(w.call(t, "tok-reader", http.MethodGet, "/v1/deployments/"+w.webDev+"/metrics", nil, http.StatusOK), &got)
	if got.Requests == nil || len(got.Requests.Rate) != 1 || got.Summary["requests.rate"].Max != 3 || got.Summary["requests.errorRate"].Max != 0.3 {
		t.Errorf("requests: %+v %+v", got.Requests, got.Summary)
	}
}

func nan025() float64 { return math.NaN() }

// TestSpec025ReadFailures: a provider's failures are answered in
// Serendip's words on every new route, never with the error's text.
func TestSpec025ReadFailures(t *testing.T) {
	w := newWorld025(t)
	w.facts.set(cilium025)
	trace := strings.Repeat("ab", 16)
	w.tracing.spans[trace] = []reconcile.Span{{SpanID: "a", Component: w.world023.web, Environment: w.dev}}
	routes := []string{
		"/v1/deployments/" + w.webDev + "/flows",
		"/v1/applications/" + w.app + "/traffic?environment=" + w.dev,
		"/v1/deployments/" + w.webDev + "/traces",
		"/v1/deployments/" + w.webDev + "/traces/" + trace,
	}
	internal := errors.New("dial tcp 10.96.0.12:5080: secret-internal")
	for _, tc := range []struct {
		name   string
		err    error
		status int
		want   string
	}{
		{"backend", internal, http.StatusBadGateway, "couldn't be read"},
		{"not collected", fmt.Errorf("x: %w", reconcile.ErrNotCollected), http.StatusConflict, "isn't collected"},
		{"not ready", reconcile.ErrProviderNotReady, http.StatusServiceUnavailable, "still being set up"},
		{"bad cursor", reconcile.ErrBadCursor, http.StatusBadRequest, "before:"},
	} {
		w.obs25.mu.Lock()
		w.obs25.err = tc.err
		w.obs25.mu.Unlock()
		w.tracing.mu.Lock()
		w.tracing.err = tc.err
		w.tracing.mu.Unlock()
		for _, path := range routes {
			rec := w.get(context.Background(), "tok-owner", path)
			body := rec.Body.String()
			if rec.Code != tc.status || !strings.Contains(body, tc.want) || strings.Contains(body, "secret-internal") || strings.Contains(body, "10.96") {
				t.Errorf("%s, %s: %d %s", tc.name, path, rec.Code, body)
			}
			if tc.err == internal && strings.Contains(path, "/traces") != strings.Contains(body, "stored traces") {
				t.Errorf("%s: the failure names the wrong data: %s", path, body)
			}
			if tc.status == http.StatusServiceUnavailable && rec.Header().Get("Retry-After") == "" {
				t.Errorf("%s, %s: no Retry-After", tc.name, path)
			}
		}
	}
}

// TestSpec025TrafficCap: an Application with more Deployments in the
// Environment than one read covers is refused, not cut.
func TestSpec025TrafficCap(t *testing.T) {
	w := newWorld025(t)
	w.facts.set(cilium025)
	ctx := context.Background()
	for i := range reconcile.MaxLogTargets {
		c, err := w.comps.Create(ctx, resource.Component{Metadata: resource.Metadata{Name: fmt.Sprintf("c%02d", i), OwnerRefs: []resource.OwnerRef{{Kind: resource.KindApplication, ID: w.app}}},
			Spec: resource.ComponentSpec{Image: "nginx"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.deps.Create(ctx, resource.Deployment{Metadata: resource.Metadata{Name: fmt.Sprintf("c%02d", i), OwnerRefs: []resource.OwnerRef{{Kind: resource.KindEnvironment, ID: w.dev}}},
			Spec: resource.DeploymentSpec{Component: c.Metadata.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	w.call(t, "tok-owner", http.MethodGet, "/v1/applications/"+w.app+"/traffic?environment="+w.dev, nil, http.StatusUnprocessableEntity)
	// Flows and traces stop at the cap instead: peers past it are outside.
	w.call(t, "tok-owner", http.MethodGet, "/v1/deployments/"+w.webDev+"/flows", nil, http.StatusOK)
	if _, peers := w.obs25.last(); len(peers) != reconcile.MaxLogTargets {
		t.Errorf("%d peers, want the cap", len(peers))
	}
}
