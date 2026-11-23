package openchoreo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// fakeObserver stands in for the API server: the OpenChoreo objects the
// scopes are resolved from, the two adapters behind the Service proxy,
// and the stores' /metrics.
type fakeObserver struct {
	mu sync.Mutex
	// objects maps "plural/name" to {uid, project}.
	objects map[string][2]string
	// lines is the logs store, in its own order.
	lines []fakeLine
	// exclusiveEnd makes the store's end bound exclusive.
	exclusiveEnd bool
	// extra is added to every component answer, filtered or not: a
	// store answering lines from outside the scope.
	extra []logEntry
	// shuffleTies answers tied lines in a new order each time (RP-058).
	shuffleTies bool
	// tooLargeOver answers more than the client takes when a query asks
	// for more than this many lines (0: never).
	tooLargeOver int
	// failComp answers 500 to queries for this Component UID.
	failComp string
	// metrics answers metric=resource.
	metrics resourceMetrics
	// storeMetrics answers each store's /metrics, by Service name.
	storeMetrics map[string]string
	// noAdminSecret answers 404 for the logs store's admin Secret, and
	// emptyAdminSecret answers it with an empty password.
	noAdminSecret, emptyAdminSecret bool

	// prom answers Prometheus' /api/v1/query, with promStatus (0: 200).
	prom       string
	promStatus int
	// promCount, when set, answers count(...) queries instead of prom.
	promCount string
	// noTotal leaves total out of the logs answer.
	noTotal bool
	// promAnswer, when set, answers /api/v1/query and /api/v1/query_range
	// by the query, before prom (spec 025).
	promAnswer func(path string, q url.Values) string
	// deployments answers the Deployments list, by label selector.
	deployments map[string][]kubernetes.Object

	logRequests     []map[string]any
	metricsRequests []map[string]any
	promQueries     []url.Values
	gets            int
}

type fakeLine struct {
	compUID, envUID, run string
	at                   time.Time
	pod, line            string
}

