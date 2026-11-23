package api

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/serendip-infra/serendip-infra/core/audit"
	"github.com/serendip-infra/serendip-infra/core/policy"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store"
)

// Networking reads (specs/025-networking plan, "API, CLI, MCP"): the
// network layer's facts, a Deployment's connection records and an
// Application's traffic. Reads only, so nothing is stored or audited.

// NetworkFactsFunc is the networking capability's cached facts, and
// whether they've been read yet. cmd/serendipd reads them on an interval;
// the API never asks the network layer per request. The slices it returns
// are the caller's (copies), never the cache's own.
type NetworkFactsFunc func() (reconcile.NetworkFacts, bool)

// WithNetworking serves GET /v1/networking from facts, and lets flows and
// traffic say plainly when the network layer doesn't provide them (spec
// criterion 2). Without it those routes answer 503.
func WithNetworking(facts NetworkFactsFunc) ServerOption {
	return func(o *serverOptions) { o.networkFacts = facts }
}

// Caps on networking reads.
const (
	// defaultFlowLimit is a page's records when ?limit is left out.
	defaultFlowLimit = 100
	// minTrafficRange is the shortest traffic window: rates over less
	// than a scrape or two say nothing.
	minTrafficRange = time.Minute
)

// NetworkingHandler serves GET /v1/networking.
type NetworkingHandler struct {
	facts NetworkFactsFunc
}

func (h *NetworkingHandler) Register(mux Mux) {
	mux.HandleFunc("GET /v1/networking", h.Get)
}

// Get answers the network layer's facts: its name and version, whether
// network policies are enforced, what can be read, the pod ranges, what
// isn't available and why, and when they were read.
func (h *NetworkingHandler) Get(w http.ResponseWriter, r *http.Request) {
	reqID := audit.RequestIDFrom(r.Context())
	if h.facts == nil {
		WriteProblem(w, http.StatusServiceUnavailable, "service unavailable", "the cluster's network layer isn't read by this server", reqID)
		return
	}
	f, ok := h.facts()
	if !ok {
		w.Header().Set("Retry-After", "10")
		WriteProblem(w, http.StatusServiceUnavailable, "service unavailable", "the cluster's network layer hasn't been read yet; retry shortly", reqID)
		return
	}
	f.PodRanges = nonNil(f.PodRanges)
	f.Unavailable = nonNil(f.Unavailable)
	writeJSON(w, http.StatusOK, f)
}

// networkReason is the reason in f.Unavailable starting with prefix ("flows: "),
// without it, or dflt.
func networkReason(f reconcile.NetworkFacts, prefix, dflt string) string {
	for _, u := range f.Unavailable {
		if rest, ok := strings.CutPrefix(u, prefix); ok && rest != "" {
			return rest
		}
	}
	return dflt
}

// networkGate says whether the network layer provides what (flows or
// request metrics, picked by has), answering itself when it doesn't: 503
// before the facts are read, 409 when it never will (retrying won't help;
// the reason is the facts' own).
func (h *ObservabilityHandler) networkGate(w http.ResponseWriter, r *http.Request, what, prefix, dflt string, has func(reconcile.NetworkFacts) bool) bool {
	reqID := audit.RequestIDFrom(r.Context())
	if h.facts == nil {
		WriteProblem(w, http.StatusServiceUnavailable, "service unavailable", what+" aren't served by this server: it doesn't read the cluster's network layer", reqID)
		return false
	}
	f, ok := h.facts()
	if !ok {
		w.Header().Set("Retry-After", "10")
		WriteProblem(w, http.StatusServiceUnavailable, "service unavailable", "the cluster's network layer hasn't been read yet; retry shortly", reqID)
		return false
	}
	if !has(f) {
		WriteProblem(w, http.StatusConflict, "not available", what+" aren't available: "+networkReason(f, prefix, dflt), reqID)
		return false
	}
	return true
}

const (
	reasonNoFlows    = "the cluster's network layer doesn't record connections"
	reasonNoRequests = "the cluster's network layer doesn't provide request metrics"
)

var (
	observeRead = policy.Action{Name: policy.ActionObservabilityRead}
)

