package openchoreo

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
)

// The traffic overlay (specs/025-networking plan decision 8): HTTP
// requests per second between the targets' Components over a window,
// from Hubble's request counter. Two queries, joined here rather than in
// PromQL: the targets' Environment's pods with their Component UIDs, and
// the request rates by source and destination pod in those pods'
// namespaces. An edge is kept only when both of its ends are targets.

var _ reconcile.TrafficReader = (*Provider)(nil)

// ErrRequestsUnavailable is Traffic's answer when the network layer
// doesn't record HTTP requests; the error's text says why.
var ErrRequestsUnavailable = errors.New("request metrics aren't available")

// dnsLabelRE is what a namespace or pod name read back from the store
// may be before it's put in a query: a DNS label (RFC 1123) or subdomain.
var dnsLabelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

// trafficPodsQuery lists the Environment's pods seen in the window (%[1]d
// seconds) with their Component UIDs (%[2]s is the Environment's UID).
const trafficPodsQuery = `max by (namespace, pod, label_openchoreo_dev_component_uid) ` +
	`(max_over_time(kube_pod_labels{label_openchoreo_dev_environment_uid="%[2]s"}[%[1]ds]))`

// trafficRatesQuery is the request rate by both ends' pods and the
// status, over %[1]d seconds, answered in the namespaces %[2]s (a regex
// alternation of checked names).
const trafficRatesQuery = `sum by (source_namespace, source_pod, destination_namespace, destination_pod, status) ` +
	`(rate(hubble_http_requests_total{reporter="server",destination_namespace=~"%[2]s"}[%[1]ds]))`

// Traffic reads the HTTP traffic between targets over [from, to).
// Targets must share one Environment. A target whose platform objects
// are gone is left out.
func (p *Provider) Traffic(ctx context.Context, targets []reconcile.LogTarget, from, to time.Time) ([]reconcile.TrafficEdge, error) {
	if len(targets) > reconcile.MaxLogTargets {
		return nil, ErrTooManyTargets
	}
	if p.requests == nil {
		return nil, fmt.Errorf("openchoreo observability: %w: the cluster's network layer doesn't provide request metrics", ErrRequestsUnavailable)
	}
	if ok, why := p.requests(ctx); !ok {
		return nil, fmt.Errorf("openchoreo observability: %w: %s", ErrRequestsUnavailable, why)
	}
	if to.IsZero() || to.After(p.now()) {
		to = p.now()
	}
	if !from.Before(to) {
		return nil, errors.New("openchoreo observability: the traffic range is empty")
	}
	comps := map[string]string{} // Component UID -> Serendip Component ID
	envUID := ""
	for _, t := range targets {
		s, err := p.scopes.component(ctx, t)
		if errors.Is(err, errNoScope) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("openchoreo observability: %w", err)
		}
		if err := checkUIDs(s); err != nil {
			return nil, err
		}
		if envUID != "" && s.EnvironmentUID != envUID {
			return nil, errors.New("openchoreo observability: traffic targets are in more than one Environment")
		}
		envUID = s.EnvironmentUID
		comps[s.ComponentUID] = t.Deployment.Spec.Component
	}
	if len(comps) == 0 {
		return []reconcile.TrafficEdge{}, nil
	}
	secs := int(math.Ceil(max(to.Sub(from), minRateWindow).Seconds()))

	pods, err := p.prometheus.vector(ctx, fmt.Sprintf(trafficPodsQuery, secs, envUID), to)
	if err != nil {
		return nil, err
	}
	owner := map[[2]string]string{} // {namespace, pod} -> Serendip Component ID
	namespaces := map[string]bool{}
	for _, s := range pods {
		comp, ok := comps[s.labels["label_openchoreo_dev_component_uid"]]
		ns, pod := s.labels["namespace"], s.labels["pod"]
		if !ok || !dnsLabelRE.MatchString(ns) || pod == "" {
			continue
		}
		owner[[2]string{ns, pod}] = comp
		namespaces[ns] = true
	}
	if len(namespaces) == 0 {
		return []reconcile.TrafficEdge{}, nil
	}
	nsList := make([]string, 0, len(namespaces))
	for ns := range namespaces {
		// A DNS label's dots are the only regex metacharacter it can hold.
		nsList = append(nsList, strings.ReplaceAll(ns, ".", `\\.`))
	}
	sort.Strings(nsList)
	rates, err := p.prometheus.vector(ctx, fmt.Sprintf(trafficRatesQuery, secs, strings.Join(nsList, "|")), to)
	if err != nil {
		return nil, err
	}
	type key struct{ from, to string }
	edges := map[key]*reconcile.TrafficEdge{}
	for _, s := range rates {
		if math.IsNaN(s.value) || math.IsInf(s.value, 0) || s.value < 0 {
			continue
		}
		src, ok1 := owner[[2]string{s.labels["source_namespace"], s.labels["source_pod"]}]
		dst, ok2 := owner[[2]string{s.labels["destination_namespace"], s.labels["destination_pod"]}]
		if !ok1 || !ok2 {
			// An end outside the targets: not returned (the caller can't
			// be shown it, or it isn't a Component).
			continue
		}
		e := edges[key{src, dst}]
		if e == nil {
			e = &reconcile.TrafficEdge{From: src, To: dst}
			edges[key{src, dst}] = e
		}
		e.Rate += s.value
		if st := s.labels["status"]; len(st) == 3 && st[0] == '5' {
			e.ErrorRate += s.value
		}
	}
	out := make([]reconcile.TrafficEdge, 0, len(edges))
	for _, e := range edges {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out, nil
}
