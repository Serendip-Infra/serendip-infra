package reconcile

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/serendip-infra/serendip-infra/core/audit"
	"github.com/serendip-infra/serendip-infra/core/graph"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store"
)

// The alert evaluator (specs/024-alerts plan decision 2): one loop in
// serendipd, beside the engine, that evaluates every due rule through the
// observability capability, keeps one Alert per fingerprint, and hands
// firings and resolutions to the delivery loop (delivery.go). It's an ops
// loop, not a resource reconcile, as package jobs are.

// Evaluator settings (decisions 2 and 11).
const (
	AlertTick           = 15 * time.Second
	AlertQueryWorkers   = 2
	AlertQueryTimeout   = 20 * time.Second
	AlertReopenWindow   = 5 * time.Minute
	AlertFlushEvery     = time.Minute
	AlertRetention      = 30 * 24 * time.Hour
	AlertRetentionBatch = 500
	// alertStaleEvaluated: a rule whose value hasn't changed still has its
	// lastEvaluated written this often, so the store's copy isn't left far
	// behind while nothing changes.
	alertStaleEvaluated = 10 * time.Minute
	alertRetentionEvery = time.Hour
	// alertRetentionBatches bounds one retention run: 20 × 500 Alerts.
	alertRetentionBatches = 20
	// alertMaxJitter bounds the spread of rules' first evaluations.
	alertMaxJitter = 10 * time.Second
)

// AlertsActor is who the evaluator's and delivery loop's writes are
// recorded as.
const AlertsActor = "system:alerts"

// OwnerReader reads a resource's direct owners (the resource graph).
type OwnerReader interface {
	Owners(ctx context.Context, ref graph.ResourceRef) ([]graph.ResourceRef, error)
}

// AlertStores are the stores the evaluator and delivery loop read.
type AlertStores struct {
	Rules        store.Store[resource.AlertRuleSpec, resource.AlertRuleStatus]
	Alerts       store.Store[resource.AlertSpec, resource.AlertStatus]
	Channels     store.Store[resource.NotificationChannelSpec, resource.NotificationChannelStatus]
	SMTP         store.Store[resource.SMTPServerSpec, resource.SMTPServerStatus]
	Deployments  store.Store[resource.DeploymentSpec, resource.DeploymentStatus]
	Components   store.Store[resource.ComponentSpec, resource.ComponentStatus]
	Environments store.Store[resource.EnvironmentSpec, resource.EnvironmentStatus]
	Applications store.Store[resource.ApplicationSpec, resource.ApplicationStatus]
	Projects     store.Store[resource.ProjectSpec, resource.ProjectStatus]
	Owners       OwnerReader
}

// alertWriter serialises the evaluator's and delivery loop's writes to
// Alerts, so neither loses the other's change: each write re-reads the
// Alert under the lock. Sends happen outside it.
type alertWriter struct {
	mu     sync.Mutex
	alerts store.Store[resource.AlertSpec, resource.AlertStatus]
}

// update applies mutate to the stored Alert and writes it if mutate
// reports a change. A conflict with another writer (the API's
// acknowledge) is retried on a fresh read.
func (w *alertWriter) update(ctx context.Context, id string, mutate func(*resource.Alert) bool) (resource.Alert, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for attempt := 0; ; attempt++ {
		a, err := w.alerts.Get(ctx, id)
		if err != nil {
			return a, err
		}
		if !mutate(&a) {
			return a, nil
		}
		got, err := w.alerts.Update(ctx, a)
		if errors.Is(err, store.ErrConflict) && attempt < 2 {
			continue
		}
		return got, err
	}
}

func (w *alertWriter) create(ctx context.Context, a resource.Alert) (resource.Alert, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.alerts.Create(ctx, a)
}

// openAlert is the evaluator's memory of one fingerprint's latest Alert:
// a firing one, or one resolved within the reopen window.
type openAlert struct {
	id         string
	ruleID     string
	component  string
	state      string
	resolvedAt time.Time
	value      float64
	valueDirty bool
}

// ruleMemory is what the evaluator holds for a rule between writes.
type ruleMemory struct {
	nextDue    time.Time
	value      *float64
	evaluated  time.Time
	valueDirty bool
	flushedAt  time.Time
}

