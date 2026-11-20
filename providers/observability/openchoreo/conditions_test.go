package openchoreo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
)

// The condition queries (spec 024 T006), over T001's recorded answers.

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func metricCond(metric, as string, above float64, forMin int) resource.AlertCondition {
	return resource.AlertCondition{Type: resource.ConditionTypeMetric, Metric: &resource.MetricCondition{Metric: metric, As: as, Above: above, ForMinutes: forMin}}
}

// assertFullScope checks a request's scope carries the namespace and all
// three UIDs (spec 023's finding: a missing one widens the query).
func assertFullScope(t *testing.T, req map[string]any, comp, env string) {
	t.Helper()
	s, _ := req["searchScope"].(map[string]any)
	if s["namespace"] != controlPlaneNamespace || s["componentUid"] != "uid-"+comp || s["environmentUid"] != "uid-"+env || s["projectUid"] != "uid-app-x" {
		t.Errorf("scope = %v: want the namespace and all three UIDs", s)
	}
}

func TestEvaluateCondition_MetricFromRecording(t *testing.T) {
	f := &fakeObserver{}
	ta := f.target("a", "dev")
	if err := json.Unmarshal(readTestdata(t, "condition-metrics-resource.json"), &f.metrics); err != nil {
		t.Fatal(err)
	}
	p := newTestProvider(t, f)
	ctx := context.Background()

	// Six points answer a 5-minute for (spike finding 1).
	v, err := p.EvaluateCondition(ctx, ta, metricCond(resource.MetricMemory, resource.MetricAsValue, 10, 5), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if want := 14110720.0 / (1 << 20); math.Abs(v.Value-want) > 1e-9 || math.Abs(v.Current-want) > 1e-9 {
		t.Errorf("memory value = %+v, want %v MiB", v, want)
	}
	v, err = p.EvaluateCondition(ctx, ta, metricCond(resource.MetricMemory, resource.MetricAsPercentOfLimit, 50, 5), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if want := 14110720.0 / 805306368 * 100; math.Abs(v.Value-want) > 1e-9 {
		t.Errorf("memory %% of limit = %v, want %v", v.Value, want)
	}
	v, err = p.EvaluateCondition(ctx, ta, metricCond(resource.MetricCPU, resource.MetricAsPercentOfRequest, 50, 5), 5*time.Minute)
	if err != nil || v.Value != 0 {
		t.Errorf("cpu %% of request = %+v, %v; want 0 (the recording is idle)", v, err)
	}

	// The request: the window ending now, a 1-minute step, a full scope.
	req := f.metricsRequests[0]
	now := t0.Add(time.Hour)
	if req["metric"] != "resource" || req["step"] != "1m" || req["startTime"] != adapterTime(now.Add(-5*time.Minute)) || req["endTime"] != adapterTime(now) {
		t.Errorf("request = %v", req)
	}
	assertFullScope(t, req, "a", "dev")

	// A 10-minute for needs 11 points: six aren't enough to decide.
	if _, err := p.EvaluateCondition(ctx, ta, metricCond(resource.MetricMemory, resource.MetricAsValue, 10, 10), 10*time.Minute); !errors.Is(err, reconcile.ErrNoData) {
		t.Errorf("short window: err = %v, want ErrNoData", err)
	}
}

func TestEvaluateCondition_MetricIsTheWindowsLowestPoint(t *testing.T) {
	f := &fakeObserver{}
	ta := f.target("a", "dev")
	pts := func(vals ...float64) []point {
		var out []point
		for k, v := range vals {
			out = append(out, point{Timestamp: t0.Add(time.Duration(k) * time.Minute).Format(time.RFC3339), Value: v})
		}
		return out
	}
	// Five points, the first outside a 3-minute for: the lowest of the
	// last four is 0.4 cores, and the latest 0.9.
	f.metrics = resourceMetrics{CPUUsage: pts(0.01, 0.5, 0.4, 0.6, 0.9), CPULimits: pts(1, 1, 1, 1, 2)}
	p := newTestProvider(t, f)
	v, err := p.EvaluateCondition(context.Background(), ta, metricCond(resource.MetricCPU, resource.MetricAsValue, 300, 3), 3*time.Minute)
	if err != nil || math.Abs(v.Value-400) > 1e-9 || math.Abs(v.Current-900) > 1e-9 {
		t.Errorf("cpu = %+v, %v; want lowest 400, current 900 millicores", v, err)
	}
	// Each point against its own limit: 0.9 of 2 cores is 45%.
	v, err = p.EvaluateCondition(context.Background(), ta, metricCond(resource.MetricCPU, resource.MetricAsPercentOfLimit, 30, 3), 3*time.Minute)
	if err != nil || math.Abs(v.Value-40) > 1e-9 || math.Abs(v.Current-45) > 1e-9 {
		t.Errorf("cpu %% of limit = %+v, %v; want lowest 40, current 45", v, err)
	}
	// A limit series on another grid pairs with the value in force: a
	// limit read 30 s after each usage point is used from the next one,
	// and a point before the first limit can't be compared.
	f.metrics.CPULimits = nil
	for k := range 5 {
		f.metrics.CPULimits = append(f.metrics.CPULimits, point{Timestamp: t0.Add(time.Duration(k)*time.Minute - 30*time.Second).Format(time.RFC3339), Value: 2})
	}
	v, err = p.EvaluateCondition(context.Background(), ta, metricCond(resource.MetricCPU, resource.MetricAsPercentOfLimit, 10, 3), 3*time.Minute)
	if err != nil || math.Abs(v.Value-20) > 1e-9 || math.Abs(v.Current-45) > 1e-9 {
		t.Errorf("misaligned limit = %+v, %v; want lowest 20, current 45", v, err)
	}
	f.metrics.CPULimits = f.metrics.CPULimits[2:]
	if _, err := p.EvaluateCondition(context.Background(), ta, metricCond(resource.MetricCPU, resource.MetricAsPercentOfLimit, 10, 3), 3*time.Minute); !errors.Is(err, reconcile.ErrNoData) {
		t.Errorf("limit starting late: err = %v, want ErrNoData", err)
	}
	// No request set: refused, with a reason a person can act on.
	_, err = p.EvaluateCondition(context.Background(), ta, metricCond(resource.MetricCPU, resource.MetricAsPercentOfRequest, 30, 3), 3*time.Minute)
	var refused *reconcile.ConditionRefusedError
	if !errors.As(err, &refused) || !strings.Contains(refused.Why, "no CPU request") {
		t.Errorf("no request: err = %v", err)
	}
}

func TestEvaluateCondition_LogMatchReadsTotal(t *testing.T) {
	f := &fakeObserver{}
	ta := f.target("a", "dev")
	now := t0.Add(time.Hour)
	for k := range 11 {
		f.lines = append(f.lines, fakeLine{compUID: "uid-a", envUID: "uid-dev", at: now.Add(-time.Duration(k+1) * time.Minute), line: "spike-crash-line"})
	}
	f.lines = append(f.lines,
		fakeLine{compUID: "uid-a", envUID: "uid-dev", at: now.Add(-20 * time.Minute), line: "spike-crash-line, too old"},
		fakeLine{compUID: "uid-a", envUID: "uid-dev", at: now.Add(-2 * time.Minute), line: "something else"})
	p := newTestProvider(t, f)
	c := resource.AlertCondition{Type: resource.ConditionTypeLogMatch, LogMatch: &resource.LogMatchCondition{Text: "spike-crash", Above: 3, WindowMinutes: 15}}
	v, err := p.EvaluateCondition(context.Background(), ta, c, 15*time.Minute)
	if err != nil || v.Value != 11 || v.Current != 11 {
		t.Fatalf("logMatch = %+v, %v; want the exact total, 11", v, err)
	}
	if len(f.logRequests) != 1 {
		t.Fatalf("%d queries, want 1", len(f.logRequests))
	}
	req := f.logRequests[0]
	if req["limit"] != 1.0 || req["searchPhrase"] != "spike-crash" || req["startTime"] != adapterTime(now.Add(-15*time.Minute)) {
		t.Errorf("request = %v: want limit 1, the text, the window", req)
	}
	assertFullScope(t, req, "a", "dev")

	// Without a total, one page of above + 1 lines is counted.
	f.noTotal = true
	v, err = p.EvaluateCondition(context.Background(), ta, c, 15*time.Minute)
	if err != nil || v.Value != 4 {
		t.Errorf("logMatch without total = %+v, %v; want 4 (above + 1)", v, err)
	}
	if got := f.logRequests[len(f.logRequests)-1]["limit"]; got != 4.0 {
		t.Errorf("fallback limit = %v, want 4", got)
	}
}

func TestEvaluateCondition_LogMatchRecordingDecodes(t *testing.T) {
	var out logsResponse
	if err := json.Unmarshal(readTestdata(t, "condition-logs-match.json"), &out); err != nil {
		t.Fatal(err)
	}
	if out.Total == nil || *out.Total != 11 || len(out.Logs) != 4 {
		t.Errorf("recording: total %v, %d lines; want 11 and 4", out.Total, len(out.Logs))
	}
}

func TestEvaluateCondition_RestartsQueryHoldsOnlyIDs(t *testing.T) {
	f := &fakeObserver{prom: string(readTestdata(t, "condition-prometheus-restarts.json"))}
	ta := f.target("a", "dev")
	p := newTestProvider(t, f)
	c := resource.AlertCondition{Type: resource.ConditionTypeRestarts, Restarts: &resource.RestartsCondition{Above: 3, WindowMinutes: 10}}
	v, err := p.EvaluateCondition(context.Background(), ta, c, 10*time.Minute)
	if err != nil || v.Value != 7 || v.Current != 7 {
		t.Fatalf("restarts = %+v, %v; want 7", v, err)
	}
	q := f.promQueries[0]
	want := `sum((max_over_time(kube_pod_container_status_restarts_total[10m]) - min_over_time(kube_pod_container_status_restarts_total[10m]))` +
		` * on (namespace, pod) group_left() max by (namespace, pod) (kube_pod_labels{label_openchoreo_dev_component_uid="uid-a",label_openchoreo_dev_environment_uid="uid-dev",label_openchoreo_dev_project_uid="uid-app-x"}))`
	if q.Get("query") != want {
		t.Errorf("query =\n%s\nwant\n%s", q.Get("query"), want)
	}
	if q.Get("time") != fmt.Sprintf("%d.000", t0.Add(time.Hour).Unix()) {
		t.Errorf("time = %q, want now in seconds", q.Get("time"))
	}

	// Nothing restarting is 0 while the pods' series are there...
	f.prom = string(readTestdata(t, "condition-prometheus-restarts-empty.json"))
	f.promCount = `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1794713268.397,"2"]}]}}`
	if v, err := p.EvaluateCondition(context.Background(), ta, c, 10*time.Minute); err != nil || v.Value != 0 {
		t.Errorf("empty vector = %+v, %v; want 0", v, err)
	}
	count := f.promQueries[len(f.promQueries)-1].Get("query")
	if !strings.HasPrefix(count, "count(last_over_time(kube_pod_container_status_restarts_total[10m])") || !strings.Contains(count, `label_openchoreo_dev_project_uid="uid-app-x"`) {
		t.Errorf("series query = %s", count)
	}
	// ...and missing data when they aren't (the exporter down, no pods),
	// so a firing Alert isn't resolved by it.
	f.promCount = string(readTestdata(t, "condition-prometheus-restarts-empty.json"))
	if _, err := p.EvaluateCondition(context.Background(), ta, c, 10*time.Minute); !errors.Is(err, reconcile.ErrNoData) {
		t.Errorf("no series: err = %v, want ErrNoData", err)
	}
	f.promCount = ""

	// A refused query names the error type, never the echoed query.
	f.prom, f.promStatus = string(readTestdata(t, "condition-prometheus-bad-query.json")), 400
	_, err = p.EvaluateCondition(context.Background(), ta, c, 10*time.Minute)
	if err == nil || strings.Contains(err.Error(), "parse error") || !strings.Contains(err.Error(), "400") {
		t.Errorf("bad query: err = %v", err)
	}
	f.promStatus = 200
	_, err = p.EvaluateCondition(context.Background(), ta, c, 10*time.Minute)
	if err == nil || !strings.Contains(err.Error(), "bad_data") || strings.Contains(err.Error(), "parse error") {
		t.Errorf("status error: err = %v", err)
	}
}

func TestEvaluateCondition_RestartsRefusesAnUnsafeUID(t *testing.T) {
	f := &fakeObserver{prom: string(readTestdata(t, "condition-prometheus-restarts.json"))}
	ta := f.target("a", "dev")
	f.objects["components/"+componentObjectName("a")] = [2]string{`x"} or vector(1) #`, "app-x"}
	c := resource.AlertCondition{Type: resource.ConditionTypeRestarts, Restarts: &resource.RestartsCondition{Above: 3, WindowMinutes: 10}}
	if _, err := newTestProvider(t, f).EvaluateCondition(context.Background(), ta, c, 10*time.Minute); err == nil {
		t.Error("a UID holding PromQL was sent")
	}
	if len(f.promQueries) != 0 {
		t.Errorf("%d queries sent", len(f.promQueries))
	}
}

func TestEvaluateCondition_GoneTargetIsNoData(t *testing.T) {
	f := &fakeObserver{}
	ta := f.target("a", "dev")
	delete(f.objects, "components/"+componentObjectName("a"))
	p := newTestProvider(t, f)
	for _, c := range []resource.AlertCondition{
		metricCond(resource.MetricCPU, resource.MetricAsValue, 100, 5),
		{Type: resource.ConditionTypeRestarts, Restarts: &resource.RestartsCondition{Above: 3, WindowMinutes: 10}},
		{Type: resource.ConditionTypeLogMatch, LogMatch: &resource.LogMatchCondition{Text: "x", Above: 3, WindowMinutes: 10}},
	} {
		if _, err := p.EvaluateCondition(context.Background(), ta, c, 10*time.Minute); !errors.Is(err, reconcile.ErrNoData) {
			t.Errorf("%s: err = %v, want ErrNoData", c.Type, err)
		}
	}
	if len(f.metricsRequests)+len(f.logRequests)+len(f.promQueries) != 0 {
		t.Error("a target with no platform objects was queried")
	}
}

func TestEvaluateCondition_RefusesAnInvalidCondition(t *testing.T) {
	f := &fakeObserver{}
	ta := f.target("a", "dev")
	var refused *reconcile.ConditionRefusedError
	c := resource.AlertCondition{Type: resource.ConditionTypeLogMatch, LogMatch: &resource.LogMatchCondition{Text: "", Above: 3, WindowMinutes: 10}}
	if _, err := newTestProvider(t, f).EvaluateCondition(context.Background(), ta, c, 10*time.Minute); !errors.As(err, &refused) {
		t.Errorf("err = %v, want refused", err)
	}
}

type fakeUsage struct {
	u   reconcile.DiskUsage
	err error
}

func (f fakeUsage) Usage(context.Context) (reconcile.DiskUsage, error) { return f.u, f.err }

func TestEvaluateCondition_PackageDisk(t *testing.T) {
	f := &fakeObserver{}
	c := resource.AlertCondition{Type: resource.ConditionTypePackageDisk, PackageDisk: &resource.PackageDiskCondition{PercentOfBudget: 85}}
	p := newTestProvider(t, f)
	var refused *reconcile.ConditionRefusedError
	if _, err := p.EvaluateCondition(context.Background(), reconcile.LogTarget{}, c, 0); !errors.As(err, &refused) {
		t.Errorf("without usage: err = %v, want refused", err)
	}
	now := t0.Add(time.Hour)
	p.WithDiskUsage(fakeUsage{u: reconcile.DiskUsage{UsedMiB: 6963, CapacityMiB: 8192, ReadAt: now.Add(-time.Minute)}}, 8192)
	v, err := p.EvaluateCondition(context.Background(), reconcile.LogTarget{}, c, 0)
	if err != nil || math.Abs(v.Value-6963.0/8192*100) > 1e-9 {
		t.Errorf("disk = %+v, %v", v, err)
	}
	// A stuck reader's old reading is missing data, not the current use.
	p.WithDiskUsage(fakeUsage{u: reconcile.DiskUsage{UsedMiB: 10, CapacityMiB: 8192, ReadAt: now.Add(-10 * time.Minute)}}, 8192)
	if _, err := p.EvaluateCondition(context.Background(), reconcile.LogTarget{}, c, 0); !errors.Is(err, reconcile.ErrNoData) {
		t.Errorf("stale reading: err = %v, want ErrNoData", err)
	}
	p.WithDiskUsage(fakeUsage{err: errors.New("store down")}, 8192)
	if _, err := p.EvaluateCondition(context.Background(), reconcile.LogTarget{}, c, 0); err == nil {
		t.Error("a failed read gave a value")
	}
}
