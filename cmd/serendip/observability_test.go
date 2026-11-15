package main

import (
	"context"
	"encoding/json"
	"fmt"
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

// fakeObs is the observability capability for the CLI's tests: a few
// stored lines, and metrics at the real provider's resolution (whole
// minutes, at most reconcile.MaxMetricPoints points).
type fakeObs struct {
	mu      sync.Mutex
	queries []reconcile.MetricsQuery
}

func (f *fakeObs) SearchLogs(_ context.Context, targets []reconcile.LogTarget, q reconcile.LogQuery) (reconcile.LogPage, error) {
	var page reconcile.LogPage
	for _, t := range targets {
		if strings.Contains("order 1234 placed", q.Text) {
			page.Lines = append(page.Lines, reconcile.StoredLogLine{Time: time.Now().Add(-time.Minute).UTC(),
				Component: t.Component.Metadata.ID, Instance: "web-1", Line: "order 1234 placed"})
		}
	}
	return page, nil
}

func (f *fakeObs) BuildLogs(context.Context, resource.Build, reconcile.LogQuery) (reconcile.LogPage, error) {
	return reconcile.LogPage{}, nil
}

func (f *fakeObs) Metrics(_ context.Context, _ reconcile.LogTarget, q reconcile.MetricsQuery) (reconcile.Metrics, error) {
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.mu.Unlock()
	step := (q.To.Sub(q.From)/reconcile.MaxMetricPoints + time.Minute - 1).Truncate(time.Minute)
	var m reconcile.Metrics
	for i, at := 0, q.From.Add(step); !at.After(q.To); i, at = i+1, at.Add(step) {
		m.CPU = append(m.CPU, reconcile.MetricPoint{Time: at, Value: float64(10 + i%7)})
		m.Memory = append(m.Memory, reconcile.MetricPoint{Time: at, Value: float64(100 + i%3)})
	}
	m.Unavailable = []string{"requests: not available"}
	return m, nil
}

// newObsWorld is newWorld with the observability capability ready, and a
// Component deployed to the world's Environment.
func newObsWorld(t *testing.T) (w *world, f *fakeObs, depID string) {
	t.Helper()
	reg := provider.NewRegistry()
	reg.SetReadiness("observability", provider.Readiness{Ready: true})
	f = &fakeObs{}
	w = newWorldWith(t, reg, api.WithObservability(f))
	ctx := context.Background()
	comp, err := sqlite.NewStore[resource.ComponentSpec, resource.ComponentStatus](w.db, resource.KindComponent).Create(ctx,
		resource.Component{Metadata: resource.Metadata{Name: "web", OwnerRefs: []resource.OwnerRef{{Kind: resource.KindApplication, ID: w.appID}}},
			Spec: resource.ComponentSpec{Image: "nginx"}})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := sqlite.NewStore[resource.DeploymentSpec, resource.DeploymentStatus](w.db, resource.KindDeployment).Create(ctx,
		resource.Deployment{Metadata: resource.Metadata{Name: "web", OwnerRefs: []resource.OwnerRef{{Kind: resource.KindEnvironment, ID: w.envID}}},
			Spec: resource.DeploymentSpec{Component: comp.Metadata.ID}})
	if err != nil {
		t.Fatal(err)
	}
	return w, f, dep.Metadata.ID
}

func TestObservabilityCommands(t *testing.T) {
	w, _, dep := newObsWorld(t)
	from, to := "2026-11-13T00:00:00Z", "2026-11-14T00:00:00Z"
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"logs", "search", "d1", "--q", "order 1234", "--limit", "50"},
			"GET " + w.api.URL + "/v1/deployments/d1/logs/search?limit=50&q=order+1234"},
		{[]string{"logs", "search", "d1", "--before", "abc"}, "GET " + w.api.URL + "/v1/deployments/d1/logs/search?before=abc"},
		{[]string{"logs", "search-application", "a1", "--environment", "e1", "--from", from},
			"GET " + w.api.URL + "/v1/applications/a1/logs/search?environment=e1&from=" + url.QueryEscape(from)},
		{[]string{"metrics", "d1", "--from", from, "--to", to, "--summary"},
			"GET " + w.api.URL + "/v1/deployments/d1/metrics?from=" + url.QueryEscape(from) + "&summary=true&to=" + url.QueryEscape(to)},
		{[]string{"metrics", "d1"}, "GET " + w.api.URL + "/v1/deployments/d1/metrics"},
	} {
		r := w.cli(ops, "", append([]string{"--dry-run"}, tt.args...)...)
		if r.code != exitOK || strings.TrimSpace(r.stdout) != tt.want {
			t.Errorf("%v: %d %q, want %q (%s)", tt.args, r.code, r.stdout, tt.want, r.stderr)
		}
	}
	for _, args := range [][]string{
		{"logs", "search-application", "a1"},       // --environment is required
		{"logs", "search", "d1", "--limit", "501"}, // over the cap
		{"metrics", "d1", "--summary", "maybe"},    // not a boolean
	} {
		if r := w.cli(ops, "", args...); r.code != exitUsage {
			t.Errorf("%v: %d, want usage (%s)", args, r.code, r.stderr)
		}
	}

	// Against the server: a search finds the line, and metrics come summarised.
	r := w.cli(ops, "", "logs", "search", dep, "--q", "order 1234")
	if r.code != exitOK || !strings.Contains(r.stdout, "order 1234 placed") {
		t.Errorf("search: %d %q %s", r.code, r.stdout, r.stderr)
	}
	r = w.cli(ops, "", "logs", "search-application", w.appID, "--environment", w.envID)
	if r.code != exitOK || !strings.Contains(r.stdout, `"omitted": 0`) {
		t.Errorf("search-application: %d %q %s", r.code, r.stdout, r.stderr)
	}
	r = w.cli(ops, "", "metrics", dep)
	var m struct {
		CPU     []any                     `json:"cpu"`
		Summary map[string]map[string]any `json:"summary"`
	}
	if r.code != exitOK || json.Unmarshal([]byte(r.stdout), &m) != nil || len(m.CPU) != 60 || m.Summary["cpu"] == nil {
		t.Errorf("metrics (an hour, a point a minute): %d %d points %s %s", r.code, len(m.CPU), r.stdout, r.stderr)
	}
}

