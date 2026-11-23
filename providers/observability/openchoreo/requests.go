package openchoreo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
)

// HTTP request metrics (specs/025-networking plan decisions 7 and 13):
// Hubble's httpV2 counters and latency histogram, scraped by the hubble
// job (prometheus-values.yaml), joined to the target's pods by the three
// UIDs on kube_pod_labels, as the restarts condition is. Only the
// server's side is read (reporter="server"): each request is then counted
// once, by the Component that answered it.
//
// The series, the conditions and the traffic overlay all use these
// queries, not the metrics adapter's metric=http: the adapter has no p95
// (T001), and its "unsuccessful" isn't defined as 5xx, which the alert
// conditions are. One definition keeps the chart and the alert agreeing.

const (
	hubbleRequests = `hubble_http_requests_total{reporter="server"}`
	hubbleFailed   = `hubble_http_requests_total{reporter="server",status=~"5.."}`
	hubbleLatency  = `hubble_http_request_duration_seconds_bucket{reporter="server"}`

	// minRateWindow is the shortest rate window: the scrape is every
	// minute, and a rate needs two samples.
	minRateWindow = 2 * time.Minute
)

// atTarget keeps expr's samples whose destination pod is one of scope's
// pods. expr is an instant vector of Hubble series; the join reads the
// pod as namespace and pod, which Hubble's series don't otherwise carry.
// win is expr's range ("[5m]"): the pods are those that existed at any
// time in it, so a pod replaced inside the window (a rollout) still
// counts. Joined to the pods alive at the instant alone, a rule resolved
// as soon as the slow pod was replaced (found in spec 025 T012). Every
// UID has been checked against uidRE.
func atTarget(expr string, s componentScope, win string) string {
	return `(label_replace(label_replace(` + expr + `, "namespace", "$1", "destination_namespace", "(.+)"), "pod", "$1", "destination_pod", "(.+)")` +
		` * on (namespace, pod) group_left() max by (namespace, pod) (max_over_time(kube_pod_labels{` +
		`label_openchoreo_dev_component_uid="` + s.ComponentUID + `",` +
		`label_openchoreo_dev_environment_uid="` + s.EnvironmentUID + `",` +
		`label_openchoreo_dev_project_uid="` + s.ProjectUID + `"}` + win + `)))`
}

// checkUIDs refuses a scope with a UID that isn't one, before it's put in
// PromQL.
func checkUIDs(s componentScope) error {
	for _, uid := range []string{s.ComponentUID, s.EnvironmentUID, s.ProjectUID} {
		if !uidRE.MatchString(uid) {
			return fmt.Errorf("openchoreo observability: a resolved UID isn't a UID (%d characters)", len(uid))
		}
	}
	return nil
}

// requestQueries are the five series' PromQL over a rate window of w
// minutes: requests and 5xx per second, and the latency percentiles in
// milliseconds. The 5xx series is 0, not missing, while there are
// requests but none failed.
func requestQueries(s componentScope, w int) (rate, failed, p50, p95, p99 string) {
	win := fmt.Sprintf("[%dm]", w)
	rate = `sum(` + atTarget(`rate(`+hubbleRequests+win+`)`, s, win) + `)`
	failed = `sum(` + atTarget(`rate(`+hubbleFailed+win+`)`, s, win) + `) or (` + rate + ` * 0)`
	q := func(p string) string {
		return `histogram_quantile(` + p + `, sum by (le) (` + atTarget(`rate(`+hubbleLatency+win+`)`, s, win) + `)) * 1000`
	}
	return rate, failed, q("0.5"), q("0.95"), q("0.99")
}

// WithRequestMetrics tells the provider whether the network layer records
// HTTP requests, and why not (the networking capability's facts, passed
// by cmd/serendipd). Without it request metrics are reported unavailable.
// Call it before the provider is shared: it isn't synchronised.
func (p *Provider) WithRequestMetrics(available func(ctx context.Context) (ok bool, why string)) *Provider {
	p.requests = available
	return p
}

// requestsAvailable says whether t's request metrics can be read, and if
// not, why, in the words Metrics' Unavailable uses.
func (p *Provider) requestsAvailable(ctx context.Context, t reconcile.LogTarget) (bool, string) {
	if p.requests == nil {
		return false, "requests: the cluster's network layer doesn't provide request metrics"
	}
	if ok, why := p.requests(ctx); !ok {
		if why == "" {
			why = "the cluster's network layer doesn't provide request metrics"
		}
		return false, "requests: " + why
	}
	if !hasHTTPEndpoint(t.Component) {
		return false, "requests: no HTTP endpoint"
	}
	return true, ""
}

// hasHTTPEndpoint reports whether c has a port served as HTTP: an exposed
// port is (the application provider's endpointFor); the others are TCP,
// which the network layer doesn't read requests on.
func hasHTTPEndpoint(c resource.Component) bool {
	for _, port := range c.Spec.Ports {
		if port.Expose {
			return true
		}
	}
	return false
}

