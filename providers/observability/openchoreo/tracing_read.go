package openchoreo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// Reading traces (specs/025-networking plan decision 12): SQL on the
// store's traces stream, scoped by the identity columns the collector
// attests (tracing-collector.yaml), with the target's UIDs resolved from
// the live objects (scope.go). Nothing a span says of itself is used to
// decide whose it is.

var _ reconcile.TracingProvider = (*Tracing)(nil)

const (
	defaultTraceLimit = 50
	// maxTraceAnswer is the most traces asked of one search.
	maxTraceAnswer = 1000
	// maxSpanAttributes and maxAttributeValue bound what one span returns.
	maxSpanAttributes = 64
	maxAttributeValue = 1024
	maxStatusMessage  = 512
	// traceLookback is how far back a read with no start goes: the
	// store's retention, which the traces share with the logs.
	traceLookback = logRetentionDays * 24 * time.Hour
)

// The identity columns the collector attests: the pod's labels and
// names, as the store flattens them.
const (
	colComponentUID   = "service_openchoreo_dev_component_uid"
	colEnvironmentUID = "service_openchoreo_dev_environment_uid"
	colProjectUID     = "service_openchoreo_dev_project_uid"
)

// Tracing is the tracing capability over the store.
type Tracing struct {
	client *kubernetes.Client
	scopes *scopes
	now    func() time.Time
	// http, when set, replaces the port-forward (tests).
	http *http.Client
}

// NewTracing returns the tracing provider over client.
func NewTracing(client *kubernetes.Client) *Tracing {
	return &Tracing{client: client, scopes: newScopes(client, time.Now), now: time.Now}
}

// scopeWhere is the SQL condition selecting s's spans. checkUIDs has
// passed, so no value holds a quote.
func scopeWhere(s componentScope) string {
	return fmt.Sprintf("(%s = '%s' AND %s = '%s' AND %s = '%s')",
		colComponentUID, s.ComponentUID, colEnvironmentUID, s.EnvironmentUID, colProjectUID, s.ProjectUID)
}

// noTraces: nothing is stored to read, yet or any more (a removal's
// deletion still running), or no span has carried the identity columns
// the query names yet (the store makes a column on its first write).
func noTraces(err error) bool {
	return errors.Is(err, errNoStream) || errors.Is(err, errStreamDeleting) || errors.Is(err, errNoField)
}

func (tr *Tracing) search(ctx context.Context, sql string, from, to time.Time, size int, hits any) error {
	return searchStore(ctx, tr.client, tr.http, "traces", "traces", sql, from, to, size, hits)
}

// resolve returns the targets' scopes; a target with nothing on the
// platform is skipped.
func (tr *Tracing) resolve(ctx context.Context, targets []reconcile.LogTarget) ([]componentScope, error) {
	var out []componentScope
	for _, t := range targets {
		s, err := tr.scopes.component(ctx, t)
		if errors.Is(err, errNoScope) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("openchoreo tracing: %w", err)
		}
		if err := checkUIDs(s); err != nil {
			return nil, fmt.Errorf("openchoreo tracing: %w", err)
		}
		out = append(out, s)
	}
	return out, nil
}

// traceRow is one trace as the summary query answers it. Times are
// nanoseconds, which the store answers as JSON integers (T007, live):
// anything else fails the decode rather than lose precision.
type traceRow struct {
	TraceID string `json:"trace_id"`
	Start   int64  `json:"t_start"`
	End     int64  `json:"t_end"`
	Spans   int    `json:"spans"`
	Errors  int    `json:"errors"`
	Root    string `json:"root"`
}

const failedSpan = "span_status = 'ERROR'"

// traceSummarySQL lists the traces with a span in where, over the spans
// in where only, newest first, started at or before upper (nanoseconds).
// Traces that started at the same nanosecond are in trace ID order, which
// the cursor's count relies on.
// rootOf names a trace's root span: the one without a parent. A stream
// where no span has ever had one has no parent column (the store makes a
// column on a field's first write), so every span is a root there
// (rootNoParents).
const (
	rootOf        = `max(CASE WHEN reference_parent_span_id IS NULL OR reference_parent_span_id = '' THEN operation_name END)`
	rootNoParents = `max(operation_name)`
)

