package openchoreo

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// Provider is the observability capability over the adapters (plan
// decisions 5–7 and 10). Callers scope before calling: it reads only the
// targets it's given, one component-scoped query each, and never a wider
// one (decision 6).
type Provider struct {
	adapters   adapters
	prometheus prometheus
	scopes     *scopes
	now        func() time.Time
	// usage and budgetMiB are the package's disk use and budget, for the
	// built-in packageDisk rule (WithDiskUsage).
	usage     reconcile.PackageUsage
	budgetMiB int
	// requests says whether the network layer records HTTP requests
	// (WithRequestMetrics); nil is no.
	requests func(ctx context.Context) (bool, string)
	// flows reads the network layer's connection records (flows.go).
	flows *flowStore
	// maxAnswer is the most lines asked of one query (adapterMaxLimit;
	// smaller in tests).
	maxAnswer int
}

var _ reconcile.ObservabilityProvider = (*Provider)(nil)

// NewProvider returns the provider over client.
func NewProvider(client *kubernetes.Client) *Provider {
	return newProvider(client, time.Now)
}

func newProvider(client *kubernetes.Client, now func() time.Time) *Provider {
	return &Provider{adapters: adapters{client: client}, prometheus: prometheus{client: client}, scopes: newScopes(client, now),
		flows: newFlowStore(client), now: now, maxAnswer: adapterMaxLimit}
}

const (
	// defaultLogLimit is a page's size when the query doesn't say.
	defaultLogLimit = 100
	// maxRounds bounds how many times one page re-queries to step past a
	// full target's oldest line (search).
	maxRounds = 4
	// queryParallel is how many targets are queried at once.
	queryParallel = 4
	// minStep is the scrape interval: a finer step only repeats samples.
	minStep = time.Minute
)

// ErrTooManyTargets is returned for a search over more than
// reconcile.MaxLogTargets Deployments.
var ErrTooManyTargets = errors.New("openchoreo observability: at most " + fmt.Sprint(reconcile.MaxLogTargets) + " targets a search")

// argoLineRE matches the lines Argo's emissary writes into a step's main
// container (spec 022 T001 item 4), which the build provider hides too.
var argoLineRE = regexp.MustCompile(`(^|\s)level=\w+\s.*(^|\s)argo=true(\s|$)`)

// argoExitStatsRE matches the memory line Argo's executor prints as its
// init and wait containers exit. A run's stored lines carry no container
// (T006), so it's matched by its shape; the build provider reads only
// the main container and never shows it (T013: 4 such lines in a 37-line
// build).
var argoExitStatsRE = regexp.MustCompile(`^time="[^"]*" level=info msg="Alloc=\d+ TotalAlloc=\d+ Sys=\d+ NumGC=\d+ Goroutines=\d+"$`)

// line is a stored line, cut to MaxLogLineBytes as soon as it's read.
type line struct {
	reconcile.StoredLogLine
	cut bool
}

// logSource is one target's stored lines: a scope, and which answered
// entries are kept, and how they're attributed.
type logSource struct {
	scope any
	text  string
	keep  func(logEntry, *line) bool
}

// answer is one query's result: the lines kept, and what the store sent
// before any was dropped (whether it was as many as asked, and the oldest
// readable time).
type answer struct {
	lines  []line
	full   bool
	oldest time.Time
}

// query asks for up to limit lines in [from, upper], newest first. An
// answer too large for the client is asked again for half as many, and
// reported full: the store may hold more than it sent.
func (p *Provider) query(ctx context.Context, s *logSource, from, upper time.Time, limit int) (answer, error) {
	asked := limit
	for {
		entries, err := p.adapters.queryLogs(ctx, logsRequest{
			// The end is asked a microsecond late, since whether the
			// store's bound is inclusive isn't known; newer lines are
			// dropped below.
			StartTime: adapterTime(from), EndTime: adapterTime(upper.Add(time.Microsecond)),
			Limit: asked, SortOrder: "desc", SearchPhrase: s.text, SearchScope: s.scope,
		})
		if errors.Is(err, kubernetes.ErrBodyTooLarge) && asked > 1 {
			asked /= 2
			continue
		}
		if err != nil {
			return answer{}, err
		}
		a := answer{full: len(entries) >= asked}
		for _, e := range entries {
			t, err := parseAdapterTime(e.Timestamp)
			if err != nil {
				continue
			}
			if a.oldest.IsZero() || t.Before(a.oldest) {
				a.oldest = t
			}
			if t.After(upper) || t.Before(from) {
				continue
			}
			text, cut := cutLine(e.Log)
			l := line{StoredLogLine: reconcile.StoredLogLine{Time: t, Line: text}, cut: cut}
			if s.keep(e, &l) {
				a.lines = append(a.lines, l)
			}
		}
		return a, nil
	}
}

