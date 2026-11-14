package openchoreo

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
)

// Spec 023 T009: cursors across time ranges, and an empty store. The tests
// in provider_test.go cover ties, filtering and a cursor past the range.

func names(ls []reconcile.StoredLogLine) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.Line)
	}
	return out
}

// rangeWorld stores 14 lines at these offsets (ms after t0), with tie
// groups of 3 (8), 2 (6) and 2 (3), named l0..l13 newest first.
func rangeWorld(t *testing.T) (*fakeObserver, reconcile.LogTarget, *Provider) {
	t.Helper()
	f := &fakeObserver{}
	ta := f.target("a", "dev")
	for k, ms := range []int{9, 8, 8, 8, 7, 6, 6, 5, 4, 3, 3, 2, 1, 0} {
		f.lines = append(f.lines, fakeLine{compUID: "uid-a", envUID: "uid-dev", at: t0.Add(time.Duration(ms) * time.Millisecond), pod: "a-pod", line: fmt.Sprintf("l%d", k)})
	}
	return f, ta, newTestProvider(t, f)
}

func ms(n int) time.Time { return t0.Add(time.Duration(n) * time.Millisecond) }

// TestSearchLogs_CursorReusedAcrossRanges: a cursor from one range, used
// with another. Narrowing From keeps what's still in range, once each;
// narrowing To at or after the cursor changes nothing; a To before the
// cursor starts again at that range's first page.
func TestSearchLogs_CursorReusedAcrossRanges(t *testing.T) {
	_, ta, p := rangeWorld(t)
	targets := []reconcile.LogTarget{ta}
	wide := reconcile.LogQuery{From: t0.Add(-time.Hour), Limit: 3}
	first, err := p.SearchLogs(context.Background(), targets, wide)
	if err != nil || first.Next == "" {
		t.Fatalf("first page %+v, %v", first, err)
	}
	cur, _ := reconcile.DecodeLogCursor(first.Next)
	if !cur.Time.Equal(ms(8)) || cur.Seen != 2 { // l0 at 9, l1 and l2 at 8
		t.Fatalf("cursor %+v, want 8 ms after 2 seen: the test's premise", cur)
	}
	all := allPages(t, func(before string) (reconcile.LogPage, error) {
		wide.Before = before
		return p.SearchLogs(context.Background(), targets, wide)
	})
	tail := all[3:] // what the first page's cursor leaves

	read := func(q reconcile.LogQuery) []reconcile.StoredLogLine {
		return allPages(t, func(before string) (reconcile.LogPage, error) {
			if before == "" {
				before = first.Next
			}
			q.Before = before
			return p.SearchLogs(context.Background(), targets, q)
		})
	}

	t.Run("From narrowed", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			from time.Time
		}{
			{"unchanged", t0.Add(-time.Hour)},
			{"at the oldest line", ms(0)},
			{"just after the oldest", ms(0).Add(time.Microsecond)},
			{"at a tie group", ms(3)},
			{"inside a tie group's timestamp", ms(6)},
			{"at the cursor's time", ms(8)},
			{"one microsecond under the cursor's time", ms(8).Add(-time.Microsecond)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var want []string
				for _, l := range tail {
					if !l.Time.Before(tc.from) {
						want = append(want, l.Line)
					}
				}
				got := read(reconcile.LogQuery{From: tc.from, Limit: 3})
				if !slices.Equal(names(got), want) {
					t.Errorf("got %v\nwant %v", names(got), want)
				}
				// A different page size over the same cursor agrees.
				if got := read(reconcile.LogQuery{From: tc.from, Limit: 1}); !slices.Equal(names(got), want) {
					t.Errorf("limit 1: got %v\nwant %v", names(got), want)
				}
			})
		}
	})

	t.Run("From after the cursor's time is an empty last page", func(t *testing.T) {
		for _, from := range []time.Time{ms(8).Add(time.Microsecond), ms(9), t0.Add(time.Hour - time.Second)} {
			page, err := p.SearchLogs(context.Background(), targets, reconcile.LogQuery{From: from, Limit: 3, Before: first.Next})
			if err != nil || len(page.Lines) != 0 || page.Next != "" || page.Lines == nil {
				t.Errorf("from %v: %+v, %v", from, page, err)
			}
		}
	})

	t.Run("To at or after the cursor changes nothing", func(t *testing.T) {
		for _, to := range []time.Time{ms(8), ms(8).Add(500 * time.Microsecond), ms(9), t0.Add(24 * time.Hour), {}} {
			got := read(reconcile.LogQuery{From: t0.Add(-time.Hour), To: to, Limit: 3})
			if !slices.Equal(names(got), names(tail)) {
				t.Errorf("to %v: got %v\nwant %v", to, names(got), names(tail))
			}
		}
	})

	t.Run("To before the cursor starts that range again", func(t *testing.T) {
		for _, to := range []time.Time{ms(7), ms(5).Add(time.Microsecond), ms(0)} {
			fresh := allPages(t, func(before string) (reconcile.LogPage, error) {
				return p.SearchLogs(context.Background(), targets, reconcile.LogQuery{From: t0.Add(-time.Hour), To: to, Limit: 3, Before: before})
			})
			got := read(reconcile.LogQuery{From: t0.Add(-time.Hour), To: to, Limit: 3})
			if len(fresh) == 0 || !slices.Equal(names(got), names(fresh)) {
				t.Errorf("to %v: got %v\nwant the fresh read %v", to, names(got), names(fresh))
			}
			for _, l := range got {
				if l.Time.After(to) {
					t.Errorf("to %v: %s at %v is past it", to, l.Line, l.Time)
				}
			}
		}
	})
}

