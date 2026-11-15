package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/api"
	"github.com/serendip-infra/serendip-infra/api/catalog"
	"github.com/serendip-infra/serendip-infra/core/identity"
	"github.com/serendip-infra/serendip-infra/core/policy"
	"github.com/serendip-infra/serendip-infra/core/provider"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store"
	"github.com/serendip-infra/serendip-infra/core/store/sqlite"
)

// This file proves specs/023-observability/spec.md's acceptance criteria
// 3, 5-8 and 11 in process (T008): the whole API stack over spec 022's
// world, with the observability package in its catalogue, a fake
// observability capability (obs023) and a fake installer (installer023).
// The console's side is T010's; the real cluster T012's; the numbers,
// the cheap resync, retention and the footprint T005, T006 and T013's.

const obsPackage = "openchoreo-observability-plane"

const removal023 = "All stored logs and metrics history are deleted. Recent logs and events on the Runtime tab keep working."

// obs023 is an observability capability over lines the test stores. It
// records the targets of every search, so scoping is asserted on what the
// provider was asked, not only on what came back.
type obs023 struct {
	mu       sync.Mutex
	lines    map[string][]reconcile.StoredLogLine // by Component ID
	builds   map[string][]reconcile.StoredLogLine // by Build ID
	searched [][]string                           // Deployment IDs, per search
	metricsQ []reconcile.MetricsQuery
}

func newObs023() *obs023 {
	return &obs023{lines: map[string][]reconcile.StoredLogLine{}, builds: map[string][]reconcile.StoredLogLine{}}
}

func (f *obs023) store(comp, instance, line string, ago time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lines[comp] = append(f.lines[comp], reconcile.StoredLogLine{Time: time.Now().Add(-ago).UTC(), Component: comp, Instance: instance, Line: line})
}

// page answers lines newest first from the cursor on, as a provider
// pages: before the cursor's time, after skipping the lines it saw there.
func page023(all []reconcile.StoredLogLine, q reconcile.LogQuery) (reconcile.LogPage, error) {
	cur, err := reconcile.DecodeLogCursor(q.Before)
	if err != nil {
		return reconcile.LogPage{}, err
	}
	var keep []reconcile.StoredLogLine
	for _, l := range all {
		if strings.Contains(l.Line, q.Text) && !l.Time.Before(q.From) && (q.To.IsZero() || l.Time.Before(q.To)) {
			keep = append(keep, l)
		}
	}
	slices.SortStableFunc(keep, func(a, b reconcile.StoredLogLine) int { return b.Time.Compare(a.Time) })
	start := 0
	if !cur.Time.IsZero() {
		for start < len(keep) && keep[start].Time.After(cur.Time) {
			start++
		}
		for skipped := 0; skipped < cur.Seen && start < len(keep) && keep[start].Time.Equal(cur.Time); skipped++ {
			start++
		}
	}
	end := min(start+q.Limit, len(keep))
	p := reconcile.LogPage{Lines: keep[start:end]}
	if end < len(keep) && end > start {
		last := keep[end-1].Time
		seen := 0
		for _, l := range keep[:end] {
			if l.Time.Equal(last) {
				seen++
			}
		}
		p.Next = reconcile.LogCursor{Time: last, Seen: seen}.Encode()
	}
	return p, nil
}

func (f *obs023) SearchLogs(_ context.Context, targets []reconcile.LogTarget, q reconcile.LogQuery) (reconcile.LogPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	var all []reconcile.StoredLogLine
	for _, t := range targets {
		ids = append(ids, t.Deployment.Metadata.ID)
		if t.Deployment.Spec.Component != t.Component.Metadata.ID || t.Environment.Metadata.ID != t.Deployment.Metadata.OwnerRefs[0].ID {
			return reconcile.LogPage{}, errors.New("a target that doesn't hang together")
		}
		all = append(all, f.lines[t.Component.Metadata.ID]...)
	}
	f.searched = append(f.searched, ids)
	return page023(all, q)
}

func (f *obs023) BuildLogs(_ context.Context, b resource.Build, q reconcile.LogQuery) (reconcile.LogPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return page023(f.builds[b.Metadata.ID], q)
}

