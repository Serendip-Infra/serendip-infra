package reconcile

import (
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/resource"
)

// A resolution says why it resolved: a package removal mustn't read
// "back under 3 (now 24)" (spec 024 T010).
func TestResolutionDetail(t *testing.T) {
	logs := resource.AlertCondition{Type: resource.ConditionTypeLogMatch, LogMatch: &resource.LogMatchCondition{Text: "boom", Above: 3, WindowMinutes: 2}}
	tests := []struct {
		name, reason string
		value        float64
		lasted       time.Duration
		want         string
	}{
		{"cleared", resource.ResolvedClear, 1, 7 * time.Minute, `Log lines matching "boom" back under 3 in 2 min (now 1) after 7 min`},
		{"package removed", resource.ResolvedPackageRemoved, 24, 12 * time.Minute, "No longer watched: the observability package was removed (last value 24) after 12 min"},
		{"rule deleted", resource.ResolvedRuleDeleted, 5, 30 * time.Second, "No longer watched: its rule was deleted (last value 5) after less than a minute"},
		{"rule disabled", resource.ResolvedRuleDisabled, 5, 65 * time.Minute, "No longer watched: its rule was turned off (last value 5) after 1 h 5 min"},
		{"component gone", resource.ResolvedComponentGone, 5, 2 * time.Hour, "No longer watched: its component is gone (last value 5) after 2 h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolvedDetail(tt.reason, tt.value, tt.lasted)
			if got == "" {
				got = conditionDetail(logs, resource.DeliveryEventResolved, tt.value, 3, tt.lasted)
			}
			if got != tt.want {
				t.Fatalf("got %q\nwant %q", got, tt.want)
			}
		})
	}
}

// The request and trace conditions' notification lines (spec 025).
func TestConditionDetail_RequestAndTrace(t *testing.T) {
	for _, tt := range []struct {
		c       resource.AlertCondition
		current float64
		want    string
	}{
		{resource.AlertCondition{RequestErrorRate: &resource.RequestErrorRateCondition{AbovePercent: 5, WindowMinutes: 5}},
			12.5, "Failed requests above 5% over 5 min (now 12.5)"},
		{resource.AlertCondition{RequestLatency: &resource.RequestLatencyCondition{Percentile: 99, AboveMs: 250, WindowMinutes: 10}},
			900, "p99 latency above 250 ms over 10 min (now 900)"},
		{resource.AlertCondition{TraceErrors: &resource.TraceErrorsCondition{Above: 0, WindowMinutes: 15}},
			3, "Traces with errors above 0 in 15 min (now 3)"},
	} {
		if got := conditionDetail(tt.c, resource.DeliveryEventFiring, tt.current, Threshold(tt.c), 0); got != tt.want {
			t.Errorf("conditionDetail = %q, want %q", got, tt.want)
		}
	}
}