// AlertEvaluator evaluates rules on each tick.
type AlertEvaluator struct {
	s         AlertStores
	writer    *alertWriter
	provider  ObservabilityProvider
	installed func(ctx context.Context) (bool, error)
	purge     func(ctx context.Context, cutoff time.Time, limit int) (int, error)
	deliver   func(alertID string)
	now       func() time.Time

	loaded      bool
	rules       map[string]*ruleMemory
	open        map[string]*openAlert // by fingerprint
	lastFlush   time.Time
	lastPurge   time.Time
	lastTick    atomic.Int64
	removedDone bool
	stats       evalStats
}

// AlertEvaluatorOptions wires an AlertEvaluator.
type AlertEvaluatorOptions struct {
	Stores   AlertStores
	Provider ObservabilityProvider
	// Installed reports whether the observability package is installed
	// (whatever its readiness): when it's removed, open Alerts resolve
	// with packageRemoved.
	Installed func(ctx context.Context) (bool, error)
	// Purge removes resolved Alerts older than the cutoff (retention).
	Purge func(ctx context.Context, cutoff time.Time, limit int) (int, error)
	Now   func() time.Time
}

// NewAlertEvaluator returns the evaluator and its delivery loop, sharing
// one Alert writer.
func NewAlertEvaluator(o AlertEvaluatorOptions, d DeliveryOptions) (*AlertEvaluator, *DeliveryLoop) {
	if o.Now == nil {
		// UTC, as every other stored time is (spec 024 T010).
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	w := &alertWriter{alerts: o.Stores.Alerts}
	loop := newDeliveryLoop(o.Stores, w, d, o.Now)
	e := &AlertEvaluator{
		s: o.Stores, writer: w, provider: o.Provider, installed: o.Installed, purge: o.Purge,
		deliver: loop.Enqueue, now: o.Now,
		rules: map[string]*ruleMemory{}, open: map[string]*openAlert{},
	}
	return e, loop
}

// LastTick is when the evaluator last completed a tick (the heartbeat's
// health check).
func (e *AlertEvaluator) LastTick() time.Time {
	n := e.lastTick.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// Run ticks until ctx ends.
func (e *AlertEvaluator) Run(ctx context.Context) {
	ctx = audit.WithInterface(audit.WithActor(ctx, AlertsActor), InterfaceName)
	t := time.NewTicker(AlertTick)
	defer t.Stop()
	for {
		if err := e.Tick(ctx); err != nil && ctx.Err() == nil {
			log.Printf("reconcile: alerts: tick: %v", err)
		}
		select {
		case <-ctx.Done():
			// Held values aren't lost on a clean stop.
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			e.flush(fctx, e.now())
			cancel()
			return
		case <-t.C:
		}
	}
}

// evalJob is one query: one rule for one target.
type evalJob struct {
	rule   resource.AlertRule
	target LogTarget
	fp     string
	comp   string
	value  ConditionValue
	err    error
	took   time.Duration
}

// Tick evaluates the due rules once.
func (e *AlertEvaluator) Tick(ctx context.Context) error {
	began := time.Now()
	now := e.now()
	if !e.loaded {
		if err := e.load(ctx, now); err != nil {
			return err
		}
		e.loaded = true
	}
	rules, err := e.s.Rules.List(ctx, store.ListOptions{})
	if err != nil {
		return err
	}
	e.forgetDeleted(rules)

	installed, err := e.installed(ctx)
	if err != nil {
		return err
	}
	if !installed {
		e.packageRemoved(ctx, rules, now)
		e.finishTick(ctx, now)
		return nil
	}
	e.removedDone = false

	var jobs []*evalJob
	var late time.Duration
	for _, r := range rules {
		mem := e.memory(r, now)
		if !r.Spec.IsEnabled() {
			// Due as soon as it's enabled again: the time it spent
			// disabled isn't lateness.
			mem.nextDue = now
			e.disabled(ctx, r, now)
			continue
		}
		if now.Before(mem.nextDue) {
			continue
		}
		late = max(late, now.Sub(mem.nextDue))
		// The next run counts from the due time, not from this tick, so
		// a rule picked up a tick late still runs every Interval (spec
		// 024 T011 measured 66 s for 1 minute when it counted from now:
		// a tick a few ms early waited one more AlertTick). A rule more
		// than an Interval behind starts over from now.
		mem.nextDue = mem.nextDue.Add(r.Spec.Interval())
		if !mem.nextDue.After(now) {
			mem.nextDue = now.Add(r.Spec.Interval())
		}
		targets, reason, msg, err := e.targets(ctx, r)
		if err != nil {
			log.Printf("reconcile: alerts: rule %s: targets: %v", r.Metadata.ID, err)
			continue
		}
		if reason != "" {
			e.setRule(ctx, r, true, "", resource.ConditionFalse, reason, msg, now)
			e.resolveRule(ctx, r, nil, resource.ResolvedComponentGone, now)
			continue
		}
		for _, t := range targets {
			jobs = append(jobs, &evalJob{rule: r, target: t, comp: t.Component.Metadata.ID,
				fp: resource.AlertFingerprint(r.Metadata.ID, t.Component.Metadata.ID, t.Environment.Metadata.ID)})
		}
		// An Application's Component that no longer runs here: its Alert
		// resolves.
		keep := map[string]bool{}
		for _, t := range targets {
			keep[t.Component.Metadata.ID] = true
		}
		e.resolveRule(ctx, r, keep, resource.ResolvedComponentGone, now)
	}
	e.evaluate(ctx, jobs)
	// The queries can take a while: transitions are timed when the
	// answers are in.
	now = e.now()
	e.apply(ctx, jobs, now)
	if len(jobs) > 0 {
		e.stats.add(jobs, time.Since(began), late)
	}
	e.finishTick(ctx, now)
	return nil
}

// finishTick flushes held values once a minute and runs retention hourly.
func (e *AlertEvaluator) finishTick(ctx context.Context, now time.Time) {
	if now.Sub(e.lastFlush) >= AlertFlushEvery {
		e.flush(ctx, now)
		e.lastFlush = now
	}
	if e.purge != nil && now.Sub(e.lastPurge) >= alertRetentionEvery {
		// Batches until caught up (at most alertRetentionBatches a run),
		// so a backlog can't outgrow the hourly pass.
		total, failed := 0, false
		for range alertRetentionBatches {
			n, err := e.purge(ctx, now.Add(-AlertRetention), AlertRetentionBatch)
			if err != nil {
				log.Printf("reconcile: alerts: retention: %v", err)
				failed = true
				break
			}
			total += n
			if n < AlertRetentionBatch {
				break
			}
		}
		if !failed {
			e.lastPurge = now
		}
		if total > 0 {
			log.Printf("reconcile: alerts: retention removed %d resolved Alerts", total)
		}
	}
	// A resolved Alert past the reopen window can't reopen: forget it.
	for fp, o := range e.open {
		if o.state != resource.AlertFiring && now.Sub(o.resolvedAt) > AlertReopenWindow {
			delete(e.open, fp)
		}
	}
	e.stats.maybeLog(now)
	e.lastTick.Store(now.UnixNano())
}

// load reads the open Alerts after a restart: evaluation resumes on them,
// and their pending deliveries are the delivery loop's.
func (e *AlertEvaluator) load(ctx context.Context, now time.Time) error {
	all, err := e.s.Alerts.List(ctx, store.ListOptions{})
	if err != nil {
		return err
	}
	for _, a := range all {
		e.remember(a, now)
	}
	return nil
}

// remember keeps a in memory if it's firing, or resolved recently enough
// to reopen.
func (e *AlertEvaluator) remember(a resource.Alert, now time.Time) {
	fp := a.Spec.Fingerprint
	var resolvedAt time.Time
	if a.Status.ResolvedAt != nil {
		resolvedAt = *a.Status.ResolvedAt
	}
	if a.Status.State != resource.AlertFiring && now.Sub(resolvedAt) > AlertReopenWindow {
		return
	}
	// On load, a fingerprint's firing Alert wins over an older resolved
	// one; the same Alert always replaces its own memory.
	if cur, ok := e.open[fp]; ok && cur.id != a.Metadata.ID && cur.state == resource.AlertFiring && a.Status.State != resource.AlertFiring {
		return
	}
	e.open[fp] = &openAlert{id: a.Metadata.ID, ruleID: a.Spec.RuleID, component: a.Spec.ComponentID,
		state: a.Status.State, resolvedAt: resolvedAt, value: a.Status.Value}
}

func (e *AlertEvaluator) memory(r resource.AlertRule, now time.Time) *ruleMemory {
	m, ok := e.rules[r.Metadata.ID]
	if !ok {
		m = &ruleMemory{nextDue: now.Add(jitter(r.Metadata.ID, r.Spec.Interval())), flushedAt: now}
		if v := r.Status.LastValue; v != nil {
			x := *v
			m.value = &x
		}
		if r.Status.LastEvaluated != nil {
			m.flushedAt = *r.Status.LastEvaluated
		}
		e.rules[r.Metadata.ID] = m
	}
	return m
}

// jitter spreads rules' first evaluations, the same for a rule on every
// start: up to a tenth of its interval, at most alertMaxJitter.
func jitter(id string, interval time.Duration) time.Duration {
	span := min(interval/10, alertMaxJitter)
	if span <= 0 {
		return 0
	}
	h := fnv.New64a()
	h.Write([]byte(id))
	return time.Duration(h.Sum64() % uint64(span))
}

// forgetDeleted drops memory of rules that are gone. Their Alerts went
// with them (a rule's delete cascades to its Alerts).
func (e *AlertEvaluator) forgetDeleted(rules []resource.AlertRule) {
	live := map[string]bool{}
	for _, r := range rules {
		live[r.Metadata.ID] = true
	}
	for id := range e.rules {
		if !live[id] {
			delete(e.rules, id)
		}
	}
	for fp, a := range e.open {
		if !live[a.ruleID] {
			delete(e.open, fp)
		}
	}
}

// targets is what r watches: each Deployment of its Component (or of its
// Application's Components) in its Environment. A built-in rule watches
// its package, with no target. reason is set when there's nothing to
// watch.
func (e *AlertEvaluator) targets(ctx context.Context, r resource.AlertRule) (out []LogTarget, reason, msg string, err error) {
	if r.Spec.BuiltIn {
		return []LogTarget{{}}, "", "", nil
	}
	if len(r.Metadata.OwnerRefs) != 1 {
		return nil, resource.ReasonComponentGone, "the rule has no Component or Application", nil
	}
	owner := r.Metadata.OwnerRefs[0]
	env, err := e.s.Environments.Get(ctx, r.Spec.EnvironmentID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, resource.ReasonComponentGone, "its Environment no longer exists", nil
	}
	if err != nil {
		return nil, "", "", err
	}
	comps := map[string]resource.Component{}
	switch owner.Kind {
	case resource.KindComponent:
		c, err := e.s.Components.Get(ctx, owner.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, resource.ReasonComponentGone, "its Component no longer exists", nil
		}
		if err != nil {
			return nil, "", "", err
		}
		comps[c.Metadata.ID] = c
	case resource.KindApplication:
		list, err := e.s.Components.List(ctx, store.ListOptions{OwnerID: owner.ID})
		if err != nil {
			return nil, "", "", err
		}
		for _, c := range list {
			comps[c.Metadata.ID] = c
		}
	default:
		return nil, resource.ReasonComponentGone, "the rule has no Component or Application", nil
	}
	deps, err := e.s.Deployments.List(ctx, store.ListOptions{OwnerID: env.Metadata.ID})
	if err != nil {
		return nil, "", "", err
	}
	for _, d := range deps {
		if c, ok := comps[d.Spec.Component]; ok {
			out = append(out, LogTarget{Deployment: d, Component: c, Environment: env})
		}
	}
	if len(out) == 0 {
		return nil, resource.ReasonNotDeployed, "nothing it watches runs in Environment " + env.Metadata.Name, nil
	}
	return out, "", "", nil
}

// evaluate runs jobs, at most AlertQueryWorkers at once, each with its own
// timeout.
func (e *AlertEvaluator) evaluate(ctx context.Context, jobs []*evalJob) {
	sem := make(chan struct{}, AlertQueryWorkers)
	var wg sync.WaitGroup
	for _, j := range jobs {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			j.err = ctx.Err()
			continue
		}
		wg.Add(1)
		go func(j *evalJob) {
			defer func() { <-sem; wg.Done() }()
			qctx, cancel := context.WithTimeout(ctx, AlertQueryTimeout)
			defer cancel()
			began := time.Now()
			j.value, j.err = e.provider.EvaluateCondition(qctx, j.target, j.rule.Spec.Condition, conditionWindow(j.rule.Spec.Condition))
			j.took = time.Since(began)
		}(j)
	}
	wg.Wait()
}

// conditionWindow is the time a condition reads over.
func conditionWindow(c resource.AlertCondition) time.Duration {
	switch {
	case c.Metric != nil:
		return time.Duration(c.Metric.ForMinutes) * time.Minute
	case c.Restarts != nil:
		return time.Duration(c.Restarts.WindowMinutes) * time.Minute
	case c.LogMatch != nil:
		return time.Duration(c.LogMatch.WindowMinutes) * time.Minute
	}
	return 0
}

// Threshold is the value a condition fires above.
func Threshold(c resource.AlertCondition) float64 {
	switch {
	case c.Metric != nil:
		return c.Metric.Above
	case c.Restarts != nil:
		return float64(c.Restarts.Above)
	case c.LogMatch != nil:
		return float64(c.LogMatch.Above)
	case c.PackageDisk != nil:
		return c.PackageDisk.PercentOfBudget
	}
	return math.Inf(1)
}

// apply turns the answers into rule and Alert state, in order. Errors
// never change an Alert: no "resolved" is sent for missing data.
func (e *AlertEvaluator) apply(ctx context.Context, jobs []*evalJob, now time.Time) {
	type outcome struct {
		rule          resource.AlertRule
		reason, msg   string
		notReady, any bool
		value         *float64
	}
	byRule := map[string]*outcome{}
	var order []string
	var refused *ConditionRefusedError
	for _, j := range jobs {
		o, ok := byRule[j.rule.Metadata.ID]
		if !ok {
			o = &outcome{rule: j.rule}
			byRule[j.rule.Metadata.ID] = o
			order = append(order, j.rule.Metadata.ID)
		}
		switch {
		case errors.Is(j.err, ErrProviderNotReady):
			o.notReady = true
			continue
		case errors.Is(j.err, ErrNoData):
			o.reason, o.msg = resource.ReasonNoData, "no data in the condition's window yet"
			continue
		case errors.As(j.err, &refused):
			o.reason, o.msg = resource.ReasonConditionRefused, refused.Why
			continue
		case j.err != nil:
			// The provider's error can name backend internals: logged,
			// not stored.
			log.Printf("reconcile: alerts: rule %s: %v", j.rule.Metadata.ID, j.err)
			o.reason, o.msg = resource.ReasonQueryFailed, "the condition's query failed; see serendipd's log"
			continue
		}
		o.any = true
		v := j.value.Value
		if o.value == nil || v > *o.value {
			o.value = &v
		}
		e.transition(ctx, j, now)
	}
	for _, id := range order {
		o := byRule[id]
		mem := e.rules[id]
		mem.evaluated = now
		if o.value != nil && (mem.value == nil || *mem.value != *o.value) {
			mem.value, mem.valueDirty = o.value, true
		}
		switch {
		case o.notReady:
			e.setRule(ctx, o.rule, false, resource.ReasonPackageNotReady, resource.ConditionFalse, resource.ReasonPackageNotReady,
				"the observability package isn't ready", now)
		case o.reason != "":
			e.setRule(ctx, o.rule, true, "", resource.ConditionFalse, o.reason, o.msg, now)
		default:
			e.setRule(ctx, o.rule, true, "", resource.ConditionTrue, "Evaluated", "", now)
		}
	}
}

// transition moves one fingerprint's Alert on one successful answer.
func (e *AlertEvaluator) transition(ctx context.Context, j *evalJob, now time.Time) {
	r, cur := j.rule, e.open[j.fp]
	threshold := Threshold(r.Spec.Condition)
	firing := j.value.Value > threshold
	switch {
	case firing && cur != nil && cur.state == resource.AlertFiring:
		if cur.value != j.value.Current {
			cur.value, cur.valueDirty = j.value.Current, true
		}
	case firing && cur != nil && now.Sub(cur.resolvedAt) <= AlertReopenWindow:
		// A flap: the same Alert fires again.
		channels, err := e.channelsFor(ctx, r)
		if err != nil {
			log.Printf("reconcile: alerts: rule %s: channels: %v", r.Metadata.ID, err)
			return
		}
		a, err := e.writer.update(ctx, cur.id, func(a *resource.Alert) bool {
			a.Status.State, a.Status.ResolvedAt, a.Status.ResolvedReason = resource.AlertFiring, nil, ""
			a.Status.Value, a.Status.Threshold = j.value.Current, threshold
			addDeliveries(&a.Status, channels, resource.DeliveryEventFiring, now)
			return true
		})
		if err != nil {
			log.Printf("reconcile: alerts: reopen %s: %v", cur.id, err)
			return
		}
		e.remember(a, now)
		e.deliver(a.Metadata.ID)
	case firing:
		channels, err := e.channelsFor(ctx, r)
		if err != nil {
			log.Printf("reconcile: alerts: rule %s: channels: %v", r.Metadata.ID, err)
			return
		}
		a := resource.Alert{
			Metadata: resource.Metadata{Name: r.Metadata.Name, OwnerRefs: []resource.OwnerRef{{Kind: resource.KindAlertRule, ID: r.Metadata.ID}}},
			Spec: resource.AlertSpec{RuleID: r.Metadata.ID, ComponentID: j.comp, EnvironmentID: j.target.Environment.Metadata.ID,
				Severity: r.Spec.Severity, Summary: r.Spec.Summary, Fingerprint: j.fp},
			Status: resource.AlertStatus{State: resource.AlertFiring, StartedAt: now, Value: j.value.Current, Threshold: threshold},
		}
		addDeliveries(&a.Status, channels, resource.DeliveryEventFiring, now)
		if err := resource.ValidateAlert(a.Metadata, a.Spec, a.Status); err != nil {
			log.Printf("reconcile: alerts: rule %s: %v", r.Metadata.ID, err)
			return
		}
		created, err := e.writer.create(ctx, a)
		if err != nil {
			log.Printf("reconcile: alerts: open for rule %s: %v", r.Metadata.ID, err)
			return
		}
		e.remember(created, now)
		e.deliver(created.Metadata.ID)
	case cur != nil && cur.state == resource.AlertFiring:
		e.resolve(ctx, cur, resource.ResolvedClear, &j.value.Current, now)
	}
}

// resolve resolves one open Alert and queues its resolution to the
// channels its firing went to.
func (e *AlertEvaluator) resolve(ctx context.Context, cur *openAlert, reason string, value *float64, now time.Time) {
	a, err := e.writer.update(ctx, cur.id, func(a *resource.Alert) bool {
		if a.Status.State != resource.AlertFiring {
			return false
		}
		t := now
		a.Status.State, a.Status.ResolvedAt, a.Status.ResolvedReason = resource.AlertResolved, &t, reason
		if value != nil {
			a.Status.Value = *value
		}
		var channels []string
		for _, d := range a.Status.Deliveries {
			if d.Event == resource.DeliveryEventFiring && !slices.Contains(channels, d.ChannelID) {
				channels = append(channels, d.ChannelID)
			}
		}
		addDeliveries(&a.Status, channels, resource.DeliveryEventResolved, now)
		return true
	})
	if errors.Is(err, store.ErrNotFound) {
		for fp, o := range e.open {
			if o.id == cur.id {
				delete(e.open, fp)
			}
		}
		return
	}
	if err != nil {
		log.Printf("reconcile: alerts: resolve %s: %v", cur.id, err)
		return
	}
	e.remember(a, now)
	e.deliver(a.Metadata.ID)
}

// resolveRule resolves r's firing Alerts, except those for the
// Components in keep (nil keeps none).
func (e *AlertEvaluator) resolveRule(ctx context.Context, r resource.AlertRule, keep map[string]bool, reason string, now time.Time) {
	for _, o := range e.firingOf(r.Metadata.ID) {
		if keep != nil && keep[o.component] {
			continue
		}
		e.resolve(ctx, o, reason, nil, now)
	}
}

func (e *AlertEvaluator) firingOf(ruleID string) []*openAlert {
	var out []*openAlert
	for _, o := range e.open {
		if o.ruleID == ruleID && o.state == resource.AlertFiring {
			out = append(out, o)
		}
	}
	slices.SortFunc(out, func(a, b *openAlert) int { return compareStrings(a.id, b.id) })
	return out
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// disabled: a disabled rule's firing Alerts resolve, and it reads
// inactive.
func (e *AlertEvaluator) disabled(ctx context.Context, r resource.AlertRule, now time.Time) {
	e.resolveRule(ctx, r, nil, resource.ResolvedRuleDisabled, now)
	e.setRule(ctx, r, false, resource.ReasonDisabled, resource.ConditionFalse, resource.ReasonDisabled, "the rule is disabled", now)
}

// packageRemoved resolves every open Alert once the package is gone, and
// that resolution is delivered (spec IN 13).
func (e *AlertEvaluator) packageRemoved(ctx context.Context, rules []resource.AlertRule, now time.Time) {
	if !e.removedDone {
		left := false
		for _, r := range rules {
			e.resolveRule(ctx, r, nil, resource.ResolvedPackageRemoved, now)
			left = left || len(e.firingOf(r.Metadata.ID)) > 0
		}
		// Done only once nothing is left firing; a failed resolve is
		// retried on the next tick.
		e.removedDone = !left
	}
	for _, r := range rules {
		e.memory(r, now)
		e.setRule(ctx, r, false, resource.ReasonPackageNotReady, resource.ConditionFalse, resource.ReasonPackageNotReady,
			"the observability package isn't installed", now)
	}
}

// setRule writes r's active flag and Evaluating condition, only when they
// change; the held value goes with it.
func (e *AlertEvaluator) setRule(ctx context.Context, r resource.AlertRule, active bool, activeReason string,
	status resource.ConditionStatus, reason, msg string, now time.Time) {
	same := func(r resource.AlertRule) bool {
		c := findCondition(r.Status.Conditions, resource.AlertRuleConditionEvaluating)
		return r.Status.Active == active && r.Status.ActiveReason == activeReason &&
			c != nil && c.Status == status && c.Reason == reason && c.Message == msg
	}
	if same(r) {
		return
	}
	e.writeRule(ctx, r.Metadata.ID, now, func(r *resource.AlertRule) bool {
		if same(*r) {
			return false
		}
		r.Status.Active, r.Status.ActiveReason = active, activeReason
		r.Status.Conditions = setCondition(r.Status.Conditions, resource.AlertRuleConditionEvaluating, status, reason, msg, now)
		return true
	})
}

// writeRule re-reads the rule (a person may have changed its spec) and
// writes mutate's change, with the held value and time.
func (e *AlertEvaluator) writeRule(ctx context.Context, id string, now time.Time, mutate func(*resource.AlertRule) bool) {
	for attempt := 0; attempt < 3; attempt++ {
		r, err := e.s.Rules.Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return
		}
		if err != nil {
			log.Printf("reconcile: alerts: rule %s: %v", id, err)
			return
		}
		changed := mutate(&r)
		mem := e.rules[id]
		if mem != nil && mem.valueDirty {
			r.Status.LastValue, changed = mem.value, true
		}
		if !changed {
			return
		}
		if mem != nil && !mem.evaluated.IsZero() {
			t := mem.evaluated
			r.Status.LastEvaluated = &t
		}
		_, err = e.s.Rules.Update(ctx, r)
		if errors.Is(err, store.ErrConflict) {
			if attempt == 2 {
				log.Printf("reconcile: alerts: rule %s: status not written after %d conflicts; retried on the next change", id, attempt+1)
			}
			continue
		}
		if err != nil {
			log.Printf("reconcile: alerts: rule %s: %v", id, err)
			return
		}
		if mem != nil {
			mem.valueDirty, mem.flushedAt = false, now
		}
		return
	}
}

// flush writes held values: a rule's when it changed (or its stored
// lastEvaluated is stale), an Alert's when it changed.
func (e *AlertEvaluator) flush(ctx context.Context, now time.Time) {
	ids := make([]string, 0, len(e.rules))
	for id := range e.rules {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		m := e.rules[id]
		stale := !m.evaluated.IsZero() && now.Sub(m.flushedAt) >= alertStaleEvaluated
		if !m.valueDirty && !stale {
			continue
		}
		e.writeRule(ctx, id, now, func(*resource.AlertRule) bool { return stale })
	}
	for _, o := range e.open {
		if !o.valueDirty || o.state != resource.AlertFiring {
			continue
		}
		v := o.value
		if _, err := e.writer.update(ctx, o.id, func(a *resource.Alert) bool {
			if a.Status.State != resource.AlertFiring || a.Status.Value == v {
				return false
			}
			a.Status.Value = v
			return true
		}); err != nil && !errors.Is(err, store.ErrNotFound) {
			log.Printf("reconcile: alerts: flush %s: %v", o.id, err)
			continue
		}
		o.valueDirty = false
	}
}

// channelsFor is where r's Alerts go: the channels it names; with none,
// its Project's default channels; with none of those (or for a built-in
// rule), the platform's default channels.
func (e *AlertEvaluator) channelsFor(ctx context.Context, r resource.AlertRule) ([]string, error) {
	if len(r.Spec.ChannelIDs) > 0 {
		return r.Spec.ChannelIDs, nil
	}
	all, err := e.s.Channels.List(ctx, store.ListOptions{})
	if err != nil {
		return nil, err
	}
	project := ""
	if !r.Spec.BuiltIn {
		if project, err = projectOf(ctx, e.s.Owners, graph.ResourceRef{Kind: resource.KindAlertRule, ID: r.Metadata.ID}); err != nil {
			return nil, err
		}
	}
	var mine, platform []string
	for _, c := range all {
		if !c.Spec.Default {
			continue
		}
		switch {
		case len(c.Metadata.OwnerRefs) == 0:
			platform = append(platform, c.Metadata.ID)
		case project != "" && c.Metadata.OwnerRefs[0].Kind == resource.KindProject && c.Metadata.OwnerRefs[0].ID == project:
			mine = append(mine, c.Metadata.ID)
		}
	}
	if len(mine) > 0 {
		return mine, nil
	}
	return platform, nil
}

// projectOf walks ref's owners up to its Project, or "" when there's none.
func projectOf(ctx context.Context, owners OwnerReader, ref graph.ResourceRef) (string, error) {
	seen := map[graph.ResourceRef]bool{}
	for depth := 0; depth < 16; depth++ {
		if seen[ref] {
			return "", nil
		}
		seen[ref] = true
		up, err := owners.Owners(ctx, ref)
		if err != nil || len(up) == 0 {
			return "", err
		}
		for _, o := range up {
			if o.Kind == resource.KindProject {
				return o.ID, nil
			}
		}
		ref = up[0]
	}
	return "", nil
}

// addDeliveries queues event to each channel.
func addDeliveries(st *resource.AlertStatus, channels []string, event string, now time.Time) {
	for _, ch := range channels {
		st.AddDelivery(resource.AlertDelivery{ChannelID: ch, Event: event, State: resource.DeliveryPending, At: now})
	}
}

// resolvedDetail is a resolution's line when the condition didn't clear:
// the Alert stopped being watched (spec 024 T010: a package removal read
// "back under 3 (now 24)"). "" for a cleared one.
func resolvedDetail(reason string, last float64, lasted time.Duration) string {
	why := map[string]string{
		resource.ResolvedPackageRemoved: "the observability package was removed",
		resource.ResolvedRuleDeleted:    "its rule was deleted",
		resource.ResolvedRuleDisabled:   "its rule was turned off",
		resource.ResolvedComponentGone:  "its component is gone",
	}[reason]
	if why == "" {
		return ""
	}
	return fmt.Sprintf("No longer watched: %s (last value %.4g) after %s", why, last, lastedWords(lasted))
}

// lastedWords is how long an Alert fired: "less than a minute", "7 min",
// "2 h 5 min".
func lastedWords(d time.Duration) string {
	m := int(d / time.Minute)
	switch {
	case m < 1:
		return "less than a minute"
	case m < 60:
		return fmt.Sprintf("%d min", m)
	case m%60 == 0:
		return fmt.Sprintf("%d h", m/60)
	}
	return fmt.Sprintf("%d h %d min", m/60, m%60)
}

// conditionDetail is the condition line a notification carries.
func conditionDetail(c resource.AlertCondition, event string, current, threshold float64, lasted time.Duration) string {
	what, unit := "value", ""
	switch {
	case c.Metric != nil:
		what = map[string]string{resource.MetricCPU: "CPU", resource.MetricMemory: "Memory"}[c.Metric.Metric]
		switch c.Metric.As {
		case resource.MetricAsPercentOfLimit:
			unit = "% of limit"
		case resource.MetricAsPercentOfRequest:
			unit = "% of request"
		case resource.MetricAsValue:
			unit = map[string]string{resource.MetricCPU: " millicores", resource.MetricMemory: " MiB"}[c.Metric.Metric]
		}
	case c.Restarts != nil:
		what, unit = "Restarts", fmt.Sprintf(" in %d min", c.Restarts.WindowMinutes)
	case c.LogMatch != nil:
		what, unit = fmt.Sprintf("Log lines matching %q", c.LogMatch.Text), fmt.Sprintf(" in %d min", c.LogMatch.WindowMinutes)
	case c.PackageDisk != nil:
		what, unit = "Observability disk use", "% of budget"
	}
	num := func(v float64) string { return fmt.Sprintf("%.4g", v) }
	if event == resource.DeliveryEventResolved {
		return fmt.Sprintf("%s back under %s%s (now %s) after %s", what, num(threshold), unit, num(current), lastedWords(lasted))
	}
	s := fmt.Sprintf("%s above %s%s (now %s)", what, num(threshold), unit, num(current))
	if c.Metric != nil {
		s = fmt.Sprintf("%s above %s%s for %d min (now %s)", what, num(threshold), unit, c.Metric.ForMinutes, num(current))
	}
	return s
}
