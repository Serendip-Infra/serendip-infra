package main

import (
	"context"
	"encoding/json"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/api"
	"github.com/serendip-infra/serendip-infra/core/provider"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store/sqlite"
)

// fakeNet adds flows and traffic to fakeObs (spec 025's optional
// FlowReader and TrafficReader), recording the queries it was given.
type fakeNet struct {
	*fakeObs
	mu    sync.Mutex
	flowQ []reconcile.FlowQuery
}

func (f *fakeNet) Flows(_ context.Context, targets []reconcile.LogTarget, q reconcile.FlowQuery) (reconcile.FlowPage, error) {
	f.mu.Lock()
	f.flowQ = append(f.flowQ, q)
	f.mu.Unlock()
	t := targets[0]
	return reconcile.FlowPage{Flows: []reconcile.Flow{{Time: time.Now().Add(-time.Minute).UTC(), Verdict: reconcile.FlowDropped,
		Reason:      "refused by a network policy",
		Source:      reconcile.FlowEnd{Outside: true},
		Destination: reconcile.FlowEnd{Component: t.Component.Metadata.ID, Environment: t.Environment.Metadata.ID},
		Protocol:    "TCP", Port: 8080, Count: 3}}, Next: "cursor-2"}, nil
}

func (f *fakeNet) Traffic(_ context.Context, targets []reconcile.LogTarget, _, _ time.Time) ([]reconcile.TrafficEdge, error) {
	if len(targets) < 2 {
		return nil, nil
	}
	return []reconcile.TrafficEdge{{From: targets[1].Component.Metadata.ID, To: targets[0].Component.Metadata.ID, Rate: 2.5, ErrorRate: 0.25}}, nil
}

// fakeTraces holds one trace of two spans, both the world's Component's.
type fakeTraces struct{ comp, env string }

const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"

func (f *fakeTraces) SearchTraces(context.Context, []reconcile.LogTarget, reconcile.TraceQuery) (reconcile.TracePage, error) {
	return reconcile.TracePage{Traces: []reconcile.TraceSummary{{TraceID: traceID, Start: time.Now().Add(-time.Minute).UTC(),
		Duration: 25 * time.Millisecond, Root: "GET /orders", Spans: 2, HasErrors: true}}}, nil
}

func (f *fakeTraces) Trace(_ context.Context, _ []reconcile.LogTarget, id string) (reconcile.Trace, error) {
	if id != traceID {
		return reconcile.Trace{}, reconcile.ErrTraceNotFound
	}
	start := time.Now().Add(-time.Minute).UTC()
	return reconcile.Trace{TraceID: id, Spans: []reconcile.Span{
		{SpanID: "00f067aa0ba902b7", Name: "GET /orders", Kind: "server", Start: start, Duration: 25 * time.Millisecond,
			Failed: true, Status: "boom", Component: f.comp, Environment: f.env},
		{SpanID: "00f067aa0ba902b8", ParentID: "00f067aa0ba902b7", Name: "SELECT orders", Start: start.Add(time.Millisecond),
			Duration: 10 * time.Millisecond, Component: f.comp, Environment: f.env, Attributes: map[string]string{"db.system": "postgresql"}},
	}}, nil
}

func (*fakeTraces) EvaluateCondition(context.Context, reconcile.LogTarget, resource.AlertCondition, time.Duration) (reconcile.ConditionValue, error) {
	return reconcile.ConditionValue{}, reconcile.ErrNoData
}

// newNetWorld is newObsWorld with spec 025's reads: Cilium's facts,
// flows and traffic on the observability provider, and tracing ready.
func newNetWorld(t *testing.T) (w *world, f *fakeNet, depID string) {
	t.Helper()
	reg := provider.NewRegistry()
	reg.SetReadiness("observability", provider.Readiness{Ready: true})
	reg.SetReadiness("tracing", provider.Readiness{Ready: true, Reason: reconcile.ReasonInstalled})
	f = &fakeNet{fakeObs: &fakeObs{}}
	tr := &fakeTraces{}
	facts := reconcile.NetworkFacts{Plugin: "cilium", Version: "1.20.5", PolicyEnforcement: reconcile.PolicyEnforced,
		Flows: true, RequestMetrics: true, PodRanges: []netip.Prefix{netip.MustParsePrefix("10.244.0.0/16")}}
	w = newWorldWith(t, reg, api.WithObservability(f), api.WithTracing(tr),
		api.WithNetworking(func() (reconcile.NetworkFacts, bool) { return facts, true }))
	// Two Components deployed to the Environment, so traffic has an edge
	// with two readable ends.
	deploy := func(name string) (comp, dep string) {
		ctx := context.Background()
		c, err := sqlite.NewStore[resource.ComponentSpec, resource.ComponentStatus](w.db, resource.KindComponent).Create(ctx,
			resource.Component{Metadata: resource.Metadata{Name: name, OwnerRefs: []resource.OwnerRef{{Kind: resource.KindApplication, ID: w.appID}}},
				Spec: resource.ComponentSpec{Image: "nginx"}})
		if err != nil {
			t.Fatal(err)
		}
		d, err := sqlite.NewStore[resource.DeploymentSpec, resource.DeploymentStatus](w.db, resource.KindDeployment).Create(ctx,
			resource.Deployment{Metadata: resource.Metadata{Name: name, OwnerRefs: []resource.OwnerRef{{Kind: resource.KindEnvironment, ID: w.envID}}},
				Spec: resource.DeploymentSpec{Component: c.Metadata.ID}})
		if err != nil {
			t.Fatal(err)
		}
		return c.Metadata.ID, d.Metadata.ID
	}
	comp, dep := deploy("web")
	deploy("api")
	tr.comp, tr.env = comp, w.envID
	return w, f, dep
}