func traceSummarySQL(where string, q reconcile.TraceQuery, upperNs int64, limit int, root string) string {
	having := []string{fmt.Sprintf("min(start_time) <= %d", upperNs)}
	if q.ErrorsOnly {
		having = append(having, fmt.Sprintf("max(CASE WHEN %s THEN 1 ELSE 0 END) = 1", failedSpan))
	}
	if q.MinDuration > 0 {
		having = append(having, fmt.Sprintf("max(end_time) - min(start_time) >= %d", q.MinDuration.Nanoseconds()))
	}
	return fmt.Sprintf(`SELECT trace_id, min(start_time) AS t_start, max(end_time) AS t_end, count(*) AS spans,`+
		` max(CASE WHEN %s THEN 1 ELSE 0 END) AS errors,`+
		` %s AS root`+
		` FROM "%s" WHERE %s GROUP BY trace_id HAVING %s ORDER BY t_start DESC, trace_id ASC LIMIT %d`,
		failedSpan, root, tracesStream, where, strings.Join(having, " AND "), limit)
}

// window bounds a query: [from, to], to no later than now, from no
// earlier than the retention.
func (tr *Tracing) window(from, to time.Time) (time.Time, time.Time) {
	now := tr.now()
	if to.IsZero() || to.After(now) {
		to = now
	}
	to = to.UTC()
	if from.Before(to.Add(-traceLookback)) {
		from = to.Add(-traceLookback)
	}
	return from.UTC(), to
}

// SearchTraces lists the traces with a span from the targets, newest
// first. A trace's duration, span count, errors and root are its spans
// from the targets only: another Component's spans aren't read.
func (tr *Tracing) SearchTraces(ctx context.Context, targets []reconcile.LogTarget, q reconcile.TraceQuery) (reconcile.TracePage, error) {
	if len(targets) > reconcile.MaxLogTargets {
		return reconcile.TracePage{}, ErrTooManyTargets
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultTraceLimit
	}
	limit = min(limit, reconcile.MaxTraceQueryLimit)
	cur, err := reconcile.DecodeLogCursor(q.Before)
	if err != nil {
		return reconcile.TracePage{}, err
	}
	from, to := tr.window(q.From, q.To)
	page := reconcile.TracePage{Traces: []reconcile.TraceSummary{}}
	upper := to
	if !cur.Time.IsZero() {
		if cur.Time.After(to) {
			cur = reconcile.LogCursor{}
		} else {
			upper = cur.Time
		}
	}
	if upper.Before(from) {
		return page, nil
	}
	scopes, err := tr.resolve(ctx, targets)
	if err != nil || len(scopes) == 0 {
		return page, err
	}
	var where []string
	for _, s := range scopes {
		where = append(where, scopeWhere(s))
	}
	ask := limit + cur.Seen + 1
	if ask > maxTraceAnswer {
		// More traces at the cursor's nanosecond than one answer holds:
		// the rest of it is stepped over, and the page says so.
		cur = reconcile.LogCursor{Time: upper.Add(-time.Nanosecond)}
		upper, ask = cur.Time, limit+1
		page.Truncated = true
	}
	// The store's window is on the spans' start, to the microsecond. It
	// ends at to, not at the cursor: a trace started before the cursor
	// keeps its later spans in its counts on every page, and the HAVING
	// bounds the trace's start exactly. A trace started before from is
	// summarised from its spans since from.
	var rows []traceRow
	sql := func(root string) string {
		return traceSummarySQL(strings.Join(where, " OR "), q, upper.UnixNano(), ask, root)
	}
	err = tr.search(ctx, sql(rootOf), from, to.Add(time.Microsecond), ask, &rows)
	if errors.Is(err, errNoField) {
		err = tr.search(ctx, sql(rootNoParents), from, to.Add(time.Microsecond), ask, &rows)
	}
	if noTraces(err) {
		return page, nil
	}
	if err != nil {
		return reconcile.TracePage{}, fmt.Errorf("openchoreo tracing: %w", err)
	}
	var kept []traceRow
	for _, r := range rows {
		if reconcile.ValidTraceID(r.TraceID) && r.Start <= upper.UnixNano() {
			kept = append(kept, r)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].Start != kept[j].Start {
			return kept[i].Start > kept[j].Start
		}
		return kept[i].TraceID < kept[j].TraceID
	})
	skip := 0
	for skip < len(kept) && skip < cur.Seen && kept[skip].Start == cur.Time.UnixNano() {
		skip++
	}
	kept = kept[skip:]
	if len(kept) > limit {
		kept = kept[:limit]
		last := time.Unix(0, kept[len(kept)-1].Start).UTC()
		seen := 0
		if last.Equal(cur.Time) {
			seen = cur.Seen
		}
		for _, r := range kept {
			if r.Start == last.UnixNano() {
				seen++
			}
		}
		page.Next = reconcile.LogCursor{Time: last, Seen: seen}.Encode()
	}
	for _, r := range kept {
		page.Traces = append(page.Traces, reconcile.TraceSummary{
			TraceID:   r.TraceID,
			Start:     time.Unix(0, r.Start).UTC(),
			Duration:  time.Duration(max(r.End-r.Start, 0)),
			Root:      cutText(r.Root, maxAttributeValue),
			Spans:     r.Spans,
			HasErrors: r.Errors > 0,
		})
	}
	return page, nil
}

