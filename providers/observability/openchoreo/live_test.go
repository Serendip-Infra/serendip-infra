package openchoreo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store"
	"github.com/serendip-infra/serendip-infra/core/store/sqlite"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// The live tests run against a real cluster with OpenChoreo and running
// Components (specs/023-observability T006, again in T012/T013). They
// skip unless OBS_LIVE_KUBECONFIG names a kubeconfig. TestLiveQueries also
// needs OBS_LIVE_DB, a copy of serendipd's store, for the Serendip IDs.

func liveClient(t *testing.T) (*kubernetes.Client, string) {
	t.Helper()
	kc := os.Getenv("OBS_LIVE_KUBECONFIG")
	if kc == "" {
		t.Skip("OBS_LIVE_KUBECONFIG not set")
	}
	cfg, err := kubernetes.LoadConfig(kc)
	if err != nil {
		t.Fatal(err)
	}
	client, err := kubernetes.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return client, kc
}

// TestLiveInstall installs the package through the real installer, and
// repeats Install until it's Ready.
func TestLiveInstall(t *testing.T) {
	client, kc := liveClient(t)
	inst, err := NewInstaller(client, kc)
	if err != nil {
		t.Fatal(err)
	}
	// Spec 025: OBS_LIVE_FLOW_SOURCE "<dir>/<file>" installs with the
	// network layer's connection records collected.
	if fs := os.Getenv("OBS_LIVE_FLOW_SOURCE"); fs != "" {
		k := strings.LastIndex(fs, "/")
		if err := inst.SetFlowSource(reconcile.FlowSource{HostPath: fs[:k], File: fs[k+1:], Format: FlowFormat}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	var applied string
	for {
		state, err := inst.Install(ctx, resource.ProviderInstance{Status: resource.ProviderInstanceStatus{Applied: applied}})
		if err != nil {
			t.Logf("%s install: %v", time.Now().Format(time.TimeOnly), err)
		} else {
			t.Logf("%s ready=%v applied=%v: %s", time.Now().Format(time.TimeOnly), state.Ready, state.Applied != "", state.Message)
			applied = state.Applied
			if state.Ready {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("not ready in time")
		case <-time.After(30 * time.Second):
		}
	}
}

// TestLiveUninstall removes the package, repeating Uninstall while the
// plane object or the namespace is still being deleted.
func TestLiveUninstall(t *testing.T) {
	client, kc := liveClient(t)
	inst, err := NewInstaller(client, kc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	for {
		err := inst.Uninstall(ctx, "live")
		t.Logf("%s uninstall: %v", time.Now().Format(time.TimeOnly), err)
		if err == nil {
			return
		}
		if !errors.Is(err, ErrNamespaceRemaining) && !errors.Is(err, ErrPlaneRemaining) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("not removed in time")
		case <-time.After(10 * time.Second):
		}
	}
}

// liveTargets reads every Deployment with its Component and Environment
// from the store copy.
func liveTargets(t *testing.T, ctx context.Context) []reconcile.LogTarget {
	t.Helper()
	path := os.Getenv("OBS_LIVE_DB")
	if path == "" {
		t.Skip("OBS_LIVE_DB not set")
	}
	db, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	deps, err := sqlite.NewStore[resource.DeploymentSpec, resource.DeploymentStatus](db, resource.KindDeployment).List(ctx, store.ListOptions{Kind: resource.KindDeployment})
	if err != nil {
		t.Fatal(err)
	}
	comps := sqlite.NewStore[resource.ComponentSpec, resource.ComponentStatus](db, resource.KindComponent)
	envs := sqlite.NewStore[resource.EnvironmentSpec, resource.EnvironmentStatus](db, resource.KindEnvironment)
	var out []reconcile.LogTarget
	for _, d := range deps {
		var envID string
		for _, o := range d.Metadata.OwnerRefs {
			if o.Kind == resource.KindEnvironment {
				envID = o.ID
			}
		}
		c, err1 := comps.Get(ctx, d.Spec.Component)
		e, err2 := envs.Get(ctx, envID)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, reconcile.LogTarget{Deployment: d, Component: c, Environment: e})
	}
	return out
}

// TestLiveQueries checks the provider against the live adapters: the
// answers' shapes, the scope resolution, paging, units, the end bound, and
// disk use.
func TestLiveQueries(t *testing.T) {
	client, _ := liveClient(t)
	ctx := context.Background()
	targets := liveTargets(t, ctx)
	p := NewProvider(client)

	// Scope resolution from real Serendip IDs.
	var scoped []reconcile.LogTarget
	var scopes []componentScope
	for _, tg := range targets {
		s, err := p.scopes.component(ctx, tg)
		if err != nil {
			t.Logf("deployment %s (%s in %s): %v", tg.Deployment.Metadata.ID, tg.Component.Metadata.Name, tg.Environment.Metadata.Name, err)
			continue
		}
		scoped, scopes = append(scoped, tg), append(scopes, s)
	}
	t.Logf("%d of %d Deployments resolved to scopes", len(scoped), len(targets))
	if len(scoped) == 0 {
		t.Fatal("no Deployment resolved")
	}
	// Each resolved scope's UIDs must be those its pods carry.
	q := "labelSelector=" + labelComponent + "%3D" + scopes[0].ComponentUID + "," + labelEnvironment + "%3D" + scopes[0].EnvironmentUID
	pods, err := client.List(ctx, "/api/v1/pods?"+q)
	if err != nil || len(pods) == 0 {
		t.Fatalf("no pods carry the first scope's UIDs (%v): the derivation doesn't match the platform", err)
	}
	if got := pods[0].Metadata.Labels[labelProject]; got != scopes[0].ProjectUID {
		t.Errorf("project UID %s, pod says %s", scopes[0].ProjectUID, got)
	}
	if got := pods[0].Metadata.Labels[labelNamespace]; got != scopes[0].Namespace {
		t.Errorf("namespace %s, pod says %s", scopes[0].Namespace, got)
	}

	// The raw answer shapes.
	now := time.Now().UTC()
	logsPath := kubernetes.ServiceProxyPath(PlaneNamespace, logsAdapterService, logsAdapterPort) + "/api/v1/logs/query"
	raw, err := client.PostRaw(ctx, logsPath, logsRequest{StartTime: adapterTime(now.Add(-24 * time.Hour)), EndTime: adapterTime(now), Limit: 3, SortOrder: "desc", SearchScope: scopes[0]})
	if err != nil {
		t.Fatalf("raw logs query: %v", err)
	}
	t.Logf("raw logs answer: %.1500s", raw)
	var rawLogs struct {
		Logs []map[string]any `json:"logs"`
	}
	if err := json.Unmarshal(raw, &rawLogs); err != nil || len(rawLogs.Logs) == 0 {
		t.Fatalf("no logs for the first target (%v)", err)
	}
	for _, key := range []string{"timestamp", "log", "metadata"} {
		if _, ok := rawLogs.Logs[0][key]; !ok {
			t.Errorf("a log entry has no %q", key)
		}
	}
	md, _ := rawLogs.Logs[0]["metadata"].(map[string]any)
	for _, key := range []string{"componentUid", "environmentUid", "projectUid", "podName", "containerName"} {
		if _, ok := md[key]; !ok {
			t.Errorf("a log entry's metadata has no %q", key)
		}
	}

	// The end bound: is a line at exactly endTime returned?
	ts, _ := rawLogs.Logs[0]["timestamp"].(string)
	at, err := parseAdapterTime(ts)
	if err != nil {
		t.Fatalf("timestamp %q: %v", ts, err)
	}
	for _, end := range []time.Time{at, at.Add(time.Microsecond)} {
		raw, err := client.PostRaw(ctx, logsPath, logsRequest{StartTime: adapterTime(at.Add(-time.Second)), EndTime: adapterTime(end), Limit: 1000, SortOrder: "desc", SearchScope: scopes[0]})
		if err != nil {
			t.Fatal(err)
		}
		var a logsResponse
		_ = json.Unmarshal(raw, &a)
		found := slices.ContainsFunc(a.Logs, func(e logEntry) bool { return e.Timestamp == ts })
		t.Logf("end bound %s for a line at %s: line returned %v", adapterTime(end), ts, found)
	}

	// Paging across the resolved targets, compared with one big page.
	from := now.Add(-24 * time.Hour)
	ts3 := scoped[:min(3, len(scoped))]
	big, err := p.SearchLogs(ctx, ts3, reconcile.LogQuery{From: from, To: now, Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	var paged []reconcile.StoredLogLine
	before := ""
	for range 200 {
		page, err := p.SearchLogs(ctx, ts3, reconcile.LogQuery{From: from, To: now, Limit: 7, Before: before})
		if err != nil {
			t.Fatal(err)
		}
		paged = append(paged, page.Lines...)
		if page.Next == "" || len(paged) >= len(big.Lines) {
			break
		}
		before = page.Next
	}
	n := min(len(paged), len(big.Lines))
	t.Logf("%d lines in one page, the first %d paged by 7", len(big.Lines), n)
	if n == 0 {
		t.Fatal("no lines")
	}
	for k := range n {
		a, b := paged[k], big.Lines[k]
		if !a.Time.Equal(b.Time) || a.Line != b.Line || a.Component != b.Component {
			t.Errorf("line %d paged %v %s %q, one page %v %s %q", k, a.Time, a.Component, a.Line, b.Time, b.Component, b.Line)
			break
		}
	}
	comps := map[string]bool{}
	for _, tg := range ts3 {
		comps[tg.Component.Metadata.ID] = true
	}
	for _, l := range big.Lines {
		if !comps[l.Component] || l.Instance == "" {
			t.Errorf("line from %q instance %q", l.Component, l.Instance)
			break
		}
	}

	// Metrics: the raw shape, then units against the pod spec.
	metricsPath := kubernetes.ServiceProxyPath(PlaneNamespace, metricsAdapterService, metricsAdapterPort) + "/api/v1/metrics/query"
	raw, err = client.PostRaw(ctx, metricsPath, metricsRequest{Metric: "resource", StartTime: adapterTime(now.Add(-30 * time.Minute)), EndTime: adapterTime(now), Step: "5m", SearchScope: scopes[0]})
	if err != nil {
		t.Fatalf("raw metrics query: %v", err)
	}
	t.Logf("raw metrics answer: %.1200s", raw)
	m, err := p.Metrics(ctx, scoped[0], reconcile.MetricsQuery{From: now.Add(-time.Hour), To: now})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("metrics: %d cpu, %d memory points; cpu req %v lim %v, mem req %v lim %v; unavailable %q",
		len(m.CPU), len(m.Memory), deref(m.CPURequest), deref(m.CPULimit), deref(m.MemoryRequest), deref(m.MemoryLimit), m.Unavailable)
	if len(m.CPU) == 0 || len(m.Memory) == 0 {
		t.Error("no cpu or memory samples")
	}
	if len(m.Memory) > 0 {
		if v := m.Memory[len(m.Memory)-1].Value; v < 1 || v > 4096 {
			t.Errorf("memory %v MiB isn't plausible", v)
		}
	}
	wantCPU, wantMem := podRequests(t, pods[0])
	if m.CPURequest != nil && wantCPU > 0 && *m.CPURequest != wantCPU*float64(len(pods)) && *m.CPURequest != wantCPU {
		t.Errorf("cpu request %v m, the pod spec says %v m (%d pods)", *m.CPURequest, wantCPU, len(pods))
	}
	if m.MemoryRequest != nil && wantMem > 0 && *m.MemoryRequest != wantMem*float64(len(pods)) && *m.MemoryRequest != wantMem {
		t.Errorf("memory request %v MiB, the pod spec says %v MiB (%d pods)", *m.MemoryRequest, wantMem, len(pods))
	}

	// Build logs, for the stand-in step pod of Build "t006-live-build"
	// (the run's namespace and Argo's workflow label).
	if id := os.Getenv("OBS_LIVE_BUILD"); id != "" {
		var b resource.Build
		b.Metadata.ID = id
		bp, err := p.BuildLogs(ctx, b, reconcile.LogQuery{From: now.Add(-24 * time.Hour), To: now, Limit: 10})
		if err != nil {
			t.Fatalf("build logs: %v", err)
		}
		var all []reconcile.StoredLogLine
		all = append(all, bp.Lines...)
		for bp.Next != "" {
			if bp, err = p.BuildLogs(ctx, b, reconcile.LogQuery{From: now.Add(-24 * time.Hour), To: now, Limit: 10, Before: bp.Next}); err != nil {
				t.Fatal(err)
			}
			all = append(all, bp.Lines...)
		}
		seen := map[string]bool{}
		for _, l := range all {
			if seen[l.Line] || strings.Contains(l.Line, "argo=true") || !strings.HasPrefix(l.Line, "t006 build line ") {
				t.Errorf("build line %q (seen before %v)", l.Line, seen[l.Line])
			}
			seen[l.Line] = true
		}
		t.Logf("build logs: %d lines, first %v", len(all), all[:min(1, len(all))])
		if len(seen) != 25 {
			t.Errorf("%d distinct build lines, want 25", len(seen))
		}
	}

	// Disk use, with no credentials.
	inst := newInstaller(nil, client)
	u, err := inst.Usage(ctx)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	t.Logf("disk use: %+v", u)
	for _, svc := range []struct {
		name string
		port int
	}{{logsStoreService, logsStorePort}, {prometheusService, prometheusPort}} {
		raw, err := client.GetRaw(ctx, kubernetes.ServiceProxyPath(PlaneNamespace, svc.name, svc.port)+"/metrics")
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(l, "storage_") || strings.HasPrefix(l, "prometheus_tsdb_storage_blocks_bytes") || strings.HasPrefix(l, "prometheus_tsdb_wal_storage_size_bytes") {
				t.Logf("%s: %s", svc.name, l)
			}
		}
	}
}

// TestLiveStreamStats prints OpenObserve's stream stats, read over the
// port-forward the package uses with the admin Secret, and what Usage
// makes of them, to compare with a du of the volumes.
func TestLiveStreamStats(t *testing.T) {
	client, _ := liveClient(t)
	ctx := context.Background()
	inst := newInstaller(nil, client)
	h, err := logsStoreAuth(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://%s.%s:%d/api/%s/streams?type=logs", logsStoreService, PlaneNamespace, logsStorePort, logsStoreOrg), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = h
	hc, done := inst.logsHTTPClient()
	defer done()
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var out struct {
		List []struct {
			Name  string         `json:"name"`
			Stats map[string]any `json:"stats"`
		} `json:"list"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("status %d: %v", resp.StatusCode, err)
	}
	for _, s := range out.List {
		t.Logf("%s: %v", s.Name, s.Stats)
	}
	u, err := inst.Usage(ctx)
	t.Logf("usage %+v, %v", u, err)
	if err != nil {
		t.Error(err)
	}
}

func deref(v *float64) string {
	if v == nil {
		return "nil"
	}
	return strconv.FormatFloat(*v, 'f', 2, 64)
}

// podRequests reads the first container's requests: millicores and MiB.
func podRequests(t *testing.T, pod kubernetes.Object) (cpu, mem float64) {
	var spec struct {
		Containers []struct {
			Resources struct {
				Requests map[string]string `json:"requests"`
			} `json:"resources"`
		} `json:"containers"`
	}
	if json.Unmarshal(pod.Spec, &spec) != nil || len(spec.Containers) == 0 {
		return 0, 0
	}
	r := spec.Containers[0].Resources.Requests
	t.Logf("pod %s requests %v", pod.Metadata.Name, r)
	if c := r["cpu"]; strings.HasSuffix(c, "m") {
		cpu, _ = strconv.ParseFloat(strings.TrimSuffix(c, "m"), 64)
	} else if c != "" {
		v, _ := strconv.ParseFloat(c, 64)
		cpu = v * 1000
	}
	if m := r["memory"]; strings.HasSuffix(m, "Mi") {
		mem, _ = strconv.ParseFloat(strings.TrimSuffix(m, "Mi"), 64)
	} else if strings.HasSuffix(m, "Gi") {
		v, _ := strconv.ParseFloat(strings.TrimSuffix(m, "Gi"), 64)
		mem = v * 1024
	}
	return cpu, mem
}

// TestLiveNetworking (spec 025 T006) reads, for the store copy's
// Components, their request series, the traffic between them, the two
// request conditions and their connection records. OBS_LIVE_RECORD names
// a directory the answers are written to, for the unit tests.
func TestLiveNetworking(t *testing.T) {
	client, _ := liveClient(t)
	ctx := context.Background()
	targets := liveTargets(t, ctx)
	p := NewProvider(client).WithRequestMetrics(func(context.Context) (bool, string) { return true, "" })
	now := time.Now()
	record := func(name string, v any) {
		dir := os.Getenv("OBS_LIVE_RECORD")
		if dir == "" {
			return
		}
		b, _ := json.MarshalIndent(v, "", " ")
		if err := os.WriteFile(dir+"/"+name, b, 0o644); err != nil {
			t.Error(err)
		}
	}
	for _, tg := range targets {
		name := tg.Component.Metadata.Name
		m, err := p.Metrics(ctx, tg, reconcile.MetricsQuery{From: now.Add(-15 * time.Minute), To: now})
		if err != nil {
			t.Errorf("%s metrics: %v", name, err)
			continue
		}
		if m.Requests != nil {
			last := func(ps []reconcile.MetricPoint) string {
				if len(ps) == 0 {
					return "-"
				}
				return strconv.FormatFloat(ps[len(ps)-1].Value, 'f', 3, 64)
			}
			r := m.Requests
			t.Logf("%s requests: %d points; last rate %s/s, 5xx %s/s, p50 %s ms, p95 %s ms, p99 %s ms",
				name, len(r.Rate), last(r.Rate), last(r.ErrorRate), last(r.LatencyP50), last(r.LatencyP95), last(r.LatencyP99))
			record("t006-requests-"+name+".json", r)
		}
		t.Logf("%s unavailable: %q", name, m.Unavailable)
		for _, c := range []resource.AlertCondition{
			{Type: resource.ConditionTypeRequestErrorRate, RequestErrorRate: &resource.RequestErrorRateCondition{AbovePercent: 5, MinRequests: 10, WindowMinutes: 10}},
			{Type: resource.ConditionTypeRequestLatency, RequestLatency: &resource.RequestLatencyCondition{Percentile: 95, AboveMs: 100, WindowMinutes: 10}},
		} {
			v, err := p.EvaluateCondition(ctx, tg, c, 10*time.Minute)
			t.Logf("%s %s: value %.3f current %.3f err %v", name, c.Type, v.Value, v.Current, err)
		}
		page, err := p.Flows(ctx, []reconcile.LogTarget{tg}, reconcile.FlowQuery{From: now.Add(-30 * time.Minute), Limit: 20})
		if err != nil {
			t.Errorf("%s flows: %v", name, err)
			continue
		}
		for _, f := range page.Flows {
			t.Logf("%s flow %s %s %s %+v -> %+v %s/%d x%d", name, f.Time.Format(time.TimeOnly), f.Verdict, f.Reason, f.Source, f.Destination, f.Protocol, f.Port, f.Count)
		}
		t.Logf("%s flows: %d, next %q", name, len(page.Flows), page.Next)
		dropped, err := p.Flows(ctx, []reconcile.LogTarget{tg}, reconcile.FlowQuery{From: now.Add(-3 * time.Hour), DroppedOnly: true, Limit: 5})
		if err != nil {
			t.Errorf("%s dropped flows: %v", name, err)
			continue
		}
		for _, f := range dropped.Flows {
			t.Logf("%s dropped %s %s %+v -> %+v %s/%d x%d", name, f.Time.Format(time.TimeOnly), f.Reason, f.Source, f.Destination, f.Protocol, f.Port, f.Count)
		}
	}
	byEnv := map[string][]reconcile.LogTarget{}
	for _, tg := range targets {
		byEnv[tg.Environment.Metadata.ID] = append(byEnv[tg.Environment.Metadata.ID], tg)
	}
	for env, ts := range byEnv {
		edges, err := p.Traffic(ctx, ts, now.Add(-5*time.Minute), now)
		if err != nil {
			t.Errorf("traffic in %s: %v", env, err)
			continue
		}
		for _, e := range edges {
			t.Logf("traffic %s -> %s: %.3f/s, 5xx %.3f/s", e.From, e.To, e.Rate, e.ErrorRate)
		}
		record("t006-traffic.json", edges)
	}
}
