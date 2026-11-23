package openchoreo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// fakeTraceStore answers the trace queries tracing_read.go writes over
// its spans, as the store would: it reads the scopes, trace ID, bounds,
// filters and limit out of the SQL, so a query missing its scope reads
// everything.
type fakeTraceStore struct {
	mu    sync.Mutex
	spans []map[string]any
	// extra is added to every span answer, matching or not: a store that
	// ignored part of the query.
	extra []map[string]any
	// noStream answers code 20002; deleting answers the store's recorded
	// answer while a stream's deletion is queued (T007).
	noStream, deleting bool
	// noParentColumn answers code 20004 to SQL naming the parent column:
	// a stream no span with a parent has been written to.
	noParentColumn bool
	deletingAnswer []byte
	sqls           []string
	deletes        []string
}

var (
	sqlScopeRE   = regexp.MustCompile(`service_openchoreo_dev_component_uid = '([^']+)' AND service_openchoreo_dev_environment_uid = '([^']+)' AND service_openchoreo_dev_project_uid = '([^']+)'`)
	sqlTraceRE   = regexp.MustCompile(`trace_id = '([^']+)'`)
	sqlUpperRE   = regexp.MustCompile(`min\(start_time\) <= (\d+)`)
	sqlMinDurRE  = regexp.MustCompile(`max\(end_time\) - min\(start_time\) >= (\d+)`)
	sqlLimitRE   = regexp.MustCompile(`LIMIT (\d+)$`)
	sqlErrorsRE  = regexp.MustCompile(`HAVING .*THEN 1 ELSE 0 END\) = 1`)
	sqlFailedOne = regexp.MustCompile(`AND span_status = 'ERROR'$`)
)

