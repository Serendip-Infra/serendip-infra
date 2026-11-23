package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/serendip-infra/serendip-infra/core/audit"
	"github.com/serendip-infra/serendip-infra/core/provider"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/providers/application/openchoreo"
	"github.com/serendip-infra/serendip-infra/providers/networking/cilium"
	"github.com/serendip-infra/serendip-infra/providers/networking/external"
)

// The networking capability's wiring (specs/025-networking plan decisions
// 1, 2, 4, 9 and 15, T008): serendipd detects the cluster's network
// layer, reads its facts on the re-ensure interval and caches them, runs
// the request-visibility pass, and hands the providers that cooperate
// with it their plain values: the flow source to the observability
// package's installer, whether request metrics exist to the observability
// provider, and the pod ranges to the notifier.

const capNetworking = "networking"

// Audit identity of a visibility pass that wrote (T004's contract-guardian
// note): the policies are derived objects Serendip keeps, so the row names
// them under the reconciler's identity.
const (
	visibilityAuditKind = "NetworkVisibility"
	visibilityAuditID   = "request-visibility"
)

// detectedNetwork is the networking provider detection chose. visibility
// and flows are nil when the network layer has neither.
type detectedNetwork struct {
	name       string
	facts      reconcile.NetworkingProvider
	visibility visibilityEnsurer
	flows      flowSourcer
}

type visibilityEnsurer interface {
	EnsureVisibility(ctx context.Context, targets []cilium.Target) (cilium.Report, error)
}

type flowSourcer interface {
	FlowSource(ctx context.Context) (reconcile.FlowSource, error)
}

// httpWorkloadLister is the application provider's list of workloads that
// serve HTTP.
type httpWorkloadLister interface {
	HTTPWorkloads(ctx context.Context) ([]openchoreo.HTTPWorkload, error)
}

// detectNetwork chooses cilium when its agent runs, external otherwise.
func detectNetwork(ctx context.Context, client cilium.Client) (detectedNetwork, error) {
	isCilium, err := cilium.Detect(ctx, client)
	if err != nil {
		return detectedNetwork{}, err
	}
	if isCilium {
		p := cilium.New(client)
		return detectedNetwork{name: "cilium", facts: p, visibility: p, flows: p}, nil
	}
	return detectedNetwork{name: "external", facts: external.New(client)}, nil
}

// How long the observability package's install waits for the first flow
// source before it's retried; a variable so a test can shorten it.
var flowSourceWait = time.Minute

// networkState runs the networking loop and holds what it learned. Safe
// for concurrent use.
type networkState struct {
	registry *provider.Registry
	detect   func(context.Context) (detectedNetwork, error)
	// setFlowSource is the observability installer's; nil without one.
	setFlowSource func(reconcile.FlowSource) error
	// record writes the audit row of a visibility pass that wrote.
	record func(ctx context.Context, kind, id string, after json.RawMessage) error
	every  time.Duration
	retry  backoff

	// workloads is the application provider once it's set up; kick asks
	// for a pass straight away (the platform just came up).
	workloads atomic.Pointer[httpWorkloadLister]
	kick      chan struct{}

	mu        sync.Mutex
	facts     reconcile.NetworkFacts
	read      bool
	flow      reconcile.FlowSource
	flowKnown chan struct{} // closed once flow is known
	flowOnce  sync.Once
	// done is closed when run returns.
	done chan struct{}

	// Only the loop's goroutine touches these: logged is the last message
	// logged per step (logChange), pending the visibility audit rows not
	// written yet, flowFails the flow-source reads failed in a row.
	logged    map[string]string
	pending   []json.RawMessage
	flowFails int
}

// maxFlowFails is how many flow-source reads may fail in a row, before
// any succeeded, until the observability package goes ahead without
// connection records: it mustn't wait for ever on a network layer whose
// configuration can't be read. A later good read adds them (one more
// apply of the log collector).
const maxFlowFails = 2