func (f *fakeObserver) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		p := r.URL.Path
		const objs = "/apis/openchoreo.dev/v1alpha1/namespaces/" + controlPlaneNamespace + "/"
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(p, objs):
			f.gets++
			o, ok := f.objects[strings.TrimPrefix(p, objs)]
			if !ok {
				http.Error(w, `{"kind":"Status","code":404}`, http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"metadata": map[string]string{"name": p, "uid": o[0]},
				"spec":     map[string]any{"owner": map[string]string{"projectName": o[1]}},
			})
		case r.Method == http.MethodGet && p == "/api/v1/namespaces/"+PlaneNamespace+"/secrets/"+adminSecretName:
			if f.noAdminSecret {
				http.Error(w, `{"kind":"Status","code":404}`, http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"metadata": map[string]string{"name": adminSecretName},
				"data":     map[string][]byte{"ZO_ROOT_USER_EMAIL": []byte("admin@x"), "ZO_ROOT_USER_PASSWORD": []byte(map[bool]string{false: "pw!1Aa", true: ""}[f.emptyAdminSecret])},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/services/logs-adapter:9098/proxy/api/v1/logs/query"):
			f.answerLogs(t, w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/services/metrics-adapter:9099/proxy/api/v1/metrics/query"):
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.metricsRequests = append(f.metricsRequests, req)
			_ = json.NewEncoder(w).Encode(f.metrics)
		case r.Method == http.MethodGet && (strings.HasSuffix(p, "/services/"+prometheusService+":"+fmt.Sprint(prometheusPort)+"/proxy/api/v1/query") ||
			strings.HasSuffix(p, "/services/"+prometheusService+":"+fmt.Sprint(prometheusPort)+"/proxy/api/v1/query_range")):
			f.promQueries = append(f.promQueries, r.URL.Query())
			if f.promStatus != 0 {
				w.WriteHeader(f.promStatus)
			}
			if f.promAnswer != nil {
				_, _ = io.WriteString(w, f.promAnswer(p, r.URL.Query()))
				return
			}
			if f.promCount != "" && strings.HasPrefix(r.URL.Query().Get("query"), "count(") {
				_, _ = io.WriteString(w, f.promCount)
				return
			}
			_, _ = io.WriteString(w, f.prom)
		case r.Method == http.MethodGet && p == "/apis/apps/v1/deployments":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": f.deployments[r.URL.Query().Get("labelSelector")]})
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/proxy/metrics"):
			for svc, text := range f.storeMetrics {
				if strings.Contains(p, "/services/"+svc+":") {
					_, _ = io.WriteString(w, text)
					return
				}
			}
			http.Error(w, "no", http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected %s %s", r.Method, p)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}
}

func (f *fakeObserver) answerLogs(t *testing.T, w http.ResponseWriter, r *http.Request) {
	var req logsRequest
	raw, _ := io.ReadAll(r.Body)
	var asMap map[string]any
	_ = json.Unmarshal(raw, &asMap)
	f.logRequests = append(f.logRequests, asMap)
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Errorf("decode logs request: %v", err)
	}
	scope, _ := asMap["searchScope"].(map[string]any)
	start, err1 := time.Parse(time.RFC3339Nano, req.StartTime)
	end, err2 := time.Parse(time.RFC3339Nano, req.EndTime)
	if err1 != nil || err2 != nil || req.SortOrder != "desc" || req.Limit < 1 || req.Limit > adapterMaxLimit {
		t.Errorf("bad logs request %s", raw)
	}
	var match []fakeLine
	for _, l := range f.lines {
		if run, ok := scope["workflowRunName"].(string); ok {
			if l.run != run {
				continue
			}
		} else if l.compUID != scope["componentUid"] || l.envUID != scope["environmentUid"] {
			continue
		}
		if l.at.Before(start) || l.at.After(end) || (f.exclusiveEnd && l.at.Equal(end)) {
			continue
		}
		if req.SearchPhrase != "" && !strings.Contains(l.line, req.SearchPhrase) {
			continue
		}
		match = append(match, l)
	}
	if f.shuffleTies {
		rand.Shuffle(len(match), func(i, j int) { match[i], match[j] = match[j], match[i] })
	}
	sort.SliceStable(match, func(i, j int) bool { return match[i].at.After(match[j].at) })
	if f.failComp != "" && scope["componentUid"] == f.failComp {
		http.Error(w, `{"title":"internalServerError","message":"secret query echo"}`, http.StatusInternalServerError)
		return
	}
	if f.tooLargeOver > 0 && req.Limit > f.tooLargeOver {
		_, _ = io.WriteString(w, strings.Repeat(" ", 9<<20))
		return
	}
	total := len(match)
	if len(match) > req.Limit {
		match = match[:req.Limit]
	}
	var out []logEntry
	for _, l := range match {
		var e logEntry
		e.Timestamp, e.Log = l.at.Format(time.RFC3339Nano), l.line
		if l.run == "" {
			e.Metadata.ComponentUID, e.Metadata.EnvironmentUID, e.Metadata.PodName, e.Metadata.ContainerName = l.compUID, l.envUID, l.pod, "main"
			e.Metadata.ProjectUID = "uid-app-x"
		}
		out = append(out, e)
	}
	if _, ok := scope["workflowRunName"]; !ok {
		out = append(out, f.extra...)
	}
	answer := map[string]any{"logs": out, "total": total, "tookMs": 1}
	if f.noTotal {
		delete(answer, "total")
	}
	_ = json.NewEncoder(w).Encode(answer)
}

var t0 = time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

// target builds a target for Component comp in Environment env, and its
// platform objects in f with UIDs "uid-<comp>" and "uid-<env>".
func (f *fakeObserver) target(comp, env string) reconcile.LogTarget {
	if f.objects == nil {
		f.objects = map[string][2]string{}
	}
	f.objects["components/"+componentObjectName(comp)] = [2]string{"uid-" + comp, "app-x"}
	f.objects["projects/app-x"] = [2]string{"uid-app-x", ""}
	f.objects["environments/"+environmentObjectName(env)] = [2]string{"uid-" + env, ""}
	var t reconcile.LogTarget
	t.Deployment.Metadata.ID = "dep-" + comp + "-" + env
	t.Deployment.Spec.Component = comp
	t.Deployment.Metadata.OwnerRefs = []resource.OwnerRef{{Kind: resource.KindEnvironment, ID: env}}
	t.Component.Metadata.ID = comp
	t.Environment.Metadata.ID = env
	return t
}

func newTestProvider(t *testing.T, f *fakeObserver) *Provider {
	t.Helper()
	return newProvider(newTestKubeClient(t, f.handler(t)), func() time.Time { return t0.Add(time.Hour) })
}

// allPages reads every page of a search, failing on a loop.
func allPages(t *testing.T, read func(before string) (reconcile.LogPage, error)) []reconcile.StoredLogLine {
	t.Helper()
	var out []reconcile.StoredLogLine
	before := ""
	cursors := map[string]bool{}
	for range 500 {
		page, err := read(before)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		out = append(out, page.Lines...)
		if page.Next == "" {
			return out
		}
		if cursors[page.Next] {
			t.Fatalf("the cursor came back to %q", page.Next)
		}
		cursors[page.Next] = true
		before = page.Next
	}
	t.Fatal("too many pages")
	return nil
}

func TestSearchLogs_TwoTargetsMergedAcrossPagesWithTies(t *testing.T) {
	// Each row is a store's times, as offsets in ms from t0, for the two
	// targets; ties within and across targets straddle the pages.
	cases := []struct {
		name string
		a, b []int
	}{
		{"distinct", []int{1, 3, 5, 7, 9}, []int{2, 4, 6, 8}},
		{"ties across targets", []int{5, 5, 4, 3, 3, 1}, []int{5, 4, 4, 3, 2, 1}},
		{"one big tie", []int{7, 7, 7, 7, 7, 7, 7, 7}, []int{7, 7, 7, 7, 7}},
		{"tie at the oldest", []int{9, 2, 2, 2, 2}, []int{8, 2, 2, 2}},
		{"one target empty", []int{3, 3, 2, 1}, nil},
	}
	for _, tc := range cases {
		for _, exclusive := range []bool{false, true} {
			for _, limit := range []int{1, 2, 3, 5, 100} {
				t.Run(fmt.Sprintf("%s/exclusive=%v/limit=%d", tc.name, exclusive, limit), func(t *testing.T) {
					f := &fakeObserver{exclusiveEnd: exclusive}
					ta, tb := f.target("a", "dev"), f.target("b", "dev")
					want := map[string]bool{}
					add := func(comp string, offs []int) {
						for k, ms := range offs {
							line := fmt.Sprintf("%s-%d", comp, k)
							f.lines = append(f.lines, fakeLine{compUID: "uid-" + comp, envUID: "uid-dev",
								at: t0.Add(time.Duration(ms) * time.Millisecond), pod: comp + "-pod", line: line})
							want[line] = true
						}
					}
					add("a", tc.a)
					add("b", tc.b)
					p := newTestProvider(t, f)
					got := allPages(t, func(before string) (reconcile.LogPage, error) {
						page, err := p.SearchLogs(context.Background(), []reconcile.LogTarget{ta, tb},
							reconcile.LogQuery{From: t0.Add(-time.Hour), Limit: limit, Before: before})
						if len(page.Lines) > limit || page.Truncated {
							t.Errorf("page of %d lines, truncated %v", len(page.Lines), page.Truncated)
						}
						return page, err
					})
					seen := map[string]bool{}
					for k, l := range got {
						if seen[l.Line] {
							t.Errorf("duplicate %s", l.Line)
						}
						seen[l.Line] = true
						if k > 0 && l.Time.After(got[k-1].Time) {
							t.Errorf("line %d (%s) is newer than the one before", k, l.Line)
						}
						if wantComp, _, _ := strings.Cut(l.Line, "-"); l.Component != wantComp || l.Instance != wantComp+"-pod" || l.Container != "main" {
							t.Errorf("line %s attributed to %q/%q/%q", l.Line, l.Component, l.Instance, l.Container)
						}
					}
					if len(seen) != len(want) {
						t.Errorf("got %d distinct lines, want %d", len(seen), len(want))
					}
				})
			}
		}
	}
}

func TestSearchLogs_NeverQueriesOutsideItsTargets(t *testing.T) {
	f := &fakeObserver{}
	ta, tb := f.target("a", "dev"), f.target("b", "prod")
	f.target("other", "dev") // on the platform, but not given
	gone := f.target("gone", "dev")
	delete(f.objects, "components/"+componentObjectName("gone"))
	for _, l := range []fakeLine{
		{compUID: "uid-a", envUID: "uid-dev", at: t0, line: "a"},
		{compUID: "uid-b", envUID: "uid-prod", at: t0, line: "b"},
		{compUID: "uid-b", envUID: "uid-dev", at: t0, line: "b in dev"},
		{compUID: "uid-other", envUID: "uid-dev", at: t0, line: "other"},
	} {
		f.lines = append(f.lines, l)
	}
	// A store that answers a line from outside the scope anyway.
	var foreign logEntry
	foreign.Timestamp, foreign.Log = t0.Format(time.RFC3339Nano), "foreign"
	foreign.Metadata.ComponentUID, foreign.Metadata.EnvironmentUID, foreign.Metadata.ProjectUID = "uid-other", "uid-dev", "uid-app-x"
	otherProject := foreign
	otherProject.Log = "other project"
	otherProject.Metadata.ComponentUID, otherProject.Metadata.EnvironmentUID, otherProject.Metadata.ProjectUID = "uid-a", "uid-dev", "uid-elsewhere"
	f.extra = []logEntry{foreign, otherProject}

	p := newTestProvider(t, f)
	page, err := p.SearchLogs(context.Background(), []reconcile.LogTarget{ta, tb, gone}, reconcile.LogQuery{From: t0.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range page.Lines {
		lines = append(lines, l.Line)
	}
	slices.Sort(lines)
	if !slices.Equal(lines, []string{"a", "b"}) {
		t.Errorf("lines = %v, want [a b]", lines)
	}
	allowed := map[[2]string]bool{{"uid-a", "uid-dev"}: true, {"uid-b", "uid-prod"}: true}
	if len(f.logRequests) != 2 {
		t.Errorf("%d queries, want one per target on the platform", len(f.logRequests))
	}
	for _, req := range f.logRequests {
		s, _ := req["searchScope"].(map[string]any)
		key := [2]string{fmt.Sprint(s["componentUid"]), fmt.Sprint(s["environmentUid"])}
		if !allowed[key] || s["namespace"] != controlPlaneNamespace || s["projectUid"] != "uid-app-x" || len(s) != 4 {
			t.Errorf("a query outside the targets: %v", s)
		}
	}
}

func TestSearchLogs_ScopesAreCached(t *testing.T) {
	f := &fakeObserver{}
	ta := f.target("a", "dev")
	p := newTestProvider(t, f)
	for range 3 {
		if _, err := p.SearchLogs(context.Background(), []reconcile.LogTarget{ta}, reconcile.LogQuery{From: t0}); err != nil {
			t.Fatal(err)
		}
	}
	if f.gets != 3 {
		t.Errorf("%d GETs for three searches, want 3 (one per object, cached)", f.gets)
	}
}

func TestSearchLogs_LongLineCut(t *testing.T) {
	f := &fakeObserver{}
	ta := f.target("a", "dev")
	long := strings.Repeat("é", reconcile.MaxLogLineBytes) // 2 bytes a rune
	f.lines = []fakeLine{
		{compUID: "uid-a", envUID: "uid-dev", at: t0, line: long},
		{compUID: "uid-a", envUID: "uid-dev", at: t0.Add(-time.Second), line: "short"},
	}
	page, err := newTestProvider(t, f).SearchLogs(context.Background(), []reconcile.LogTarget{ta}, reconcile.LogQuery{From: t0.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Truncated || len(page.Lines) != 2 {
		t.Fatalf("truncated=%v, %d lines", page.Truncated, len(page.Lines))
	}
	if l := page.Lines[0].Line; len(l) > reconcile.MaxLogLineBytes || len(l) < reconcile.MaxLogLineBytes-3 || !utf8.ValidString(l) {
		t.Errorf("cut line is %d bytes, valid UTF-8 %v", len(l), utf8.ValidString(l))
	}
	if page.Lines[1].Line != "short" {
		t.Errorf("second line = %q", page.Lines[1].Line)
	}
}

func TestSearchLogs_RefusesBadInput(t *testing.T) {
	f := &fakeObserver{}
	p := newTestProvider(t, f)
	targets := make([]reconcile.LogTarget, reconcile.MaxLogTargets+1)
	if _, err := p.SearchLogs(context.Background(), targets, reconcile.LogQuery{}); !errors.Is(err, ErrTooManyTargets) {
		t.Errorf("too many targets: %v", err)
	}
	ta := f.target("a", "dev")
	if _, err := p.SearchLogs(context.Background(), []reconcile.LogTarget{ta}, reconcile.LogQuery{Before: "nope"}); !errors.Is(err, reconcile.ErrBadCursor) {
		t.Errorf("bad cursor: %v", err)
	}
	mismatched := ta
	mismatched.Component.Metadata.ID = "b"
	if _, err := p.SearchLogs(context.Background(), []reconcile.LogTarget{mismatched}, reconcile.LogQuery{}); err == nil {
		t.Error("a target whose Component isn't the Deployment's was accepted")
	}
	if len(f.logRequests) != 0 {
		t.Errorf("%d queries sent for refused searches", len(f.logRequests))
	}
}

func TestSearchLogs_TextAndRangePassedOn(t *testing.T) {
	f := &fakeObserver{}
	ta := f.target("a", "dev")
	f.lines = []fakeLine{
		{compUID: "uid-a", envUID: "uid-dev", at: t0, line: "GET /health"},
		{compUID: "uid-a", envUID: "uid-dev", at: t0.Add(-time.Minute), line: "error: boom"},
		{compUID: "uid-a", envUID: "uid-dev", at: t0.Add(-2 * time.Hour), line: "error: too old"},
	}
	page, err := newTestProvider(t, f).SearchLogs(context.Background(), []reconcile.LogTarget{ta},
		reconcile.LogQuery{From: t0.Add(-time.Hour), To: t0, Text: "error"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) != 1 || page.Lines[0].Line != "error: boom" || page.Next != "" {
		t.Errorf("page = %+v", page)
	}
	if f.logRequests[0]["searchPhrase"] != "error" {
		t.Errorf("phrase not passed on: %v", f.logRequests[0])
	}
}

func TestBuildLogs_ByRunWithoutArgoLines(t *testing.T) {
	f := &fakeObserver{}
	run := buildRunName("build-1")
	f.lines = []fakeLine{
		{run: run, at: t0, line: "Step 3/3 : done"},
		{run: run, at: t0.Add(-time.Second), line: `time="x" level=info msg="sub-process exited" argo=true error="<nil>"`},
		{run: run, at: t0.Add(-2 * time.Second), line: "Step 1/3 : FROM alpine"},
		{run: run, at: t0.Add(-3 * time.Second), line: `time="2026-11-14T14:49:07.011Z" level=info msg="Alloc=8445 TotalAlloc=15886 Sys=22614 NumGC=6 Goroutines=4"`},
		{run: run, at: t0.Add(-4 * time.Second), line: `msg="Alloc=1 is a line a build printed"`},
		{run: "build-other", at: t0, line: "someone else's"},
	}
	var b resource.Build
	b.Metadata.ID = "build-1"
	b.Metadata.OwnerRefs = []resource.OwnerRef{{Kind: resource.KindComponent, ID: "comp-1"}}
	page, err := newTestProvider(t, f).BuildLogs(context.Background(), b, reconcile.LogQuery{From: t0.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range page.Lines {
		lines = append(lines, l.Line)
		if l.Component != "comp-1" {
			t.Errorf("component = %q", l.Component)
		}
	}
	if !slices.Equal(lines, []string{"Step 3/3 : done", "Step 1/3 : FROM alpine", `msg="Alloc=1 is a line a build printed"`}) {
		t.Errorf("lines = %q", lines)
	}
	s, _ := f.logRequests[0]["searchScope"].(map[string]any)
	if s["namespace"] != buildsNamespace || s["workflowRunName"] != run || len(s) != 2 {
		t.Errorf("scope = %v", s)
	}
}

func TestMetrics_UnitsLimitsAndUnavailable(t *testing.T) {
	f := &fakeObserver{}
	ta := f.target("a", "dev")
	pts := func(vals ...float64) []point {
		var out []point
		for k, v := range vals {
			out = append(out, point{Timestamp: t0.Add(time.Duration(k) * time.Minute).Format(time.RFC3339), Value: v})
		}
		return out
	}
	f.metrics = resourceMetrics{
		CPUUsage:       pts(0.05, 0.1),
		MemoryUsage:    pts(10<<20, 20<<20),
		CPURequests:    pts(0.1, 0.25),
		MemoryRequests: pts(64 << 20),
		MemoryLimits:   pts(128 << 20),
	}
	m, err := newTestProvider(t, f).Metrics(context.Background(), ta, reconcile.MetricsQuery{From: t0, To: t0.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.CPU) != 2 || m.CPU[1].Value != 100 || m.Memory[1].Value != 20 {
		t.Errorf("cpu %v, memory %v: want millicores and MiB", m.CPU, m.Memory)
	}
	if m.CPURequest == nil || *m.CPURequest != 250 || m.MemoryRequest == nil || *m.MemoryRequest != 64 || *m.MemoryLimit != 128 || m.CPULimit != nil {
		t.Errorf("requests and limits = %v %v %v %v", m.CPURequest, m.CPULimit, m.MemoryRequest, m.MemoryLimit)
	}
	if !slices.Contains(m.Unavailable, "requests: the cluster's network layer doesn't provide request metrics") || !slices.Contains(m.Unavailable, "cpu limit: not set") {
		t.Errorf("unavailable = %q", m.Unavailable)
	}
	req := f.metricsRequests[0]
	s, _ := req["searchScope"].(map[string]any)
	if req["metric"] != "resource" || req["step"] != "1m" || s["componentUid"] != "uid-a" || s["environmentUid"] != "uid-dev" {
		t.Errorf("request = %v", req)
	}
	for _, r := range f.metricsRequests {
		if r["metric"] == "http" {
			t.Error("request metrics were queried")
		}
	}
}

func TestMetrics_AtMostMaxPoints(t *testing.T) {
	f := &fakeObserver{}
	ta := f.target("a", "dev")
	var many []point
	for k := range 500 {
		many = append(many, point{Timestamp: t0.Add(time.Duration(k) * time.Minute).Format(time.RFC3339), Value: float64(k)})
	}
	f.metrics = resourceMetrics{CPUUsage: many, MemoryUsage: many}
	m, err := newTestProvider(t, f).Metrics(context.Background(), ta, reconcile.MetricsQuery{From: t0, To: t0.Add(72 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.CPU) != reconcile.MaxMetricPoints || m.CPU[len(m.CPU)-1].Value != 499*1000 {
		t.Errorf("%d points, last %v: want the newest %d", len(m.CPU), m.CPU[len(m.CPU)-1], reconcile.MaxMetricPoints)
	}
	if step := f.metricsRequests[0]["step"]; step != "15m" {
		t.Errorf("step for 72h = %v, want 15m", step)
	}
}

func TestMetricsStep(t *testing.T) {
	cases := []struct {
		span, asked, want time.Duration
	}{
		{15 * time.Minute, 0, time.Minute},
		{299 * time.Minute, 0, time.Minute},
		{5 * time.Hour, 0, 2 * time.Minute}, // 301 points at 1m
		{24 * time.Hour, 0, 5 * time.Minute},
		{72 * time.Hour, 0, 15 * time.Minute},
		{time.Hour, 90 * time.Second, 2 * time.Minute},
		{time.Hour, time.Second, time.Minute},
	}
	for _, c := range cases {
		got := metricsStep(c.span, c.asked)
		if got != c.want {
			t.Errorf("metricsStep(%v, %v) = %v, want %v", c.span, c.asked, got, c.want)
		}
		if points := int(c.span/got) + 1; points > reconcile.MaxMetricPoints {
			t.Errorf("span %v at step %v is %d points", c.span, got, points)
		}
	}
}

func TestMetrics_GoneTargetIsUnavailable(t *testing.T) {
	f := &fakeObserver{}
	ta := f.target("a", "dev")
	delete(f.objects, "environments/"+environmentObjectName("dev"))
	m, err := newTestProvider(t, f).Metrics(context.Background(), ta, reconcile.MetricsQuery{From: t0, To: t0.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.metricsRequests) != 0 || len(m.Unavailable) == 0 {
		t.Errorf("queried %d times, unavailable %q", len(f.metricsRequests), m.Unavailable)
	}
}

// fakeStreams is the logs store's streams API, behind the port-forward.
type fakeStreams struct {
	mu     sync.Mutex
	body   string
	status int
	auths  []string
}

func (f *fakeStreams) client(t *testing.T) *http.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		if r.URL.Path != "/api/"+logsStoreOrg+"/streams" || r.URL.Query().Get("type") != "logs" {
			t.Errorf("streams request %s", r.URL)
		}
		if user, pass, ok := r.BasicAuth(); !ok || user != "admin@x" || pass != "pw!1Aa" {
			http.Error(w, `{"code":401}`, http.StatusUnauthorized)
			return
		}
		if f.status != 0 {
			http.Error(w, "no", f.status)
			return
		}
		_, _ = io.WriteString(w, f.body)
	}))
	t.Cleanup(srv.Close)
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}}}
}

const streamsBody = `{"list":[` +
	`{"name":"default","stream_type":"logs","stats":{"doc_num":9,"storage_size":40.5,"compressed_size":3.0,"index_size":0.5}},` +
	`{"name":"audit_logs","stream_type":"logs","stats":{"doc_num":0,"storage_size":0.0,"compressed_size":0.0,"index_size":0.0}}],"total":2}`

func TestUsage_SumsTheStoresAndCaches(t *testing.T) {
	f := &fakeObserver{storeMetrics: map[string]string{
		prometheusService: "prometheus_tsdb_storage_blocks_bytes 3145728\nprometheus_tsdb_wal_storage_size_bytes 1048577\n",
	}}
	streams := &fakeStreams{body: streamsBody}
	now := t0
	inst := newInstaller(nil, newTestKubeClient(t, f.handler(t)))
	inst.now = func() time.Time { return now }
	inst.logsHTTP = streams.client(t)
	u, err := inst.Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 3.5 MiB of logs and 4 MiB and a byte of metrics.
	if u.UsedMiB != 8 || u.CapacityMiB != 8192 || !u.ReadAt.Equal(t0) {
		t.Errorf("usage = %+v, want 8 MiB of 8192", u)
	}
	f.mu.Lock()
	f.storeMetrics[prometheusService] = "prometheus_tsdb_storage_blocks_bytes 0\n" // WAL gauge renamed
	f.mu.Unlock()
	now = t0.Add(10 * time.Second)
	if again, err := inst.Usage(context.Background()); err != nil || again != u {
		t.Errorf("within 30 s: %+v, %v; want the cached read", again, err)
	}
	now = t0.Add(time.Minute)
	if _, err := inst.Usage(context.Background()); err == nil || !strings.Contains(err.Error(), "prometheus_tsdb_wal_storage_size_bytes") {
		t.Errorf("a missing metric: %v", err)
	}
}

func TestUsage_LogsStoreAnswers(t *testing.T) {
	cases := []struct {
		name, body  string
		status      int
		noSecret    bool
		emptySecret bool
		want        string
	}{
		{name: "a renamed field", body: `{"list":[{"name":"default","stats":{"compressed_size":1}}]}`, want: "no compressed_size or index_size"},
		{name: "an empty Secret value", emptySecret: true, want: "missing a key"},
		{name: "a negative size", body: `{"list":[{"name":"default","stats":{"compressed_size":-1,"index_size":0}}]}`, want: "a size of -1"},
		{name: "an error status", status: http.StatusServiceUnavailable, want: "status 503"},
		{name: "no admin Secret", noSecret: true, want: "credentials"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeObserver{noAdminSecret: c.noSecret, emptyAdminSecret: c.emptySecret}
			streams := &fakeStreams{body: c.body, status: c.status}
			inst := newInstaller(nil, newTestKubeClient(t, f.handler(t)))
			inst.logsHTTP = streams.client(t)
			for range 2 { // the second answer is the cached one
				_, err := inst.Usage(context.Background())
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Errorf("err = %v, want %q", err, c.want)
				}
				if err != nil && strings.Contains(err.Error(), "pw!1Aa") {
					t.Error("the password is in the error")
				}
			}
		})
	}
}

func TestSumSamples_RefusesBadValues(t *testing.T) {
	for _, text := range []string{"x{a=\"b\" 1\n", "x\n", "x -1\n", "x NaN\n"} {
		if _, err := sumSamples([]byte(text), []string{"x"}); err == nil {
			t.Errorf("%q accepted", text)
		}
	}
	got, err := sumSamples([]byte("x_total 5\nx 1 1700000000000\nx{l=\"}\"} 2\n"), []string{"x"})
	if err != nil || got["x"] != 3 {
		t.Errorf("sum = %v, %v; want 3", got, err)
	}
}

// TestUsage_CapacityMatchesTheVolumes keeps capacityMiB equal to the
// volumes the charts are given.
func TestUsage_CapacityMatchesTheVolumes(t *testing.T) {
	gi := func(s string) int {
		var n int
		if _, err := fmt.Sscanf(s, "%dGi", &n); err != nil {
			t.Fatalf("volume size %q: %v", s, err)
		}
		return n << 10
	}
	var metricsSize string
	for _, line := range strings.Split(string(prometheusValues), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "size: "); ok {
			metricsSize = v
		}
	}
	if got := gi(logVolumeSize) + gi(metricsSize); got != capacityMiB {
		t.Errorf("volumes total %d MiB, capacityMiB is %d", got, capacityMiB)
	}
}

func TestSearchLogs_EdgesOfPaging(t *testing.T) {
	newEnv := func(t *testing.T) (*fakeObserver, reconcile.LogTarget, *Provider) {
		f := &fakeObserver{}
		ta := f.target("a", "dev")
		return f, ta, newTestProvider(t, f)
	}
	add := func(f *fakeObserver, at time.Time, line string) {
		f.lines = append(f.lines, fakeLine{compUID: "uid-a", envUID: "uid-dev", at: at, pod: "a-pod", line: line})
	}
	distinct := func(t *testing.T, got []reconcile.StoredLogLine) map[string]bool {
		t.Helper()
		seen := map[string]bool{}
		for k, l := range got {
			if seen[l.Line] {
				t.Errorf("duplicate %s", l.Line)
			}
			seen[l.Line] = true
			if k > 0 && l.Time.After(got[k-1].Time) {
				t.Errorf("%s is newer than the line before", l.Line)
			}
		}
		return seen
	}

	t.Run("a tie group bigger than one answer is cut, and paging ends", func(t *testing.T) {
		f, ta, p := newEnv(t)
		p.maxAnswer = 6
		for k := range 20 {
			add(f, t0, fmt.Sprintf("tie-%d", k))
		}
		add(f, t0.Add(-time.Second), "older")
		truncated := false
		got := allPages(t, func(before string) (reconcile.LogPage, error) {
			page, err := p.SearchLogs(context.Background(), []reconcile.LogTarget{ta}, reconcile.LogQuery{From: t0.Add(-time.Hour), Limit: 2, Before: before})
			truncated = truncated || page.Truncated
			return page, err
		})
		seen := distinct(t, got)
		if !truncated || !seen["older"] || len(seen) >= 21 {
			t.Errorf("truncated %v, %d lines, older found %v", truncated, len(seen), seen["older"])
		}
	})

	t.Run("full answers thinned by filtering lose nothing", func(t *testing.T) {
		f := &fakeObserver{}
		run := buildRunName("b1")
		want := 0
		for k := range 30 {
			text := fmt.Sprintf("step line %d", k)
			if k%3 != 0 {
				text = fmt.Sprintf(`level=info msg="x %d" argo=true`, k)
			} else {
				want++
			}
			f.lines = append(f.lines, fakeLine{run: run, at: t0.Add(-time.Duration(k/2) * time.Millisecond), line: text})
		}
		p := newTestProvider(t, f)
		var b resource.Build
		b.Metadata.ID = "b1"
		got := allPages(t, func(before string) (reconcile.LogPage, error) {
			return p.BuildLogs(context.Background(), b, reconcile.LogQuery{From: t0.Add(-time.Hour), Limit: 2, Before: before})
		})
		if seen := distinct(t, got); len(seen) != want {
			t.Errorf("%d lines, want %d", len(seen), want)
		}
	})

	t.Run("sub-microsecond times page by microsecond", func(t *testing.T) {
		f, ta, p := newEnv(t)
		for k := range 9 {
			// Three lines in each microsecond.
			add(f, t0.Add(time.Duration(k/3)*time.Microsecond+time.Duration(k%3)*100), fmt.Sprintf("l%d", k))
		}
		got := allPages(t, func(before string) (reconcile.LogPage, error) {
			return p.SearchLogs(context.Background(), []reconcile.LogTarget{ta}, reconcile.LogQuery{From: t0.Add(-time.Hour), Limit: 2, Before: before})
		})
		if seen := distinct(t, got); len(seen) != 9 {
			t.Errorf("%d lines, want 9", len(seen))
		}
		for _, l := range got {
			if l.Time.Nanosecond()%1000 != 0 {
				t.Errorf("%s at %v: not whole microseconds", l.Line, l.Time)
			}
		}
	})

	t.Run("limits default and clamp", func(t *testing.T) {
		f, ta, p := newEnv(t)
		for _, c := range []struct{ limit, ask int }{{0, defaultLogLimit + 1}, {5000, reconcile.MaxLogQueryLimit + 1}} {
			f.logRequests = nil
			if _, err := p.SearchLogs(context.Background(), []reconcile.LogTarget{ta}, reconcile.LogQuery{From: t0, Limit: c.limit}); err != nil {
				t.Fatal(err)
			}
			if got := f.logRequests[0]["limit"]; got != float64(c.ask) {
				t.Errorf("limit %d asked %v, want %d", c.limit, got, c.ask)
			}
		}
	})

	t.Run("a cursor past the range starts over; one before From ends", func(t *testing.T) {
		f, ta, p := newEnv(t)
		add(f, t0, "new")
		add(f, t0.Add(-time.Hour), "old")
		future := reconcile.LogCursor{Time: t0.Add(48 * time.Hour), Seen: 1}.Encode()
		page, err := p.SearchLogs(context.Background(), []reconcile.LogTarget{ta}, reconcile.LogQuery{From: t0.Add(-2 * time.Hour), Before: future})
		if err != nil || len(page.Lines) != 2 {
			t.Errorf("past the range: %d lines, %v", len(page.Lines), err)
		}
		early := reconcile.LogCursor{Time: t0.Add(-3 * time.Hour)}.Encode()
		page, err = p.SearchLogs(context.Background(), []reconcile.LogTarget{ta}, reconcile.LogQuery{From: t0.Add(-2 * time.Hour), Before: early})
		if err != nil || len(page.Lines) != 0 || page.Next != "" {
			t.Errorf("before From: %+v, %v", page, err)
		}
	})

	t.Run("a future To is now", func(t *testing.T) {
		f, ta, p := newEnv(t)
		add(f, t0.Add(2*time.Hour), "from the future") // now is t0+1h
		add(f, t0, "now")
		page, err := p.SearchLogs(context.Background(), []reconcile.LogTarget{ta}, reconcile.LogQuery{From: t0.Add(-time.Hour), To: t0.Add(24 * time.Hour)})
		if err != nil || len(page.Lines) != 1 || page.Lines[0].Line != "now" {
			t.Errorf("page = %+v, %v", page, err)
		}
	})

	t.Run("an answer too large is asked again smaller", func(t *testing.T) {
		f, ta, p := newEnv(t)
		f.tooLargeOver = 4
		for k := range 10 {
			add(f, t0.Add(-time.Duration(k)*time.Second), fmt.Sprintf("l%d", k))
		}
		got := allPages(t, func(before string) (reconcile.LogPage, error) {
			return p.SearchLogs(context.Background(), []reconcile.LogTarget{ta}, reconcile.LogQuery{From: t0.Add(-time.Hour), Limit: 7, Before: before})
		})
		if seen := distinct(t, got); len(seen) != 10 {
			t.Errorf("%d lines, want 10", len(seen))
		}
	})

	t.Run("a source that answered everything isn't asked again", func(t *testing.T) {
		f := &fakeObserver{}
		ta, tb := f.target("a", "dev"), f.target("b", "dev")
		for k := range 12 {
			f.lines = append(f.lines, fakeLine{compUID: "uid-a", envUID: "uid-dev", at: t0.Add(-time.Duration(k) * time.Second), line: fmt.Sprintf("a%d", k)})
		}
		f.lines = append(f.lines, fakeLine{compUID: "uid-b", envUID: "uid-dev", at: t0.Add(-30 * time.Second), line: "b0"})
		p := newTestProvider(t, f)
		p.maxAnswer = 3
		if _, err := p.SearchLogs(context.Background(), []reconcile.LogTarget{ta, tb}, reconcile.LogQuery{From: t0.Add(-time.Hour), Limit: 10}); err != nil {
			t.Fatal(err)
		}
		bQueries := 0
		for _, req := range f.logRequests {
			if s, _ := req["searchScope"].(map[string]any); s["componentUid"] == "uid-b" {
				bQueries++
			}
		}
		if bQueries != 1 || len(f.logRequests) < 3 {
			t.Errorf("b asked %d times in %d queries, want once", bQueries, len(f.logRequests))
		}
	})

	t.Run("one failing target fails the page without its body", func(t *testing.T) {
		f := &fakeObserver{failComp: "uid-b"}
		ta, tb := f.target("a", "dev"), f.target("b", "dev")
		_, err := newTestProvider(t, f).SearchLogs(context.Background(), []reconcile.LogTarget{ta, tb}, reconcile.LogQuery{From: t0})
		if err == nil || !strings.Contains(err.Error(), "status 500") || strings.Contains(err.Error(), "secret query echo") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("a cancelled search stops", func(t *testing.T) {
		f, ta, p := newEnv(t)
		// Resolve the scope first, so the cancel meets the query.
		if _, err := p.SearchLogs(context.Background(), []reconcile.LogTarget{ta}, reconcile.LogQuery{From: t0}); err != nil {
			t.Fatal(err)
		}
		f.logRequests = nil
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := p.SearchLogs(ctx, []reconcile.LogTarget{ta}, reconcile.LogQuery{From: t0}); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("a target given twice is read once", func(t *testing.T) {
		f, ta, p := newEnv(t)
		add(f, t0, "once")
		page, err := p.SearchLogs(context.Background(), []reconcile.LogTarget{ta, ta}, reconcile.LogQuery{From: t0.Add(-time.Hour)})
		if err != nil || len(page.Lines) != 1 || len(f.logRequests) != 1 {
			t.Errorf("%d lines in %d queries, %v", len(page.Lines), len(f.logRequests), err)
		}
	})

	t.Run("a Deployment of another Environment is refused", func(t *testing.T) {
		f, ta, p := newEnv(t)
		f.target("a", "prod")
		ta.Environment.Metadata.ID = "prod"
		if _, err := p.SearchLogs(context.Background(), []reconcile.LogTarget{ta}, reconcile.LogQuery{From: t0}); err == nil || len(f.logRequests) != 0 {
			t.Errorf("err %v after %d queries", err, len(f.logRequests))
		}
	})

	t.Run("ties in a new order each query: paging still ends, newest first", func(t *testing.T) {
		f := &fakeObserver{shuffleTies: true}
		ta, tb := f.target("a", "dev"), f.target("b", "dev")
		for k := range 15 {
			f.lines = append(f.lines,
				fakeLine{compUID: "uid-a", envUID: "uid-dev", at: t0.Add(-time.Duration(k/5) * time.Millisecond), line: fmt.Sprintf("a%d", k)},
				fakeLine{compUID: "uid-b", envUID: "uid-dev", at: t0.Add(-time.Duration(k/4) * time.Millisecond), line: fmt.Sprintf("b%d", k)})
		}
		p := newTestProvider(t, f)
		got := allPages(t, func(before string) (reconcile.LogPage, error) {
			return p.SearchLogs(context.Background(), []reconcile.LogTarget{ta, tb}, reconcile.LogQuery{From: t0.Add(-time.Hour), Limit: 3, Before: before})
		})
		for k, l := range got {
			if k > 0 && l.Time.After(got[k-1].Time) {
				t.Errorf("%s is newer than the line before", l.Line)
			}
		}
		if len(got) == 0 {
			t.Error("no lines")
		}
	})
}

func TestSearchLogs_MissingObjectsAreCachedBriefly(t *testing.T) {
	f := &fakeObserver{}
	gone := f.target("gone", "dev")
	delete(f.objects, "components/"+componentObjectName("gone"))
	now := t0
	p := newProvider(newTestKubeClient(t, f.handler(t)), func() time.Time { return now })
	for range 3 {
		if _, err := p.SearchLogs(context.Background(), []reconcile.LogTarget{gone}, reconcile.LogQuery{From: t0.Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if f.gets != 1 {
		t.Errorf("%d GETs for a missing object in 5 s, want 1", f.gets)
	}
	now = now.Add(10 * time.Second)
	_, _ = p.SearchLogs(context.Background(), []reconcile.LogTarget{gone}, reconcile.LogQuery{From: t0.Add(-time.Hour)})
	if f.gets != 2 {
		t.Errorf("%d GETs after the missing entry expired, want 2", f.gets)
	}
}

func TestUsage_FailureIsCachedToo(t *testing.T) {
	f := &fakeObserver{storeMetrics: map[string]string{
		prometheusService: "prometheus_tsdb_storage_blocks_bytes 0\nprometheus_tsdb_wal_storage_size_bytes 0\n",
	}}
	streams := &fakeStreams{body: streamsBody, status: http.StatusServiceUnavailable}
	now := t0
	inst := newInstaller(nil, newTestKubeClient(t, f.handler(t)))
	inst.now = func() time.Time { return now }
	inst.logsHTTP = streams.client(t)
	if _, err := inst.Usage(context.Background()); err == nil || !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("err = %v", err)
	}
	streams.mu.Lock()
	streams.status = 0
	streams.mu.Unlock()
	if _, err := inst.Usage(context.Background()); err == nil {
		t.Error("a failed read wasn't kept for 30 s")
	}
	now = now.Add(time.Minute)
	if _, err := inst.Usage(context.Background()); err != nil {
		t.Errorf("after 30 s: %v", err)
	}
}

func TestUsage_EmptyAndStatlessStreams(t *testing.T) {
	for _, body := range []string{`{"list":[],"total":0}`, `{"list":[{"name":"new"}],"total":1}`} {
		f := &fakeObserver{storeMetrics: map[string]string{
			prometheusService: "prometheus_tsdb_storage_blocks_bytes 0\nprometheus_tsdb_wal_storage_size_bytes 0\n",
		}}
		inst := newInstaller(nil, newTestKubeClient(t, f.handler(t)))
		inst.logsHTTP = (&fakeStreams{body: body}).client(t)
		if u, err := inst.Usage(context.Background()); err != nil || u.UsedMiB != 0 {
			t.Errorf("%s: %+v, %v", body, u, err)
		}
	}
}

// TestLogsModuleValuesStayOffline pins the settings that keep the logs
// store from calling out (seen live in T006).
func TestLogsModuleValuesStayOffline(t *testing.T) {
	var args []string
	for _, r := range releases {
		if r.name == logsRelease {
			args = r.installArgs()
		}
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"openobserve-standalone.config.ZO_TELEMETRY=false",
		"openobserve-standalone.config.ZO_MMDB_DISABLE_DOWNLOAD=true",
		"openobserve-standalone.extraEnv[0].name=ZO_MODEL_PRICING_ENABLED",
		"openobserve-standalone.extraEnv[0].value=false",
		"openobserve-standalone.extraEnv[1].name=ZO_SSRF_ALLOW_LOOPBACK",
		"openobserve-standalone.extraEnv[1].value=true",
	} {
		if !strings.Contains(joined, "--set-string "+want) {
			t.Errorf("the logs module isn't given --set-string %s", want)
		}
	}
}