// TestNetworkingCommands: spec 025's CLI words send the plan's requests,
// refuse bad arguments before sending, and print the API's answers;
// traces show takes the Deployment and the trace ID by position (T010).
func TestNetworkingCommands(t *testing.T) {
	w, f, dep := newNetWorld(t)
	from, to := "2026-11-13T00:00:00Z", "2026-11-14T00:00:00Z"
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"networking", "show"}, "GET " + w.api.URL + "/v1/networking"},
		{[]string{"flows", "d1", "--verdict", "dropped", "--limit", "20", "--before", "c1"},
			"GET " + w.api.URL + "/v1/deployments/d1/flows?before=c1&limit=20&verdict=dropped"},
		{[]string{"traffic", "a1", "--environment", "e1", "--from", from, "--to", to},
			"GET " + w.api.URL + "/v1/applications/a1/traffic?environment=e1&from=" + url.QueryEscape(from) + "&to=" + url.QueryEscape(to)},
		{[]string{"traces", "list", "d1", "--errorsOnly", "--minDuration", "250ms", "--limit", "10"},
			"GET " + w.api.URL + "/v1/deployments/d1/traces?errorsOnly=true&limit=10&minDuration=250ms"},
		{[]string{"traces", "show", "d1", traceID}, "GET " + w.api.URL + "/v1/deployments/d1/traces/" + traceID},
		{[]string{"traces", "show", "--traceId", traceID, "d1"}, "GET " + w.api.URL + "/v1/deployments/d1/traces/" + traceID},
	} {
		r := w.cli(ops, "", append([]string{"--dry-run"}, tt.args...)...)
		if r.code != exitOK || strings.TrimSpace(r.stdout) != tt.want {
			t.Errorf("%v: %d %q, want %q (%s)", tt.args, r.code, r.stdout, tt.want, r.stderr)
		}
	}
	for _, args := range [][]string{
		{"traffic", "a1"},                            // --environment is required
		{"flows", "d1", "--limit", "501"},            // over the cap
		{"flows", "d1", "--verdict", "refused"},      // not in the enum
		{"traces", "list", "d1", "--limit", "201"},   // over the cap
		{"traces", "list", "d1", "--errorsOnly=yes"}, // not a boolean
		{"traces", "show", "d1"},                     // the trace ID is missing
		{"traces", "show", "d1", traceID, "extra"},   // one positional too many
	} {
		if r := w.cli(ops, "", args...); r.code != exitUsage {
			t.Errorf("%v: %d, want usage (%s)", args, r.code, r.stderr)
		}
	}
	before := len(w.sent())

	// Against the server.
	r := w.cli(ops, "", "networking", "show")
	if r.code != exitOK || !strings.Contains(r.stdout, `"plugin": "cilium"`) || !strings.Contains(r.stdout, "10.244.0.0/16") {
		t.Errorf("networking show: %d %q %s", r.code, r.stdout, r.stderr)
	}
	r = w.cli(ops, "", "flows", dep, "--verdict", "dropped")
	if r.code != exitOK || !strings.Contains(r.stdout, "refused by a network policy") || !strings.Contains(r.stdout, `"outside": true`) {
		t.Errorf("flows: %d %q %s", r.code, r.stdout, r.stderr)
	}
	f.mu.Lock()
	if len(f.flowQ) != 1 || !f.flowQ[0].DroppedOnly {
		t.Errorf("flow queries %+v, want one, dropped only", f.flowQ)
	}
	f.mu.Unlock()
	r = w.cli(ops, "", "traffic", w.appID, "--environment", w.envID)
	if r.code != exitOK || !strings.Contains(r.stdout, `"rate": 2.5`) {
		t.Errorf("traffic: %d %q %s", r.code, r.stdout, r.stderr)
	}
	r = w.cli(ops, "", "traces", "list", dep, "--errorsOnly")
	if r.code != exitOK || !strings.Contains(r.stdout, traceID) || !strings.Contains(r.stdout, `"hasErrors": true`) {
		t.Errorf("traces list: %d %q %s", r.code, r.stdout, r.stderr)
	}
	r = w.cli(ops, "", "traces", "show", dep, traceID)
	var tr struct {
		TraceID string           `json:"traceId"`
		Spans   []reconcile.Span `json:"spans"`
		Partial bool             `json:"partial"`
	}
	if r.code != exitOK || json.Unmarshal([]byte(r.stdout), &tr) != nil || tr.TraceID != traceID || len(tr.Spans) != 2 || tr.Partial {
		t.Errorf("traces show: %d %q %s", r.code, r.stdout, r.stderr)
	}
	// A trace the Deployment has no spans in is not found (exit 5), and an
	// ID that isn't one is refused by the API.
	if r := w.cli(ops, "", "traces", "show", dep, strings.Repeat("1", 32)); r.code != exitNotFound {
		t.Errorf("unknown trace: %d, want %d (%s)", r.code, exitNotFound, r.stderr)
	}
	if r := w.cli(ops, "", "traces", "show", dep, "not-a-trace"); r.code != exitError {
		t.Errorf("bad trace ID: %d, want %d (%s)", r.code, exitError, r.stderr)
	}
	if got := len(w.sent()) - before; got != 7 {
		t.Errorf("%d requests reached the server, want 7 (usage errors send none)", got)
	}
}

