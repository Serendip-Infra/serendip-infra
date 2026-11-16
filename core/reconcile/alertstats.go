package reconcile

import (
	"errors"
	"fmt"
	"log"
	"slices"
	"time"
)

// AlertStatsEvery is how often the evaluator logs its query timings
// (spec 024 T011): how long condition queries take, and whether rules
// are evaluated on time. One line per window, and none while no rule is
// evaluated.
const AlertStatsEvery = 10 * time.Minute

// AlertStatsPrefix starts the evaluator's timing line. The fields after
// it are space-separated key=value pairs; the bench harness reads them.
const AlertStatsPrefix = "reconcile: alerts: stats:"

// evalStats collects one window's timings. It's only touched by the
// evaluator's goroutine: each query's time is copied from its job after
// the workers are done.
type evalStats struct {
	since   time.Time
	queries []time.Duration
	failed  int
	// noData counts queries that answered "nothing in the window yet",
	// which aren't failures.
	noData  int
	ticks   int
	maxTick time.Duration
	// maxLate is the furthest past its due time (nextDue) a rule was
	// picked up by a tick.
	maxLate time.Duration
}

// add records one tick's queries and how long the tick took.
func (s *evalStats) add(jobs []*evalJob, tick, late time.Duration) {
	for _, j := range jobs {
		if j.took == 0 && j.err != nil {
			// Skipped at shutdown: no query ran.
			continue
		}
		s.queries = append(s.queries, j.took)
		switch {
		case errors.Is(j.err, ErrNoData):
			s.noData++
		case j.err != nil:
			s.failed++
		}
	}
	s.ticks++
	s.maxTick = max(s.maxTick, tick)
	s.maxLate = max(s.maxLate, late)
}

// line renders the window ending at now, or "" if no query ran in it.
func (s *evalStats) line(now time.Time) string {
	n := len(s.queries)
	if n == 0 {
		return ""
	}
	q := slices.Clone(s.queries)
	slices.Sort(q)
	var sum time.Duration
	for _, d := range q {
		sum += d
	}
	pct := func(p int) time.Duration { return q[(n*p+99)/100-1] }
	ms := func(d time.Duration) string { return fmt.Sprintf("%dms", d.Milliseconds()) }
	return fmt.Sprintf("%s window=%s queries=%d failed=%d noData=%d mean=%s p50=%s p95=%s max=%s ticks=%d tickMax=%s lateMax=%s",
		AlertStatsPrefix, now.Sub(s.since).Round(time.Second), n, s.failed, s.noData,
		ms(sum/time.Duration(n)), ms(pct(50)), ms(pct(95)), ms(q[n-1]),
		s.ticks, ms(s.maxTick), ms(s.maxLate))
}

// maybeLog logs the window and starts the next once AlertStatsEvery has
// passed.
func (s *evalStats) maybeLog(now time.Time) {
	if s.since.IsZero() {
		s.since = now
		return
	}
	if now.Sub(s.since) < AlertStatsEvery {
		return
	}
	if l := s.line(now); l != "" {
		log.Print(l)
	}
	*s = evalStats{since: now, queries: s.queries[:0]}
}