func (n *networkState) flowFailed() {
	n.flowFails++
	select {
	case <-n.flowKnown:
		return // the last known source stays
	default:
	}
	if n.flowFails >= maxFlowFails {
		log.Printf("serendipd: networking: connection records not collected until the network layer's configuration can be read")
		n.setFlow(reconcile.FlowSource{})
	}
}

func newNetworkState(registry *provider.Registry, detect func(context.Context) (detectedNetwork, error), every time.Duration) *networkState {
	registry.SetReadiness(capNetworking, provider.Readiness{Reason: readinessSettingUp, Message: "the network layer hasn't been read yet"})
	return &networkState{registry: registry, detect: detect, every: every, retry: setupBackoff,
		kick: make(chan struct{}, 1), flowKnown: make(chan struct{}), done: make(chan struct{})}
}

// Facts is the cached facts, and whether they've been read.
func (n *networkState) Facts() (reconcile.NetworkFacts, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	f := n.facts
	f.PodRanges = slices.Clone(f.PodRanges)
	f.Unavailable = slices.Clone(f.Unavailable)
	return f, n.read
}

// RequestMetrics is the observability provider's WithRequestMetrics
// answer: whether the network layer records HTTP requests, and if not why,
// without the "requests: " the facts' reasons start with.
func (n *networkState) RequestMetrics(context.Context) (bool, string) {
	f, ok := n.Facts()
	if !ok {
		return false, "the cluster's network layer hasn't been read yet"
	}
	if f.RequestMetrics {
		return true, ""
	}
	for _, u := range f.Unavailable {
		if why, ok := strings.CutPrefix(u, "requests: "); ok {
			return false, why
		}
	}
	// The facts gave no reason: never refuse with a blank one.
	return false, "the cluster's network layer doesn't record HTTP requests"
}

// podRanges is the cached pod ranges.
func (n *networkState) podRanges() []netip.Prefix {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.facts.PodRanges)
}

// clusterRanges is read with the network layer's pod ranges added (plan
// decision 15, RP-076): with cluster-pool IPAM the nodes' podCIDRs don't
// hold them.
func (n *networkState) clusterRanges(read func(context.Context) ([]netip.Prefix, error)) func(context.Context) ([]netip.Prefix, error) {
	return func(ctx context.Context) ([]netip.Prefix, error) {
		ranges, err := read(ctx)
		return mergeRanges(ranges, n.podRanges()), err
	}
}

// platformReady gives the loop the application provider and asks for a
// pass, so request visibility doesn't wait a whole interval.
func (n *networkState) platformReady(w httpWorkloadLister) {
	n.workloads.Store(&w)
	select {
	case n.kick <- struct{}{}:
	default:
	}
}

// waitFlowSource waits until the flow source is known, for at most
// flowSourceWait. Until then the observability package isn't installed or
// checked: one check without it would roll the log collector twice.
func (n *networkState) waitFlowSource(ctx context.Context) error {
	t := time.NewTimer(flowSourceWait)
	defer t.Stop()
	select {
	case <-n.flowKnown:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return fmt.Errorf("%w: the cluster's network layer hasn't been read yet", reconcile.ErrProviderNotReady)
	}
}

// run detects the network layer, retrying until it can tell, then makes a
// pass every interval, or sooner when kicked, until ctx ends.
func (n *networkState) run(ctx context.Context) {
	defer close(n.done)
	var d detectedNetwork
	delay := n.retry.initial
	for attempt := 1; ; attempt++ {
		var err error
		if d, err = n.detect(ctx); err == nil {
			break
		}
		if ctx.Err() != nil {
			return
		}
		log.Printf("serendipd: networking: can't tell the cluster's network layer yet (attempt %d, retrying in %s): %v", attempt, delay, err)
		n.registry.SetReadiness(capNetworking, provider.Readiness{Reason: readinessSetupFailed, Message: err.Error()})
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, n.retry.max)
	}
	n.registry.Register(capNetworking, d.facts)
	log.Printf("serendipd: kubernetes: networking provider (%s) detected", d.name)
	if d.flows == nil {
		n.setFlow(reconcile.FlowSource{})
	}
	ticker := time.NewTicker(n.every)
	defer ticker.Stop()
	for {
		n.pass(ctx, d)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-n.kick:
		}
	}
}