// TestSearchLogs_CursorAtTheEdgesOfTheRange: From is inclusive, so a cursor
// at From still reads the lines there that it hasn't counted; one
// microsecond before it has nothing left. A cursor at To counts its tie
// group from the top.
func TestSearchLogs_CursorAtTheEdgesOfTheRange(t *testing.T) {
	_, ta, p := rangeWorld(t)
	targets := []reconcile.LogTarget{ta}
	// Offsets 3 ms hold l9 and l10; 2 ms holds l11.
	for _, tc := range []struct {
		name   string
		q      reconcile.LogQuery
		cursor reconcile.LogCursor
		want   []string
	}{
		{"at From, none seen", reconcile.LogQuery{From: ms(3)}, reconcile.LogCursor{Time: ms(3)}, []string{"l9", "l10"}},
		{"at From, one seen", reconcile.LogQuery{From: ms(3)}, reconcile.LogCursor{Time: ms(3), Seen: 1}, []string{"l10"}},
		{"at From, all seen", reconcile.LogQuery{From: ms(3)}, reconcile.LogCursor{Time: ms(3), Seen: 2}, nil},
		{"seen more than there are", reconcile.LogQuery{From: ms(3)}, reconcile.LogCursor{Time: ms(3), Seen: 50}, nil},
		{"just before From", reconcile.LogQuery{From: ms(3)}, reconcile.LogCursor{Time: ms(3).Add(-time.Microsecond)}, nil},
		{"at To, none seen", reconcile.LogQuery{From: ms(0), To: ms(8)}, reconcile.LogCursor{Time: ms(8)}, []string{"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9", "l10", "l11", "l12", "l13"}},
		{"at To, two seen", reconcile.LogQuery{From: ms(0), To: ms(8)}, reconcile.LogCursor{Time: ms(8), Seen: 2}, []string{"l3", "l4", "l5", "l6", "l7", "l8", "l9", "l10", "l11", "l12", "l13"}},
		{"between lines", reconcile.LogQuery{From: ms(0)}, reconcile.LogCursor{Time: ms(7).Add(500 * time.Microsecond)}, []string{"l4", "l5", "l6", "l7", "l8", "l9", "l10", "l11", "l12", "l13"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.q.Limit = 4
			got := allPages(t, func(before string) (reconcile.LogPage, error) {
				if before == "" {
					before = tc.cursor.Encode()
				}
				tc.q.Before = before
				return p.SearchLogs(context.Background(), targets, tc.q)
			})
			if !slices.Equal(names(got), tc.want) {
				t.Errorf("got %v\nwant %v", names(got), tc.want)
			}
		})
	}
}