// Metrics answers a minute's samples over the range, with a load placed
// on it 20 minutes ago (CPU 900m for 5 minutes).
func (f *obs023) Metrics(_ context.Context, t reconcile.LogTarget, q reconcile.MetricsQuery) (reconcile.Metrics, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metricsQ = append(f.metricsQ, q)
	req, lim := 100.0, 500.0
	m := reconcile.Metrics{CPURequest: &req, CPULimit: &lim, MemoryRequest: &req,
		Unavailable: []string{"memoryLimit: not set", "requests: needs request metrics, not installed"}}
	for at := q.From.Truncate(time.Minute); at.Before(q.To); at = at.Add(time.Minute) {
		cpu := 20.0
		if ago := time.Since(at); ago <= 20*time.Minute && ago > 15*time.Minute {
			cpu = 900
		}
		m.CPU = append(m.CPU, reconcile.MetricPoint{Time: at, Value: cpu})
		m.Memory = append(m.Memory, reconcile.MetricPoint{Time: at, Value: 64})
	}
	return m, nil
}

func (f *obs023) lastSearch() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.searched) == 0 {
		return nil
	}
	return f.searched[len(f.searched)-1]
}

// installer023 installs the observability package and reports its disk
// use.
type installer023 struct {
	installer022
}

func (i *installer023) Usage(context.Context) (reconcile.DiskUsage, error) {
	return reconcile.DiskUsage{UsedMiB: 42, CapacityMiB: 8192, ReadAt: time.Now()}, nil
}

// world023 is spec 022's world with the observability package, and:
// Components web and api in Application shop, each deployed to dev; and
// a Deployment in the other Project's Environment.
type world023 struct {
	*world022
	obs       *obs023
	obsInst   *installer023
	webDev    string
	apiDev    string
	api       string
	otherApp  string
	otherDep  string
	otherComp string
}

func newWorld023(t *testing.T) *world023 {
	t.Helper()
	return newWorld023Wrapped(t, nil)
}

// newWorld023Wrapped is newWorld023 with the server given wrap(obs023) as its
// observability capability, when wrap isn't nil (T009's fault injection).
func newWorld023Wrapped(t *testing.T, wrap func(*obs023) reconcile.ObservabilityProvider) *world023 {
	t.Helper()
	f := newObs023()
	var served reconcile.ObservabilityProvider = f
	if wrap != nil {
		served = wrap(f)
	}
	inst := &installer023{}
	pkg := &reconcile.Package{Provider: obsPackage, Capability: "observability", Description: "Stored logs and metrics",
		Installer: inst, Removal: removal023,
		Requirements: &provider.Requirements{IdleMemoryMiB: 400, IdleCPUMillicores: 50, PeakMemoryMiB: 600, PeakCPUMillicores: 300,
			MeasuredOn: "edge-1 simulated T470", Results: "profiles/edge-1/bench/results/obs",
			Disk: &provider.DiskRequirements{GrowthMiBPerDay: 120, RetentionDays: 3, BudgetMiB: 5120}}}
	w := &world023{world022: newWorld022With(t, pkg, api.WithObservability(served)), obs: f, obsInst: inst}
	ctx := context.Background()

	var comp resource.Component
	json.Unmarshal(w.call(t, "tok-devops", http.MethodPost, "/v1/components", map[string]any{
		"metadata": map[string]any{"name": "api", "ownerRefs": []any{map[string]any{"kind": "Application", "id": w.app}}},
		"spec":     map[string]any{"image": "nginx"}}, http.StatusCreated), &comp)
	w.api = comp.Metadata.ID
	w.webDev = decodeDeployment(t, w.call(t, "tok-devops", http.MethodPost, "/v1/deployments", w.deployWorld.deployment(w.dev, ""), http.StatusCreated)).Metadata.ID
	w.apiDev = w.deployTo(t, w.api, w.dev).Metadata.ID

	// The other Project: an Application, a Component and a Deployment.
	app, err := sqlite.NewStore[resource.ApplicationSpec, resource.ApplicationStatus](w.db, resource.KindApplication).Create(ctx,
		resource.Application{Metadata: resource.Metadata{Name: "billing", OwnerRefs: []resource.OwnerRef{{Kind: resource.KindProject, ID: w.other}}}})
	if err != nil {
		t.Fatal(err)
	}
	w.otherApp = app.Metadata.ID
	oc, err := w.comps.Create(ctx, resource.Component{Metadata: resource.Metadata{Name: "ledger", OwnerRefs: []resource.OwnerRef{{Kind: resource.KindApplication, ID: w.otherApp}}},
		Spec: resource.ComponentSpec{Image: "nginx"}})
	if err != nil {
		t.Fatal(err)
	}
	w.otherComp = oc.Metadata.ID
	od, err := w.deps.Create(ctx, resource.Deployment{Metadata: resource.Metadata{Name: "ledger", OwnerRefs: []resource.OwnerRef{{Kind: resource.KindEnvironment, ID: w.elsewhere}}},
		Spec: resource.DeploymentSpec{Component: w.otherComp}})
	if err != nil {
		t.Fatal(err)
	}
	w.otherDep = od.Metadata.ID

	// What was logged: web's old pod is gone, replaced by a new one.
	f.store(w.web, "web-7d9f-old", "GET /health 200", 50*time.Minute)
	f.store(w.web, "web-7d9f-old", "order 1234 placed", 40*time.Minute)
	f.store(w.api, "api-55c1-a", "order 1234 charged", 30*time.Minute)
	f.store(w.web, "web-7d9f-new", "order 1235 placed", 10*time.Minute)
	f.store(w.otherComp, "ledger-1", "order 1234 booked", 20*time.Minute)
	return w
}

