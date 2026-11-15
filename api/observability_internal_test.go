package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/provider"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
)

// Regression tests for spec 023 T008's review findings; T009 has the
// wider edge cases.

func points(vals ...float64) []reconcile.MetricPoint {
	out := make([]reconcile.MetricPoint, len(vals))
	t0 := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	for i, v := range vals {
		out[i] = reconcile.MetricPoint{Time: t0.Add(time.Duration(i) * time.Minute), Value: v}
	}
	return out
}

func TestDownsample(t *testing.T) {
	for _, tc := range []struct{ n, max, want int }{{60, 60, 60}, {61, 60, 31}, {121, 60, 41}, {300, 60, 60}, {5, 60, 5}} {
		vals := make([]float64, tc.n)
		for i := range vals {
			vals[i] = float64(i)
		}
		got := downsample(points(vals...), tc.max)
		if len(got) != tc.want || len(got) > tc.max {
			t.Errorf("%d points to at most %d: %d, want %d", tc.n, tc.max, len(got), tc.want)
		}
		if last := got[len(got)-1].Time; !last.Equal(points(vals...)[tc.n-1].Time) {
			t.Errorf("%d points: the last bucket ends at %v", tc.n, last)
		}
	}
}

// A NaN or infinite sample is dropped, so the answer still encodes.
func TestFiniteMetrics(t *testing.T) {
	got := finite(points(1, math.NaN(), 3, math.Inf(1), math.Inf(-1)))
	if len(got) != 2 || got[0].Value != 1 || got[1].Value != 3 {
		t.Errorf("finite: %v", got)
	}
	if s := summarise(got); s.Max != 3 || s.Avg != 2 {
		t.Errorf("summary: %+v", s)
	}
	if _, err := json.Marshal(metricsAnswer{Metrics: reconcile.Metrics{CPU: got}, Summary: map[string]*seriesSummary{"cpu": summarise(got)}}); err != nil {
		t.Error(err)
	}
	if finite(nil) == nil {
		t.Error("finite(nil) is nil; the answer would carry null")
	}
}

// Ready in the registry, but no capability given to the server: a 503,
// not a nil error passed on (the review's finding 1).
func TestObservabilityNoProvider(t *testing.T) {
	reg := provider.NewRegistry()
	reg.SetReadiness(observabilityCapability, provider.Readiness{Ready: true})
	h := &ObservabilityHandler{ready: observabilityReadiness(reg, nil), retention: defaultRetention, now: time.Now}
	if err := h.unavailable(); err == nil {
		t.Fatal("unavailable with no provider")
	}
	rec := httptest.NewRecorder()
	h.fail(rec, httptest.NewRequest(http.MethodGet, "/", nil), h.unavailable())
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Errorf("answer: %d %v", rec.Code, rec.Header())
	}
}

// Not installed is said before anything else, so an Application with no
// readable Deployments doesn't read as "no logs" (finding 2).
func TestObservabilityReadinessFirst(t *testing.T) {
	h := &ObservabilityHandler{ready: observabilityReadiness(provider.NewRegistry(), nil), retention: defaultRetention, now: time.Now}
	for _, path := range []string{"/v1/applications/a/logs/search?environment=e", "/v1/deployments/d/logs/search", "/v1/deployments/d/metrics"} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		switch {
		case strings.HasPrefix(path, "/v1/applications"):
			h.SearchApplication(rec, r) // the stores are nil: it must answer first
		case strings.HasSuffix(path, "metrics"):
			h.Metrics(rec, r)
		default:
			h.SearchDeployment(rec, r)
		}
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "isn't installed") {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
}

// storedPages answers the pages it holds in turn, recording each query.
type storedPages struct {
	pages []reconcile.LogPage
	asked []reconcile.LogQuery
}

func (s *storedPages) BuildLogs(_ context.Context, _ resource.Build, q reconcile.LogQuery) (reconcile.LogPage, error) {
	s.asked = append(s.asked, q)
	if len(s.pages) == 0 {
		return reconcile.LogPage{}, nil
	}
	p := s.pages[0]
	s.pages = s.pages[1:]
	return p, nil
}

func (s *storedPages) SearchLogs(context.Context, []reconcile.LogTarget, reconcile.LogQuery) (reconcile.LogPage, error) {
	return reconcile.LogPage{}, nil
}

func (s *storedPages) Metrics(context.Context, reconcile.LogTarget, reconcile.MetricsQuery) (reconcile.Metrics, error) {
	return reconcile.Metrics{}, nil
}

func stored(texts ...string) []reconcile.StoredLogLine {
	out := make([]reconcile.StoredLogLine, len(texts))
	for i, s := range texts {
		out[i] = reconcile.StoredLogLine{Time: time.Now().Add(-time.Duration(i) * time.Second), Line: s}
	}
	return out
}

func TestReadStored(t *testing.T) {
	b := resource.Build{Metadata: resource.Metadata{ID: "b1", CreatedAt: time.Now().Add(-time.Hour)}}
	t.Run("empty pages with next read on", func(t *testing.T) {
		f := &storedPages{pages: []reconcile.LogPage{{Next: "c1"}, {Lines: stored("c", "b"), Next: "c2"}, {Next: "c3"}, {Lines: stored("a")}}}
		h := &BuildHandler{stored: f}
		lines, truncated, err := h.readStored(context.Background(), b, reconcile.LogOptions{})
		if err != nil || truncated || len(lines) != 3 || lines[0].Line != "a" || lines[2].Line != "c" || lines[0].Instance != "build" {
			t.Fatalf("lines %v, truncated %v, err %v", lines, truncated, err)
		}
		if len(f.asked) != 4 || f.asked[1].Before != "c1" || f.asked[3].Before != "c3" {
			t.Errorf("queries: %+v", f.asked)
		}
	})
	t.Run("tailLines caps it", func(t *testing.T) {
		f := &storedPages{pages: []reconcile.LogPage{{Lines: stored("e", "d"), Next: "c1"}, {Lines: stored("c", "b"), Next: "c2"}}}
		lines, truncated, _ := (&BuildHandler{stored: f}).readStored(context.Background(), b, reconcile.LogOptions{TailLines: 3})
		if len(lines) != 3 || !truncated || lines[2].Line != "e" || f.asked[1].Limit != 1 {
			t.Errorf("lines %v, truncated %v, asked %+v", lines, truncated, f.asked)
		}
	})
	t.Run("a byte budget", func(t *testing.T) {
		big := strings.Repeat("x", reconcile.MaxLogLineBytes)
		var page []string
		for range 300 {
			page = append(page, big)
		}
		f := &storedPages{pages: []reconcile.LogPage{{Lines: stored(page...), Next: "c1"}, {Lines: stored(page...)}}}
		lines, truncated, _ := (&BuildHandler{stored: f}).readStored(context.Background(), b, reconcile.LogOptions{})
		if !truncated || len(lines)*reconcile.MaxLogLineBytes > maxRecentBytes {
			t.Errorf("%d lines of 16 KiB, truncated %v", len(lines), truncated)
		}
	})
	t.Run("no creation time scans retention at most", func(t *testing.T) {
		f := &storedPages{}
		(&BuildHandler{stored: f}).readStored(context.Background(), resource.Build{}, reconcile.LogOptions{})
		if time.Since(f.asked[0].From) > defaultRetention+time.Minute {
			t.Errorf("from %v", f.asked[0].From)
		}
	})
}