// envTargets are the Deployments in env the caller may act on, as
// targets, sorted by name then ID, leaving out skip and any whose
// Component is gone. keep (when set) filters by Component first. At most
// limit are returned; more says some were left out for the cap. With
// count false it stops at the cap, and omitted and more are partial.
func (h *ObservabilityHandler) envTargets(ctx context.Context, env resource.Environment, act policy.Action, skip string, keep func(resource.Component) bool, limit int, count bool) (targets []reconcile.LogTarget, omitted int, more bool, err error) {
	deps, err := h.deployments.List(ctx, store.ListOptions{OwnerID: env.Metadata.ID})
	if err != nil {
		return nil, 0, false, err
	}
	slices.SortFunc(deps, func(a, b resource.Deployment) int {
		return cmp.Or(strings.Compare(a.Metadata.Name, b.Metadata.Name), strings.Compare(a.Metadata.ID, b.Metadata.ID))
	})
	for _, d := range deps {
		if !count && len(targets) == limit {
			return targets, omitted, true, nil
		}
		if d.Metadata.ID == skip {
			continue
		}
		comp, err := h.comps.Get(ctx, d.Spec.Component)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, 0, false, err
		}
		if keep != nil && !keep(comp) {
			continue
		}
		ok, err := allowedAt(ctx, act, resource.Scope{Kind: resource.KindDeployment, ID: d.Metadata.ID})
		if err != nil {
			return nil, 0, false, err
		}
		if !ok {
			omitted++
			continue
		}
		if len(targets) == limit {
			more = true
			continue
		}
		targets = append(targets, reconcile.LogTarget{Deployment: d, Component: comp, Environment: env})
	}
	return targets, omitted, more, nil
}

// flowQuery reads ?from, ?to, ?verdict, ?limit and ?before.
func (h *ObservabilityHandler) flowQuery(r *http.Request) (reconcile.FlowQuery, error) {
	q := r.URL.Query()
	from, to, err := h.timeRange(r, 0)
	if err != nil {
		return reconcile.FlowQuery{}, err
	}
	fq := reconcile.FlowQuery{From: from, To: to, Limit: defaultFlowLimit, Before: q.Get("before")}
	switch q.Get("verdict") {
	case "", "all":
	case reconcile.FlowDropped:
		fq.DroppedOnly = true
	default:
		return fq, errors.New("verdict: all or dropped")
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > reconcile.MaxFlowQueryLimit {
			return fq, errors.New("limit: a whole number from 1 to " + strconv.Itoa(reconcile.MaxFlowQueryLimit))
		}
		fq.Limit = n
	}
	if _, err := reconcile.DecodeLogCursor(fq.Before); err != nil {
		return fq, errors.New("before: not a cursor from a search's next; start again without it")
	}
	return fq, nil
}