// Each step of a pass has its own bound, so a slow one doesn't starve
// the others.
const (
	visibilityTimeout = time.Minute
	readTimeout       = 30 * time.Second
)

// pass runs the visibility pass first, so the facts it reads next say
// what the pass left out, then the flow source, then the facts.
func (n *networkState) pass(ctx context.Context, d detectedNetwork) {
	if d.visibility != nil {
		if w := n.workloads.Load(); w != nil {
			vctx, cancel := context.WithTimeout(ctx, visibilityTimeout)
			n.visibilityPass(vctx, d.visibility, *w)
			cancel()
		}
	}
	if d.flows != nil {
		fctx, cancel := context.WithTimeout(ctx, readTimeout)
		src, err := d.flows.FlowSource(fctx)
		cancel()
		switch {
		case ctx.Err() != nil:
			return // shutting down
		case err != nil:
			n.logChange("flows", "serendipd: networking: where the network layer writes connection records: "+err.Error())
			n.flowFailed()
		default:
			n.logChange("flows", "")
			n.flowFails = 0
			n.setFlow(src)
		}
	}
	fctx, cancel := context.WithTimeout(ctx, readTimeout)
	f, err := d.facts.Facts(fctx)
	cancel()
	if ctx.Err() != nil {
		return // shutting down: not facts
	}
	if fctx.Err() != nil && errors.Is(err, fctx.Err()) {
		// Timed out: the facts already cached stay, and readiness says so.
		n.registry.SetReadiness(capNetworking, provider.Readiness{Reason: readinessRepairFailed, Message: "the network layer didn't answer in time: " + err.Error()})
		n.logChange("facts", "serendipd: networking: reading the network layer: "+err.Error())
		return
	}
	n.mu.Lock()
	n.facts, n.read = f, true
	n.mu.Unlock()
	if err != nil {
		n.registry.SetReadiness(capNetworking, provider.Readiness{Reason: readinessRepairFailed, Message: "part of the network layer can't be read: " + err.Error()})
	} else {
		n.registry.SetReadiness(capNetworking, provider.Readiness{Ready: true, Reason: readinessReady})
	}
	n.logChange("facts", describeFacts(d.name, f, err))
}

// logChange logs msg when it differs from the last one logged for step,
// so a lasting failure isn't logged every pass on a small machine. ""
// marks the step healthy again without logging.
func (n *networkState) logChange(step, msg string) {
	if n.logged == nil {
		n.logged = map[string]string{}
	}
	if n.logged[step] == msg {
		return
	}
	n.logged[step] = msg
	if msg != "" {
		log.Print(msg)
	}
}

// setFlow hands src to the observability installer when it changed, and
// marks the flow source known.
func (n *networkState) setFlow(src reconcile.FlowSource) {
	n.mu.Lock()
	same := n.flow == src
	n.flow = src
	n.mu.Unlock()
	select {
	case <-n.flowKnown:
		if same {
			return
		}
	default:
	}
	if n.setFlowSource != nil {
		if err := n.setFlowSource(src); err != nil {
			// The source fails the collector's checks: collect none.
			log.Printf("serendipd: networking: connection records not collected: %v", err)
			if err := n.setFlowSource(reconcile.FlowSource{}); err != nil {
				log.Printf("serendipd: networking: %v", err)
			}
		}
	}
	n.flowOnce.Do(func() { close(n.flowKnown) })
}

