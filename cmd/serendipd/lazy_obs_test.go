package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
)

// obsOnly is an observability provider with neither flows nor traffic.
type obsOnly struct{}

func (obsOnly) SearchLogs(context.Context, []reconcile.LogTarget, reconcile.LogQuery) (reconcile.LogPage, error) {
	return reconcile.LogPage{}, nil
}
func (obsOnly) BuildLogs(context.Context, resource.Build, reconcile.LogQuery) (reconcile.LogPage, error) {
	return reconcile.LogPage{}, nil
}
func (obsOnly) Metrics(context.Context, reconcile.LogTarget, reconcile.MetricsQuery) (reconcile.Metrics, error) {
	return reconcile.Metrics{}, nil
}
func (obsOnly) EvaluateCondition(context.Context, reconcile.LogTarget, resource.AlertCondition, time.Duration) (reconcile.ConditionValue, error) {
	return reconcile.ConditionValue{}, nil
}

// obsFlows keeps flows and traffic.
type obsFlows struct{ obsOnly }

func (obsFlows) Flows(context.Context, []reconcile.LogTarget, reconcile.FlowQuery) (reconcile.FlowPage, error) {
	return reconcile.FlowPage{Flows: []reconcile.Flow{{Count: 1}}}, nil
}
func (obsFlows) Traffic(context.Context, []reconcile.LogTarget, time.Time, time.Time) ([]reconcile.TrafficEdge, error) {
	return []reconcile.TrafficEdge{{From: "a", To: "b"}}, nil
}

// The lazy handle forwards flows and traffic (spec 025 T009): not ready
// before it's set, not collected by a provider without them.
func TestLazyObservability_FlowsAndTraffic(t *testing.T) {
	ctx := context.Background()
	var l lazyObservability
	if _, err := l.Flows(ctx, nil, reconcile.FlowQuery{}); !errors.Is(err, reconcile.ErrProviderNotReady) {
		t.Errorf("unset flows: %v", err)
	}
	l.set(obsOnly{})
	if _, err := l.Flows(ctx, nil, reconcile.FlowQuery{}); !errors.Is(err, reconcile.ErrNotCollected) {
		t.Errorf("flows without a reader: %v", err)
	}
	if _, err := l.Traffic(ctx, nil, time.Time{}, time.Time{}); !errors.Is(err, reconcile.ErrNotCollected) {
		t.Errorf("traffic without a reader: %v", err)
	}
	l.set(obsFlows{})
	if p, err := l.Flows(ctx, nil, reconcile.FlowQuery{}); err != nil || len(p.Flows) != 1 {
		t.Errorf("flows: %v %v", p, err)
	}
	if e, err := l.Traffic(ctx, nil, time.Time{}, time.Time{}); err != nil || len(e) != 1 {
		t.Errorf("traffic: %v %v", e, err)
	}
}
