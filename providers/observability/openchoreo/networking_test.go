package openchoreo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// Spec 025 T006: request metrics, traffic and flows, against answers
// recorded live on t025 (testdata/t006-*). The t006-flows-hits.json
// records are the node's Hubble export, flattened as the Lua filter
// does (fluent-bit/serendip-flows.lua).

// The live test bed's UIDs: srv, caller and spoofer in one Environment.
const (
	liveEnvUID     = "96c5a7b1-d0ad-4b25-a5aa-82721deebb44"
	liveProjectUID = "83d34c46-c27b-4571-b8f1-278128ac4177"
	srvUID         = "2f248807-64e8-456f-9a73-8daf80436acd"
	callerUID      = "ce92de52-7552-4da1-a743-416524622372"
	spooferUID     = "a6af1c83-fe86-4f14-9cc8-ce2286d60272"
	liveNamespace  = "dp-openchoreo-co-app-7781ea963-env-8582189a-5f623755"
)

// liveTarget is a target with the test bed's UIDs; exposed gives it an
// HTTP endpoint.
func (f *fakeObserver) liveTarget(comp, uid string, exposed bool) reconcile.LogTarget {
	if f.objects == nil {
		f.objects = map[string][2]string{}
	}
	f.objects["components/"+componentObjectName(comp)] = [2]string{uid, "app-x"}
	f.objects["projects/app-x"] = [2]string{liveProjectUID, ""}
	f.objects["environments/"+environmentObjectName("dev")] = [2]string{liveEnvUID, ""}
	var t reconcile.LogTarget
	t.Deployment.Metadata.ID = "dep-" + comp
	t.Deployment.Spec.Component = comp
	t.Deployment.Metadata.OwnerRefs = []resource.OwnerRef{{Kind: resource.KindEnvironment, ID: "dev"}}
	t.Component.Metadata.ID = comp
	t.Component.Spec.Ports = []resource.ComponentPort{{Name: "http", Port: 80, Expose: exposed}}
	t.Environment.Metadata.ID = "dev"
	return t
}

func available(context.Context) (bool, string) { return true, "" }

func TestMetrics_RequestSeriesFromRecording(t *testing.T) {
	rate := string(readTestdata(t, "t006-prometheus-requests-rate.json"))
	p95 := string(readTestdata(t, "t006-prometheus-requests-p95.json"))
	f := &fakeObserver{promAnswer: func(path string, q url.Values) string {
		if !strings.HasSuffix(path, "/query_range") {
			t.Errorf("an instant query for a series: %s", q.Get("query"))
		}
		if strings.HasPrefix(q.Get("query"), "histogram_quantile(") {
			return p95
		}
		return rate
	}}
	srv := f.liveTarget("srv", srvUID, true)
	p := newTestProvider(t, f).WithRequestMetrics(available)
	now := t0.Add(time.Hour)
	m, err := p.Metrics(context.Background(), srv, reconcile.MetricsQuery{From: now.Add(-10 * time.Minute), To: now})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range m.Unavailable {
		if strings.HasPrefix(u, "requests") {
			t.Errorf("unavailable: %q", u)
		}
	}
	var recorded promResponse
	_ = json.Unmarshal([]byte(rate), &recorded)
	n := len(recorded.Data.Result[0].Values)
	r := m.Requests
	if r == nil || n < 5 || len(r.Rate) != n || len(r.LatencyP95) != n {
		t.Fatalf("requests = %+v, want %d points", r, n)
	}
	if v := r.Rate[0].Value; math.Abs(v-5.5788) > 0.001 {
		t.Errorf("rate = %v, want the recorded 5.5788/s", v)
	}
	if v := r.LatencyP95[0].Value; math.Abs(v-24.53) > 0.01 {
		t.Errorf("p95 = %v ms, want the recorded 24.53", v)
	}
	if !r.Rate[0].Time.Equal(time.Unix(1795355576, 0)) {
		t.Errorf("first point at %v", r.Rate[0].Time)
	}
	// Five range queries: rate, 5xx, three percentiles, each scoped by
	// all three UIDs on the server's side, at the metrics step.
	var queries []string
	for _, q := range f.promQueries {
		queries = append(queries, q.Get("query"))
		if q.Get("step") != "60" {
			t.Errorf("step = %q", q.Get("step"))
		}
	}
	if len(queries) != 5 {
		t.Fatalf("queries = %q", queries)
	}
	scope := `kube_pod_labels{label_openchoreo_dev_component_uid="` + srvUID + `",label_openchoreo_dev_environment_uid="` + liveEnvUID +
		`",label_openchoreo_dev_project_uid="` + liveProjectUID + `"}`
	want := []string{
		`sum((label_replace(label_replace(rate(hubble_http_requests_total{reporter="server"}[2m]), "namespace", "$1", "destination_namespace", "(.+)"), "pod", "$1", "destination_pod", "(.+)") * on (namespace, pod) group_left() max by (namespace, pod) (` + scope + `)))`,
		`status=~"5.."`, `histogram_quantile(0.5, `, `histogram_quantile(0.95, `, `histogram_quantile(0.99, `,
	}
	if !slices.Contains(queries, want[0]) {
		t.Errorf("no rate query %s in %q", want[0], queries)
	}
	for _, w := range want[1:] {
		if !slices.ContainsFunc(queries, func(q string) bool { return strings.Contains(q, w) && strings.Contains(q, scope) }) {
			t.Errorf("no scoped query with %s", w)
		}
	}
	// The 5xx series is 0 while requests succeed, not missing.
	if !slices.ContainsFunc(queries, func(q string) bool { return strings.Contains(q, `status=~"5.."`) && strings.Contains(q, ") or (sum(") }) {
		t.Error("the 5xx query has no zero fallback")
	}
}

