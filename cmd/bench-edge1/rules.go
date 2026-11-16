package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// The rules phases (specs/024-alerts T011) measure the alert evaluator
// on top of the observability package, under spec 023's log load:
//  1. Baseline: light-workload Components deployed, no rules but the
//     built-in one.
//  2. Disabled: the rules created disabled, so serendipd only resyncs
//     them (the resync cost).
//  3. Evaluating: the rules enabled, each at a 1-minute interval.
//
// The disk phase that follows, when there is one, still runs with the
// rules on, so the evaluator's timings cover the store growing.

// alertStatsPrefix is core/reconcile.AlertStatsPrefix. The harness
// doesn't import core: the line is read from serendipd's log.
const alertStatsPrefix = "reconcile: alerts: stats:"

// RulesRun is what the rules phases measured.
type RulesRun struct {
	Count      int            `json:"count"`
	Components int            `json:"components"`
	Mix        map[string]int `json:"mix"`
	Baseline   *LoadRun       `json:"baseline,omitempty"`
	Disabled   *LoadRun       `json:"disabled,omitempty"`
	Evaluating *LoadRun       `json:"evaluating,omitempty"`
	// ResyncMillicores is serendipd's CPU with the rules disabled minus
	// the baseline's; EvaluationMillicores and EvaluationRSSMiB are the
	// evaluating phase's minus the baseline's.
	ResyncMillicores     float64 `json:"resyncMillicores"`
	EvaluationMillicores float64 `json:"evaluationMillicores"`
	EvaluationRSSMiB     float64 `json:"evaluationRssMiB"`
	// Stats are the evaluator's timing lines from the rules' enabling to
	// the end of the run.
	Stats []EvalStats `json:"stats,omitempty"`
	// Reasons counts the rules' Evaluating condition at the end, as
	// "status/reason".
	Reasons map[string]int `json:"reasons,omitempty"`
	// Firing is how many Alerts were firing at the end.
	Firing int `json:"firing"`

	logOffset int64
}

// StoreWrites is how much the store changed over one phase: each
// resource's version rises by one per write (a create starts at 1), and
// every mutation adds an audit row.
type StoreWrites struct {
	Seconds float64 `json:"seconds"`
	// ByKind is the rise in the sum of resource versions, by kind.
	ByKind          map[string]int64 `json:"byKind"`
	Resources       int64            `json:"resources"`
	Audit           int64            `json:"audit"`
	ResourcesPerMin float64          `json:"resourcesPerMinute"`
	AuditPerMin     float64          `json:"auditPerMinute"`
}

// storeCounts is one reading of the store.
type storeCounts struct {
	at       time.Time
	versions map[string]int64
	audit    int64
}