func (s *fakeTraceStore) serve(t *testing.T) *http.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.Header.Get("Authorization") == "" {
			t.Errorf("no credentials on %s %s", r.Method, r.URL)
		}
		if r.Method == http.MethodDelete {
			s.deletes = append(s.deletes, r.URL.RequestURI())
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/default/_search" || r.URL.Query().Get("type") != "traces" {
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
		var body struct {
			Query struct {
				SQL       string `json:"sql"`
				StartTime int64  `json:"start_time"`
				EndTime   int64  `json:"end_time"`
				Size      int    `json:"size"`
			} `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sql := body.Query.SQL
		s.sqls = append(s.sqls, sql)
		if s.deleting {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write(s.deletingAnswer)
			return
		}
		if s.noParentColumn && strings.Contains(sql, "reference_parent_span_id") {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 20004, "message": "echoes " + sql})
			return
		}
		if s.noStream {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 20002, "message": "echoes " + sql})
			return
		}
		scopes := map[[3]string]bool{}
		for _, m := range sqlScopeRE.FindAllStringSubmatch(sql, -1) {
			scopes[[3]string{m[1], m[2], m[3]}] = true
		}
		own := func(sp map[string]any) bool {
			return scopes[[3]string{fmt.Sprint(sp[colComponentUID]), fmt.Sprint(sp[colEnvironmentUID]), fmt.Sprint(sp[colProjectUID])}]
		}
		traceID := ""
		if m := sqlTraceRE.FindStringSubmatch(sql); m != nil {
			traceID = m[1]
		}
		var in []map[string]any
		for _, sp := range s.spans {
			ts := num(sp["start_time"]) / 1000
			if ts < body.Query.StartTime || ts >= body.Query.EndTime {
				continue
			}
			if traceID != "" && sp["trace_id"] != traceID {
				continue
			}
			in = append(in, sp)
		}
		var hits []map[string]any
		switch {
		case strings.Contains(sql, "count(*) AS total"):
			n, mine := 0, 0
			for _, sp := range in {
				n++
				if own(sp) {
					mine++
				}
			}
			hits = []map[string]any{{"total": n, "own": mine}}
		case strings.Contains(sql, "count(DISTINCT trace_id)"):
			ids := map[any]bool{}
			for _, sp := range in {
				if own(sp) && (!sqlFailedOne.MatchString(sql) || sp["span_status"] == "ERROR") {
					ids[sp["trace_id"]] = true
				}
			}
			hits = []map[string]any{{"n": len(ids)}}
		case strings.Contains(sql, "GROUP BY trace_id"):
			type agg struct {
				id                  string
				start, end, n, errs int64
				root                string
				hasRoot             bool
			}
			by := map[string]*agg{}
			for _, sp := range in {
				if !own(sp) {
					continue
				}
				id := sp["trace_id"].(string)
				a := by[id]
				if a == nil {
					a = &agg{id: id, start: num(sp["start_time"])}
					by[id] = a
				}
				a.start, a.end, a.n = min(a.start, num(sp["start_time"])), max(a.end, num(sp["end_time"])), a.n+1
				if sp["span_status"] == "ERROR" {
					a.errs = 1
				}
				if p, _ := sp["reference_parent_span_id"].(string); p == "" || !strings.Contains(sql, "reference_parent_span_id") {
					a.root, a.hasRoot = sp["operation_name"].(string), true
				}
			}
			upper, _ := strconv.ParseInt(sqlUpperRE.FindStringSubmatch(sql)[1], 10, 64)
			var minDur int64
			if m := sqlMinDurRE.FindStringSubmatch(sql); m != nil {
				minDur, _ = strconv.ParseInt(m[1], 10, 64)
			}
			var rows []*agg
			for _, a := range by {
				if a.start <= upper && a.end-a.start >= minDur && (!sqlErrorsRE.MatchString(sql) || a.errs == 1) {
					rows = append(rows, a)
				}
			}
			sort.Slice(rows, func(i, j int) bool {
				if rows[i].start != rows[j].start {
					return rows[i].start > rows[j].start
				}
				return rows[i].id < rows[j].id
			})
			for _, a := range rows {
				h := map[string]any{"trace_id": a.id, "t_start": a.start, "t_end": a.end, "spans": a.n, "errors": a.errs}
				if a.hasRoot {
					h["root"] = a.root
				}
				hits = append(hits, h)
			}
		default:
			for _, sp := range in {
				if own(sp) {
					hits = append(hits, sp)
				}
			}
			sort.SliceStable(hits, func(i, j int) bool { return num(hits[i]["start_time"]) < num(hits[j]["start_time"]) })
			hits = append(hits, s.extra...)
		}
		if m := sqlLimitRE.FindStringSubmatch(sql); m != nil {
			n, _ := strconv.Atoi(m[1])
			hits = hits[:min(len(hits), n)]
		}
		hits = hits[:min(len(hits), body.Query.Size)]
		_ = json.NewEncoder(w).Encode(map[string]any{"hits": hits})
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}}}
}

func num(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

const recordedTrace = "b45e5fc32a336c39e18115a4851b3ade"

// traceFixture is the recorded trace (a caller span and srv's span under
// it) behind a provider whose clock is a minute after it.
func traceFixture(t *testing.T) (*Tracing, *fakeTraceStore, reconcile.LogTarget, reconcile.LogTarget) {
	t.Helper()
	var rec struct {
		Hits []map[string]any `json:"hits"`
	}
	dec := json.NewDecoder(strings.NewReader(string(readTestdata(t, "t001-openobserve-trace-spans.json"))))
	dec.UseNumber()
	if err := dec.Decode(&rec); err != nil {
		t.Fatal(err)
	}
	for _, h := range rec.Hits {
		for k, v := range h {
			if n, ok := v.(json.Number); ok {
				i, _ := n.Int64()
				h[k] = i
			}
		}
	}
	f := &fakeObserver{}
	srv, caller := f.liveTarget("srv", srvUID, true), f.liveTarget("caller", callerUID, false)
	store := &fakeTraceStore{spans: rec.Hits}
	newest := time.Unix(0, num(rec.Hits[0]["start_time"])).Add(time.Minute)
	client := newTestKubeClient(t, f.handler(t))
	now := func() time.Time { return newest }
	return &Tracing{client: client, scopes: newScopes(client, now), now: now, http: store.serve(t)}, store, srv, caller
}

func TestTraces_SearchFromRecording(t *testing.T) {
	tr, store, srv, caller := traceFixture(t)
	ctx := context.Background()
	page, err := tr.SearchTraces(ctx, []reconcile.LogTarget{srv}, reconcile.TraceQuery{From: tr.now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Traces) != 1 {
		t.Fatalf("traces = %+v", page.Traces)
	}
	got := page.Traces[0]
	// srv's span only: its 20.4 ms, and no root (the caller started it).
	if got.TraceID != recordedTrace || got.Spans != 1 || got.Root != "" || got.HasErrors ||
		got.Duration != 20407772*time.Nanosecond || !got.Start.Equal(time.Unix(0, 1795132722883835879)) {
		t.Errorf("srv's trace = %+v", got)
	}
	if !strings.Contains(store.sqls[0], "'"+srvUID+"'") || strings.Contains(store.sqls[0], callerUID) {
		t.Errorf("sql = %s", store.sqls[0])
	}
	page, err = tr.SearchTraces(ctx, []reconcile.LogTarget{caller}, reconcile.TraceQuery{})
	if err != nil || len(page.Traces) != 1 || !strings.HasPrefix(page.Traces[0].Root, "GET http://cmp-ac7df9f12c.") {
		t.Errorf("caller's traces = %+v, %v; want its root span", page.Traces, err)
	}
	// Limits are capped.
	store.sqls = nil
	if _, err := tr.SearchTraces(ctx, []reconcile.LogTarget{srv}, reconcile.TraceQuery{Limit: 5000}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(store.sqls[0], fmt.Sprintf("LIMIT %d", reconcile.MaxTraceQueryLimit+1)) {
		t.Errorf("an uncapped limit: %s", store.sqls[0])
	}
}

func TestTrace_SpansOfTheTargetOnly(t *testing.T) {
	tr, store, srv, caller := traceFixture(t)
	ctx := context.Background()
	got, err := tr.Trace(ctx, []reconcile.LogTarget{srv}, recordedTrace)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Spans) != 1 || got.Hidden != 1 || got.Truncated {
		t.Fatalf("trace = %+v; want srv's one span, the caller's counted", got)
	}
	sp := got.Spans[0]
	if sp.SpanID != "e56e3114e9141c71" || sp.ParentID != "26dbfdbda95fb545" || sp.Name != "GET /" || sp.Kind != "server" ||
		sp.Failed || sp.Component != "srv" || sp.Environment != "dev" || sp.Duration != 20407772*time.Nanosecond {
		t.Errorf("span = %+v", sp)
	}
	for k := range sp.Attributes {
		if strings.HasPrefix(k, "service_") {
			t.Errorf("a resource attribute shown: %s", k)
		}
	}
	// A guessed or another Component's trace ID is not found, whatever it
	// holds: the caller's own trace looked up as a Component with no span
	// in it.
	store.spans = slices.DeleteFunc(store.spans, func(sp map[string]any) bool { return sp[colComponentUID] == srvUID })
	if _, err := tr.Trace(ctx, []reconcile.LogTarget{srv}, recordedTrace); !errors.Is(err, reconcile.ErrTraceNotFound) {
		t.Errorf("another's trace = %v, want ErrTraceNotFound", err)
	}
	if _, err := tr.Trace(ctx, []reconcile.LogTarget{caller}, recordedTrace); err != nil {
		t.Errorf("caller's own = %v", err)
	}
	// An ID that isn't one never reaches the store.
	store.sqls = nil
	for _, id := range []string{"", "x' OR '1'='1", strings.Repeat("0", 32), strings.ToUpper(recordedTrace)} {
		if _, err := tr.Trace(ctx, []reconcile.LogTarget{caller}, id); !errors.Is(err, reconcile.ErrTraceNotFound) {
			t.Errorf("Trace(%q) = %v", id, err)
		}
	}
	if len(store.sqls) != 0 {
		t.Errorf("queried for a bad ID: %v", store.sqls)
	}
}

// What a span claims of identity is never shown: resource attributes
// (service_*) and span attributes named like the platform's.
func TestTrace_SpoofedAttributesRemoved(t *testing.T) {
	tr, store, srv, _ := traceFixture(t)
	for _, sp := range store.spans {
		if sp[colComponentUID] == srvUID {
			sp["http_route"] = "/orders"
			sp["http_status_code"] = int64(200)
			sp["k8s_pod_name"] = "victim-pod"
			sp["openchoreo_dev_component_uid"] = callerUID
			sp["attr_service_openchoreo_dev_component_uid"] = callerUID
			sp["container_name"] = "victim"
			sp["service_k8s_pod_name"] = "x"
		}
	}
	got, err := tr.Trace(context.Background(), []reconcile.LogTarget{srv}, recordedTrace)
	if err != nil {
		t.Fatal(err)
	}
	attrs := got.Spans[0].Attributes
	if len(attrs) != 2 || attrs["http_route"] != "/orders" || attrs["http_status_code"] != "200" {
		t.Errorf("attributes = %v; want only http_route and http_status_code", attrs)
	}
	// A store that answered another Component's span anyway: not shown.
	other := map[string]any{}
	for _, sp := range store.spans {
		if sp[colComponentUID] == callerUID {
			for k, v := range sp {
				other[k] = v
			}
		}
	}
	store.extra = []map[string]any{other}
	got, err = tr.Trace(context.Background(), []reconcile.LogTarget{srv}, recordedTrace)
	if err != nil || len(got.Spans) != 1 || got.Spans[0].SpanID != "e56e3114e9141c71" {
		t.Errorf("with the store answering another's span: %+v, %v", got, err)
	}
}

// span is a span of srv's in trace id, starting at start.
func span(id string, start time.Time, dur time.Duration, failed bool, n int) map[string]any {
	status := "OK"
	if failed {
		status = "ERROR"
	}
	return map[string]any{
		"trace_id": id, "span_id": fmt.Sprintf("%016x", n+1), "operation_name": "GET /",
		"start_time": start.UnixNano(), "end_time": start.Add(dur).UnixNano(), "span_status": status, "span_kind": "2",
		colComponentUID: srvUID, colEnvironmentUID: liveEnvUID, colProjectUID: liveProjectUID,
	}
}

func traceID(n int) string { return fmt.Sprintf("%032x", n+1) }

func TestTrace_SpanCap(t *testing.T) {
	tr, store, srv, _ := traceFixture(t)
	start := tr.now().Add(-10 * time.Minute)
	store.spans = nil
	for n := range reconcile.MaxTraceSpans + 5 {
		store.spans = append(store.spans, span(traceID(0), start.Add(time.Duration(n)*time.Microsecond), time.Millisecond, false, n))
	}
	got, err := tr.Trace(context.Background(), []reconcile.LogTarget{srv}, traceID(0))
	if err != nil || len(got.Spans) != reconcile.MaxTraceSpans || !got.Truncated || got.Hidden != 0 {
		t.Errorf("trace = %d spans, truncated %v, hidden %d, %v", len(got.Spans), got.Truncated, got.Hidden, err)
	}
}

// Pages follow each other without repeating or dropping a trace, ties
// included, and the filters are applied.
func TestTraces_PagesAndFilters(t *testing.T) {
	tr, store, srv, _ := traceFixture(t)
	base := tr.now().Add(-30 * time.Minute)
	store.spans = nil
	// Seven traces; 2, 3 and 4 start together; the odd ones failed; the
	// n-th lasts n ms.
	for n := range 7 {
		at := base.Add(time.Duration(n) * time.Minute)
		if n >= 2 && n <= 4 {
			at = base.Add(2 * time.Minute)
		}
		store.spans = append(store.spans, span(traceID(n), at, time.Duration(n+1)*time.Millisecond, n%2 == 1, n))
	}
	var seen []string
	q := reconcile.TraceQuery{Limit: 2}
	for range 10 {
		page, err := tr.SearchTraces(context.Background(), []reconcile.LogTarget{srv}, q)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range page.Traces {
			seen = append(seen, x.TraceID)
		}
		if page.Next == "" {
			break
		}
		q.Before = page.Next
	}
	want := []string{traceID(6), traceID(5), traceID(2), traceID(3), traceID(4), traceID(1), traceID(0)}
	if !slices.Equal(seen, want) {
		t.Errorf("pages = %v, want %v", seen, want)
	}
	page, err := tr.SearchTraces(context.Background(), []reconcile.LogTarget{srv},
		reconcile.TraceQuery{ErrorsOnly: true, MinDuration: 3 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, x := range page.Traces {
		ids = append(ids, x.TraceID)
		if !x.HasErrors || x.Duration < 3*time.Millisecond {
			t.Errorf("filtered trace %+v", x)
		}
	}
	if !slices.Equal(ids, []string{traceID(5), traceID(3)}) {
		t.Errorf("errors only, at least 3 ms = %v", ids)
	}
	if _, err := tr.SearchTraces(context.Background(), []reconcile.LogTarget{srv}, reconcile.TraceQuery{Before: "nope"}); !errors.Is(err, reconcile.ErrBadCursor) {
		t.Errorf("bad cursor = %v", err)
	}
}

func TestTraces_NothingStoredYet(t *testing.T) {
	tr, store, srv, _ := traceFixture(t)
	store.noStream = true
	page, err := tr.SearchTraces(context.Background(), []reconcile.LogTarget{srv}, reconcile.TraceQuery{})
	if err != nil || len(page.Traces) != 0 || page.Traces == nil {
		t.Errorf("no stream: %+v, %v; want an empty page", page, err)
	}
	if _, err := tr.Trace(context.Background(), []reconcile.LogTarget{srv}, recordedTrace); !errors.Is(err, reconcile.ErrTraceNotFound) {
		t.Errorf("no stream: Trace = %v", err)
	}
	c := resource.AlertCondition{Type: resource.ConditionTypeTraceErrors, TraceErrors: &resource.TraceErrorsCondition{Above: 0, WindowMinutes: 5}}
	if _, err := tr.EvaluateCondition(context.Background(), srv, c, 5*time.Minute); !errors.Is(err, reconcile.ErrNoData) {
		t.Errorf("no stream: condition = %v, want ErrNoData", err)
	}
}

func TestTraces_ErrorCondition(t *testing.T) {
	tr, store, srv, _ := traceFixture(t)
	now := tr.now()
	store.spans = nil
	for n := range 6 {
		// Two spans each; traces 0, 2 and 4 failed, 4 outside the window.
		at := now.Add(-time.Duration(n+1) * time.Minute)
		if n == 4 {
			at = now.Add(-time.Hour)
		}
		store.spans = append(store.spans, span(traceID(n), at, time.Millisecond, n%2 == 0, 2*n), span(traceID(n), at, time.Millisecond, false, 2*n+1))
	}
	c := resource.AlertCondition{Type: resource.ConditionTypeTraceErrors, TraceErrors: &resource.TraceErrorsCondition{Above: 1, WindowMinutes: 10}}
	v, err := tr.EvaluateCondition(context.Background(), srv, c, 10*time.Minute)
	if err != nil || v.Value != 2 || v.Current != 2 {
		t.Errorf("traceErrors = %+v, %v; want 2", v, err)
	}
	m := resource.AlertCondition{Type: resource.ConditionTypeRestarts, Restarts: &resource.RestartsCondition{Above: 1, WindowMinutes: 10}}
	var refused *reconcile.ConditionRefusedError
	if _, err := tr.EvaluateCondition(context.Background(), srv, m, 10*time.Minute); !errors.As(err, &refused) {
		t.Errorf("a restarts condition = %v, want refused", err)
	}
}

// The tracing installer.

type tracingEnv struct {
	api   *fakeAPI
	helm  *fakeHelm
	inst  *TracingInstaller
	store *fakeTraceStore
	now   time.Time
}

const collectorPodPath = planePodsPath + "/collector-1"

func newTracingEnv(t *testing.T, observability bool) *tracingEnv {
	t.Helper()
	e := &tracingEnv{api: newFakeAPI(), store: &fakeTraceStore{}, now: time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)}
	if observability {
		e.api.put(planePodsPath+"/openobserve-0", readyPod(logsStoreSelector))
		e.api.put(adminPath, kubernetes.Object{Kind: "Secret",
			Data: json.RawMessage(`{"ZO_ROOT_USER_EMAIL":"YQ==","ZO_ROOT_USER_PASSWORD":"Yg=="}`)})
	}
	e.helm = &fakeHelm{api: e.api, respond: func(args []string) ([]byte, error) {
		if args[0] == "upgrade" && args[2] == tracingRelease {
			pod := readyPod(collectorSelector)
			pod.Metadata.Name = "collector-1"
			e.api.put(collectorPodPath, pod)
		}
		return nil, nil
	}}
	e.inst = newTracingInstaller(e.helm.run, newTestKubeClient(t, e.api.handler(t)))
	e.inst.now = func() time.Time { return e.now }
	e.inst.storeHTTP = e.store.serve(t)
	e.inst.deleteWait = time.Millisecond
	return e
}

func TestTracingInstall_NeedsObservability(t *testing.T) {
	e := newTracingEnv(t, false)
	state, err := e.inst.Install(context.Background(), instance(""))
	if err != nil || state.Ready || !strings.Contains(state.Message, "observability package") || state.Applied != "" {
		t.Errorf("without observability: %+v, %v", state, err)
	}
	if len(e.helm.calls) != 0 || e.api.count("APPLY") != 0 {
		t.Errorf("installed something: %v", e.api.timeline())
	}
}

func TestTracingInstall_FirstAndResync(t *testing.T) {
	e := newTracingEnv(t, true)
	ctx := context.Background()
	state, err := e.inst.Install(ctx, instance(""))
	if err != nil || !state.Ready || state.Applied != TracingFingerprint() || state.Message != tracingReadyMessage {
		t.Fatalf("first install = %+v, %v", state, err)
	}
	cm, ok := e.api.get(configMapsPath + "/" + collectorConfigName)
	if !ok || !dataEqual(cm.Data, collectorConfigMap().Data) {
		t.Errorf("collector config = %s", cm.Data)
	}
	if tl := e.api.timeline(); slices.Index(tl, "APPLY "+configMapsPath+"/"+collectorConfigName) > slices.Index(tl, "HELM upgrade "+tracingRelease) {
		t.Errorf("the module before its config: %v", tl)
	}
	args := e.helm.calls[0]
	for _, want := range []string{"openobserve-standalone.enabled=false", "opentelemetry-collector.configMap.existingName=" + collectorConfigName,
		"opentelemetryCollectorCustomizations.tailSampling.enabled=false", "adapter.enabled=false",
		"opentelemetry-collector.podAnnotations.serendip-collector-config=" + collectorConfigHash()} {
		if !slices.Contains(args, want) {
			t.Errorf("helm args lack %q: %v", want, args)
		}
	}
	if slices.Contains(args, "--wait") {
		t.Errorf("Helm waits: %v", args)
	}
	// A resync with the same fingerprint and a ready collector: no writes.
	e.helm.calls = nil
	e.api.reset()
	state, err = e.inst.Install(ctx, instance(TracingFingerprint()))
	if err != nil || !state.Ready || len(e.helm.calls) != 0 || e.api.count("APPLY") != 0 || e.api.count("DELETE") != 0 {
		t.Errorf("resync = %+v, %v; %v", state, err, e.api.timeline())
	}
}

func TestTracingInstall_WaitsThenRepairs(t *testing.T) {
	e := newTracingEnv(t, true)
	ctx := context.Background()
	e.helm.respond = func([]string) ([]byte, error) { return nil, nil } // the collector never comes up
	state, err := e.inst.Install(ctx, instance(""))
	if err != nil || state.Ready || state.Message != "waiting for the trace collector to be ready" || state.Applied != TracingFingerprint() {
		t.Fatalf("install, collector down = %+v, %v", state, err)
	}
	e.helm.calls = nil
	e.now = e.now.Add(time.Minute)
	if state, _ := e.inst.Install(ctx, instance(TracingFingerprint())); state.Ready || len(e.helm.calls) != 0 {
		t.Errorf("inside the settle window: %+v, helm %v", state, e.helm.calls)
	}
	e.now = e.now.Add(settle)
	if _, err := e.inst.Install(ctx, instance(TracingFingerprint())); err != nil || len(e.helm.calls) != 1 {
		t.Errorf("past the window: %v, helm %v; want one install", err, e.helm.calls)
	}
}

func TestTracingInstall_ChangedConfigRestored(t *testing.T) {
	e := newTracingEnv(t, true)
	ctx := context.Background()
	if _, err := e.inst.Install(ctx, instance("")); err != nil {
		t.Fatal(err)
	}
	e.api.put(configMapsPath+"/"+collectorConfigName, kubernetes.Object{Kind: "ConfigMap",
		Metadata: kubernetes.ObjectMeta{Name: collectorConfigName},
		Data:     mustJSON(map[string]string{collectorConfigKey: "receivers: {}"})})
	e.helm.calls = nil
	state, err := e.inst.Install(ctx, instance(TracingFingerprint()))
	if err != nil || state.Ready || !strings.Contains(state.Message, "changed") {
		t.Errorf("changed config = %+v, %v", state, err)
	}
	cm, _ := e.api.get(configMapsPath + "/" + collectorConfigName)
	if !dataEqual(cm.Data, collectorConfigMap().Data) {
		t.Error("the config wasn't restored")
	}
	if _, ok := e.api.get(collectorPodPath); ok {
		t.Error("the collector wasn't restarted")
	}
	if len(e.helm.calls) != 0 {
		t.Errorf("Helm ran: %v", e.helm.calls)
	}
}

func TestTracingUninstall(t *testing.T) {
	e := newTracingEnv(t, true)
	ctx := context.Background()
	if _, err := e.inst.Install(ctx, instance("")); err != nil {
		t.Fatal(err)
	}
	e.helm.calls = nil
	if err := e.inst.Uninstall(ctx, "pi-1"); err != nil {
		t.Fatal(err)
	}
	if len(e.helm.calls) != 1 || e.helm.calls[0][0] != "uninstall" || e.helm.calls[0][1] != tracingRelease {
		t.Errorf("helm = %v", e.helm.calls)
	}
	if _, ok := e.api.get(configMapsPath + "/" + collectorConfigName); ok {
		t.Error("the config is left")
	}
	// Only the traces stream: the logs stream has the same name.
	if !slices.Equal(e.store.deletes, []string{"/api/default/streams/default?type=traces"}) {
		t.Errorf("store deletes = %v", e.store.deletes)
	}
	// It waits for the store to run the deletion, and gives up with the
	// context.
	e.inst.deleteWait = time.Hour
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := e.inst.Uninstall(cctx, "pi-1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("uninstall while the deletion runs = %v, want the context's error", err)
	}
	e.inst.deleteWait = time.Millisecond
	// Twice is clean, and with the store gone there's nothing to delete.
	e.api.del(adminPath)
	e.api.del(planePodsPath + "/openobserve-0")
	e.store.deletes = nil
	if err := e.inst.Uninstall(ctx, "pi-1"); err != nil || len(e.store.deletes) != 0 {
		t.Errorf("again, store gone = %v, deletes %v", err, e.store.deletes)
	}
}

func TestTracingFingerprint_CoversConfig(t *testing.T) {
	a := TracingFingerprint()
	if a != TracingFingerprint() || !strings.HasPrefix(a, "tracing-") {
		t.Errorf("fingerprint = %q", a)
	}
	saved := collectorConfig
	t.Cleanup(func() { collectorConfig = saved })
	collectorConfig += "\n# changed"
	if TracingFingerprint() == a {
		t.Error("a changed config kept the fingerprint")
	}
}

// The collector config keeps identity the collector's (spec criterion 8).
func TestCollectorConfig_Identity(t *testing.T) {
	for _, want := range []string{
		`keep_matching_keys(attributes, "^(service|telemetry|process|deployment)[.][a-z0-9_.]{1,128}$")`,
		`delete_matching_keys(attributes, "(?i)^[^a-z0-9]*service[^a-z0-9]")`,
		"- from: connection",
		"passthrough: false",
		"processors: [memory_limiter, transform/identity, k8s_attributes, filter/unattributed, batch]",
		`resource.attributes["openchoreo.dev/component-uid"] == nil`,
	} {
		if !strings.Contains(collectorConfig, want) {
			t.Errorf("collector config lacks %q", want)
		}
	}
	if strings.Contains(collectorConfig, "tail_sampling") {
		t.Error("tail sampling is on")
	}
	span := regexp.MustCompile(`(?i)^[^a-z0-9]*service[^a-z0-9]`)
	for key, drop := range map[string]bool{
		"service.openchoreo.dev/component-uid": true, "service_openchoreo_dev_environment_uid": true, "SERVICE.k8s.pod.name": true,
		"_service.x": true, "http.route": false, "services.count": false, "user.service": false,
	} {
		if span.MatchString(key) != drop {
			t.Errorf("span attribute %q dropped = %v, want %v", key, !drop, drop)
		}
	}
	re := regexp.MustCompile(`^(service|telemetry|process|deployment)[.][a-z0-9_.]{1,128}$`)
	for key, keep := range map[string]bool{
		"service.name": true, "telemetry.sdk.language": true, "deployment.environment": true,
		"k8s.pod.name": false, "K8S.pod.name": false, "openchoreo.dev/component-uid": false, "OpenChoreo.dev/component-uid": false,
		"openchoreo_dev_environment_uid": false, "container.id": false, "Service.name": false, "service.k8s/pod": false,
	} {
		if re.MatchString(key) != keep {
			t.Errorf("resource attribute %q kept = %v, want %v", key, !keep, keep)
		}
	}
}

// A removal's deletion still queued: reads find nothing, and an install
// waits rather than write spans the deletion would take.
func TestTracing_StreamBeingDeleted(t *testing.T) {
	tr, store, srv, _ := traceFixture(t)
	store.deleting, store.deletingAnswer = true, readTestdata(t, "t007-openobserve-stream-deleting.json")
	page, err := tr.SearchTraces(context.Background(), []reconcile.LogTarget{srv}, reconcile.TraceQuery{})
	if err != nil || len(page.Traces) != 0 {
		t.Errorf("search while deleting = %+v, %v", page, err)
	}
	if _, err := tr.Trace(context.Background(), []reconcile.LogTarget{srv}, recordedTrace); !errors.Is(err, reconcile.ErrTraceNotFound) {
		t.Errorf("open while deleting = %v", err)
	}

	e := newTracingEnv(t, true)
	e.store.deleting, e.store.deletingAnswer = true, store.deletingAnswer
	state, err := e.inst.Install(context.Background(), instance(""))
	if err != nil || state.Ready || !strings.Contains(state.Message, "being deleted") || len(e.helm.calls) != 0 || e.api.count("APPLY") != 0 {
		t.Errorf("install while deleting = %+v, %v; helm %v", state, err, e.helm.calls)
	}
	e.store.deleting = false
	if state, err := e.inst.Install(context.Background(), instance("")); err != nil || !state.Ready {
		t.Errorf("install once deleted = %+v, %v", state, err)
	}
}

// A stream with no parent column yet: every span is a root, and the
// listing still answers.
func TestTraces_NoParentColumnYet(t *testing.T) {
	tr, store, srv, _ := traceFixture(t)
	start := tr.now().Add(-5 * time.Minute)
	store.spans = []map[string]any{span(traceID(0), start, time.Millisecond, false, 0)}
	store.noParentColumn = true
	page, err := tr.SearchTraces(context.Background(), []reconcile.LogTarget{srv}, reconcile.TraceQuery{})
	if err != nil || len(page.Traces) != 1 || page.Traces[0].Root != "GET /" {
		t.Errorf("no parent column = %+v, %v", page, err)
	}
	if len(store.sqls) != 2 || !strings.Contains(store.sqls[1], rootNoParents+" AS root") {
		t.Errorf("sql = %q", store.sqls)
	}
}

// A trace's counts are the same on every page: one started before the
// cursor keeps its later spans (go-reviewer, T007).
func TestTraces_LaterSpansCountedOnLaterPages(t *testing.T) {
	tr, store, srv, _ := traceFixture(t)
	base := tr.now().Add(-30 * time.Minute)
	long := span(traceID(1), base, time.Millisecond, false, 0)
	child := span(traceID(1), base.Add(20*time.Minute), time.Millisecond, true, 1)
	child["reference_parent_span_id"] = long["span_id"]
	store.spans = []map[string]any{long, child, span(traceID(2), base.Add(10*time.Minute), time.Millisecond, false, 2)}
	page, err := tr.SearchTraces(context.Background(), []reconcile.LogTarget{srv}, reconcile.TraceQuery{Limit: 1})
	if err != nil || len(page.Traces) != 1 || page.Traces[0].TraceID != traceID(2) || page.Next == "" {
		t.Fatalf("page 1 = %+v, %v", page, err)
	}
	page, err = tr.SearchTraces(context.Background(), []reconcile.LogTarget{srv}, reconcile.TraceQuery{Limit: 1, Before: page.Next})
	if err != nil || len(page.Traces) != 1 {
		t.Fatalf("page 2 = %+v, %v", page, err)
	}
	if got := page.Traces[0]; got.TraceID != traceID(1) || got.Spans != 2 || !got.HasErrors || got.Duration != 20*time.Minute+time.Millisecond {
		t.Errorf("the trace straddling the cursor = %+v; want both spans", got)
	}
}

// A stream no attributed span has reached has no identity columns: reads
// find nothing rather than fail.
func TestTraces_NoIdentityColumnsYet(t *testing.T) {
	tr, _, srv, _ := traceFixture(t)
	srvStore := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 20004, "message": "Search field not found: service_openchoreo_dev_component_uid"})
	}))
	t.Cleanup(srvStore.Close)
	addr := srvStore.Listener.Addr().String()
	tr.http = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}}}
	if page, err := tr.SearchTraces(context.Background(), []reconcile.LogTarget{srv}, reconcile.TraceQuery{}); err != nil || len(page.Traces) != 0 {
		t.Errorf("search = %+v, %v", page, err)
	}
	if _, err := tr.Trace(context.Background(), []reconcile.LogTarget{srv}, recordedTrace); !errors.Is(err, reconcile.ErrTraceNotFound) {
		t.Errorf("open = %v", err)
	}
	c := resource.AlertCondition{Type: resource.ConditionTypeTraceErrors, TraceErrors: &resource.TraceErrorsCondition{Above: 0, WindowMinutes: 5}}
	if _, err := tr.EvaluateCondition(context.Background(), srv, c, 5*time.Minute); !errors.Is(err, reconcile.ErrNoData) {
		t.Errorf("condition = %v", err)
	}
}

func TestSpanKind_StringOrNumber(t *testing.T) {
	for raw, want := range map[string]string{`"2"`: "server", `3`: "client", `"9"`: "", `null`: ""} {
		if got := (spanRow{"span_kind": json.RawMessage(raw)}).kind(); got != want {
			t.Errorf("kind(%s) = %q, want %q", raw, got, want)
		}
	}
}