// TestMCPNetworkingTools: the five reads are MCP tools (traces too, like
// stored logs: they need their own permission, which an assistant acting
// for a person has only if the person does), and answer as the API does.
func TestMCPNetworkingTools(t *testing.T) {
	w, _, dep := newNetWorld(t)
	w.signIn(t)
	r, got := w.session(t, nil, nil,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+meta+`}}`,
		call(2, "networking_get", `{}`),
		call(3, "deployments_flows", `{"id":"`+dep+`","verdict":"dropped"}`),
		call(4, "applications_traffic", `{"id":"`+w.appID+`","environment":"`+w.envID+`"}`),
		call(5, "deployments_traces_list", `{"id":"`+dep+`","errorsOnly":true}`),
		call(6, "deployments_traces_get", `{"id":"`+dep+`","traceId":"`+traceID+`"}`),
		call(7, "deployments_traces_get", `{"id":"`+dep+`"}`),
	)
	if r.code != exitOK || len(got) != 7 {
		t.Fatalf("serendip mcp: %d %v %s", r.code, got, r.stderr)
	}
	tools := map[string]map[string]any{}
	for _, tl := range got[1]["result"].(map[string]any)["tools"].([]any) {
		tool := tl.(map[string]any)
		tools[tool["name"].(string)] = tool
	}
	for _, name := range []string{"networking_get", "deployments_flows", "applications_traffic", "deployments_traces_list", "deployments_traces_get"} {
		tool, ok := tools[name]
		if !ok {
			t.Errorf("tool %s not listed", name)
			continue
		}
		if ann, _ := tool["annotations"].(map[string]any); ann["readOnlyHint"] != true {
			t.Errorf("%s isn't read-only: %v", name, tool["annotations"])
		}
	}
	if req, _ := tools["deployments_traces_get"]["inputSchema"].(map[string]any)["required"].([]any); len(req) != 2 {
		t.Errorf("deployments_traces_get requires %v, want id and traceId", req)
	}
	for id, want := range map[int]string{2: `"plugin":"cilium"`, 3: "refused by a network policy", 4: `"errorRate":0.25`,
		5: `"root":"GET /orders"`, 6: `"db.system":"postgresql"`} {
		text, isErr := toolText(t, got[id])
		if isErr || !strings.Contains(strings.ReplaceAll(text, " ", ""), strings.ReplaceAll(want, " ", "")) {
			t.Errorf("call %d: %v %q, want %s", id, isErr, text, want)
		}
	}
	if text, isErr := toolText(t, got[7]); !isErr || !strings.Contains(text, "traceId") {
		t.Errorf("trace without its ID: %v %q, want refused", isErr, text)
	}
	for _, s := range w.sent() {
		if (strings.Contains(s, "/traces") || strings.Contains(s, "/flows") || strings.Contains(s, "/networking")) && !strings.HasSuffix(s, " mcp") {
			t.Errorf("request %q didn't declare mcp", s)
		}
	}
}
