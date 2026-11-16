package reconcile

import (
	"errors"
	"testing"
	"time"
)

func TestEvalStats_Line(t *testing.T) {
	t0 := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	s := evalStats{since: t0}
	if got := s.line(t0.Add(time.Minute)); got != "" {
		t.Fatalf("no queries: got %q, want no line", got)
	}
	var jobs []*evalJob
	for i := 1; i <= 100; i++ {
		j := &evalJob{took: time.Duration(i) * time.Millisecond}
		switch {
		case i%25 == 0:
			j.err = errors.New("timeout")
		case i%10 == 0:
			j.err = ErrNoData
		}
		jobs = append(jobs, j)
	}
	s.add(jobs[:50], 3*time.Second, 2*time.Second)
	s.add(jobs[50:], 9*time.Second, time.Second)
	want := AlertStatsPrefix + " window=10m0s queries=100 failed=4 noData=8 mean=50ms p50=50ms p95=95ms max=100ms ticks=2 tickMax=9000ms lateMax=2000ms"
	if got := s.line(t0.Add(10 * time.Minute)); got != want {
		t.Fatalf("line:\n got %q\nwant %q", got, want)
	}
}

func TestEvalStats_MaybeLogStartsNextWindow(t *testing.T) {
	t0 := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	var s evalStats
	s.maybeLog(t0)
	if !s.since.Equal(t0) {
		t.Fatalf("first call should start the window at %v, got %v", t0, s.since)
	}
	s.add([]*evalJob{{took: time.Millisecond}}, time.Second, 0)
	s.maybeLog(t0.Add(5 * time.Minute))
	if len(s.queries) != 1 {
		t.Fatalf("window ended early: %d queries held", len(s.queries))
	}
	end := t0.Add(AlertStatsEvery)
	s.maybeLog(end)
	if len(s.queries) != 0 || s.ticks != 0 || !s.since.Equal(end) {
		t.Fatalf("window not reset: %+v", s)
	}
}