func TestMetrics_RequestsUnavailable(t *testing.T) {
	cases := []struct {
		name      string
		requests  func(context.Context) (bool, string)
		exposed   bool
		want      string
		promCalls bool
	}{
		{"not wired", nil, true, "requests: the cluster's network layer doesn't provide request metrics", false},
		{"network layer", func(context.Context) (bool, string) {
			return false, "the cluster's CNI doesn't provide request metrics"
		}, true,
			"requests: the cluster's CNI doesn't provide request metrics", false},
		{"TCP only", available, false, "requests: no HTTP endpoint", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeObserver{promAnswer: func(string, url.Values) string { return `{}` }}
			tg := f.liveTarget("srv", srvUID, c.exposed)
			p := newTestProvider(t, f)
			if c.requests != nil {
				p.WithRequestMetrics(c.requests)
			}
			now := t0.Add(time.Hour)
			m, err := p.Metrics(context.Background(), tg, reconcile.MetricsQuery{From: now.Add(-10 * time.Minute), To: now})
			if err != nil {
				t.Fatal(err)
			}
			if m.Requests != nil || !slices.Contains(m.Unavailable, c.want) {
				t.Errorf("requests %+v, unavailable %q; want %q", m.Requests, m.Unavailable, c.want)
			}
			if len(f.promQueries) != 0 {
				t.Errorf("queried Prometheus: %v", f.promQueries)
			}
		})
	}
}

