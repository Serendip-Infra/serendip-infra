package reconcile_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
)

// fakeTracing answers trace conditions with one value, or err.
type fakeTracing struct {
	reconcile.TracingProvider
	value float64
	err   error
	calls atomic.Int64
}

func (f *fakeTracing) EvaluateCondition(context.Context, reconcile.LogTarget, resource.AlertCondition, time.Duration) (reconcile.ConditionValue, error) {
	f.calls.Add(1)
	if f.err != nil {
		return reconcile.ConditionValue{}, f.err
	}
	return reconcile.ConditionValue{Value: f.value, Current: f.value}, nil
}

// withSources rebuilds f's evaluator with a tracing provider and a
// request-metrics check (specs/025-networking plan decision 13).
func (f *alertFixture) withSources(tr reconcile.TracingProvider, requests func(context.Context) (bool, string)) {
	f.e, f.d = reconcile.NewAlertEvaluator(reconcile.AlertEvaluatorOptions{
		Stores: f.stores, Provider: f.provider, Tracing: tr, RequestMetrics: requests,
		Installed: func(context.Context) (bool, error) { return true, nil },
		Now:       func() time.Time { return f.now },
	}, reconcile.DeliveryOptions{Notifier: f.notifier, ConsoleURL: "http://localhost:8080"})
}

func (f *alertFixture) ruleWith(name, comp string, c resource.AlertCondition) resource.AlertRule {
	f.t.Helper()
	return mustV(f.rules.Create(f.ctx, resource.AlertRule{
		Metadata: resource.Metadata{Name: name, OwnerRefs: []resource.OwnerRef{{Kind: resource.KindComponent, ID: comp}}},
		Spec: resource.AlertRuleSpec{EnvironmentID: f.env.Metadata.ID, Severity: resource.SeverityWarning, Summary: name,
			ChannelIDs: []string{f.channel}, Condition: c},
	}))
}

func (f *alertFixture) evaluating(id string) resource.Condition {
	f.t.Helper()
	c := findCond(f.ruleNow(id).Status.Conditions, resource.AlertRuleConditionEvaluating)
	if c == nil {
		f.t.Fatalf("rule %s has no Evaluating condition", id)
	}
	return *c
}

var (
	traceErrorsCond = resource.AlertCondition{Type: resource.ConditionTypeTraceErrors,
		TraceErrors: &resource.TraceErrorsCondition{Above: 0, WindowMinutes: 10}}
	latencyCond = resource.AlertCondition{Type: resource.ConditionTypeRequestLatency,
		RequestLatency: &resource.RequestLatencyCondition{Percentile: 95, AboveMs: 250, WindowMinutes: 5}}
)

// A trace condition without the tracing package is refused with that
// reason, and never reaches the observability provider.
func TestAlerts_TraceConditionWithoutTracing(t *testing.T) {
	f := newAlertFixture(t)
	web := f.component("web")
	r := f.ruleWith("trace-errors", web, traceErrorsCond)
	f.tick(0)
	f.tick(time.Minute)
	c := f.evaluating(r.Metadata.ID)
	if c.Reason != resource.ReasonConditionRefused || !strings.Contains(c.Message, "tracing package installed") {
		t.Fatalf("Evaluating = %+v, want ConditionRefused naming the tracing package", c)
	}
	if n := f.provider.calls.Load(); n != 0 {
		t.Errorf("the observability provider was asked a trace condition %d times", n)
	}
	if got := f.alertsOf(r.Metadata.ID); len(got) != 0 {
		t.Errorf("a refused condition raised %d Alerts", len(got))
	}
}

// With the tracing package, trace conditions go to it, and fire.
func TestAlerts_TraceConditionUsesTracing(t *testing.T) {
	f := newAlertFixture(t)
	tr := &fakeTracing{value: 3}
	f.withSources(tr, nil)
	web := f.component("web")
	r := f.ruleWith("trace-errors", web, traceErrorsCond)
	f.tick(0)
	f.tick(time.Minute)
	if tr.calls.Load() == 0 || f.provider.calls.Load() != 0 {
		t.Fatalf("tracing calls %d, observability calls %d: want tracing only", tr.calls.Load(), f.provider.calls.Load())
	}
	got := f.alertsOf(r.Metadata.ID)
	if len(got) != 1 || got[0].Status.State != resource.AlertFiring {
		t.Fatalf("alerts = %+v, want one firing", got)
	}

	// The tracing package not ready: inactive, with its own name.
	tr.err = reconcile.ErrProviderNotReady
	f.tick(time.Minute)
	if rr := f.ruleNow(r.Metadata.ID); rr.Status.Active || rr.Status.ActiveReason != resource.ReasonPackageNotReady {
		t.Errorf("rule status = %+v, want inactive PackageNotReady", rr.Status)
	}
	if c := f.evaluating(r.Metadata.ID); c.Message != "the tracing package isn't ready" {
		t.Errorf("not-ready message = %q, want the tracing package named", c.Message)
	}
}