// assign gives a role through the API, so the policy index learns of it
// at once (a store write alone waits for the next rebuild).
func (w *world023) assign(t *testing.T, principal map[string]any, role string, at resource.Scope) {
	t.Helper()
	scope := map[string]any{"kind": at.Kind, "id": at.ID}
	if at == resource.RootScope {
		scope = map[string]any{"kind": "", "id": ""}
	}
	w.call(t, "tok-owner", http.MethodPost, "/v1/authorization/roleAssignments", map[string]any{"spec": map[string]any{
		"principal": principal, "roleDefinitionId": role, "scope": scope}}, http.StatusCreated)
}

var agent023 = map[string]any{"type": "agent", "id": "am-dev/agent-1", "sponsor": map[string]any{"type": "person", "id": "sub-owner"}}

func (w *world023) obsReady() {
	w.registry.SetReadiness("observability", provider.Readiness{Ready: true, Reason: reconcile.ReasonInstalled})
}

type search023 struct {
	Lines     []reconcile.StoredLogLine `json:"lines"`
	Next      string                    `json:"next"`
	Truncated bool                      `json:"truncated"`
	Omitted   *int                      `json:"omitted"`
}

func (w *world023) search(t *testing.T, token, path string, q url.Values, want int) search023 {
	t.Helper()
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out search023
	body := w.call(t, token, http.MethodGet, path, nil, want)
	if want == http.StatusOK {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
	}
	return out
}

func lineTexts(ls []reconcile.StoredLogLine) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Line)
	}
	return out
}

// TestSpec023Criterion3_Install: without the package the routes answer
// 503 naming it; a Contributor's install is refused; an Owner's is
// audited, reads "installing", then serves; and the package's disk use
// shows once it's installed.
func TestSpec023Criterion3_Install(t *testing.T) {
	w := newWorld023(t)
	logs := "/v1/deployments/" + w.webDev + "/logs/search"
	if body := w.call(t, "tok-owner", http.MethodGet, logs, nil, http.StatusServiceUnavailable); !strings.Contains(string(body), obsPackage) || !strings.Contains(string(body), "isn't installed") {
		t.Errorf("before install: %s", body)
	}
	if strings.Contains(string(w.call(t, "tok-owner", http.MethodGet, "/v1/packages", nil, http.StatusOK)), `"usage"`) {
		t.Error("an uninstalled package reports disk use")
	}
	w.call(t, "tok-devops", http.MethodPost, "/v1/provider-instances", install(obsPackage, "observability"), http.StatusForbidden)

	var inst resource.ProviderInstance
	json.Unmarshal(w.call(t, "tok-owner", http.MethodPost, "/v1/provider-instances", install(obsPackage, "observability"), http.StatusCreated), &inst)
	reconcileOnce(t, w.instRecon, resource.KindProviderInstance, inst.Metadata.ID)
	if body := w.call(t, "tok-owner", http.MethodGet, "/v1/deployments/"+w.webDev+"/metrics", nil, http.StatusServiceUnavailable); !strings.Contains(string(body), "installing") {
		t.Errorf("while installing: %s", body)
	}
	w.obsInst.setReady()
	reconcileOnce(t, w.instRecon, resource.KindProviderInstance, inst.Metadata.ID)
	w.call(t, "tok-owner", http.MethodGet, logs, nil, http.StatusOK)
	w.call(t, "tok-owner", http.MethodGet, "/v1/deployments/"+w.webDev+"/metrics", nil, http.StatusOK)

	var pk struct {
		Packages []struct {
			Provider string               `json:"provider"`
			Usage    *reconcile.DiskUsage `json:"usage"`
		} `json:"packages"`
	}
	json.Unmarshal(w.call(t, "tok-owner", http.MethodGet, "/v1/packages", nil, http.StatusOK), &pk)
	for _, p := range pk.Packages {
		if p.Provider == obsPackage && (p.Usage == nil || p.Usage.UsedMiB != 42 || p.Usage.CapacityMiB != 8192) {
			t.Errorf("the installed package's disk use: %+v", p.Usage)
		}
	}

	var actor string
	if err := w.db.QueryRow(`SELECT actor FROM audit_log WHERE resource_id = ? AND interface = ?`, inst.Metadata.ID, api.InterfaceName).Scan(&actor); err != nil || actor != "person:owner" {
		t.Errorf("the install audited as %q (%v)", actor, err)
	}
}