func TestMetrics_RequestReadFailureKeepsCPU(t *testing.T) {
	f := &fakeObserver{promStatus: http.StatusInternalServerError, prom: `{"status":"error","error":"secret query text"}`,
		metrics: resourceMetrics{CPUUsage: []point{{Timestamp: t0.Format(time.RFC3339), Value: 0.1}}}}
	tg := f.liveTarget("srv", srvUID, true)
	p := newTestProvider(t, f).WithRequestMetrics(available)
	m, err := p.Metrics(context.Background(), tg, reconcile.MetricsQuery{From: t0.Add(-time.Minute), To: t0.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.CPU) != 1 || m.Requests != nil || !slices.Contains(m.Unavailable, "requests: the metrics store couldn't be read") {
		t.Errorf("metrics = %+v", m)
	}
	for _, u := range m.Unavailable {
		if strings.Contains(u, "secret") {
			t.Errorf("the store's answer leaked: %q", u)
		}
	}
}

func TestMetrics_NoRequestsInRange(t *testing.T) {
	f := &fakeObserver{promAnswer: func(string, url.Values) string {
		return `{"status":"success","data":{"resultType":"matrix","result":[]}}`
	}}
	tg := f.liveTarget("srv", srvUID, true)
	p := newTestProvider(t, f).WithRequestMetrics(available)
	m, err := p.Metrics(context.Background(), tg, reconcile.MetricsQuery{From: t0, To: t0.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if m.Requests == nil || len(m.Requests.Rate) != 0 || !slices.Contains(m.Unavailable, "requests: none in this range") {
		t.Errorf("metrics = %+v", m)
	}
}

func vec(v string) string {
	if v == "" {
		return `{"status":"success","data":{"resultType":"vector","result":[]}}`
	}
	return `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1795355576.1,"` + v + `"]}]}}`
}

func TestEvaluateCondition_RequestErrorRate(t *testing.T) {
	cases := []struct {
		name          string
		total, failed string
		minRequests   int
		want          float64
		wantErr       error
	}{
		{"rate", "1200", "108", 10, 9, nil},
		{"under minRequests", "8", "8", 10, 0, nil},
		{"no failures", "1200", "", 10, 0, nil},
		{"no requests", "0", "", 0, 0, nil},
		{"no series", "", "", 10, 0, reconcile.ErrNoData},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeObserver{promAnswer: func(_ string, q url.Values) string {
				if strings.Contains(q.Get("query"), `status=~"5.."`) {
					return vec(c.failed)
				}
				return vec(c.total)
			}}
			tg := f.liveTarget("srv", srvUID, true)
			p := newTestProvider(t, f).WithRequestMetrics(available)
			cond := resource.AlertCondition{Type: resource.ConditionTypeRequestErrorRate,
				RequestErrorRate: &resource.RequestErrorRateCondition{AbovePercent: 5, MinRequests: c.minRequests, WindowMinutes: 10}}
			v, err := p.EvaluateCondition(context.Background(), tg, cond, 10*time.Minute)
			if !errors.Is(err, c.wantErr) || (c.wantErr == nil && v.Value != c.want) {
				t.Errorf("= %+v, %v; want %v, %v", v, err, c.want, c.wantErr)
			}
			if q := f.promQueries[0].Get("query"); !strings.Contains(q, `increase(hubble_http_requests_total{reporter="server"}[10m])`) ||
				!strings.Contains(q, `label_openchoreo_dev_component_uid="`+srvUID+`"`) {
				t.Errorf("query = %s", q)
			}
		})
	}
}

func TestEvaluateCondition_RequestLatency(t *testing.T) {
	answer := vec("24.5")
	f := &fakeObserver{promAnswer: func(string, url.Values) string { return answer }}
	tg := f.liveTarget("srv", srvUID, true)
	p := newTestProvider(t, f).WithRequestMetrics(available)
	cond := resource.AlertCondition{Type: resource.ConditionTypeRequestLatency,
		RequestLatency: &resource.RequestLatencyCondition{Percentile: 99, AboveMs: 100, WindowMinutes: 5}}
	v, err := p.EvaluateCondition(context.Background(), tg, cond, 5*time.Minute)
	if err != nil || v.Value != 24.5 {
		t.Fatalf("= %+v, %v", v, err)
	}
	if q := f.promQueries[0].Get("query"); !strings.HasPrefix(q, "histogram_quantile(0.99, sum by (le) (") ||
		!strings.Contains(q, `increase(hubble_http_request_duration_seconds_bucket{reporter="server"}[5m])`) || !strings.HasSuffix(q, ") * 1000") {
		t.Errorf("query = %s", q)
	}
	// No requests in the window: the quantile is NaN, which is no data.
	answer = vec("NaN")
	if _, err := p.EvaluateCondition(context.Background(), tg, cond, 5*time.Minute); !errors.Is(err, reconcile.ErrNoData) {
		t.Errorf("NaN: err = %v", err)
	}
	// A TCP-only Component is refused with the reason.
	tcp := f.liveTarget("db", callerUID, false)
	_, err = p.EvaluateCondition(context.Background(), tcp, cond, 5*time.Minute)
	var refused *reconcile.ConditionRefusedError
	if !errors.As(err, &refused) || !strings.Contains(refused.Why, "no HTTP endpoint") {
		t.Errorf("TCP only: err = %v", err)
	}
}

func TestTraffic_FromRecording(t *testing.T) {
	pods := string(readTestdata(t, "t006-prometheus-traffic-pods.json"))
	rates := string(readTestdata(t, "t006-prometheus-traffic-rates.json"))
	f := &fakeObserver{promAnswer: func(_ string, q url.Values) string {
		if strings.Contains(q.Get("query"), "kube_pod_labels") {
			return pods
		}
		return rates
	}}
	srv, caller, spoofer := f.liveTarget("srv", srvUID, true), f.liveTarget("caller", callerUID, false), f.liveTarget("spoofer", spooferUID, false)
	p := newTestProvider(t, f).WithRequestMetrics(available)
	now := t0.Add(time.Hour)
	edges, err := p.Traffic(context.Background(), []reconcile.LogTarget{srv, caller, spoofer}, now.Add(-5*time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	// The recording's sums, by status: caller -> srv and spoofer -> srv.
	want := map[[2]string][2]float64{}
	var raw promResponse
	_ = json.Unmarshal([]byte(rates), &raw)
	podComp := map[string]string{}
	var praw promResponse
	_ = json.Unmarshal([]byte(pods), &praw)
	byUID := map[string]string{srvUID: "srv", callerUID: "caller", spooferUID: "spoofer"}
	for _, r := range praw.Data.Result {
		podComp[r.Metric["pod"]] = byUID[r.Metric["label_openchoreo_dev_component_uid"]]
	}
	for _, r := range raw.Data.Result {
		v, _ := promValue(r.Value)
		k := [2]string{podComp[r.Metric["source_pod"]], podComp[r.Metric["destination_pod"]]}
		w := want[k]
		w[0] += v
		if strings.HasPrefix(r.Metric["status"], "5") {
			w[1] += v
		}
		want[k] = w
	}
	if len(edges) != 2 || len(want) != 2 {
		t.Fatalf("edges = %+v, want %v", edges, want)
	}
	for _, e := range edges {
		w := want[[2]string{e.From, e.To}]
		if e.To != "srv" || math.Abs(e.Rate-w[0]) > 1e-9 || math.Abs(e.ErrorRate-w[1]) > 1e-9 || e.ErrorRate <= 0 {
			t.Errorf("edge %+v, want %v", e, w)
		}
	}
	q0, q1 := f.promQueries[0].Get("query"), f.promQueries[1].Get("query")
	if q0 != fmt.Sprintf(trafficPodsQuery, 300, liveEnvUID) {
		t.Errorf("pods query = %s", q0)
	}
	if q1 != fmt.Sprintf(trafficRatesQuery, 300, liveNamespace) {
		t.Errorf("rates query = %s", q1)
	}

	// An end that isn't a target isn't returned.
	edges, err = p.Traffic(context.Background(), []reconcile.LogTarget{srv, caller}, now.Add(-5*time.Minute), now)
	if err != nil || len(edges) != 1 || edges[0].From != "caller" {
		t.Errorf("without spoofer: %+v, %v", edges, err)
	}
	// A short window is read over two scrapes.
	f.promQueries = nil
	if _, err := p.Traffic(context.Background(), []reconcile.LogTarget{srv}, now.Add(-30*time.Second), now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.promQueries[0].Get("query"), "[120s]") {
		t.Errorf("short window: %s", f.promQueries[0].Get("query"))
	}
}

func TestTraffic_Refusals(t *testing.T) {
	f := &fakeObserver{promAnswer: func(string, url.Values) string { return vec("") }}
	srv := f.liveTarget("srv", srvUID, true)
	now := t0.Add(time.Hour)
	p := newTestProvider(t, f)
	if _, err := p.Traffic(context.Background(), []reconcile.LogTarget{srv}, now.Add(-time.Minute), now); !errors.Is(err, ErrRequestsUnavailable) {
		t.Errorf("not wired: %v", err)
	}
	p.WithRequestMetrics(func(context.Context) (bool, string) {
		return false, "the cluster's CNI doesn't provide request metrics"
	})
	if _, err := p.Traffic(context.Background(), []reconcile.LogTarget{srv}, now.Add(-time.Minute), now); !errors.Is(err, ErrRequestsUnavailable) ||
		!strings.Contains(err.Error(), "CNI") {
		t.Errorf("unavailable: %v", err)
	}
	p.WithRequestMetrics(available)
	other := srv
	other.Deployment.Metadata.ID, other.Environment.Metadata.ID = "dep-other", "prod"
	other.Deployment.Metadata.OwnerRefs = []resource.OwnerRef{{Kind: resource.KindEnvironment, ID: "prod"}}
	f.objects["environments/"+environmentObjectName("prod")] = [2]string{"0b0c0d0e-0000-4000-8000-000000000001", ""}
	if _, err := p.Traffic(context.Background(), []reconcile.LogTarget{srv, other}, now.Add(-time.Minute), now); err == nil ||
		!strings.Contains(err.Error(), "more than one Environment") {
		t.Errorf("two Environments: %v", err)
	}
	if _, err := p.Traffic(context.Background(), []reconcile.LogTarget{srv}, now, now); err == nil {
		t.Error("an empty range was read")
	}
}

// fakeFlowStore answers OpenObserve's search from hits, newest first,
// within the asked bounds, with codes like the store's.
type fakeFlowStore struct {
	mu   sync.Mutex
	hits []flowRecord
	// code, when set, answers 400 with it.
	code int
	sqls []string
	asks []int
}

func (s *fakeFlowStore) serve(t *testing.T) *http.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.Method != http.MethodPost || r.URL.Path != "/api/default/_search" || r.Header.Get("Authorization") == "" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
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
		s.sqls, s.asks = append(s.sqls, body.Query.SQL), append(s.asks, body.Query.Size)
		if s.code != 0 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": s.code, "message": "echoes " + body.Query.SQL})
			return
		}
		named := map[string]bool{}
		for _, m := range sqlWorkloadRE.FindAllStringSubmatch(body.Query.SQL, -1) {
			named[m[1]] = true
		}
		var out []flowRecord
		for _, h := range s.hits {
			if !named[h.SrcWorkload] && !named[h.DstWorkload] {
				continue
			}
			if h.Timestamp < body.Query.StartTime || h.Timestamp >= body.Query.EndTime {
				continue
			}
			if strings.Contains(body.Query.SQL, "verdict = 'DROPPED'") && h.Verdict != "DROPPED" {
				continue
			}
			out = append(out, h)
		}
		// Ties in an order of the store's own, unless the SQL fixes one:
		// reversed, so a reader relying on the store's order would fail.
		slices.SortStableFunc(out, func(a, b flowRecord) int { return int(b.Timestamp - a.Timestamp) })
		if strings.HasSuffix(body.Query.SQL, flowOrder) {
			slices.SortStableFunc(out, func(a, b flowRecord) int {
				switch {
				case recordLess(a, b):
					return -1
				case recordLess(b, a):
					return 1
				}
				return 0
			})
		} else {
			slices.Reverse(out)
		}
		out = out[:min(len(out), body.Query.Size)]
		_ = json.NewEncoder(w).Encode(map[string]any{"hits": out})
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}}}
}