// requestSeries reads the five series over [from, to] at step.
func (p *Provider) requestSeries(ctx context.Context, s componentScope, from, to time.Time, step time.Duration) (*reconcile.RequestSeries, error) {
	if err := checkUIDs(s); err != nil {
		return nil, err
	}
	w := int(max(step, minRateWindow) / time.Minute)
	rate, failed, p50, p95, p99 := requestQueries(s, w)
	queries := []string{rate, failed, p50, p95, p99}
	out := make([][]reconcile.MetricPoint, len(queries))
	errs := make([]error, len(queries))
	// Two at a time: each is a join over every pod's series, and the
	// smallest machines run Prometheus beside everything else.
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for k, q := range queries {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			out[k], errs[k] = p.prometheus.series(ctx, q, from, to, step)
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &reconcile.RequestSeries{Rate: out[0], ErrorRate: out[1], LatencyP50: out[2], LatencyP95: out[3], LatencyP99: out[4]}, nil
}

// requestErrorRateCondition is the percentage of the window's requests
// that failed (5xx). Under MinRequests requests (at least one) it's 0, so
// it doesn't fire; with no request series at all it's no data.
func (p *Provider) requestErrorRateCondition(ctx context.Context, t reconcile.LogTarget, s componentScope, r resource.RequestErrorRateCondition, now time.Time, window time.Duration) (reconcile.ConditionValue, error) {
	if !hasHTTPEndpoint(t.Component) {
		return reconcile.ConditionValue{}, &reconcile.ConditionRefusedError{Why: "the Component has no HTTP endpoint, so it has no request metrics"}
	}
	if err := checkUIDs(s); err != nil {
		return reconcile.ConditionValue{}, err
	}
	win := fmt.Sprintf("[%dm]", conditionMinutes(window))
	total, ok, err := p.prometheus.instant(ctx, `sum(`+atTarget(`increase(`+hubbleRequests+win+`)`, s, win)+`)`, now)
	if err != nil {
		return reconcile.ConditionValue{}, err
	}
	if !ok {
		// No request series for its pods in the window: never served one,
		// or nothing is collected. Either way it's not a 0% error rate.
		return reconcile.ConditionValue{}, reconcile.ErrNoData
	}
	failed, ok, err := p.prometheus.instant(ctx, `sum(`+atTarget(`increase(`+hubbleFailed+win+`)`, s, win)+`)`, now)
	if err != nil {
		return reconcile.ConditionValue{}, err
	}
	if !ok {
		failed = 0
	}
	if total < float64(max(r.MinRequests, 1)) {
		return reconcile.ConditionValue{Value: 0, Current: 0}, nil
	}
	v := min(failed/total*100, 100)
	return reconcile.ConditionValue{Value: v, Current: v}, nil
}

// requestLatencyCondition is the window's latency percentile in
// milliseconds. With no requests in the window it's no data: a latency
// can't be read without one.
func (p *Provider) requestLatencyCondition(ctx context.Context, t reconcile.LogTarget, s componentScope, r resource.RequestLatencyCondition, now time.Time, window time.Duration) (reconcile.ConditionValue, error) {
	if !hasHTTPEndpoint(t.Component) {
		return reconcile.ConditionValue{}, &reconcile.ConditionRefusedError{Why: "the Component has no HTTP endpoint, so it has no request metrics"}
	}
	if err := checkUIDs(s); err != nil {
		return reconcile.ConditionValue{}, err
	}
	// Percentile is 95 or 99 (resource.ValidateCondition), so 0.95 or 0.99.
	win := fmt.Sprintf("[%dm]", conditionMinutes(window))
	q := fmt.Sprintf(`histogram_quantile(0.%d, sum by (le) (%s)) * 1000`, r.Percentile,
		atTarget(`increase(`+hubbleLatency+win+`)`, s, win))
	v, ok, err := p.prometheus.instant(ctx, q, now)
	if err != nil {
		return reconcile.ConditionValue{}, err
	}
	if !ok {
		return reconcile.ConditionValue{}, reconcile.ErrNoData
	}
	return reconcile.ConditionValue{Value: v, Current: v}, nil
}

// conditionMinutes is a request condition's window in minutes, at least
// minRateWindow: a rule may ask for 1 minute, and over one scrape
// interval there's at most one sample, so it would never have data.
func conditionMinutes(window time.Duration) int {
	return int(max(window, minRateWindow) / time.Minute)
}

// requestsMessage words a failed request read for Metrics' Unavailable,
// without the store's answer.
func requestsMessage(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "requests: the read was cut short"
	}
	if errors.Is(err, errStoreUnreachable) {
		return "requests: the metrics store isn't reachable"
	}
	return "requests: the metrics store couldn't be read"
}