// TestSpec023Criterion5_LogSearch: a line from a pod that's gone is found
// by text and time; an Application's search finds both Components' lines;
// results are capped and page to the end; and the CLI's and MCP tool's
// requests are the same route's.
func TestSpec023Criterion5_LogSearch(t *testing.T) {
	w := newWorld023(t)
	w.obsReady()
	logs := "/v1/deployments/" + w.webDev + "/logs/search"

	got := w.search(t, "tok-logreader", logs, url.Values{"q": {"order 1234"}}, http.StatusOK)
	if len(got.Lines) != 1 || got.Lines[0].Instance != "web-7d9f-old" || got.Lines[0].Component != w.web || got.Next != "" || got.Omitted != nil {
		t.Errorf("the replaced pod's line: %+v", got)
	}
	// By time: the last 20 minutes leave it out.
	from := time.Now().Add(-20 * time.Minute).UTC().Format(time.RFC3339)
	if got := w.search(t, "tok-logreader", logs, url.Values{"q": {"order"}, "from": {from}}, http.StatusOK); !slices.Equal(lineTexts(got.Lines), []string{"order 1235 placed"}) {
		t.Errorf("the last 20 minutes: %v", lineTexts(got.Lines))
	}

	// The Application in dev: both Components, newest first, a page at a
	// time until next is empty. The other Project's line never appears.
	app := "/v1/applications/" + w.app + "/logs/search"
	var all []string
	q := url.Values{"environment": {w.dev}, "q": {"order"}, "limit": {"1"}}
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("the search didn't end")
		}
		got := w.search(t, "tok-logreader", app, q, http.StatusOK)
		if got.Omitted == nil || *got.Omitted != 0 {
			t.Errorf("omitted: %v", got.Omitted)
		}
		all = append(all, lineTexts(got.Lines)...)
		if got.Next == "" {
			break
		}
		q.Set("before", got.Next)
	}
	if want := []string{"order 1235 placed", "order 1234 charged", "order 1234 placed"}; !slices.Equal(all, want) {
		t.Errorf("paged across the Application: %v, want %v", all, want)
	}
	if got := w.obs.lastSearch(); !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values([]string{w.webDev, w.apiDev}))) {
		t.Errorf("the provider was asked for %v", got)
	}

	// Caps and validation.
	for _, bad := range []url.Values{
		{"limit": {"501"}},
		{"before": {"not-a-cursor"}},
		{"from": {time.Now().Add(-4 * 24 * time.Hour).UTC().Format(time.RFC3339)}},
		{"from": {time.Now().UTC().Format(time.RFC3339)}, "to": {time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}},
		{"q": {strings.Repeat("x", 257)}},
	} {
		w.search(t, "tok-logreader", logs, bad, http.StatusBadRequest)
	}
	w.search(t, "tok-logreader", app, nil, http.StatusBadRequest) // no environment
	w.search(t, "tok-logreader", app, url.Values{"environment": {"nope"}}, http.StatusNotFound)

	// The CLI command and the MCP tool are the catalogue's operation.
	for _, op := range []catalog.Operation{operation(t, "GET /v1/deployments/{id}/logs/search"), operation(t, "GET /v1/deployments/{id}/logs/search").ForMCP()} {
		req, err := op.Request(map[string]any{"id": w.webDev, "q": "order 1234"})
		if err != nil {
			t.Fatal(err)
		}
		var got search023
		json.Unmarshal(w.call(t, "tok-logreader", req.Method, req.Path, nil, http.StatusOK), &got)
		if len(got.Lines) != 1 || got.Lines[0].Line != "order 1234 placed" {
			t.Errorf("%s: %+v", op.Name, got)
		}
	}
	req, err := operation(t, "GET /v1/applications/{id}/logs/search").ForMCP().Request(map[string]any{"id": w.app, "environment": w.dev, "q": "1234"})
	if err != nil {
		t.Fatal(err)
	}
	var viaMCP search023
	json.Unmarshal(w.call(t, "tok-logreader", req.Method, req.Path, nil, http.StatusOK), &viaMCP)
	if len(viaMCP.Lines) != 2 {
		t.Errorf("the MCP tool's Application search: %+v", viaMCP)
	}
}

