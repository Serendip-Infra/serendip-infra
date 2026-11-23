package openchoreo

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
)

// The alert conditions (specs/024-alerts plan decision 3, as amended by
// T001's spike findings 1–3). Each is a fixed query built from the
// condition's validated settings and the target's resolved UIDs:
//   - metric: the metrics adapter's resource series over [now − for, now]
//     at a 1-minute step, which answers for + 1 points; the value is the
//     window's lowest point, so it's above only when every point is;
//   - logMatch: the logs adapter with limit 1, reading the answer's exact
//     total (spike finding 2); the lines are never kept (RP-067);
//   - restarts: kube-state-metrics' restart counter in Prometheus, max −
//     min over the window per container, joined to the pods' labels on
//     the three UIDs (spike finding 3: increase() extrapolates);
//   - packageDisk: the package's own disk use against its budget;
//   - requestErrorRate and requestLatency (spec 025): Hubble's request
//     counter and latency histogram over the window, joined the same way
//     (requests.go).
//
// A target whose platform objects don't exist has no data: ErrNoData,
// never a zero, so nothing resolves for lack of data.

// uidRE is what a resolved UID may hold before it's put in PromQL.
// Kubernetes UIDs are UUIDs; anything else is refused, so no quote or
// backslash can reach a query.
var uidRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// restartsQuery is the restarts template: %[1]d is the window in
// minutes; %[2]s, %[3]s and %[4]s the Component's, Environment's and
// Project's UIDs.
const restartsQuery = `sum((max_over_time(kube_pod_container_status_restarts_total[%[1]dm])` +
	` - min_over_time(kube_pod_container_status_restarts_total[%[1]dm]))` +
	` * on (namespace, pod) group_left() max by (namespace, pod) (kube_pod_labels{` +
	`label_openchoreo_dev_component_uid="%[2]s",` +
	`label_openchoreo_dev_environment_uid="%[3]s",` +
	`label_openchoreo_dev_project_uid="%[4]s"}))`

// restartSeriesQuery asks whether the target's pods have any restart
// series in the window, with restartsQuery's arguments.
const restartSeriesQuery = `count(last_over_time(kube_pod_container_status_restarts_total[%[1]dm])` +
	` * on (namespace, pod) group_left() max by (namespace, pod) (kube_pod_labels{` +
	`label_openchoreo_dev_component_uid="%[2]s",` +
	`label_openchoreo_dev_environment_uid="%[3]s",` +
	`label_openchoreo_dev_project_uid="%[4]s"}))`

// diskStale is how old a disk reading may be and still be used: the
// reader caches for 30 s (usage.go), so an older one means it's stuck.
const diskStale = 5 * time.Minute

// WithDiskUsage gives the provider its package's disk use and budget, for
// the built-in packageDisk rule. Without it that condition is refused.
// Call it before the provider is shared: it isn't synchronised.
func (p *Provider) WithDiskUsage(u reconcile.PackageUsage, budgetMiB int) *Provider {
	p.usage, p.budgetMiB = u, budgetMiB
	return p
}

// EvaluateCondition reads one alert condition (plan decision 3).
func (p *Provider) EvaluateCondition(ctx context.Context, t reconcile.LogTarget, c resource.AlertCondition, window time.Duration) (reconcile.ConditionValue, error) {
	if err := resource.ValidateCondition(c, c.PackageDisk != nil); err != nil {
		return reconcile.ConditionValue{}, &reconcile.ConditionRefusedError{Why: err.Error()}
	}
	if c.PackageDisk != nil {
		return p.packageDisk(ctx)
	}
	if window <= 0 || window%time.Minute != 0 {
		return reconcile.ConditionValue{}, fmt.Errorf("openchoreo observability: a condition's window is whole minutes, not %s", window)
	}
	scope, err := p.scopes.component(ctx, t)
	if errors.Is(err, errNoScope) {
		return reconcile.ConditionValue{}, reconcile.ErrNoData
	}
	if err != nil {
		return reconcile.ConditionValue{}, fmt.Errorf("openchoreo observability: %w", err)
	}
	now := p.now()
	switch {
	case c.Metric != nil:
		return p.metricCondition(ctx, scope, *c.Metric, now, window)
	case c.LogMatch != nil:
		return p.logMatchCondition(ctx, scope, *c.LogMatch, now, window)
	case c.Restarts != nil:
		return p.restartsCondition(ctx, scope, now, window)
	case c.RequestErrorRate != nil:
		return p.requestErrorRateCondition(ctx, t, scope, *c.RequestErrorRate, now, window)
	case c.RequestLatency != nil:
		return p.requestLatencyCondition(ctx, t, scope, *c.RequestLatency, now, window)
	}
	return reconcile.ConditionValue{}, &reconcile.ConditionRefusedError{Why: "unknown condition type " + c.Type}
}