// spanColumns are the store's own span fields; every other field is an
// attribute.
var spanColumns = map[string]bool{
	"_timestamp": true, "_o2_ingest_ts": true, "trace_id": true, "span_id": true,
	"reference_parent_span_id": true, "reference_parent_trace_id": true, "reference_ref_type": true,
	"operation_name": true, "span_kind": true, "span_status": true, "status_code": true, "status_message": true,
	"start_time": true, "end_time": true, "duration": true, "flags": true, "events": true, "links": true,
}

// identityAttrRE is an attribute named like the platform's identity: a
// resource attribute (service_*, the store's prefix for them; the
// attested ones are shown as Component and Environment) or a span's own
// claim to one (k8s_*, openchoreo_*, container_*, and attr_*, the store's
// name for a span attribute that collides with a column). None is shown,
// so a span can't appear to name another workload.
var identityAttrRE = regexp.MustCompile(`^(service|k8s|openchoreo|container|attr)_`)

// spanKinds are OTLP's span kinds, as the store writes them.
var spanKinds = map[string]string{"1": "internal", "2": "server", "3": "client", "4": "producer", "5": "consumer"}

// spanRow is one stored span: its fields, decoded as they come.
type spanRow map[string]json.RawMessage

func (r spanRow) str(k string) string {
	var s string
	if json.Unmarshal(r[k], &s) == nil {
		return s
	}
	return ""
}

func (r spanRow) int(k string) int64 {
	var n json.Number
	if json.Unmarshal(r[k], &n) != nil {
		return 0
	}
	v, err := n.Int64()
	if err != nil {
		f, ferr := n.Float64()
		if ferr != nil || math.IsNaN(f) || f < math.MinInt64 || f >= math.MaxInt64 {
			return 0
		}
		return int64(f)
	}
	return v
}

// kind reads the span kind, written as a string ("2", as seen live) or a
// number.
func (r spanRow) kind() string {
	return spanKinds[strings.Trim(string(r["span_kind"]), `"`)]
}

