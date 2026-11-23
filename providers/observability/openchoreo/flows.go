package openchoreo

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// Connection records (specs/025-networking plan decision 9), read from
// the serendip_flows stream Fluent Bit fills (fluentbit.go), with SQL on
// OpenObserve's search API over the port-forward usage.go uses, with the
// admin credentials. A target is named by its Deployment's namespace and
// name, which Hubble records as the workload: they're read from the live
// Deployment by the Component's and Environment's UIDs, never taken from
// the caller, and checked as DNS names before they're put in SQL.

var _ reconcile.FlowReader = (*Provider)(nil)

const (
	defaultFlowLimit = 100
	// maxFlowAnswer is the most records asked of one search.
	maxFlowAnswer = 1000
	// endpointIdentityMin and localIdentityMin bound the network layer's
	// cluster endpoint identities: below are reserved ones (host, world,
	// ...), from the second local CIDR ones.
	endpointIdentityMin = 256
	localIdentityMin    = 1 << 24
	// Reserved identities that are outside the cluster: world, and its
	// IPv4 and IPv6 halves.
	identityWorld, identityWorldV4, identityWorldV6 = 2, 9, 10
	identityHost, identityRemoteNode, identityAPI   = 1, 6, 7
)

// platformNamespaces and platformPrefixes are the namespaces whose
// workloads a flow names to anyone reading one of its Components: the
// platform's own services (the planes, the gateway, the cluster's
// system). Any other in-cluster end that isn't a target is Outside,
// whatever it's called: an allow-list, so a Component, a build pod or a
// bare pod elsewhere is never named by mistake (spec criterion 6).
var (
	platformNamespaces = map[string]bool{"kube-system": true, "kgateway-system": true, "cert-manager": true}
	platformPrefixes   = []string{"openchoreo-", "serendip-"}
)

func platformNamespace(ns string) bool {
	if platformNamespaces[ns] {
		return true
	}
	for _, p := range platformPrefixes {
		if strings.HasPrefix(ns, p) && ns != buildsNamespace && ns != "workflows-"+buildsNamespace {
			return true
		}
	}
	return false
}

// flowLookback is how far back a query with no start reads: the stream's
// retention (the logs', values.go).
const flowLookback = logRetentionDays * 24 * time.Hour

// dropReasonRE is what a drop reason may be to be passed on.
var dropReasonRE = regexp.MustCompile(`^[A-Z0-9_]{1,64}$`)

// flowRecord is one stored record, as the Lua filter flattened it.
type flowRecord struct {
	Timestamp    int64   `json:"_timestamp"`
	Verdict      string  `json:"verdict"`
	DropReason   string  `json:"drop_reason"`
	Direction    string  `json:"direction"`
	SrcNamespace string  `json:"src_namespace"`
	SrcWorkload  string  `json:"src_workload"`
	SrcIdentity  float64 `json:"src_identity"`
	DstNamespace string  `json:"dst_namespace"`
	DstWorkload  string  `json:"dst_workload"`
	DstIdentity  float64 `json:"dst_identity"`
	Protocol     string  `json:"protocol"`
	Port         float64 `json:"port"`
	Count        float64 `json:"count"`
}

func (r flowRecord) time() time.Time { return time.UnixMicro(r.Timestamp).UTC() }

// workloadRef is a Deployment.
type workloadRef struct{ namespace, name string }

type workloadEntry struct {
	refs []workloadRef
	at   time.Time
}

// flowStore searches the stream and resolves targets' Deployments.
type flowStore struct {
	client *kubernetes.Client
	// http, when set, replaces the port-forward (tests).
	http *http.Client

	mu        sync.Mutex
	workloads map[componentScope]workloadEntry
}

func newFlowStore(client *kubernetes.Client) *flowStore {
	return &flowStore{client: client, workloads: map[componentScope]workloadEntry{}}
}