// TestSearch_EmptyStore: nothing stored reads as an empty page that ends,
// never null, an error, or a Next to follow.
func TestSearch_EmptyStore(t *testing.T) {
	cursor := reconcile.LogCursor{Time: ms(5), Seen: 2}.Encode()
	var b resource.Build
	b.Metadata.ID = "build-empty"
	for _, tc := range []struct {
		name string
		read func(*Provider, reconcile.LogTarget, string) (reconcile.LogPage, error)
	}{
		{"deployment search", func(p *Provider, ta reconcile.LogTarget, before string) (reconcile.LogPage, error) {
			return p.SearchLogs(context.Background(), []reconcile.LogTarget{ta}, reconcile.LogQuery{From: t0.Add(-time.Hour), Before: before})
		}},
		{"two targets", func(p *Provider, ta reconcile.LogTarget, before string) (reconcile.LogPage, error) {
			tb := ta
			tb.Component.Metadata.ID, tb.Deployment.Spec.Component = "b", "b"
			return p.SearchLogs(context.Background(), []reconcile.LogTarget{ta, tb}, reconcile.LogQuery{From: t0.Add(-time.Hour), Before: before})
		}},
		{"text matching nothing", func(p *Provider, ta reconcile.LogTarget, before string) (reconcile.LogPage, error) {
			return p.SearchLogs(context.Background(), []reconcile.LogTarget{ta}, reconcile.LogQuery{From: t0.Add(-time.Hour), Text: "nope", Before: before})
		}},
		{"build logs", func(p *Provider, _ reconcile.LogTarget, before string) (reconcile.LogPage, error) {
			return p.BuildLogs(context.Background(), b, reconcile.LogQuery{From: t0.Add(-time.Hour), Before: before})
		}},
		{"no targets", func(p *Provider, _ reconcile.LogTarget, before string) (reconcile.LogPage, error) {
			return p.SearchLogs(context.Background(), nil, reconcile.LogQuery{From: t0.Add(-time.Hour), Before: before})
		}},
	} {
		for _, before := range []string{"", cursor} {
			t.Run(fmt.Sprintf("%s/cursor=%v", tc.name, before != ""), func(t *testing.T) {
				f := &fakeObserver{}
				ta := f.target("a", "dev")
				f.target("b", "dev")
				page, err := tc.read(newTestProvider(t, f), ta, before)
				if err != nil {
					t.Fatal(err)
				}
				if page.Lines == nil || len(page.Lines) != 0 || page.Next != "" || page.Truncated {
					t.Errorf("page = %+v", page)
				}
				if raw, _ := json.Marshal(page); string(raw) != `{"lines":[]}` {
					t.Errorf("encoded as %s", raw)
				}
			})
		}
	}
}

func TestMetrics_EmptyStore(t *testing.T) {
	f := &fakeObserver{}
	ta := f.target("a", "dev")
	m, err := newTestProvider(t, f).Metrics(context.Background(), ta, reconcile.MetricsQuery{From: t0, To: t0.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if m.CPU == nil || m.Memory == nil || len(m.CPU) != 0 || len(m.Memory) != 0 {
		t.Errorf("cpu %#v, memory %#v: want empty, not nil", m.CPU, m.Memory)
	}
	if m.CPURequest != nil || m.CPULimit != nil || m.MemoryRequest != nil || m.MemoryLimit != nil {
		t.Errorf("requests and limits from nothing: %+v", m)
	}
	for _, want := range []string{"cpu: no samples in this range", "memory: no samples in this range", "cpu request: not set", "memory limit: not set", httpUnavailable} {
		if !slices.Contains(m.Unavailable, want) {
			t.Errorf("unavailable %q lacks %q", m.Unavailable, want)
		}
	}
	var enc map[string]json.RawMessage
	raw, _ := json.Marshal(m)
	_ = json.Unmarshal(raw, &enc)
	if string(enc["cpu"]) != "[]" || string(enc["memory"]) != "[]" {
		t.Errorf("encoded as %s", raw)
	}
}