var sqlWorkloadRE = regexp.MustCompile(`_workload = '([^']+)'`)

func deploymentSelector(uid string) string {
	return "openchoreo.dev/component-uid=" + uid + ",openchoreo.dev/environment-uid=" + liveEnvUID + ",openchoreo.dev/project-uid=" + liveProjectUID
}

func deployment(ns, name string) kubernetes.Object {
	return kubernetes.Object{Metadata: kubernetes.ObjectMeta{Namespace: ns, Name: name}}
}

// flowFixture is the recording behind a provider, with srv (and, if
// given, caller) as Deployments.
func flowFixture(t *testing.T, withCaller bool) (*Provider, *fakeFlowStore, reconcile.LogTarget, reconcile.LogTarget) {
	t.Helper()
	var rec struct {
		Hits []flowRecord `json:"hits"`
	}
	if err := json.Unmarshal(readTestdata(t, "t006-flows-hits.json"), &rec); err != nil {
		t.Fatal(err)
	}
	f := &fakeObserver{deployments: map[string][]kubernetes.Object{
		deploymentSelector(srvUID): {deployment(liveNamespace, "cmp-ac7df9f12c-env-8582189a02-2e63393c")},
	}}
	if withCaller {
		f.deployments[deploymentSelector(callerUID)] = []kubernetes.Object{deployment(liveNamespace, "cmp-34f31b6c12-env-8582189a02-97469aac")}
	}
	srv, caller := f.liveTarget("srv", srvUID, true), f.liveTarget("caller", callerUID, false)
	store := &fakeFlowStore{hits: rec.Hits}
	newest := slices.MaxFunc(rec.Hits, func(a, b flowRecord) int { return int(a.Timestamp - b.Timestamp) })
	p := newProvider(newTestKubeClient(t, f.handler(t)), func() time.Time { return newest.time().Add(time.Minute) })
	p.flows.http = store.serve(t)
	return p, store, srv, caller
}