// Request conditions are refused while the network layer doesn't record
// requests, with its reason, and go to the observability provider when
// it does.
func TestAlerts_RequestConditionNeedsRequestMetrics(t *testing.T) {
	f := newAlertFixture(t)
	var ok atomic.Bool
	const why = "request metrics need a network layer that records HTTP requests; this cluster's doesn't"
	f.withSources(nil, func(context.Context) (bool, string) {
		if ok.Load() {
			return true, ""
		}
		return false, why
	})
	web := f.component("web")
	r := f.ruleWith("latency", web, latencyCond)
	f.tick(0)
	f.tick(time.Minute)
	if c := f.evaluating(r.Metadata.ID); c.Reason != resource.ReasonConditionRefused || c.Message != why {
		t.Fatalf("Evaluating = %+v, want ConditionRefused with the network layer's reason", c)
	}
	if n := f.provider.calls.Load(); n != 0 {
		t.Fatalf("a refused request condition was queried %d times", n)
	}

	ok.Store(true)
	f.provider.set(web, 900)
	f.tick(time.Minute)
	if f.provider.calls.Load() == 0 {
		t.Fatal("the request condition wasn't queried once request metrics were available")
	}
	got := f.alertsOf(r.Metadata.ID)
	if len(got) != 1 || got[0].Status.State != resource.AlertFiring {
		t.Fatalf("alerts = %+v, want one firing at 900 ms over 250", got)
	}
	if c := f.evaluating(r.Metadata.ID); c.Status != resource.ConditionTrue {
		t.Errorf("Evaluating = %+v, want True", c)
	}
}

func TestThreshold_NewConditions(t *testing.T) {
	for _, tc := range []struct {
		c    resource.AlertCondition
		want float64
	}{
		{latencyCond, 250},
		{traceErrorsCond, 0},
		{resource.AlertCondition{Type: resource.ConditionTypeRequestErrorRate,
			RequestErrorRate: &resource.RequestErrorRateCondition{AbovePercent: 5, WindowMinutes: 5}}, 5},
	} {
		if got := reconcile.Threshold(tc.c); got != tc.want {
			t.Errorf("Threshold(%s) = %v, want %v", tc.c.Type, got, tc.want)
		}
	}
}

func TestValidTraceID(t *testing.T) {
	for id, want := range map[string]bool{
		"b45e5fc32a336c39e18115a4851b3ade":  true,
		"B45E5FC32A336C39E18115A4851B3ADE":  false, // W3C IDs are lowercase
		"00000000000000000000000000000000":  false, // the invalid all-zero ID
		"b45e5fc32a336c39e18115a4851b3ad":   false,
		"b45e5fc32a336c39e18115a4851b3adee": false,
		"b45e5fc32a336c39e18115a4851b3adg":  false,
		"' OR 1=1 --":                       false,
		"":                                  false,
	} {
		if got := reconcile.ValidTraceID(id); got != want {
			t.Errorf("ValidTraceID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestFlowSource_IsZero(t *testing.T) {
	if !(reconcile.FlowSource{}).IsZero() || !(reconcile.FlowSource{HostPath: "/var/run/x"}).IsZero() {
		t.Error("a FlowSource without a path and file isn't zero")
	}
	if (reconcile.FlowSource{HostPath: "/var/run/x", File: "events.log", Format: "json"}).IsZero() {
		t.Error("a complete FlowSource is zero")
	}
}

// Unwired, request metrics aren't assumed: request conditions are refused
// (constitution 6; T002 review).
func TestAlerts_RequestConditionUnwiredIsRefused(t *testing.T) {
	f := newAlertFixture(t)
	web := f.component("web")
	r := f.ruleWith("latency", web, latencyCond)
	f.tick(0)
	f.tick(time.Minute)
	if c := f.evaluating(r.Metadata.ID); c.Reason != resource.ReasonConditionRefused || c.Message != "request metrics aren't available on this server" {
		t.Fatalf("Evaluating = %+v, want ConditionRefused", c)
	}
	if n := f.provider.calls.Load(); n != 0 {
		t.Errorf("an unwired request condition was queried %d times", n)
	}
}