// readStoreCounts reads serendipd's SQLite database alongside it (WAL
// lets a reader in while serendipd writes).
func readStoreCounts(ctx context.Context, path string) (storeCounts, error) {
	c := storeCounts{at: time.Now(), versions: map[string]int64{}}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		return c, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT kind, COALESCE(SUM(resource_version), 0) FROM resources GROUP BY kind`)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var v int64
		if err := rows.Scan(&kind, &v); err != nil {
			return c, err
		}
		c.versions[kind] = v
	}
	if err := rows.Err(); err != nil {
		return c, err
	}
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&c.audit)
	return c, err
}

// writesBetween is the store's change from a to b.
func writesBetween(a, b storeCounts) *StoreWrites {
	w := &StoreWrites{Seconds: b.at.Sub(a.at).Seconds(), ByKind: map[string]int64{}, Audit: b.audit - a.audit}
	kinds := map[string]bool{}
	for k := range a.versions {
		kinds[k] = true
	}
	for k := range b.versions {
		kinds[k] = true
	}
	for k := range kinds {
		if d := b.versions[k] - a.versions[k]; d != 0 {
			w.ByKind[k] = d
			w.Resources += d
		}
	}
	if w.Seconds > 0 {
		w.ResourcesPerMin = float64(w.Resources) * 60 / w.Seconds
		w.AuditPerMin = float64(w.Audit) * 60 / w.Seconds
	}
	return w
}

// EvalStats is one of the evaluator's timing lines.
type EvalStats struct {
	Window  Duration `json:"window"`
	Queries int      `json:"queries"`
	Failed  int      `json:"failed"`
	// NoData is the queries answering "nothing in the window yet"
	// (absent before spec 024 T011's first run, where Failed included
	// them).
	NoData    int   `json:"noData"`
	MeanMs    int64 `json:"meanMs"`
	P50Ms     int64 `json:"p50Ms"`
	P95Ms     int64 `json:"p95Ms"`
	MaxMs     int64 `json:"maxMs"`
	Ticks     int   `json:"ticks"`
	TickMaxMs int64 `json:"tickMaxMs"`
	LateMaxMs int64 `json:"lateMaxMs"`
}

// parseEvalStats reads one timing line, from AlertStatsPrefix on.
func parseEvalStats(line string) (EvalStats, error) {
	var s EvalStats
	i := strings.Index(line, alertStatsPrefix)
	if i < 0 {
		return s, fmt.Errorf("not a stats line")
	}
	ms := func(v string) (int64, error) { return strconv.ParseInt(strings.TrimSuffix(v, "ms"), 10, 64) }
	for _, f := range strings.Fields(line[i+len(alertStatsPrefix):]) {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return s, fmt.Errorf("field %q", f)
		}
		var err error
		switch k {
		case "window":
			var d time.Duration
			d, err = time.ParseDuration(v)
			s.Window = Duration(d)
		case "queries":
			s.Queries, err = strconv.Atoi(v)
		case "failed":
			s.Failed, err = strconv.Atoi(v)
		case "noData":
			s.NoData, err = strconv.Atoi(v)
		case "ticks":
			s.Ticks, err = strconv.Atoi(v)
		case "mean":
			s.MeanMs, err = ms(v)
		case "p50":
			s.P50Ms, err = ms(v)
		case "p95":
			s.P95Ms, err = ms(v)
		case "max":
			s.MaxMs, err = ms(v)
		case "tickMax":
			s.TickMaxMs, err = ms(v)
		case "lateMax":
			s.LateMaxMs, err = ms(v)
		}
		if err != nil {
			return s, fmt.Errorf("%s: %w", k, err)
		}
	}
	return s, nil
}

// readEvalStats reads the timing lines in serendipd's log from offset on.
func readEvalStats(path string, offset int64) ([]EvalStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	var out []EvalStats
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if !strings.Contains(sc.Text(), alertStatsPrefix) {
			continue
		}
		s, err := parseEvalStats(sc.Text())
		if err != nil {
			return out, err
		}
		out = append(out, s)
	}
	return out, sc.Err()
}

// logSize is the log's current length, where a later read starts.
func logSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// benchRule is the i-th rule's body, spread over the Components and the
// condition types. Thresholds are out of reach, so nothing fires: the
// phases measure evaluation, not delivery.
func benchRule(i int, comp, env string, enabled bool) (map[string]any, string) {
	var typ string
	var cond map[string]any
	switch i % 3 {
	case 0:
		metric := "cpu"
		if (i/3)%2 == 1 {
			metric = "memory"
		}
		typ = "metric-" + metric
		cond = map[string]any{"type": "metric", "metric": map[string]any{"metric": metric, "as": "value", "above": 1000000, "forMinutes": 5}}
	case 1:
		typ = "restarts"
		cond = map[string]any{"type": "restarts", "restarts": map[string]any{"above": 999, "windowMinutes": 10}}
	default:
		window := 5
		if (i/3)%2 == 1 {
			window = 15
		}
		typ = fmt.Sprintf("logMatch-%dm", window)
		cond = map[string]any{"type": "logMatch", "logMatch": map[string]any{"text": "error", "above": 999, "windowMinutes": window}}
	}
	return map[string]any{
		"metadata": map[string]any{
			"name":      fmt.Sprintf("bench-rule-%03d", i+1),
			"ownerRefs": []any{map[string]any{"kind": "Component", "id": comp}},
		},
		"spec": map[string]any{
			"environmentId": env, "severity": "warning", "summary": "bench rule", "condition": cond,
			"intervalMinutes": 1, "enabled": enabled,
		},
	}, typ
}

// measureRules runs the three rules phases. sample is the load phase's
// sampler; created records the Components for clean-up.
func measureRules(ctx context.Context, o options, cl *cluster, a api, d *daemon, phase phaseFunc, p *PackageRun, created *workload) error {
	po := o.pkg
	r := &RulesRun{Count: po.rules, Components: o.components, Mix: map[string]int{}}
	p.Rules = r

	log.Printf("rules: deploying %d Components for the rules to watch", o.components)
	w, _, err := a.deployWorkload(ctx, o.components, o.image)
	*created = w
	if err != nil {
		return fmt.Errorf("rules: deploy: %w", err)
	}
	comps, err := a.componentIDs(ctx, w.app)
	if err != nil {
		return err
	}
	if len(comps) == 0 {
		return fmt.Errorf("rules: no Components deployed")
	}
	if err := cl.waitAllPodsReady(ctx, 10*time.Minute); err != nil {
		return err
	}
	if err := sleep(ctx, o.settle); err != nil {
		return err
	}
	if r.Baseline, err = phase(ctx, "rules baseline (Components, no rules)", po.idleSamples, po.idleInterval); err != nil {
		return err
	}

	log.Printf("rules: creating %d rules, disabled", po.rules)
	ids := make([]string, 0, po.rules)
	for i := range po.rules {
		body, typ := benchRule(i, comps[i%len(comps)], w.env, false)
		obj, err := a.do(ctx, http.MethodPost, "/v1/alertrules", body)
		if err != nil {
			return fmt.Errorf("rules: create %d: %w", i+1, err)
		}
		ids = append(ids, idOf(obj))
		r.Mix[typ]++
	}
	// Two ticks, so every rule's disabled status is written before the
	// window opens.
	if err := sleep(ctx, time.Minute); err != nil {
		return err
	}
	if r.Disabled, err = phase(ctx, "rules disabled (resync only)", po.idleSamples, po.idleInterval); err != nil {
		return err
	}
	r.ResyncMillicores = r.Disabled.Serendipd.CPUMillicores.Mean - r.Baseline.Serendipd.CPUMillicores.Mean

	log.Printf("rules: enabling %d rules", len(ids))
	r.logOffset = logSize(d.log)
	for _, id := range ids {
		if err := a.setEnabled(ctx, id, true); err != nil {
			return fmt.Errorf("rules: enable %s: %w", id, err)
		}
	}
	if r.Evaluating, err = phase(ctx, "rules evaluating", po.rulesSamples, po.idleInterval); err != nil {
		return err
	}
	r.EvaluationMillicores = r.Evaluating.Serendipd.CPUMillicores.Mean - r.Baseline.Serendipd.CPUMillicores.Mean
	r.EvaluationRSSMiB = r.Evaluating.Serendipd.RSSMiB.Mean - r.Baseline.Serendipd.RSSMiB.Mean
	return nil
}

// finishRules reads what the rules ended as, and the evaluator's timing
// lines, once every phase is done.
func finishRules(ctx context.Context, a api, d *daemon, r *RulesRun) {
	if r == nil {
		return
	}
	var err error
	if r.Stats, err = readEvalStats(d.log, r.logOffset); err != nil {
		log.Printf("rules: reading the evaluator's stats: %v", err)
	}
	rules, err := a.list(ctx, "/v1/alertrules")
	if err != nil {
		log.Printf("rules: listing rules: %v", err)
	}
	r.Reasons = map[string]int{}
	for _, rule := range rules {
		st, _ := rule["status"].(map[string]any)
		conds, _ := st["conditions"].([]any)
		key := "none"
		for _, c := range conds {
			m, _ := c.(map[string]any)
			if m["type"] == "Evaluating" {
				key = fmt.Sprintf("%v/%v", m["status"], m["reason"])
			}
		}
		r.Reasons[key]++
	}
	alerts, err := a.list(ctx, "/v1/alerts?state=firing")
	if err != nil {
		log.Printf("rules: listing Alerts: %v", err)
	}
	r.Firing = len(alerts)
}

// setEnabled turns a rule on or off.
func (a api) setEnabled(ctx context.Context, id string, on bool) error {
	obj, err := a.do(ctx, http.MethodGet, "/v1/alertrules/"+id, nil)
	if err != nil {
		return err
	}
	spec, _ := obj["spec"].(map[string]any)
	if spec == nil {
		return fmt.Errorf("rule %s has no spec", id)
	}
	spec["enabled"] = on
	delete(obj, "status")
	_, err = a.do(ctx, http.MethodPut, "/v1/alertrules/"+id, obj)
	return err
}

// writeRulesSummary renders the rules phases.
func writeRulesSummary(b *strings.Builder, r *RulesRun) {
	if r == nil {
		return
	}
	keys := make([]string, 0, len(r.Mix))
	for k := range r.Mix {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var mix []string
	for _, k := range keys {
		mix = append(mix, fmt.Sprintf("%s %d", k, r.Mix[k]))
	}
	fmt.Fprintf(b, "\n## Alert rules\n\n%d rules at 1 minute over %d Components (%s), under the log load.\n\n", r.Count, r.Components, strings.Join(mix, ", "))
	b.WriteString("| Phase | Node mean / max (MiB) | Node CPU mean / max (mcores) | serendipd RSS mean / max (MiB) | serendipd CPU mean / max (mcores) | Resource writes / min | Audit rows / min |\n|---|---|---|---|---|---|---|\n")
	for _, ph := range []struct {
		name string
		l    *LoadRun
	}{{"Baseline (no rules)", r.Baseline}, {"Rules disabled", r.Disabled}, {"Rules evaluating", r.Evaluating}} {
		if ph.l == nil {
			continue
		}
		l := ph.l
		wr, au := "–", "–"
		if l.Writes != nil {
			wr, au = fmt.Sprintf("%.1f", l.Writes.ResourcesPerMin), fmt.Sprintf("%.1f", l.Writes.AuditPerMin)
		}
		fmt.Fprintf(b, "| %s | %.0f / %.0f | %.0f / %.0f | %.1f / %.1f | %.2f / %.2f | %s | %s |\n", ph.name,
			l.Cluster.NodeMemoryMiB.Mean, l.Cluster.NodeMemoryMiB.Max, l.Cluster.NodeCPUMillicores.Mean, l.Cluster.NodeCPUMillicores.Max,
			l.Serendipd.RSSMiB.Mean, l.Serendipd.RSSMiB.Max, l.Serendipd.CPUMillicores.Mean, l.Serendipd.CPUMillicores.Max, wr, au)
	}
	fmt.Fprintf(b, "\n- Resync (rules disabled − baseline): **%+.2f mcores**\n- Evaluation (evaluating − baseline): **%+.2f mcores, %+.1f MiB RSS**\n",
		r.ResyncMillicores, r.EvaluationMillicores, r.EvaluationRSSMiB)
	if r.Evaluating != nil && r.Evaluating.Writes != nil && len(r.Evaluating.Writes.ByKind) > 0 {
		kinds := make([]string, 0, len(r.Evaluating.Writes.ByKind))
		for k := range r.Evaluating.Writes.ByKind {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		var parts []string
		for _, k := range kinds {
			parts = append(parts, fmt.Sprintf("%s %d", k, r.Evaluating.Writes.ByKind[k]))
		}
		fmt.Fprintf(b, "- Resource writes while evaluating, by kind: %s\n", strings.Join(parts, ", "))
	}
	if len(r.Reasons) > 0 {
		keys := make([]string, 0, len(r.Reasons))
		for k := range r.Reasons {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s %d", k, r.Reasons[k]))
		}
		fmt.Fprintf(b, "- Rules' Evaluating condition at the end: %s; Alerts firing: %d\n", strings.Join(parts, ", "), r.Firing)
	}
	if len(r.Stats) == 0 {
		b.WriteString("\nNo evaluator timing lines were logged.\n")
		return
	}
	b.WriteString("\n### Evaluator timings (one line per window, from serendipd's log)\n\n| # | Window | Queries | Failed | No data | Mean | p50 | p95 | Max | Slowest tick | Latest pick-up |\n|---|---|---|---|---|---|---|---|---|---|---|\n")
	for i, s := range r.Stats {
		fmt.Fprintf(b, "| %d | %s | %d | %d | %d | %d ms | %d ms | %d ms | %d ms | %.1f s | %.1f s |\n", i+1, fmtDur(s.Window), s.Queries, s.Failed, s.NoData,
			s.MeanMs, s.P50Ms, s.P95Ms, s.MaxMs, float64(s.TickMaxMs)/1000, float64(s.LateMaxMs)/1000)
	}
}

// maxBenchRules is the install's cap (200 enabled rules) less the
// built-in disk rule.
const maxBenchRules = 199