// toSpan converts r, which the SQL selected by t's identity columns, to a
// Span named by t's Serendip IDs.
func toSpan(r spanRow, t reconcile.LogTarget) reconcile.Span {
	start, end := r.int("start_time"), r.int("end_time")
	sp := reconcile.Span{
		SpanID:      r.str("span_id"),
		ParentID:    r.str("reference_parent_span_id"),
		Name:        cutText(r.str("operation_name"), maxAttributeValue),
		Kind:        r.kind(),
		Start:       time.Unix(0, start).UTC(),
		Duration:    time.Duration(max(end-start, 0)),
		Failed:      r.str("span_status") == "ERROR",
		Status:      cutText(r.str("status_message"), maxStatusMessage),
		Component:   t.Deployment.Spec.Component,
		Environment: t.Environment.Metadata.ID,
	}
	var keys []string
	for k := range r {
		if !spanColumns[k] && !identityAttrRE.MatchString(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(sp.Attributes) == maxSpanAttributes {
			break
		}
		v := r[k]
		var s string
		if json.Unmarshal(v, &s) != nil {
			s = string(v) // a number or a boolean, as written
		}
		if s == "" || s == "null" {
			continue
		}
		if sp.Attributes == nil {
			sp.Attributes = map[string]string{}
		}
		sp.Attributes[cutText(k, 128)] = cutText(s, maxAttributeValue)
	}
	return sp
}

// cutText cuts s to at most n bytes, on a rune boundary.
func cutText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// Trace returns traceID's spans from t, in start order. Spans from other
// Components are counted in Hidden and never read. None from t is
// ErrTraceNotFound, whatever else the trace has.
func (tr *Tracing) Trace(ctx context.Context, t reconcile.LogTarget, traceID string) (reconcile.Trace, error) {
	if !reconcile.ValidTraceID(traceID) {
		return reconcile.Trace{}, reconcile.ErrTraceNotFound
	}
	scopes, err := tr.resolve(ctx, []reconcile.LogTarget{t})
	if err != nil {
		return reconcile.Trace{}, err
	}
	if len(scopes) == 0 {
		return reconcile.Trace{}, reconcile.ErrTraceNotFound
	}
	from, to := tr.window(time.Time{}, time.Time{})
	where := scopeWhere(scopes[0])
	var rows []spanRow
	sql := fmt.Sprintf(`SELECT * FROM "%s" WHERE trace_id = '%s' AND %s ORDER BY start_time ASC, span_id ASC LIMIT %d`,
		tracesStream, traceID, where, reconcile.MaxTraceSpans+1)
	err = tr.search(ctx, sql, from, to.Add(time.Microsecond), reconcile.MaxTraceSpans+1, &rows)
	if noTraces(err) {
		return reconcile.Trace{}, reconcile.ErrTraceNotFound
	}
	if err != nil {
		return reconcile.Trace{}, fmt.Errorf("openchoreo tracing: %w", err)
	}
	out := reconcile.Trace{TraceID: traceID, Spans: []reconcile.Span{}}
	for _, r := range rows {
		// The SQL selected these; checked again, so a store that ignored
		// part of the query can't show another Component's span.
		if r.str("trace_id") != traceID || r.str(colComponentUID) != scopes[0].ComponentUID ||
			r.str(colEnvironmentUID) != scopes[0].EnvironmentUID || r.str(colProjectUID) != scopes[0].ProjectUID {
			continue
		}
		if len(out.Spans) == reconcile.MaxTraceSpans {
			out.Truncated = true
			break
		}
		out.Spans = append(out.Spans, toSpan(r, t))
	}
	if len(out.Spans) == 0 {
		return reconcile.Trace{}, reconcile.ErrTraceNotFound
	}
	// Only counts of the others: one row, no field of theirs.
	var counts []struct {
		Total int `json:"total"`
		Own   int `json:"own"`
	}
	sql = fmt.Sprintf(`SELECT count(*) AS total, sum(CASE WHEN %s THEN 1 ELSE 0 END) AS own FROM "%s" WHERE trace_id = '%s'`,
		where, tracesStream, traceID)
	if err := tr.search(ctx, sql, from, to.Add(time.Microsecond), 1, &counts); err != nil {
		return reconcile.Trace{}, fmt.Errorf("openchoreo tracing: %w", err)
	}
	if len(counts) == 1 {
		out.Hidden = max(counts[0].Total-counts[0].Own, 0)
	}
	return out, nil
}

// EvaluateCondition reads a traceErrors condition: how many traces with a
// failed span from t were stored over the window. No traces stored at all
// is ErrNoData, not zero.
func (tr *Tracing) EvaluateCondition(ctx context.Context, t reconcile.LogTarget, c resource.AlertCondition, window time.Duration) (reconcile.ConditionValue, error) {
	if err := resource.ValidateCondition(c, false); err != nil {
		return reconcile.ConditionValue{}, &reconcile.ConditionRefusedError{Why: err.Error()}
	}
	if c.TraceErrors == nil {
		return reconcile.ConditionValue{}, &reconcile.ConditionRefusedError{Why: "the tracing capability reads only traceErrors conditions, not " + c.Type}
	}
	if window <= 0 || window%time.Minute != 0 {
		return reconcile.ConditionValue{}, fmt.Errorf("openchoreo tracing: a condition's window is whole minutes, not %s", window)
	}
	scopes, err := tr.resolve(ctx, []reconcile.LogTarget{t})
	if err != nil {
		return reconcile.ConditionValue{}, err
	}
	if len(scopes) == 0 {
		return reconcile.ConditionValue{}, reconcile.ErrNoData
	}
	now := tr.now().UTC()
	var rows []struct {
		N int `json:"n"`
	}
	sql := fmt.Sprintf(`SELECT count(DISTINCT trace_id) AS n FROM "%s" WHERE %s AND %s`, tracesStream, scopeWhere(scopes[0]), failedSpan)
	err = tr.search(ctx, sql, now.Add(-window), now, 1, &rows)
	if noTraces(err) {
		return reconcile.ConditionValue{}, reconcile.ErrNoData
	}
	if err != nil {
		return reconcile.ConditionValue{}, fmt.Errorf("openchoreo tracing: %w", err)
	}
	if len(rows) != 1 {
		return reconcile.ConditionValue{}, errors.New("openchoreo tracing: the count's answer has no row")
	}
	if rows[0].N < 0 {
		return reconcile.ConditionValue{}, fmt.Errorf("openchoreo tracing: the count's answer is %d", rows[0].N)
	}
	n := float64(rows[0].N)
	return reconcile.ConditionValue{Value: n, Current: n}, nil
}