// flowsAnswer is a page of connection records, with the window read.
type flowsAnswer struct {
	reconcile.FlowPage
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// Flows answers one page of a Deployment's connection records, newest
// first: those with an end at it, dropped ones with the reason. The other
// end is named only when the caller may read it too (another Deployment
// in the Environment they hold observability read on, or a platform
// service); otherwise it's outside (spec criterion 6). Paged as stored
// logs are.
func (h *ObservabilityHandler) Flows(w http.ResponseWriter, r *http.Request) {
	q, err := h.flowQuery(r)
	if err != nil {
		WriteProblem(w, http.StatusBadRequest, "validation failed", err.Error(), audit.RequestIDFrom(r.Context()))
		return
	}
	if err := h.unavailable(); err != nil {
		h.fail(w, r, err)
		return
	}
	if !h.networkGate(w, r, "connection records", "flows: ", reasonNoFlows, func(f reconcile.NetworkFacts) bool { return f.Flows }) {
		return
	}
	// serendipd's handle always has the method and answers ErrNotCollected
	// for a package without it; another wiring may not.
	fr, ok := h.provider.(reconcile.FlowReader)
	if !ok {
		WriteProblem(w, http.StatusConflict, "not available", "connection records aren't available: the observability package doesn't keep them", audit.RequestIDFrom(r.Context()))
		return
	}
	d, err := h.deployments.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	t, err := h.target(r.Context(), d)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// Peers name the other end; they're never searched. Past the cap an
	// end is shown as outside, never named by mistake.
	q.Peers, _, _, err = h.envTargets(r.Context(), t.Environment, observeRead, d.Metadata.ID, nil, reconcile.MaxLogTargets, false)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ctx, done, ok := h.begin(w, r)
	if !ok {
		return
	}
	defer done()
	page, err := fr.Flows(ctx, []reconcile.LogTarget{t}, q)
	if err != nil {
		h.readFailed(w, r, ctx, err)
		return
	}
	// Whatever the provider named, only the target and its peers stay
	// named (defence in depth: plan decision 9's scoping is the API's).
	known := map[[2]string]bool{{t.Component.Metadata.ID, t.Environment.Metadata.ID}: true}
	for _, p := range q.Peers {
		known[[2]string{p.Component.Metadata.ID, p.Environment.Metadata.ID}] = true
	}
	page.Flows = nonNil(page.Flows)
	for i := range page.Flows {
		f := &page.Flows[i]
		f.Source, f.Destination = redactEnd(f.Source, known), redactEnd(f.Destination, known)
	}
	writeJSON(w, http.StatusOK, flowsAnswer{FlowPage: page, From: q.From, To: q.To})
}

// redactEnd keeps e's names only for a Component in known.
func redactEnd(e reconcile.FlowEnd, known map[[2]string]bool) reconcile.FlowEnd {
	if e.Component == "" && e.Environment == "" {
		return e
	}
	if known[[2]string{e.Component, e.Environment}] {
		return reconcile.FlowEnd{Component: e.Component, Environment: e.Environment}
	}
	return reconcile.FlowEnd{Outside: true}
}

// trafficAnswer is an Application's traffic in one Environment. Omitted
// counts its Deployments the caller may not read, without naming them;
// no edge touches one.
type trafficAnswer struct {
	Edges   []reconcile.TrafficEdge `json:"edges"`
	From    time.Time               `json:"from"`
	To      time.Time               `json:"to"`
	Omitted int                     `json:"omitted"`
}

// Traffic answers the HTTP traffic between an Application's Components in
// ?environment over ?from to ?to (the last hour by default, at least a
// minute): per edge, requests and failed requests per second. Only edges
// whose two ends the caller may read are shown (spec criterion 5).
func (h *ObservabilityHandler) Traffic(w http.ResponseWriter, r *http.Request) {
	reqID := audit.RequestIDFrom(r.Context())
	from, to, err := h.timeRange(r, minTrafficRange)
	if err != nil {
		WriteProblem(w, http.StatusBadRequest, "validation failed", err.Error(), reqID)
		return
	}
	envID := r.URL.Query().Get("environment")
	if envID == "" {
		WriteProblem(w, http.StatusBadRequest, "validation failed", "environment: the ID of the Environment to read is required", reqID)
		return
	}
	if err := h.unavailable(); err != nil {
		h.fail(w, r, err)
		return
	}
	if !h.networkGate(w, r, "request metrics", "requests: ", reasonNoRequests, func(f reconcile.NetworkFacts) bool { return f.RequestMetrics }) {
		return
	}
	// As in Flows: for a wiring without the method.
	tr, ok := h.provider.(reconcile.TrafficReader)
	if !ok {
		WriteProblem(w, http.StatusConflict, "not available", "request metrics aren't available: the observability package doesn't keep them", reqID)
		return
	}
	app, err := h.apps.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	env, ok := h.readableEnvironment(w, r, envID)
	if !ok {
		return
	}
	ofApp := func(c resource.Component) bool { return owner(c.Metadata, resource.KindApplication) == app.Metadata.ID }
	targets, omitted, more, err := h.envTargets(r.Context(), env, observeRead, "", ofApp, reconcile.MaxLogTargets, true)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if more {
		WriteProblem(w, http.StatusUnprocessableEntity, "too many components",
			"the Application has more than "+strconv.Itoa(reconcile.MaxLogTargets)+" Deployments here, more than one traffic read covers", reqID)
		return
	}
	if len(targets) == 0 && omitted > 0 {
		WriteProblem(w, http.StatusForbidden, "forbidden", "missing "+policy.ActionObservabilityRead+" on every Deployment of Application/"+app.Metadata.ID+" in Environment/"+envID, reqID)
		return
	}
	out := trafficAnswer{Edges: []reconcile.TrafficEdge{}, From: from, To: to, Omitted: omitted}
	if len(targets) < 2 {
		// No edge has two readable ends.
		writeJSON(w, http.StatusOK, out)
		return
	}
	ctx, done, ok := h.begin(w, r)
	if !ok {
		return
	}
	defer done()
	edges, err := tr.Traffic(ctx, targets, from, to)
	if err != nil {
		h.readFailed(w, r, ctx, err)
		return
	}
	readable := map[string]bool{}
	for _, t := range targets {
		readable[t.Component.Metadata.ID] = true
	}
	for _, e := range edges {
		if readable[e.From] && readable[e.To] && finiteNumber(e.Rate) && finiteNumber(e.ErrorRate) {
			out.Edges = append(out.Edges, e)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