type metrics023 struct {
	CPU         []reconcile.MetricPoint `json:"cpu"`
	Memory      []reconcile.MetricPoint `json:"memory"`
	CPURequest  *float64                `json:"cpuRequest"`
	MemoryLimit *float64                `json:"memoryLimit"`
	Unavailable []string                `json:"unavailable"`
	Summary     map[string]struct {
		Min, Avg, Max, Latest float64
		Points                int
	} `json:"summary"`
}

// TestSpec023Criterion6_Metrics: the last hour of CPU and memory, with a
// load that shows; what can't be measured reads "not available"; and the
// MCP tool gets a summary of at most 60 points.
func TestSpec023Criterion6_Metrics(t *testing.T) {
	w := newWorld023(t)
	w.obsReady()
	path := "/v1/deployments/" + w.webDev + "/metrics"
	var m metrics023
	json.Unmarshal(w.call(t, "tok-reader", http.MethodGet, path, nil, http.StatusOK), &m)
	if len(m.CPU) < 59 || len(m.Memory) != len(m.CPU) || m.CPURequest == nil || *m.CPURequest != 100 || m.MemoryLimit != nil {
		t.Errorf("an hour of metrics: %d CPU points, request %v, memory limit %v", len(m.CPU), m.CPURequest, m.MemoryLimit)
	}
	if s := m.Summary["cpu"]; s.Max != 900 || s.Min != 20 || s.Points != len(m.CPU) {
		t.Errorf("the load doesn't show in the CPU summary: %+v", s)
	}
	if !slices.ContainsFunc(m.Unavailable, func(s string) bool { return strings.HasPrefix(s, "requests:") }) {
		t.Errorf("request metrics aren't listed as unavailable: %v", m.Unavailable)
	}
	if q := w.obs.metricsQ[len(w.obs.metricsQ)-1]; q.To.Sub(q.From) != time.Hour {
		t.Errorf("the default range: %v", q.To.Sub(q.From))
	}

	// At least 15 minutes, and within retention.
	now := time.Now().UTC()
	w.call(t, "tok-reader", http.MethodGet, path+"?from="+now.Add(-10*time.Minute).Format(time.RFC3339), nil, http.StatusBadRequest)
	w.call(t, "tok-reader", http.MethodGet, path+"?from="+now.Add(-4*24*time.Hour).Format(time.RFC3339), nil, http.StatusBadRequest)

	// The MCP tool: a summary and at most 60 points, over a day.
	op := operation(t, "GET /v1/deployments/{id}/metrics").ForMCP()
	if slices.ContainsFunc(op.Params, func(p catalog.Param) bool { return p.Name == "summary" }) {
		t.Error("summary is an MCP parameter; it should always be on")
	}
	req, err := op.Request(map[string]any{"id": w.webDev, "from": now.Add(-24 * time.Hour).Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.Path, "summary=true") {
		t.Errorf("the MCP request: %s", req.Path)
	}
	var brief metrics023
	json.Unmarshal(w.call(t, "tok-reader", req.Method, req.Path, nil, http.StatusOK), &brief)
	if len(brief.CPU) > 60 || len(brief.CPU) == 0 || brief.Summary["cpu"].Max != 900 || brief.Summary["cpu"].Points < 1000 {
		t.Errorf("the MCP answer: %d points, summary %+v", len(brief.CPU), brief.Summary["cpu"])
	}
}

// TestSpec023Criterion7_BuildLogs: once a build's run is gone its logs are
// read from the store, oldest first, followed or not; without the package
// it's 410 as before.
func TestSpec023Criterion7_BuildLogs(t *testing.T) {
	for _, withObs := range []bool{false, true} {
		var w022 *world022
		var f *obs023
		if withObs {
			w := newWorld023(t)
			w.obsReady()
			w022, f = w.world022, w.obs
		} else {
			w022 = newWorld022(t)
		}
		w022.packageReady()
		comp := w022.createSource(t, "tok-devops", map[string]any{"repository": "https://example.com/shop/api"})
		b := w022.succeed(t, w022.trigger(t, comp.Metadata.ID, "").Metadata.ID)
		if f != nil {
			f.mu.Lock()
			for i, l := range []string{"STEP 1/2: FROM alpine", "STEP 2/2: RUN make", "pushed localhost:5000/shop/api"} {
				f.builds[b.Metadata.ID] = append(f.builds[b.Metadata.ID], reconcile.StoredLogLine{Time: time.Now().Add(time.Duration(i) * time.Second), Line: l})
			}
			f.mu.Unlock()
		}
		// The run expires: its pods are gone.
		w022.builder.RemoveBuild(context.Background(), b)

		logs := "/v1/builds/" + b.Metadata.ID + "/logs"
		if !withObs {
			if body := w022.call(t, "tok-logreader", http.MethodGet, logs, nil, http.StatusGone); !strings.Contains(string(body), "expired") {
				t.Errorf("without observability: %s", body)
			}
			continue
		}
		var got struct {
			Lines  []reconcile.LogLine `json:"lines"`
			Stored bool                `json:"stored"`
		}
		json.Unmarshal(w022.call(t, "tok-logreader", http.MethodGet, logs, nil, http.StatusOK), &got)
		if !got.Stored || len(got.Lines) != 3 || got.Lines[0].Line != "STEP 1/2: FROM alpine" || got.Lines[0].Instance != "build" {
			t.Errorf("stored build logs: %+v", got)
		}
		w022.call(t, "tok-reader", http.MethodGet, logs, nil, http.StatusForbidden)

		// Followed: the same lines, then the stream ends.
		srv := httptest.NewServer(w022.h)
		defer srv.Close()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+logs+"?follow=true", nil)
		req.Header.Set("Authorization", "Bearer tok-logreader")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var streamed []string
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			streamed = append(streamed, sc.Text())
		}
		resp.Body.Close()
		if len(streamed) != 4 || !strings.Contains(streamed[0], "FROM alpine") || streamed[3] != `{"end":"ended"}` {
			t.Errorf("followed stored logs: %v", streamed)
		}
	}
}