// SearchLogs reads targets' stored lines, newest first, one query per
// target, merged by time (decision 6). A target whose platform objects
// are gone is skipped; one given twice is read once.
func (p *Provider) SearchLogs(ctx context.Context, targets []reconcile.LogTarget, q reconcile.LogQuery) (reconcile.LogPage, error) {
	if len(targets) > reconcile.MaxLogTargets {
		return reconcile.LogPage{}, ErrTooManyTargets
	}
	var sources []*logSource
	seen := map[componentScope]bool{}
	for _, t := range targets {
		scope, err := p.scopes.component(ctx, t)
		if errors.Is(err, errNoScope) {
			// Nothing stored can be scoped to it exactly, and it's never
			// widened instead.
			continue
		}
		if err != nil {
			return reconcile.LogPage{}, fmt.Errorf("openchoreo observability: %w", err)
		}
		if seen[scope] {
			continue
		}
		seen[scope] = true
		compID := t.Deployment.Spec.Component
		sources = append(sources, &logSource{scope: scope, text: q.Text, keep: func(e logEntry, l *line) bool {
			// Defence in depth (spike finding 5): a line from anywhere
			// else is dropped, never passed on. Keep it: it's also what
			// makes the search phrase harmless, whatever the adapter does
			// with it upstream (T014 security review).
			m := e.Metadata
			if m.ComponentUID != scope.ComponentUID || m.EnvironmentUID != scope.EnvironmentUID || m.ProjectUID != scope.ProjectUID {
				return false
			}
			l.Component, l.Instance, l.Container = compID, m.PodName, m.ContainerName
			return true
		}})
	}
	return p.search(ctx, sources, q)
}

// BuildLogs reads a Build's stored lines by its run (decision 10). A
// build run's lines carry only their time and text, so Instance is
// empty, and Argo's own lines (its emissary's and its executor's exit
// stats) are dropped.
func (p *Provider) BuildLogs(ctx context.Context, b resource.Build, q reconcile.LogQuery) (reconcile.LogPage, error) {
	if b.Metadata.ID == "" {
		return reconcile.LogPage{}, errors.New("openchoreo observability: a Build without an ID")
	}
	var comp string
	for _, o := range b.Metadata.OwnerRefs {
		if o.Kind == resource.KindComponent {
			comp = o.ID
		}
	}
	s := &logSource{
		scope: workflowScope{Namespace: buildsNamespace, WorkflowRunName: buildRunName(b.Metadata.ID)},
		text:  q.Text,
		keep: func(e logEntry, l *line) bool {
			l.Component = comp
			return !argoLineRE.MatchString(e.Log) && !argoExitStatsRE.MatchString(strings.TrimSpace(e.Log))
		},
	}
	return p.search(ctx, []*logSource{s}, q)
}