// deployments are the Deployments carrying s's Component and Environment
// UIDs, cached for scopeTTL. None is errNoScope.
func (f *flowStore) deployments(ctx context.Context, s componentScope, now time.Time) ([]workloadRef, error) {
	f.mu.Lock()
	e, ok := f.workloads[s]
	f.mu.Unlock()
	if ok && now.Sub(e.at) < scopeTTL {
		if len(e.refs) == 0 {
			return nil, errNoScope
		}
		return e.refs, nil
	}
	if err := checkUIDs(s); err != nil {
		return nil, err
	}
	sel := "openchoreo.dev/component-uid=" + s.ComponentUID + ",openchoreo.dev/environment-uid=" + s.EnvironmentUID +
		",openchoreo.dev/project-uid=" + s.ProjectUID
	items, err := f.client.List(ctx, "/apis/apps/v1/deployments?labelSelector="+url.QueryEscape(sel))
	if err != nil {
		return nil, fmt.Errorf("list the Component's Deployments: %w", err)
	}
	var refs []workloadRef
	for _, d := range items {
		ns, name := d.Metadata.Namespace, d.Metadata.Name
		if dnsLabelRE.MatchString(ns) && dnsLabelRE.MatchString(name) {
			refs = append(refs, workloadRef{ns, name})
		}
	}
	f.mu.Lock()
	for k, e := range f.workloads {
		// Pruned on write, so Components that come and go don't grow it.
		if now.Sub(e.at) >= scopeTTL {
			delete(f.workloads, k)
		}
	}
	f.workloads[s] = workloadEntry{refs: refs, at: now}
	f.mu.Unlock()
	if len(refs) == 0 {
		return nil, errNoScope
	}
	return refs, nil
}

// search runs sql over [from, to] for up to size records.
func (f *flowStore) search(ctx context.Context, sql string, from, to time.Time, size int) ([]flowRecord, error) {
	var hits []flowRecord
	err := searchStore(ctx, f.client, f.http, "logs", "connection records", sql, from, to, size, &hits)
	if errors.Is(err, errNoField) {
		return nil, errors.New("the connection records' store doesn't have the fields Serendip writes; is the log collector's configuration Serendip's?")
	}
	return hits, err
}

// target is a Deployment's Serendip names.
type flowTarget struct{ component, environment string }

// flowOrder is the records' order: newest first, then by every field a
// record is told apart by, missing ones first. Records written in the
// same microsecond are then in one order every time, which the cursor's
// count of records seen at its time relies on; recordLess is the same in
// Go.
const flowOrder = ` ORDER BY _timestamp DESC, src_namespace ASC NULLS FIRST, src_workload ASC NULLS FIRST,` +
	` dst_namespace ASC NULLS FIRST, dst_workload ASC NULLS FIRST, direction ASC NULLS FIRST,` +
	` verdict ASC NULLS FIRST, protocol ASC NULLS FIRST, port ASC NULLS FIRST`

func recordLess(a, b flowRecord) bool {
	return cmp.Or(
		cmp.Compare(b.Timestamp, a.Timestamp),
		cmp.Compare(a.SrcNamespace, b.SrcNamespace),
		cmp.Compare(a.SrcWorkload, b.SrcWorkload),
		cmp.Compare(a.DstNamespace, b.DstNamespace),
		cmp.Compare(a.DstWorkload, b.DstWorkload),
		cmp.Compare(a.Direction, b.Direction),
		cmp.Compare(a.Verdict, b.Verdict),
		cmp.Compare(a.Protocol, b.Protocol),
		cmp.Compare(a.Port, b.Port),
	) < 0
}

// flowSQL selects the records with an end among refs in [from, upper],
// in flowOrder. The bounds are in the SQL too, so a record past them
// can't take a place in the answer. Every name has been checked against
// dnsLabelRE, so none holds a quote. It selects every field: naming one
// the stream has never had (drop_reason, before the first drop) is an
// error.
func flowSQL(refs []workloadRef, droppedOnly bool, from, upper time.Time) string {
	var ends []string
	for _, r := range refs {
		ends = append(ends,
			fmt.Sprintf("(src_namespace = '%s' AND src_workload = '%s')", r.namespace, r.name),
			fmt.Sprintf("(dst_namespace = '%s' AND dst_workload = '%s')", r.namespace, r.name))
	}
	sql := fmt.Sprintf(`SELECT * FROM "%s" WHERE _timestamp >= %d AND _timestamp <= %d AND (%s)`,
		flowStream, from.UnixMicro(), upper.UnixMicro(), strings.Join(ends, " OR "))
	if droppedOnly {
		sql += ` AND verdict = 'DROPPED'`
	}
	return sql + flowOrder
}