// TestSpec023Criterion7_BuildPodsGone: a finished run that's still there
// but whose step pods are gone (deleted by hand, evicted) answers its
// stored lines, not an empty list (T013, found on serendip-t018). A
// running build's empty read stays empty: its pods may not exist yet.
func TestSpec023Criterion7_BuildPodsGone(t *testing.T) {
	w := newWorld023(t)
	w.obsReady()
	w.packageReady()
	comp := w.createSource(t, "tok-devops", map[string]any{"repository": "https://example.com/shop/api"})
	b := w.succeed(t, w.trigger(t, comp.Metadata.ID, "").Metadata.ID)
	w.obs.mu.Lock()
	w.obs.builds[b.Metadata.ID] = []reconcile.StoredLogLine{{Time: time.Now(), Line: "pushed localhost:5000/shop/api"}}
	w.obs.mu.Unlock()
	r := w.builder.run(b.Metadata.ID)
	w.builder.mu.Lock()
	r.podsGone = true
	w.builder.mu.Unlock()

	var got struct {
		Lines  []reconcile.LogLine `json:"lines"`
		Stored bool                `json:"stored"`
	}
	json.Unmarshal(w.call(t, "tok-logreader", http.MethodGet, "/v1/builds/"+b.Metadata.ID+"/logs", nil, http.StatusOK), &got)
	if !got.Stored || len(got.Lines) != 1 || got.Lines[0].Line != "pushed localhost:5000/shop/api" {
		t.Errorf("a finished run without its pods: %+v", got)
	}

	// Without the package ready, the empty read is the answer, as before.
	w.registry.SetReadiness("observability", provider.Readiness{Ready: false, Reason: reconcile.ReasonNotInstalled})
	got.Stored, got.Lines = false, nil
	json.Unmarshal(w.call(t, "tok-logreader", http.MethodGet, "/v1/builds/"+b.Metadata.ID+"/logs", nil, http.StatusOK), &got)
	if got.Stored || len(got.Lines) != 0 {
		t.Errorf("without the package: %+v", got)
	}
}