// visibilityPass turns on request visibility for every workload that
// serves HTTP, and audits a pass that wrote. An audit row that couldn't be
// written is kept and written at the start of the next pass, so no write
// goes unrecorded while serendipd runs.
func (n *networkState) visibilityPass(ctx context.Context, v visibilityEnsurer, w httpWorkloadLister) {
	n.recordPending(ctx)
	workloads, err := w.HTTPWorkloads(ctx)
	if err != nil {
		if ctx.Err() == nil {
			n.logChange("visibility", "serendipd: networking: request visibility: "+err.Error())
		}
		return
	}
	targets := make([]cilium.Target, len(workloads))
	for i, wl := range workloads {
		targets[i] = cilium.Target{Namespace: wl.Namespace, Policy: wl.Policy, HTTPPorts: wl.Ports}
	}
	rep, err := v.EnsureVisibility(ctx, targets)
	switch {
	case err != nil && ctx.Err() == nil:
		n.logChange("visibility", "serendipd: networking: request visibility: "+err.Error())
	case err == nil:
		n.logChange("visibility", "")
	}
	if len(rep.Applied) == 0 && len(rep.Deleted) == 0 {
		return
	}
	log.Printf("serendipd: networking: request visibility: applied %v, deleted %v", rep.Applied, rep.Deleted)
	after, err := json.Marshal(struct {
		Applied  []string `json:"applied,omitempty"`
		Deleted  []string `json:"deleted,omitempty"`
		Disabled string   `json:"disabled,omitempty"`
	}{rep.Applied, rep.Deleted, rep.Disabled})
	if err != nil {
		log.Printf("serendipd: networking: audit the visibility pass: %v", err)
		return
	}
	n.pending = append(n.pending, after)
	n.recordPending(ctx)
}

// maxPendingAudit bounds the audit rows kept for a store that keeps
// failing; past it the oldest are logged in full and dropped.
const maxPendingAudit = 100

// recordPending writes the visibility passes' audit rows not yet written.
// The writes they record are made, so they're written even after the
// pass's own context ran out.
func (n *networkState) recordPending(ctx context.Context) {
	if n.record == nil {
		n.pending = nil
		return
	}
	for len(n.pending) > 0 {
		// At shutdown (ctx done) each try is short, so stopping isn't held
		// up by a store that hangs.
		wait := 10 * time.Second
		if ctx.Err() != nil {
			wait = 2 * time.Second
		}
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wait)
		actx = audit.WithInterface(audit.WithActor(actx, reconcile.ActorName), reconcile.InterfaceName)
		err := n.record(actx, visibilityAuditKind, visibilityAuditID, n.pending[0])
		cancel()
		if err != nil {
			log.Printf("serendipd: networking: audit the visibility pass (kept, retried next pass): %v", err)
			if over := len(n.pending) - maxPendingAudit; over > 0 {
				for _, row := range n.pending[:over] {
					log.Printf("serendipd: networking: visibility audit row dropped, the store keeps failing: %s", row)
				}
				n.pending = n.pending[over:]
			}
			return
		}
		n.pending = n.pending[1:]
	}
	n.pending = nil
}

// describeFacts is the log line for f: the plugin and what it gives.
func describeFacts(name string, f reconcile.NetworkFacts, err error) string {
	yes := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	plugin := f.Plugin
	if f.Version != "" {
		plugin += " " + f.Version
	}
	msg := fmt.Sprintf("serendipd: networking (%s): %s; network policies %s (%s); connection records %s; request metrics %s",
		name, plugin, f.PolicyEnforcement, f.PolicyReason, yes(f.Flows), yes(f.RequestMetrics))
	if len(f.Unavailable) > 0 {
		msg += "; " + strings.Join(f.Unavailable, "; ")
	}
	if err != nil {
		msg += "; read failed: " + err.Error()
	}
	return msg
}

// traceEndpoint is where workloads send traces, held here because the
// application provider is made only once the platform is set up.
type traceEndpoint struct {
	mu    sync.Mutex
	value string
	p     *openchoreo.Provider
}

func (e *traceEndpoint) set(v string) {
	e.mu.Lock()
	changed := v != e.value
	e.value = v
	if e.p != nil {
		e.p.SetTraceEndpoint(v)
	}
	e.mu.Unlock()
	switch {
	case !changed:
	case v == "":
		log.Print("serendipd: tracing: new releases no longer carry a trace endpoint")
	default:
		log.Printf("serendipd: tracing: new releases send traces to %s", v)
	}
}

// attach gives p the endpoint, before its first pass.
func (e *traceEndpoint) attach(p *openchoreo.Provider) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.p = p
	p.SetTraceEndpoint(e.value)
}
