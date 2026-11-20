package api

import (
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/resource"
)

// The stored lastEvaluated is kept when it's newer than the live one, or
// when there's no live one (RP-080).
func TestWithLiveEvaluated(t *testing.T) {
	stored := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	older, newer := stored.Add(-time.Minute), stored.Add(time.Minute)
	live := func(at time.Time, ok bool) func(string) (time.Time, bool) {
		return func(string) (time.Time, bool) { return at, ok }
	}
	tests := map[string]struct {
		evaluated func(string) (time.Time, bool)
		want      time.Time
	}{
		"no evaluator":        {nil, stored},
		"not evaluated since": {live(newer, false), stored},
		"live is older":       {live(older, true), stored},
		"live is newer":       {live(newer, true), newer},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := stored
			r := resource.AlertRule{Status: resource.AlertRuleStatus{LastEvaluated: &s}}
			got := withLiveEvaluated(r, tt.evaluated)
			if !got.Status.LastEvaluated.Equal(tt.want) {
				t.Errorf("lastEvaluated = %v, want %v", got.Status.LastEvaluated, tt.want)
			}
			if !r.Status.LastEvaluated.Equal(stored) {
				t.Error("the stored rule was changed")
			}
		})
	}
}