func (p *Provider) metricCondition(ctx context.Context, scope componentScope, m resource.MetricCondition, now time.Time, window time.Duration) (reconcile.ConditionValue, error) {
	r, err := p.adapters.queryResourceMetrics(ctx, metricsRequest{
		Metric: "resource", StartTime: adapterTime(now.Add(-window)), EndTime: adapterTime(now),
		Step: "1m", SearchScope: scope,
	})
	if err != nil {
		return reconcile.ConditionValue{}, err
	}
	// The adapter answers cores and bytes; rules are in millicores and MiB.
	const millicores, mib = 1000.0, 1.0 / (1 << 20)
	usage, requests, limits, scale, what := r.CPUUsage, r.CPURequests, r.CPULimits, millicores, "CPU"
	if m.Metric == resource.MetricMemory {
		usage, requests, limits, scale, what = r.MemoryUsage, r.MemoryRequests, r.MemoryLimits, mib, "memory"
	}
	pts := series(usage, scale)
	// for minutes at a 1-minute step is for + 1 points (spike finding 1).
	need := int(window/time.Minute) + 1
	if len(pts) < need {
		return reconcile.ConditionValue{}, reconcile.ErrNoData
	}
	pts = pts[len(pts)-need:]
	var base []reconcile.MetricPoint
	switch m.As {
	case resource.MetricAsPercentOfRequest:
		base = series(requests, scale)
		if len(base) == 0 {
			return reconcile.ConditionValue{}, &reconcile.ConditionRefusedError{Why: "the Component sets no " + what + " request, so a percentage of it can't be read"}
		}
	case resource.MetricAsPercentOfLimit:
		base = series(limits, scale)
		if len(base) == 0 {
			return reconcile.ConditionValue{}, &reconcile.ConditionRefusedError{Why: "the Component sets no " + what + " limit, so a percentage of it can't be read"}
		}
	}
	lowest := math.Inf(1)
	var current float64
	for _, pt := range pts {
		v := pt.Value
		if base != nil {
			b, ok := baseAt(base, pt.Time)
			if !ok || b <= 0 {
				// A point without its request or limit can't be compared.
				return reconcile.ConditionValue{}, reconcile.ErrNoData
			}
			v = v / b * 100
		}
		lowest = min(lowest, v)
		current = v
	}
	return reconcile.ConditionValue{Value: lowest, Current: current}, nil
}

// baseAt is a request or limit series' value at t: its latest point at
// or before t. Requests and limits are step functions, so a point on a
// slightly different grid still pairs with the value then in force.
// ps is oldest first (series).
func baseAt(ps []reconcile.MetricPoint, t time.Time) (float64, bool) {
	i := sort.Search(len(ps), func(i int) bool { return ps[i].Time.After(t) })
	if i == 0 {
		return 0, false
	}
	return ps[i-1].Value, true
}

func (p *Provider) logMatchCondition(ctx context.Context, scope componentScope, l resource.LogMatchCondition, now time.Time, window time.Duration) (reconcile.ConditionValue, error) {
	req := logsRequest{
		StartTime: adapterTime(now.Add(-window)), EndTime: adapterTime(now),
		Limit: 1, SortOrder: "desc", SearchPhrase: l.Text, SearchScope: scope,
	}
	out, err := p.adapters.countLogs(ctx, req)
	if err != nil {
		return reconcile.ConditionValue{}, err
	}
	if out.Total == nil {
		// No total: count a page of above + 1 instead (plan decision 3's
		// fallback), so the value is at least "more than above".
		req.Limit = min(l.Above+1, p.maxAnswer)
		if out, err = p.adapters.countLogs(ctx, req); err != nil {
			return reconcile.ConditionValue{}, err
		}
		n := float64(min(len(out.Logs), req.Limit))
		return reconcile.ConditionValue{Value: n, Current: n}, nil
	}
	if *out.Total < 0 {
		return reconcile.ConditionValue{}, fmt.Errorf("openchoreo observability: the logs answer's total is %d", *out.Total)
	}
	n := float64(*out.Total)
	return reconcile.ConditionValue{Value: n, Current: n}, nil
}

func (p *Provider) restartsCondition(ctx context.Context, scope componentScope, now time.Time, window time.Duration) (reconcile.ConditionValue, error) {
	if err := checkUIDs(scope); err != nil {
		return reconcile.ConditionValue{}, err
	}
	q := fmt.Sprintf(restartsQuery, int(window/time.Minute), scope.ComponentUID, scope.EnvironmentUID, scope.ProjectUID)
	v, ok, err := p.prometheus.instant(ctx, q, now)
	if err != nil {
		return reconcile.ConditionValue{}, err
	}
	if !ok {
		// Nothing restarted inside the window (spike finding 3), or
		// nothing is collected: only the first is 0. If the pods' restart
		// series aren't there at all (kube-state-metrics down, or no pods),
		// it's missing data, so a firing Alert isn't resolved by it.
		seen, err := p.prometheus.exists(ctx, fmt.Sprintf(restartSeriesQuery, int(window/time.Minute),
			scope.ComponentUID, scope.EnvironmentUID, scope.ProjectUID), now)
		if err != nil {
			return reconcile.ConditionValue{}, err
		}
		if !seen {
			return reconcile.ConditionValue{}, reconcile.ErrNoData
		}
		v = 0
	}
	return reconcile.ConditionValue{Value: v, Current: v}, nil
}

func (p *Provider) packageDisk(ctx context.Context) (reconcile.ConditionValue, error) {
	if p.usage == nil || p.budgetMiB <= 0 {
		return reconcile.ConditionValue{}, &reconcile.ConditionRefusedError{Why: "this package's disk use can't be read"}
	}
	u, err := p.usage.Usage(ctx)
	if err != nil {
		return reconcile.ConditionValue{}, err
	}
	if u.ReadAt.IsZero() || p.now().Sub(u.ReadAt) > diskStale {
		return reconcile.ConditionValue{}, reconcile.ErrNoData
	}
	v := float64(u.UsedMiB) / float64(p.budgetMiB) * 100
	return reconcile.ConditionValue{Value: v, Current: v}, nil
}