// The MCP metrics tool always gets the summary: a day's metrics come back
// as at most 60 points a series, with the summary over every point (plan
// decision 11, T010's Verify).
func TestMCPMetricsAlwaysSummarised(t *testing.T) {
	w, f, dep := newObsWorld(t)
	w.signIn(t)
	to := time.Now().UTC().Truncate(time.Minute)
	from := to.Add(-24 * time.Hour)
	args := func(extra string) string {
		return fmt.Sprintf(`{"id":%q,"from":%q,"to":%q%s}`, dep, from.Format(time.RFC3339), to.Format(time.RFC3339), extra)
	}
	r, got := w.session(t, nil, nil,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+meta+`}}`,
		call(2, "deployments_metrics", args("")),
		call(3, "deployments_metrics", args(`,"summary":false`)),
		call(4, "deployments_logs_search", fmt.Sprintf(`{"id":%q,"q":"order 1234"}`, dep)),
	)
	if r.code != exitOK || len(got) != 4 {
		t.Fatalf("serendip mcp: %d %v %s", r.code, got, r.stderr)
	}

	// The tool's schema doesn't offer summary: it's CLI-only.
	listed := map[string]bool{}
	for _, tl := range got[1]["result"].(map[string]any)["tools"].([]any) {
		tool := tl.(map[string]any)
		listed[tool["name"].(string)] = true
		if tool["name"] == "deployments_metrics" {
			props := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
			if _, ok := props["summary"]; ok {
				t.Errorf("deployments_metrics offers summary: %v", props)
			}
		}
	}
	for _, name := range []string{"deployments_logs_search", "applications_logs_search", "deployments_metrics"} {
		if !listed[name] {
			t.Errorf("tool %s not listed", name)
		}
	}

	text, isErr := toolText(t, got[2])
	var m struct {
		CPU     []reconcile.MetricPoint `json:"cpu"`
		Memory  []reconcile.MetricPoint `json:"memory"`
		Summary map[string]struct {
			Min, Max float64
			Points   int
		} `json:"summary"`
	}
	if isErr || json.Unmarshal([]byte(text), &m) != nil {
		t.Fatalf("metrics: %v %q", isErr, text)
	}
	if len(m.CPU) == 0 || len(m.CPU) > 60 || len(m.Memory) == 0 || len(m.Memory) > 60 {
		t.Errorf("a day's metrics: %d CPU and %d memory points, want 1-60 each", len(m.CPU), len(m.Memory))
	}
	if s := m.Summary["cpu"]; s.Points != 288 || s.Min != 10 || s.Max != 16 {
		t.Errorf("CPU summary = %+v, want every one of 288 points, min 10, max 16", s)
	}
	f.mu.Lock()
	if len(f.queries) == 0 || !f.queries[0].From.Equal(from) || !f.queries[0].To.Equal(to) {
		t.Errorf("provider asked for %v, want %v to %v", f.queries, from, to)
	}
	f.mu.Unlock()

	// An assistant can't ask for every point: summary isn't its to pass.
	if text, isErr := toolText(t, got[3]); !isErr || !strings.Contains(text, `unknown parameter "summary"`) {
		t.Errorf("summary=false from an assistant: %v %q, want refused", isErr, text)
	}
	if text, isErr := toolText(t, got[4]); isErr || !strings.Contains(text, "order 1234 placed") {
		t.Errorf("logs search: %v %q", isErr, text)
	}
	for _, s := range w.sent() {
		if strings.Contains(s, "/metrics") && !strings.HasSuffix(s, " mcp") {
			t.Errorf("request %q didn't declare mcp", s)
		}
	}
}