// search pages over sources, newest first. Any source failing fails the
// page, so a partial answer never looks complete.
//
// Each round asks every source for the page's remaining lines, plus those
// the cursor skips, plus one, and merges them by time; ties keep the
// sources' order, then each store's own (RP-058). A source that answered
// less than asked has sent everything in the window, and isn't asked
// again for this page. One that answered in full may hold lines older
// than its oldest, so lines at or before the newest such oldest time (the
// boundary) aren't trusted that round: the page takes what's newer, and
// the next round starts at the boundary. When the boundary is the
// round's own upper bound, a tie group fills a whole answer: it's passed
// on as far as the cursor can count it, then skipped, and the page says
// it was cut.
//
// A page can come back short, even empty, with Next set, when its rounds
// ran out; the caller reads on.
func (p *Provider) search(ctx context.Context, sources []*logSource, q reconcile.LogQuery) (reconcile.LogPage, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLogLimit
	}
	limit = min(limit, reconcile.MaxLogQueryLimit)
	cur, err := reconcile.DecodeLogCursor(q.Before)
	if err != nil {
		return reconcile.LogPage{}, err
	}
	// The stores keep microseconds, and so do this package's cursors.
	cur.Time = cur.Time.Truncate(time.Microsecond)
	now := p.now()
	to := q.To
	if to.IsZero() || to.After(now) {
		to = now
	}
	to = to.UTC().Truncate(time.Microsecond)
	from := q.From.UTC().Truncate(time.Microsecond)
	if !cur.Time.IsZero() && cur.Time.After(to) {
		cur = reconcile.LogCursor{}
	}
	page := reconcile.LogPage{Lines: []reconcile.StoredLogLine{}}
	if len(sources) == 0 {
		return page, nil
	}
	var taken []line
	// complete[k]: source k sent everything in [from, its upper then],
	// kept in done[k].
	complete := make([]bool, len(sources))
	done := make([][]line, len(sources))
	more := false
	for round := 0; round < maxRounds && len(taken) < limit; round++ {
		upper := to
		if !cur.Time.IsZero() {
			upper = cur.Time
		}
		if upper.Before(from) {
			more = false
			break
		}
		need := limit - len(taken)
		ask := min(need+cur.Seen+1, p.maxAnswer)
		answers, err := p.queryAll(ctx, sources, complete, from, upper, ask)
		if err != nil {
			return reconcile.LogPage{}, err
		}
		var merged []line
		var boundary time.Time
		anyFull := false
		for k := range sources {
			if complete[k] {
				for _, l := range done[k] {
					if !l.Time.After(upper) {
						merged = append(merged, l)
					}
				}
				continue
			}
			a := answers[k]
			merged = append(merged, a.lines...)
			if !a.full {
				complete[k], done[k] = true, a.lines
				continue
			}
			anyFull = true
			oldest := a.oldest
			if oldest.IsZero() || oldest.After(upper) {
				// Nothing it sent says where its answer ended.
				oldest = upper
			}
			if oldest.After(boundary) {
				boundary = oldest
			}
		}
		sort.SliceStable(merged, func(i, j int) bool { return merged[i].Time.After(merged[j].Time) })
		merged = skipSeen(merged, cur)
		if !anyFull {
			take := min(need, len(merged))
			taken = append(taken, merged[:take]...)
			more = len(merged) > need
			if more {
				cur = cursorAfter(cur, merged[:take])
			}
			break
		}
		more = true
		newer := merged
		for k, l := range merged {
			if !l.Time.After(boundary) {
				newer = merged[:k]
				break
			}
		}
		if len(newer) > need {
			taken = append(taken, newer[:need]...)
			cur = cursorAfter(cur, newer[:need])
			break
		}
		taken = append(taken, newer...)
		if boundary.Before(upper) {
			cur = reconcile.LogCursor{Time: boundary}
			continue
		}
		// A full answer at upper alone: only that tie group is trusted.
		ties := merged
		for k, l := range merged {
			if !l.Time.Equal(upper) {
				ties = merged[:k]
				break
			}
		}
		ties = ties[:min(need, len(ties))]
		taken = append(taken, ties...)
		if next := cursorAfterOr(cur, ties); len(ties) > 0 && next.Seen+limit+1 <= p.maxAnswer {
			cur = next
		} else {
			cur = reconcile.LogCursor{Time: upper.Add(-time.Microsecond)}
			page.Truncated = true
		}
	}
	if more {
		page.Next = cur.Encode()
	}
	for _, l := range taken {
		page.Lines = append(page.Lines, l.StoredLogLine)
		page.Truncated = page.Truncated || l.cut
	}
	return page, nil
}

// queryAll asks every source not yet complete, a few at a time. The
// first failure stops the rest.
func (p *Provider) queryAll(ctx context.Context, sources []*logSource, complete []bool, from, upper time.Time, ask int) ([]answer, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	answers := make([]answer, len(sources))
	errs := make([]error, len(sources))
	sem := make(chan struct{}, queryParallel)
	var wg sync.WaitGroup
	for k, s := range sources {
		if complete[k] {
			continue
		}
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[k] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			answers[k], errs[k] = p.query(ctx, s, from, upper, ask)
			if errs[k] != nil {
				cancel()
			}
		})
	}
	wg.Wait()
	// Report the cause, not a sibling cancelled because of it.
	var first error
	for _, err := range errs {
		if err == nil {
			continue
		}
		if !errors.Is(err, context.Canceled) {
			return nil, fmt.Errorf("openchoreo observability: %w", err)
		}
		if first == nil {
			first = err
		}
	}
	if first != nil {
		return nil, fmt.Errorf("openchoreo observability: %w", first)
	}
	return answers, nil
}

// skipSeen drops the lines at the cursor's time that earlier pages
// returned. They sort first, since the round's upper bound is that time.
func skipSeen(lines []line, cur reconcile.LogCursor) []line {
	n := 0
	for n < len(lines) && n < cur.Seen && lines[n].Time.Equal(cur.Time) {
		n++
	}
	return lines[n:]
}

// cursorAfter is the cursor following taken, which continues from cur.
func cursorAfter(cur reconcile.LogCursor, taken []line) reconcile.LogCursor {
	last := taken[len(taken)-1].Time
	seen := 0
	if last.Equal(cur.Time) {
		seen = cur.Seen
	}
	for _, l := range taken {
		if l.Time.Equal(last) {
			seen++
		}
	}
	return reconcile.LogCursor{Time: last, Seen: seen}
}

// cursorAfterOr is cursorAfter, or cur when nothing was taken.
func cursorAfterOr(cur reconcile.LogCursor, taken []line) reconcile.LogCursor {
	if len(taken) == 0 {
		return cur
	}
	return cursorAfter(cur, taken)
}

