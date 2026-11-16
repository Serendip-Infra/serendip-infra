package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/resource"
)

func TestParseEvalStats(t *testing.T) {
	line := "2026/11/15 10:10:00 reconcile: alerts: stats: window=10m0s queries=2000 failed=3 noData=12 mean=120ms p50=100ms p95=400ms max=1250ms ticks=40 tickMax=25000ms lateMax=14000ms"
	got, err := parseEvalStats(line)
	if err != nil {
		t.Fatal(err)
	}
	want := EvalStats{Window: Duration(10 * time.Minute), Queries: 2000, Failed: 3, NoData: 12, MeanMs: 120, P50Ms: 100, P95Ms: 400, MaxMs: 1250, Ticks: 40, TickMaxMs: 25000, LateMaxMs: 14000}
	if got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	for _, bad := range []string{"no prefix here", alertStatsPrefix + " queries=x", alertStatsPrefix + " loose"} {
		if _, err := parseEvalStats(bad); err == nil {
			t.Errorf("parseEvalStats(%q): want an error", bad)
		}
	}
}

func TestReadEvalStats_FromOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serendipd.log")
	early := "x " + alertStatsPrefix + " window=10m0s queries=1 failed=0 mean=1ms p50=1ms p95=1ms max=1ms ticks=1 tickMax=1ms lateMax=0ms\n"
	if err := os.WriteFile(path, []byte(early), 0o644); err != nil {
		t.Fatal(err)
	}
	off := logSize(path)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("some other line\n")
	f.WriteString("y " + alertStatsPrefix + " window=10m0s queries=7 failed=0 mean=2ms p50=2ms p95=3ms max=4ms ticks=2 tickMax=5ms lateMax=0ms\n")
	f.Close()
	got, err := readEvalStats(path, off)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Queries != 7 {
		t.Fatalf("got %+v, want only the line after the offset", got)
	}
}

func TestStoreCountsAndWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serendipd.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`CREATE TABLE resources (id TEXT PRIMARY KEY, kind TEXT, resource_version INTEGER)`)
	exec(`CREATE TABLE audit_log (id TEXT PRIMARY KEY)`)
	exec(`INSERT INTO resources VALUES ('r1', 'AlertRule', 3), ('c1', 'Component', 2)`)
	exec(`INSERT INTO audit_log VALUES ('a1')`)
	ctx := context.Background()
	a, err := readStoreCounts(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	// Two rule updates, a new Alert, two audit rows; the Component is
	// unchanged.
	exec(`UPDATE resources SET resource_version = 5 WHERE id = 'r1'`)
	exec(`INSERT INTO resources VALUES ('al1', 'Alert', 1)`)
	exec(`INSERT INTO audit_log VALUES ('a2'), ('a3')`)
	b, err := readStoreCounts(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	b.at = a.at.Add(2 * time.Minute)
	w := writesBetween(a, b)
	if w.Resources != 3 || w.Audit != 2 || w.ByKind["AlertRule"] != 2 || w.ByKind["Alert"] != 1 {
		t.Fatalf("writes = %+v", w)
	}
	if _, ok := w.ByKind["Component"]; ok {
		t.Errorf("an unchanged kind is listed: %+v", w.ByKind)
	}
	if w.ResourcesPerMin != 1.5 || w.AuditPerMin != 1 {
		t.Errorf("per minute = %v resources, %v audit; want 1.5, 1", w.ResourcesPerMin, w.AuditPerMin)
	}
}

func TestBenchRule_MixAndShape(t *testing.T) {
	mix := map[string]int{}
	names := map[string]bool{}
	for i := range maxBenchRules {
		body, typ := benchRule(i, "comp", "env", false)
		mix[typ]++
		md := body["metadata"].(map[string]any)
		names[md["name"].(string)] = true
		spec := body["spec"].(map[string]any)
		if spec["intervalMinutes"] != 1 || spec["enabled"] != false || spec["environmentId"] != "env" {
			t.Fatalf("rule %d spec = %v", i, spec)
		}
		cond := spec["condition"].(map[string]any)
		if _, ok := cond[cond["type"].(string)]; !ok {
			t.Fatalf("rule %d: condition %v lacks its %q block", i, cond, cond["type"])
		}
	}
	if len(names) != maxBenchRules {
		t.Errorf("names not unique: %d of %d", len(names), maxBenchRules)
	}
	// A third each of metric, restarts and logMatch, each split in two.
	if mix["restarts"] != 66 || mix["metric-cpu"]+mix["metric-memory"] != 67 || mix["logMatch-5m"]+mix["logMatch-15m"] != 66 {
		t.Errorf("mix = %v", mix)
	}
}

// TestBenchRule_PassesValidation runs every bench rule through the API's
// own validator, so a threshold out of range fails here rather than four
// hours into a run.
func TestBenchRule_PassesValidation(t *testing.T) {
	id := "01a1e0bf-b505-7e4c-831a-3012b241e988"
	for i := range maxBenchRules {
		body, typ := benchRule(i, id, id, true)
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		var r struct {
			Metadata resource.Metadata      `json:"metadata"`
			Spec     resource.AlertRuleSpec `json:"spec"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatalf("rule %d (%s): %v", i, typ, err)
		}
		if err := resource.ValidateAlertRule(r.Metadata, r.Spec); err != nil {
			t.Fatalf("rule %d (%s): %v", i, typ, err)
		}
	}
}

func TestRunLoad_RulesBetweenLoadAndDisk(t *testing.T) {
	var order []string
	load := func(_ context.Context, verb string) (string, error) {
		order = append(order, "load "+verb)
		return "", nil
	}
	sample := func(_ context.Context, n int, _ time.Duration) ([]Sample, []procSample, error) {
		return nil, nil, nil
	}
	reads := 0
	writes := func(context.Context) (storeCounts, error) {
		reads++
		return storeCounts{at: time.Now(), versions: map[string]int64{"AlertRule": int64(reads)}}, nil
	}
	between := func(ctx context.Context, phase phaseFunc) error {
		order = append(order, "rules")
		_, err := phase(ctx, "rules evaluating", 2, time.Second)
		return err
	}
	po := packageOptions{loadSamples: 1, diskSamples: 2, diskInterval: time.Second, idleInterval: time.Second}
	var p PackageRun
	if err := runLoad(context.Background(), po, load, sample, "", &p, between, writes); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, ", "); got != "load start, rules, load latency, load stop" {
		t.Errorf("order = %s", got)
	}
	if p.Load == nil || p.Disk == nil || p.Load.Writes == nil || p.Disk.Writes == nil {
		t.Fatalf("phases or their writes missing: load %+v disk %+v", p.Load, p.Disk)
	}
	// Three phases, each read before and after.
	if reads != 6 {
		t.Errorf("store read %d times, want 6", reads)
	}
}

func TestKeepLog_RenamesToFirstFree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "serendipd.log")
	keepLog(path) // nothing there yet
	for _, body := range []string{"cold", "warm"} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		keepLog(path)
	}
	for n, want := range map[string]string{"serendipd.1.log": "cold", "serendipd.2.log": "warm"} {
		got, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", n, got, err, want)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s still there", path)
	}
}