// flowRefs adds targets' workloads to names, returning each workload once
// in the order first seen. A target whose platform objects are gone is
// left out.
func (p *Provider) flowRefs(ctx context.Context, targets []reconcile.LogTarget, names map[workloadRef]flowTarget, now time.Time) ([]workloadRef, error) {
	var refs []workloadRef
	for _, t := range targets {
		s, err := p.scopes.component(ctx, t)
		if errors.Is(err, errNoScope) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("openchoreo observability: %w", err)
		}
		deps, err := p.flows.deployments(ctx, s, now)
		if errors.Is(err, errNoScope) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("openchoreo observability: %w", err)
		}
		for _, d := range deps {
			if _, dup := names[d]; !dup {
				refs = append(refs, d)
			}
			names[d] = flowTarget{component: t.Deployment.Spec.Component, environment: t.Environment.Metadata.ID}
		}
	}
	return refs, nil
}

// Flows reads the connection records with an end among targets, newest
// first. The other end is named only if it's a target too, one of
// q.Peers, or a platform service (platformNamespace); any other in-cluster end is marked
// Outside, and an address outside the cluster External. A page with no
// records doesn't say whether records are collected: the caller checks
// the networking capability's facts for that.
func (p *Provider) Flows(ctx context.Context, targets []reconcile.LogTarget, q reconcile.FlowQuery) (reconcile.FlowPage, error) {
	if len(targets) > reconcile.MaxLogTargets || len(q.Peers) > reconcile.MaxLogTargets {
		return reconcile.FlowPage{}, ErrTooManyTargets
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultFlowLimit
	}
	limit = min(limit, reconcile.MaxFlowQueryLimit)
	cur, err := reconcile.DecodeLogCursor(q.Before)
	if err != nil {
		return reconcile.FlowPage{}, err
	}
	cur.Time = cur.Time.Truncate(time.Microsecond)
	now := p.now()
	to := q.To
	if to.IsZero() || to.After(now) {
		to = now
	}
	to = to.UTC().Truncate(time.Microsecond)
	from := q.From.UTC().Truncate(time.Microsecond)
	if from.Before(to.Add(-flowLookback)) {
		from = to.Add(-flowLookback)
	}
	if !cur.Time.IsZero() && cur.Time.After(to) {
		cur = reconcile.LogCursor{}
	}
	page := reconcile.FlowPage{Flows: []reconcile.Flow{}}

	names := map[workloadRef]flowTarget{}
	refs, err := p.flowRefs(ctx, targets, names, now)
	if err != nil {
		return reconcile.FlowPage{}, err
	}
	// Peers are named, never searched: their refs are dropped. A target's
	// own name wins over a peer's for a shared workload.
	peerNames := map[workloadRef]flowTarget{}
	if _, err := p.flowRefs(ctx, q.Peers, peerNames, now); err != nil {
		return reconcile.FlowPage{}, err
	}
	for d, t := range peerNames {
		if _, own := names[d]; !own {
			names[d] = t
		}
	}
	upper := to
	if !cur.Time.IsZero() {
		upper = cur.Time
	}
	if len(refs) == 0 || upper.Before(from) {
		return page, nil
	}
	ask := limit + cur.Seen + 1
	if ask > maxFlowAnswer {
		// More records at the cursor's time than one answer holds: the
		// rest of that microsecond is stepped over, and the page says so.
		cur = reconcile.LogCursor{Time: upper.Add(-time.Microsecond)}
		upper, ask = cur.Time, limit+1
		page.Truncated = true
		if upper.Before(from) {
			return page, nil
		}
	}
	// The end is asked a microsecond late (whether it's inclusive isn't
	// known); newer records are dropped below.
	recs, err := p.flows.search(ctx, flowSQL(refs, q.DroppedOnly, from, upper), from, upper.Add(time.Microsecond), ask)
	if errors.Is(err, errNoStream) {
		return page, nil
	}
	if err != nil {
		return reconcile.FlowPage{}, fmt.Errorf("openchoreo observability: %w", err)
	}
	var kept []flowRecord
	for _, r := range recs {
		if t := r.time(); !t.After(upper) && !t.Before(from) {
			kept = append(kept, r)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return recordLess(kept[i], kept[j]) })
	skip := 0
	for skip < len(kept) && skip < cur.Seen && kept[skip].time().Equal(cur.Time) {
		skip++
	}
	kept = kept[skip:]
	if len(kept) > limit {
		kept = kept[:limit]
		page.Next = flowCursorAfter(cur, kept).Encode()
	}
	for _, r := range dedupe(kept) {
		page.Flows = append(page.Flows, toFlow(r, names))
	}
	return page, nil
}