// cutLine cuts s to reconcile.MaxLogLineBytes on a rune boundary.
func cutLine(s string) (string, bool) {
	if len(s) <= reconcile.MaxLogLineBytes {
		return s, false
	}
	n := reconcile.MaxLogLineBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

// Metrics reads t's CPU and memory, with its requests and limits
// (decision 7), and its HTTP request series where the network layer
// records them (requests.go). A failed request read is reported in
// Unavailable rather than failing the CPU and memory it came with.
func (p *Provider) Metrics(ctx context.Context, t reconcile.LogTarget, q reconcile.MetricsQuery) (reconcile.Metrics, error) {
	to := q.To
	if to.IsZero() {
		to = p.now()
	}
	if !q.From.Before(to) {
		return reconcile.Metrics{}, errors.New("openchoreo observability: the metrics range is empty")
	}
	reqOK, reqWhy := p.requestsAvailable(ctx, t)
	scope, err := p.scopes.component(ctx, t)
	if errors.Is(err, errNoScope) {
		if reqOK {
			reqWhy = "requests: the Deployment isn't on the platform"
		}
		return reconcile.Metrics{CPU: []reconcile.MetricPoint{}, Memory: []reconcile.MetricPoint{},
			Unavailable: []string{"cpu and memory: the Deployment isn't on the platform", reqWhy}}, nil
	}
	if err != nil {
		return reconcile.Metrics{}, fmt.Errorf("openchoreo observability: %w", err)
	}
	step := metricsStep(to.Sub(q.From), q.Step)
	r, err := p.adapters.queryResourceMetrics(ctx, metricsRequest{
		Metric: "resource", StartTime: adapterTime(q.From), EndTime: adapterTime(to),
		Step: fmt.Sprintf("%dm", int(step/time.Minute)), SearchScope: scope,
	})
	if err != nil {
		return reconcile.Metrics{}, err
	}
	// The adapter answers cores and bytes.
	const millicores, mib = 1000.0, 1.0 / (1 << 20)
	out := reconcile.Metrics{CPU: series(r.CPUUsage, millicores), Memory: series(r.MemoryUsage, mib)}
	var unavailable []string
	latest := func(ps []point, scale float64, what string) *float64 {
		s := series(ps, scale)
		if len(s) == 0 {
			unavailable = append(unavailable, what+": not set")
			return nil
		}
		v := s[len(s)-1].Value
		return &v
	}
	out.CPURequest = latest(r.CPURequests, millicores, "cpu request")
	out.CPULimit = latest(r.CPULimits, millicores, "cpu limit")
	out.MemoryRequest = latest(r.MemoryRequests, mib, "memory request")
	out.MemoryLimit = latest(r.MemoryLimits, mib, "memory limit")
	if len(out.CPU) == 0 {
		unavailable = append(unavailable, "cpu: no samples in this range")
	}
	if len(out.Memory) == 0 {
		unavailable = append(unavailable, "memory: no samples in this range")
	}
	switch {
	case !reqOK:
		unavailable = append(unavailable, reqWhy)
	default:
		req, err := p.requestSeries(ctx, scope, q.From, to, step)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return reconcile.Metrics{}, ctx.Err()
			}
			unavailable = append(unavailable, requestsMessage(err))
		case len(req.Rate) == 0:
			out.Requests = req
			unavailable = append(unavailable, "requests: none in this range")
		default:
			out.Requests = req
		}
	}
	out.Unavailable = unavailable
	return out, nil
}

// metricsStep is a whole number of minutes, at least asked and minStep
// (the scrape interval), giving at most MaxMetricPoints points over span.
func metricsStep(span, asked time.Duration) time.Duration {
	need := time.Duration(math.Ceil(float64(span) / float64(reconcile.MaxMetricPoints-1)))
	step := max(asked, need, minStep)
	return (step + time.Minute - 1) / time.Minute * time.Minute
}

// series converts ps by scale, oldest first, dropping unreadable or
// non-finite points, and keeps at most the newest MaxMetricPoints.
func series(ps []point, scale float64) []reconcile.MetricPoint {
	out := make([]reconcile.MetricPoint, 0, len(ps))
	for _, pt := range ps {
		t, err := time.Parse(time.RFC3339Nano, pt.Timestamp)
		if err != nil || math.IsNaN(pt.Value) || math.IsInf(pt.Value, 0) {
			continue
		}
		out = append(out, reconcile.MetricPoint{Time: t.UTC(), Value: pt.Value * scale})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	if len(out) > reconcile.MaxMetricPoints {
		out = out[len(out)-reconcile.MaxMetricPoints:]
	}
	return out
}