// TestSpec023Criterion8_Authorization: every new route has a rule; a
// Reader sees metrics but not logs; a Log Reader scoped to one Project
// can't read another's logs; and an Application's search asks the
// provider only for the Deployments the caller may read, a person's or an
// agent's, saying how many it left out.
func TestSpec023Criterion8_Authorization(t *testing.T) {
	withAgentTokens(t)
	people["tok-onelog"] = identity.Principal{Subject: "sub-onelog", Name: "onelog"}
	t.Cleanup(func() { delete(people, "tok-onelog") })
	w := newWorld023(t)
	w.obsReady()
	// NewServer refuses a route without a rule, and
	// TestEveryRouteHasARule lists them; these answer, so they have one.
	routes := []string{"GET /v1/deployments/{id}/logs/search", "GET /v1/applications/{id}/logs/search", "GET /v1/deployments/{id}/metrics"}

	logs := "/v1/deployments/" + w.webDev + "/logs/search"
	if body := w.call(t, "tok-reader", http.MethodGet, logs, nil, http.StatusForbidden); !strings.Contains(string(body), policy.ActionComponentLogsRead) {
		t.Errorf("the Reader's logs refusal: %s", body)
	}
	w.call(t, "tok-reader", http.MethodGet, "/v1/applications/"+w.app+"/logs/search?environment="+w.dev, nil, http.StatusForbidden)
	w.call(t, "tok-reader", http.MethodGet, "/v1/deployments/"+w.webDev+"/metrics", nil, http.StatusOK)

	// The Log Reader at p20 can't reach the other Project.
	w.call(t, "tok-logreader", http.MethodGet, "/v1/deployments/"+w.otherDep+"/logs/search", nil, http.StatusForbidden)
	w.call(t, "tok-logreader", http.MethodGet, "/v1/applications/"+w.otherApp+"/logs/search?environment="+w.elsewhere, nil, http.StatusForbidden)
	w.call(t, "tok-logreader", http.MethodGet, "/v1/deployments/"+w.otherDep+"/metrics", nil, http.StatusForbidden)
	// Nor name its Environment for its own Application.
	w.call(t, "tok-logreader", http.MethodGet, "/v1/applications/"+w.app+"/logs/search?environment="+w.elsewhere, nil, http.StatusNotFound)

	// Reader at p20, logs on web's Deployment only.
	onelog := map[string]any{"type": "person", "id": "sub-onelog"}
	w.assign(t, onelog, policy.ReaderRoleID, resource.Scope{Kind: resource.KindProject, ID: w.proj})
	w.assign(t, onelog, policy.LogReaderRoleID, resource.Scope{Kind: resource.KindDeployment, ID: w.webDev})
	got := w.search(t, "tok-onelog", "/v1/applications/"+w.app+"/logs/search", url.Values{"environment": {w.dev}, "q": {"order"}}, http.StatusOK)
	if got.Omitted == nil || *got.Omitted != 1 || !slices.Equal(w.obs.lastSearch(), []string{w.webDev}) {
		t.Errorf("one readable Deployment: omitted %v, the provider asked for %v", got.Omitted, w.obs.lastSearch())
	}
	for _, l := range got.Lines {
		if l.Component != w.web {
			t.Errorf("a line from Component %s", l.Component)
		}
	}

	// An agent with logs on api's Deployment only, under its own roles.
	w.assign(t, agent023, policy.ReaderRoleID, resource.Scope{Kind: resource.KindProject, ID: w.proj})
	w.assign(t, agent023, policy.LogReaderRoleID, resource.Scope{Kind: resource.KindDeployment, ID: w.apiDev})
	got = w.search(t, "tok-agent", "/v1/applications/"+w.app+"/logs/search", url.Values{"environment": {w.dev}}, http.StatusOK)
	if got.Omitted == nil || *got.Omitted != 1 || !slices.Equal(w.obs.lastSearch(), []string{w.apiDev}) {
		t.Errorf("the agent's search: omitted %v, the provider asked for %v", got.Omitted, w.obs.lastSearch())
	}
	w.call(t, "tok-agent", http.MethodGet, logs, nil, http.StatusForbidden)

	// Every route is on the CLI and MCP.
	for _, r := range routes {
		if op := operation(t, r); len(op.CLI) == 0 || !op.MCP {
			t.Errorf("%s: CLI %v, MCP %v", r, op.CLI, op.MCP)
		}
	}
}