func TestFlows_FromRecording(t *testing.T) {
	p, store, srv, caller := flowFixture(t, true)
	now := p.now()
	page, err := p.Flows(context.Background(), []reconcile.LogTarget{srv}, reconcile.FlowQuery{From: now.Add(-time.Hour), Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	from, upper := now.Add(-time.Hour).UnixMicro(), now.UnixMicro()
	if len(store.sqls) != 1 || store.sqls[0] != fmt.Sprintf(`SELECT * FROM "serendip_flows" WHERE _timestamp >= %d AND _timestamp <= %d AND `+
		`((src_namespace = '`+liveNamespace+`' AND src_workload = 'cmp-ac7df9f12c-env-8582189a02-2e63393c') OR (dst_namespace = '`+liveNamespace+
		`' AND dst_workload = 'cmp-ac7df9f12c-env-8582189a02-2e63393c'))`, from, upper)+flowOrder {
		t.Errorf("sql = %q", store.sqls)
	}
	var dropped, toCollector, fromOutside int
	for _, f := range page.Flows {
		if f.Verdict == reconcile.FlowDropped {
			dropped++
			// The probe: a bare pod in default, refused at srv's ingress.
			// default isn't the platform's, so it isn't named.
			if f.Reason != "refused by a network policy" || f.Source != (reconcile.FlowEnd{Outside: true}) ||
				f.Destination != (reconcile.FlowEnd{Component: "srv", Environment: "dev"}) || f.Port != 80 || f.Protocol != "TCP" || f.Count < 1 {
				t.Errorf("dropped flow %+v", f)
			}
			continue
		}
		switch {
		case f.Source.Component == "srv" && f.Destination.Namespace == PlaneNamespace:
			toCollector++
			if f.Destination.Workload != "opentelemetry-collector" || f.Port != 4318 {
				t.Errorf("collector flow %+v", f)
			}
		case f.Destination.Component == "srv" && f.Source.Outside:
			// caller and spoofer, which weren't given.
			fromOutside++
			if f.Source != (reconcile.FlowEnd{Outside: true}) {
				t.Errorf("outside end named: %+v", f.Source)
			}
		default:
			t.Errorf("unexpected flow %+v", f)
		}
	}
	// srv -> collector is recorded at both ends; the INGRESS one is kept.
	if dropped != 1 || toCollector != 3 || fromOutside != 6 {
		t.Errorf("dropped %d, to the collector %d, from outside %d", dropped, toCollector, fromOutside)
	}

	// Given caller too, its end is named, and its own records come in.
	page, err = p.Flows(context.Background(), []reconcile.LogTarget{srv, caller}, reconcile.FlowQuery{From: now.Add(-time.Hour), Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	named := 0
	for _, f := range page.Flows {
		if f.Source == (reconcile.FlowEnd{Component: "caller", Environment: "dev"}) {
			named++
		}
	}
	if named != 3+3 {
		t.Errorf("caller named in %d flows, want 6 (to srv and to the collector)", named)
	}
	if !strings.Contains(store.sqls[1], "cmp-34f31b6c12-env-8582189a02-97469aac") {
		t.Errorf("sql = %s", store.sqls[1])
	}

	// Dropped only.
	page, err = p.Flows(context.Background(), []reconcile.LogTarget{srv}, reconcile.FlowQuery{From: now.Add(-time.Hour), DroppedOnly: true})
	if err != nil || len(page.Flows) != 1 || !strings.HasSuffix(store.sqls[2], ` AND verdict = 'DROPPED'`+flowOrder) {
		t.Errorf("dropped only: %d flows, %v, sql %s", len(page.Flows), err, store.sqls[2])
	}
}

func TestFlows_Paging(t *testing.T) {
	p, _, srv, _ := flowFixture(t, false)
	now := p.now()
	var all []reconcile.Flow
	before := ""
	for range 50 {
		page, err := p.Flows(context.Background(), []reconcile.LogTarget{srv}, reconcile.FlowQuery{From: now.Add(-time.Hour), Limit: 3, Before: before})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page.Flows...)
		if page.Next == "" {
			break
		}
		before = page.Next
	}
	whole, err := p.Flows(context.Background(), []reconcile.LogTarget{srv}, reconcile.FlowQuery{From: now.Add(-time.Hour), Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	// Pages dedupe within themselves only, so they may hold a few more.
	if len(all) < len(whole.Flows) || len(all) > len(whole.Flows)+3 {
		t.Errorf("paged %d flows, whole %d", len(all), len(whole.Flows))
	}
	if !slices.IsSortedFunc(all, func(a, b reconcile.Flow) int { return b.Time.Compare(a.Time) }) {
		t.Error("pages aren't newest first")
	}
	if _, err := p.Flows(context.Background(), []reconcile.LogTarget{srv}, reconcile.FlowQuery{Before: "not a cursor"}); err == nil {
		t.Error("a bad cursor was read")
	}
}

func TestFlows_StoreAnswers(t *testing.T) {
	p, store, srv, _ := flowFixture(t, false)
	q := reconcile.FlowQuery{From: p.now().Add(-time.Hour)}
	store.code = 20002
	page, err := p.Flows(context.Background(), []reconcile.LogTarget{srv}, q)
	if err != nil || len(page.Flows) != 0 {
		t.Errorf("no stream yet: %+v, %v", page, err)
	}
	store.code = 20004
	if _, err := p.Flows(context.Background(), []reconcile.LogTarget{srv}, q); err == nil || strings.Contains(err.Error(), "echoes") {
		t.Errorf("unknown field: %v", err)
	}
	store.code = 20003
	if _, err := p.Flows(context.Background(), []reconcile.LogTarget{srv}, q); err == nil || strings.Contains(err.Error(), "echoes") || !strings.Contains(err.Error(), "400") {
		t.Errorf("other: %v", err)
	}
	// A target with no Deployment yet reads nothing, and isn't searched.
	store.code = 0
	f := &fakeObserver{}
	tg := f.liveTarget("srv", srvUID, true)
	p2 := newProvider(newTestKubeClient(t, f.handler(t)), p.now)
	p2.flows.http = store.serve(t)
	n := len(store.sqls)
	page, err = p2.Flows(context.Background(), []reconcile.LogTarget{tg}, q)
	if err != nil || len(page.Flows) != 0 || len(store.sqls) != n {
		t.Errorf("no Deployment: %+v, %v", page, err)
	}
}

func TestFlows_UnsafeNamesNeverReachSQL(t *testing.T) {
	f := &fakeObserver{deployments: map[string][]kubernetes.Object{
		deploymentSelector(srvUID): {deployment(liveNamespace, "x' OR '1'='1")},
	}}
	tg := f.liveTarget("srv", srvUID, true)
	p := newTestProvider(t, f)
	store := &fakeFlowStore{}
	p.flows.http = store.serve(t)
	page, err := p.Flows(context.Background(), []reconcile.LogTarget{tg}, reconcile.FlowQuery{From: t0})
	if err != nil || len(page.Flows) != 0 || len(store.sqls) != 0 {
		t.Errorf("= %+v, %v; sql %q", page, err, store.sqls)
	}
}

func TestFlowEnds(t *testing.T) {
	names := map[workloadRef]flowTarget{{"ns-a", "cmp-0000000000-env-1111111111-abcd"}: {"web", "dev"}}
	cases := []struct {
		ns, workload string
		identity     float64
		want         reconcile.FlowEnd
	}{
		{"ns-a", "cmp-0000000000-env-1111111111-abcd", 20000, reconcile.FlowEnd{Component: "web", Environment: "dev"}},
		{"ns-b", "cmp-2222222222-env-1111111111-ef01", 20001, reconcile.FlowEnd{Outside: true}},
		{"openchoreo-observability-plane", "opentelemetry-collector", 2505, reconcile.FlowEnd{Namespace: "openchoreo-observability-plane", Workload: "opentelemetry-collector"}},
		{"default", "", 36146, reconcile.FlowEnd{Outside: true}},
		{"kube-system", "coredns", 30000, reconcile.FlowEnd{Namespace: "kube-system", Workload: "coredns"}},
		{"serendip-system", "", 30001, reconcile.FlowEnd{Namespace: "serendip-system"}},
		{"workflows-serendip-builds", "build-abc", 30002, reconcile.FlowEnd{Outside: true}},
		{"ns-c", "not-a-component-name", 30003, reconcile.FlowEnd{Outside: true}},
		{"", "", 2, reconcile.FlowEnd{External: true}},
		{"", "", 9, reconcile.FlowEnd{External: true}},
		{"", "", 16777217, reconcile.FlowEnd{External: true}},
		{"", "", 1, reconcile.FlowEnd{Workload: "node"}},
		{"", "", 6, reconcile.FlowEnd{Workload: "node"}},
		{"", "", 7, reconcile.FlowEnd{Workload: "kube-apiserver"}},
		{"", "", 3, reconcile.FlowEnd{Workload: "unknown"}},
	}
	for _, c := range cases {
		if got := flowEnd(c.ns, c.workload, c.identity, names); got != c.want {
			t.Errorf("flowEnd(%q, %q, %v) = %+v, want %+v", c.ns, c.workload, c.identity, got, c.want)
		}
	}
	for code, want := range map[string]string{
		"POLICY_DENIED":           "refused by a network policy",
		"":                        "dropped by the network layer",
		"CT_MAP_INSERTION_FAILED": "dropped by the network layer: ct map insertion failed",
		"<script>":                "dropped by the network layer",
	} {
		if got := dropReason(code); got != want {
			t.Errorf("dropReason(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestFluentBitConfig_FlowSource(t *testing.T) {
	src := reconcile.FlowSource{HostPath: "/var/run/cilium/hubble", File: "events.log", Format: FlowFormat}
	var plain, with map[string]string
	_ = json.Unmarshal(fluentBitConfig(reconcile.FlowSource{}).Data, &plain)
	_ = json.Unmarshal(fluentBitConfig(src).Data, &with)
	if strings.Contains(plain["fluent-bit.conf"], "serendip.flows") || plain[flowScriptKey] != "" {
		t.Error("the flow pipeline is there without a source")
	}
	conf := with["fluent-bit.conf"]
	for _, want := range []string{
		"Path /serendip/flows/events.log", "Parser serendip_hubble_flow", "script /fluent-bit/etc/conf/serendip-flows.lua",
		"call serendip_flow", "URI /api/default/serendip_flows/_json", "Match serendip.flows",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("conf lacks %q", want)
		}
	}
	// The chart's own pipeline is unchanged but for the parsers line: the
	// logs keep their tag.
	if !strings.HasPrefix(strings.Replace(conf, "    Parsers_File /fluent-bit/etc/conf/serendip-parsers.conf\n", "", 1), plain["fluent-bit.conf"]) ||
		strings.Count(conf, "Match kube.*") != strings.Count(plain["fluent-bit.conf"], "Match kube.*") {
		t.Error("the chart's pipeline changed")
	}
	if !strings.Contains(with[flowParsersKey], "Name serendip_hubble_flow") || !strings.Contains(with[flowScriptKey], "function serendip_flow(") {
		t.Error("the parser or the script is missing")
	}
	// The chart's parsers stay as they are, and the flow parser's file is
	// read by absolute path: the chart's workdir isn't the ConfigMap's.
	if with["parsers.conf"] != plain["parsers.conf"] ||
		!strings.Contains(conf, "    Parsers_File  parsers.conf\n    Parsers_File /fluent-bit/etc/conf/serendip-parsers.conf\n") {
		t.Error("the parsers files are wrong")
	}
	if fluentBitConfigHash(src) == fluentBitConfigHash(reconcile.FlowSource{}) || fingerprint(src) == fingerprint(reconcile.FlowSource{}) {
		t.Error("the source doesn't change the hash or the fingerprint")
	}
	logs, _ := releaseNamed(src, logsRelease)
	args := strings.Join(logs.installArgs(), " ")
	for _, want := range []string{
		"--set fluent-bit.extraVolumes[0].name=db --set fluent-bit.extraVolumes[0].hostPath.path=/var/lib/fluent-bit/db",
		"--set fluent-bit.extraVolumeMounts[0].name=db --set fluent-bit.extraVolumeMounts[0].mountPath=/var/lib/fluent-bit/db",
		"--set fluent-bit.extraVolumes[1].hostPath.path=/var/run/cilium/hubble",
		"--set fluent-bit.extraVolumeMounts[1].mountPath=/serendip/flows",
		"--set fluent-bit.extraVolumeMounts[1].readOnly=true",
		"--set-string fluent-bit.podAnnotations.serendip-fluent-bit-config=" + fluentBitConfigHash(src),
	} {
		if !strings.Contains(args, want) {
			t.Errorf("logs release lacks %q", want)
		}
	}
	plainLogs, _ := releaseNamed(reconcile.FlowSource{}, logsRelease)
	if strings.Contains(strings.Join(plainLogs.installArgs(), " "), "extraVolumes") {
		t.Error("a volume without a source")
	}
	// The base releases aren't changed by building one with a source.
	if slices.ContainsFunc(releases[1].sets, func(s string) bool { return strings.Contains(s, "extraVolumes") }) {
		t.Error("releasesFor changed the shared releases")
	}
}

func TestSetFlowSource_Refusals(t *testing.T) {
	inst := newInstaller(nil, nil)
	ok := reconcile.FlowSource{HostPath: "/var/run/cilium/hubble", File: "events.log", Format: FlowFormat}
	if err := inst.SetFlowSource(ok); err != nil {
		t.Fatal(err)
	}
	if err := inst.SetFlowSource(reconcile.FlowSource{}); err != nil {
		t.Errorf("zero: %v", err)
	}
	for _, bad := range []reconcile.FlowSource{
		{HostPath: "/var/run", File: "events.log", Format: "pcap"},
		{HostPath: "relative/dir", File: "events.log", Format: FlowFormat},
		{HostPath: "/var/run,x=y", File: "events.log", Format: FlowFormat},
		{HostPath: "/var/../etc", File: "events.log", Format: FlowFormat},
		{HostPath: "/var/run", File: "a\nb", Format: FlowFormat},
		{HostPath: "/var/run", File: "..", Format: FlowFormat},
	} {
		if err := inst.SetFlowSource(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if !inst.flowSource.IsZero() {
		t.Errorf("a refused source replaced the last one: %+v", inst.flowSource)
	}
}

// TestFlows_StoredAnswer reads OpenObserve's own answer for srv, recorded
// once Fluent Bit tailed the records live (T006): the fields the Lua
// filter writes are the ones flowRecord decodes.
func TestFlows_StoredAnswer(t *testing.T) {
	var rec struct {
		Hits []flowRecord `json:"hits"`
	}
	if err := json.Unmarshal(readTestdata(t, "t006-openobserve-flows.json"), &rec); err != nil {
		t.Fatal(err)
	}
	names := map[workloadRef]flowTarget{{liveNamespace, "cmp-ac7df9f12c-env-8582189a02-2e63393c"}: {"srv", "dev"}}
	recs := dedupe(rec.Hits)
	if len(recs) == 0 || len(recs) >= len(rec.Hits) {
		t.Fatalf("%d records, %d after dedupe", len(rec.Hits), len(recs))
	}
	f := toFlow(recs[0], names)
	if f.Verdict != reconcile.FlowDropped || f.Reason != "refused by a network policy" || f.Source != (reconcile.FlowEnd{Outside: true}) ||
		f.Destination != (reconcile.FlowEnd{Component: "srv", Environment: "dev"}) || f.Port != 80 || f.Count != 24 ||
		!f.Time.Equal(time.UnixMicro(1795396727704034).UTC()) {
		t.Errorf("first = %+v", f)
	}
	for _, r := range recs[1:] {
		if f := toFlow(r, names); f.Verdict != reconcile.FlowForwarded || f.Count < 1 || f.Protocol != "TCP" ||
			(f.Source.Component != "srv" && f.Destination.Component != "srv") {
			t.Errorf("flow %+v", f)
		}
	}
}

// TestFlows_TiesPageOnce: records written in the same microsecond, more
// than a page, come back once each across pages.
func TestFlows_TiesPageOnce(t *testing.T) {
	f := &fakeObserver{deployments: map[string][]kubernetes.Object{
		deploymentSelector(srvUID): {deployment(liveNamespace, "cmp-ac7df9f12c-env-8582189a02-2e63393c")},
	}}
	srv := f.liveTarget("srv", srvUID, true)
	at := t0.Add(30 * time.Minute).UnixMicro()
	store := &fakeFlowStore{}
	for k := range 7 {
		store.hits = append(store.hits, flowRecord{Timestamp: at, Verdict: "DROPPED", Direction: "INGRESS",
			SrcNamespace: fmt.Sprintf("ns-%d", k), DstNamespace: liveNamespace, DstWorkload: "cmp-ac7df9f12c-env-8582189a02-2e63393c",
			Protocol: "TCP", Port: 80, Count: 1})
	}
	p := newTestProvider(t, f)
	p.flows.http = store.serve(t)
	seen := map[string]int{}
	before := ""
	for range 10 {
		page, err := p.Flows(context.Background(), []reconcile.LogTarget{srv}, reconcile.FlowQuery{From: t0, Limit: 3, Before: before})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Flows) > 3 {
			t.Fatalf("page of %d", len(page.Flows))
		}
		for range page.Flows {
			seen["x"]++
		}
		if page.Next == "" {
			break
		}
		before = page.Next
	}
	if seen["x"] != 7 {
		t.Errorf("read %d records across pages, want 7", seen["x"])
	}
	// The SQL holds the order: names are all "outside", so check the
	// requests instead of the answers.
	for _, sql := range store.sqls {
		if !strings.HasSuffix(sql, flowOrder) {
			t.Errorf("sql = %s", sql)
		}
	}
}

// TestFlows_CursorPastAnAnswer: a cursor counting more records at its
// time than one answer holds steps past them and says the page was cut.
func TestFlows_CursorPastAnAnswer(t *testing.T) {
	p, store, srv, _ := flowFixture(t, false)
	newest := p.now().Add(-time.Minute)
	cur := reconcile.LogCursor{Time: newest, Seen: 1500}.Encode()
	page, err := p.Flows(context.Background(), []reconcile.LogTarget{srv}, reconcile.FlowQuery{From: p.now().Add(-time.Hour), Limit: 5, Before: cur})
	if err != nil || !page.Truncated {
		t.Fatalf("= %+v, %v", page, err)
	}
	if n := store.asks[len(store.asks)-1]; n != 6 {
		t.Errorf("asked %d, want limit + 1", n)
	}
	for _, f := range page.Flows {
		if !f.Time.Before(newest) {
			t.Errorf("a record at or after the cursor: %v", f.Time)
		}
	}
	// At the window's start there's nothing older to step to.
	cur = reconcile.LogCursor{Time: p.now().Add(-time.Hour), Seen: 1500}.Encode()
	n := len(store.sqls)
	page, err = p.Flows(context.Background(), []reconcile.LogTarget{srv}, reconcile.FlowQuery{From: p.now().Add(-time.Hour), Before: cur})
	if err != nil || !page.Truncated || len(page.Flows) != 0 || len(store.sqls) != n {
		t.Errorf("at the start: %+v, %v", page, err)
	}
}

func TestDedupe_PairsOneToOne(t *testing.T) {
	r := func(ts int64, dir string) flowRecord {
		return flowRecord{Timestamp: ts, Verdict: "FORWARDED", Direction: dir, SrcNamespace: "a", SrcWorkload: "x",
			DstNamespace: "b", DstWorkload: "y", Protocol: "TCP", Port: 80, Count: 1}
	}
	const minute = int64(time.Minute / time.Microsecond)
	// Two minutes of the source's records, one of the destination's: one
	// source record is paired, the other kept.
	got := dedupe([]flowRecord{r(2*minute, "EGRESS"), r(2*minute+3, "INGRESS"), r(minute+10, "EGRESS")})
	if len(got) != 2 || got[0].Direction != "INGRESS" || got[1].Timestamp != minute+10 {
		t.Errorf("got %+v", got)
	}
	// A destination record further than a minute away pairs with nothing.
	if got := dedupe([]flowRecord{r(0, "EGRESS"), r(minute, "INGRESS")}); len(got) != 2 {
		t.Errorf("far apart: %+v", got)
	}
}