// flowCursorAfter is the cursor following taken, which continues cur.
func flowCursorAfter(cur reconcile.LogCursor, taken []flowRecord) reconcile.LogCursor {
	last := taken[len(taken)-1].time()
	seen := 0
	if last.Equal(cur.Time) {
		seen = cur.Seen
	}
	for _, r := range taken {
		if r.time().Equal(last) {
			seen++
		}
	}
	return reconcile.LogCursor{Time: last, Seen: seen}
}

// dedupe drops the source's record of a connection the destination also
// recorded: both ends of a connection between cluster endpoints write one
// (EGRESS, then INGRESS), in the same aggregation minute. The
// destination's is kept: it has the final verdict. Each destination
// record stands for one source record, the nearest in time, so a second
// source record (another minute's) isn't lost. A connection through the
// L7 proxy has only the source's (seen live on t025, T006). Records are
// compared within one page only, so a pair split across two pages shows
// twice (RP-084).
func dedupe(recs []flowRecord) []flowRecord {
	type key struct {
		srcNS, srcW, dstNS, dstW, proto string
		port                            float64
	}
	k := func(r flowRecord) key {
		return key{r.SrcNamespace, r.SrcWorkload, r.DstNamespace, r.DstWorkload, r.Protocol, r.Port}
	}
	ingress := map[key][]int64{}
	for _, r := range recs {
		if r.Direction == "INGRESS" && r.Verdict == "FORWARDED" {
			ingress[k(r)] = append(ingress[k(r)], r.Timestamp)
		}
	}
	const minute = int64(time.Minute / time.Microsecond)
	out := recs[:0:0]
	for _, r := range recs {
		if r.Direction == "EGRESS" && r.Verdict == "FORWARDED" && r.SrcWorkload != "" && r.DstWorkload != "" {
			ts := ingress[k(r)]
			best := -1
			for j, t := range ts {
				d := t - r.Timestamp
				if d > -minute && d < minute && (best < 0 || abs(d) < abs(ts[best]-r.Timestamp)) {
					best = j
				}
			}
			if best >= 0 {
				// Paired: each destination record is used once.
				ingress[k(r)] = append(ts[:best:best], ts[best+1:]...)
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

func abs(d int64) int64 {
	if d < 0 {
		return -d
	}
	return d
}

// toFlow converts r, naming its ends.
func toFlow(r flowRecord, names map[workloadRef]flowTarget) reconcile.Flow {
	f := reconcile.Flow{
		Time:        r.time(),
		Verdict:     reconcile.FlowForwarded,
		Source:      flowEnd(r.SrcNamespace, r.SrcWorkload, r.SrcIdentity, names),
		Destination: flowEnd(r.DstNamespace, r.DstWorkload, r.DstIdentity, names),
		Protocol:    r.Protocol,
		Port:        int(r.Port),
		Count:       max(int(r.Count), 1),
	}
	if r.Verdict == "DROPPED" {
		f.Verdict = reconcile.FlowDropped
		f.Reason = dropReason(r.DropReason)
	}
	return f
}

func flowEnd(ns, workload string, identity float64, names map[workloadRef]flowTarget) reconcile.FlowEnd {
	if t, ok := names[workloadRef{ns, workload}]; ok && ns != "" {
		return reconcile.FlowEnd{Component: t.component, Environment: t.environment}
	}
	if ns != "" {
		if platformNamespace(ns) {
			// A platform service, or a bare pod of the platform's
			// (namespace only).
			return reconcile.FlowEnd{Namespace: ns, Workload: workload}
		}
		// Another Component, or anything else the caller wasn't given.
		return reconcile.FlowEnd{Outside: true}
	}
	id := int64(identity)
	switch {
	case id == identityWorld || id == identityWorldV4 || id == identityWorldV6 || id >= localIdentityMin:
		return reconcile.FlowEnd{External: true}
	case id == identityHost || id == identityRemoteNode:
		return reconcile.FlowEnd{Workload: "node"}
	case id == identityAPI:
		return reconcile.FlowEnd{Workload: "kube-apiserver"}
	}
	return reconcile.FlowEnd{Workload: "unknown"}
}

// dropReason words a drop reason in Serendip's terms.
func dropReason(code string) string {
	switch code {
	case "POLICY_DENIED", "POLICY_DENY":
		return "refused by a network policy"
	case "":
		return "dropped by the network layer"
	}
	if !dropReasonRE.MatchString(code) {
		return "dropped by the network layer"
	}
	return "dropped by the network layer: " + strings.ToLower(strings.ReplaceAll(code, "_", " "))
}