// TestSpec023Criterion11_Removal: removal states what's lost, needs an
// Approval, and afterwards the routes say the package isn't installed,
// expired build logs are 410 again, and builds still works.
func TestSpec023Criterion11_Removal(t *testing.T) {
	withAgentTokens(t)
	w := newWorld023(t)
	w.packageReady()
	w.assign(t, agent023, policy.ContributorRoleID, resource.RootScope)
	var inst resource.ProviderInstance
	json.Unmarshal(w.call(t, "tok-owner", http.MethodPost, "/v1/provider-instances", install(obsPackage, "observability"), http.StatusCreated), &inst)
	w.obsInst.setReady()
	reconcileOnce(t, w.instRecon, resource.KindProviderInstance, inst.Metadata.ID)
	w.call(t, "tok-logreader", http.MethodGet, "/v1/deployments/"+w.webDev+"/logs/search", nil, http.StatusOK)
	if body := w.call(t, "tok-owner", http.MethodGet, "/v1/packages", nil, http.StatusOK); !strings.Contains(string(body), removal023) {
		t.Errorf("the removal sentence: %s", body)
	}

	w.call(t, "tok-agent", http.MethodDelete, "/v1/provider-instances/"+inst.Metadata.ID, nil, http.StatusForbidden)
	req := w.requestApproval("tok-agent", deleteRequest(resource.Scope{Kind: resource.KindProviderInstance, ID: inst.Metadata.ID}, false, "unused"), http.StatusCreated)
	reconcileOnce(t, w.approvalRecon, resource.KindApproval, req.Metadata.ID)
	w.call(t, "tok-logreader", http.MethodGet, "/v1/deployments/"+w.webDev+"/logs/search", nil, http.StatusOK)
	w.call(t, "tok-owner", http.MethodPost, "/v1/approvals/"+req.Metadata.ID+"/approve", nil, http.StatusOK)
	for range 5 {
		reconcileOnce(t, w.approvalRecon, resource.KindApproval, req.Metadata.ID)
	}
	if _, err := w.instances.Get(context.Background(), inst.Metadata.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the ProviderInstance after removal: %v", err)
	}
	if installed, uninstalls := w.obsInst.state(); installed || uninstalls != 1 {
		t.Errorf("after removal: installed %v, uninstalls %d", installed, uninstalls)
	}
	for _, p := range []string{"/v1/deployments/" + w.webDev + "/logs/search", "/v1/deployments/" + w.webDev + "/metrics", "/v1/applications/" + w.app + "/logs/search?environment=" + w.dev} {
		if body := w.call(t, "tok-owner", http.MethodGet, p, nil, http.StatusServiceUnavailable); !strings.Contains(string(body), "isn't installed") {
			t.Errorf("%s after removal: %s", p, body)
		}
	}

	// Builds still works, and an expired run's logs are 410 as before.
	comp := w.createSource(t, "tok-devops", map[string]any{"repository": "https://example.com/shop/api"})
	b := w.succeed(t, w.trigger(t, comp.Metadata.ID, "").Metadata.ID)
	w.call(t, "tok-logreader", http.MethodGet, "/v1/builds/"+b.Metadata.ID+"/logs", nil, http.StatusOK)
	w.builder.RemoveBuild(context.Background(), b)
	w.call(t, "tok-logreader", http.MethodGet, "/v1/builds/"+b.Metadata.ID+"/logs", nil, http.StatusGone)
}
