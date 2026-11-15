package reconcile_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/graph"
	"github.com/serendip-infra/serendip-infra/core/notify"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store"
	"github.com/serendip-infra/serendip-infra/core/store/sqlite"
)

// counting wraps a store and counts its writes.
type counting[S, St any] struct {
	store.Store[S, St]
	writes *atomic.Int64
}

func (c counting[S, St]) Create(ctx context.Context, r resource.Resource[S, St]) (resource.Resource[S, St], error) {
	c.writes.Add(1)
	return c.Store.Create(ctx, r)
}

func (c counting[S, St]) Update(ctx context.Context, r resource.Resource[S, St]) (resource.Resource[S, St], error) {
	c.writes.Add(1)
	return c.Store.Update(ctx, r)
}

// fakeConditions answers EvaluateCondition from a value per Component,
// tracking how many queries run at once.
type fakeConditions struct {
	reconcile.ObservabilityProvider
	mu       sync.Mutex
	values   map[string]float64 // Component ID -> value
	err      error
	delay    time.Duration
	inFlight atomic.Int64
	maxSeen  atomic.Int64
	calls    atomic.Int64
}

func (f *fakeConditions) set(comp string, v float64) {
	f.mu.Lock()
	f.values[comp] = v
	f.mu.Unlock()
}

func (f *fakeConditions) EvaluateCondition(ctx context.Context, t reconcile.LogTarget, c resource.AlertCondition, window time.Duration) (reconcile.ConditionValue, error) {
	f.calls.Add(1)
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		m := f.maxSeen.Load()
		if n <= m || f.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return reconcile.ConditionValue{}, f.err
	}
	v := f.values[t.Component.Metadata.ID]
	return reconcile.ConditionValue{Value: v, Current: v}, nil
}

// fakeNotifier records what's sent and answers from a script.
type fakeNotifier struct {
	mu      sync.Mutex
	sent    []notify.Message
	answers []notify.Result // used in order; then sent
}

func (n *fakeNotifier) Deliver(_ context.Context, t notify.Target, m notify.Message) notify.Result {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, m)
	if len(n.answers) > 0 {
		r := n.answers[0]
		n.answers = n.answers[1:]
		return r
	}
	return notify.Result{State: resource.DeliverySent, ThreadRef: fmt.Sprint(len(n.sent))}
}

func (n *fakeNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.sent)
}

type alertFixture struct {
	t        *testing.T
	ctx      context.Context
	db       *sql.DB
	now      time.Time
	provider *fakeConditions
	notifier *fakeNotifier
	writes   atomic.Int64
	stores   reconcile.AlertStores
	rules    *sqlite.Store[resource.AlertRuleSpec, resource.AlertRuleStatus]
	alerts   *sqlite.Store[resource.AlertSpec, resource.AlertStatus]
	channels *sqlite.Store[resource.NotificationChannelSpec, resource.NotificationChannelStatus]
	comps    *sqlite.Store[resource.ComponentSpec, resource.ComponentStatus]
	deps     *sqlite.Store[resource.DeploymentSpec, resource.DeploymentStatus]
	project  resource.Project
	env      resource.Environment
	app      resource.Application
	channel  string
	e        *reconcile.AlertEvaluator
	d        *reconcile.DeliveryLoop
}

// mustV returns v, panicking on err (fixture setup only).
func mustV[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func newAlertFixture(t *testing.T) *alertFixture {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "alerts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	f := &alertFixture{t: t, ctx: ctx, db: db, now: time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC),
		provider: &fakeConditions{values: map[string]float64{}}, notifier: &fakeNotifier{}}
	f.rules = sqlite.NewStore[resource.AlertRuleSpec, resource.AlertRuleStatus](db, resource.KindAlertRule)
	f.alerts = sqlite.NewStore[resource.AlertSpec, resource.AlertStatus](db, resource.KindAlert)
	f.channels = sqlite.NewStore[resource.NotificationChannelSpec, resource.NotificationChannelStatus](db, resource.KindNotificationChannel)
	f.comps = sqlite.NewStore[resource.ComponentSpec, resource.ComponentStatus](db, resource.KindComponent)
	f.deps = sqlite.NewStore[resource.DeploymentSpec, resource.DeploymentStatus](db, resource.KindDeployment)
	projects := sqlite.NewStore[resource.ProjectSpec, resource.ProjectStatus](db, resource.KindProject)
	envs := sqlite.NewStore[resource.EnvironmentSpec, resource.EnvironmentStatus](db, resource.KindEnvironment)
	apps := sqlite.NewStore[resource.ApplicationSpec, resource.ApplicationStatus](db, resource.KindApplication)

	f.project = mustV(projects.Create(ctx, resource.Project{Metadata: resource.Metadata{Name: "shop"}}))
	owned := func(k resource.Kind, id string) []resource.OwnerRef { return []resource.OwnerRef{{Kind: k, ID: id}} }
	f.env = mustV(envs.Create(ctx, resource.Environment{Metadata: resource.Metadata{Name: "prod", OwnerRefs: owned(resource.KindProject, f.project.Metadata.ID)}}))
	f.app = mustV(apps.Create(ctx, resource.Application{Metadata: resource.Metadata{Name: "store", OwnerRefs: owned(resource.KindProject, f.project.Metadata.ID)}}))
	f.channel = mustV(f.channels.Create(ctx, resource.NotificationChannel{
		Metadata: resource.Metadata{Name: "ops", OwnerRefs: owned(resource.KindProject, f.project.Metadata.ID)},
		Spec:     resource.NotificationChannelSpec{Type: resource.ChannelWebhook, Webhook: &resource.WebhookSettings{URL: "https://hooks.example.com/a"}},
	})).Metadata.ID

	f.stores = reconcile.AlertStores{
		Rules:        counting[resource.AlertRuleSpec, resource.AlertRuleStatus]{f.rules, &f.writes},
		Alerts:       counting[resource.AlertSpec, resource.AlertStatus]{f.alerts, &f.writes},
		Channels:     f.channels,
		SMTP:         sqlite.NewStore[resource.SMTPServerSpec, resource.SMTPServerStatus](db, resource.KindSMTPServer),
		Deployments:  f.deps,
		Components:   f.comps,
		Environments: envs,
		Applications: apps,
		Projects:     projects,
		Owners:       graph.NewSQLiteGraph(db),
	}
	f.e, f.d = reconcile.NewAlertEvaluator(reconcile.AlertEvaluatorOptions{
		Stores: f.stores, Provider: f.provider,
		Installed: func(context.Context) (bool, error) { return true, nil },
		Now:       func() time.Time { return f.now },
	}, reconcile.DeliveryOptions{Notifier: f.notifier, ConsoleURL: "http://localhost:8080"})
	return f
}

// component makes a Component of the Application, deployed in prod.
func (f *alertFixture) component(name string) string {
	f.t.Helper()
	c := mustV(f.comps.Create(f.ctx, resource.Component{Metadata: resource.Metadata{Name: name,
		OwnerRefs: []resource.OwnerRef{{Kind: resource.KindApplication, ID: f.app.Metadata.ID}}}}))
	mustV(f.deps.Create(f.ctx, resource.Deployment{Metadata: resource.Metadata{Name: name + "-prod",
		OwnerRefs: []resource.OwnerRef{{Kind: resource.KindEnvironment, ID: f.env.Metadata.ID}}}, Spec: resource.DeploymentSpec{Component: c.Metadata.ID}}))
	return c.Metadata.ID
}

func (f *alertFixture) rule(name, comp string, channels ...string) resource.AlertRule {
	f.t.Helper()
	return mustV(f.rules.Create(f.ctx, resource.AlertRule{
		Metadata: resource.Metadata{Name: name, OwnerRefs: []resource.OwnerRef{{Kind: resource.KindComponent, ID: comp}}},
		Spec: resource.AlertRuleSpec{EnvironmentID: f.env.Metadata.ID, Severity: resource.SeverityCritical, Summary: name + " CPU high", ChannelIDs: channels,
			Condition: resource.AlertCondition{Type: resource.ConditionTypeMetric, Metric: &resource.MetricCondition{Metric: resource.MetricCPU, As: resource.MetricAsValue, Above: 1, ForMinutes: 5}}},
	}))
}

func (f *alertFixture) tick(advance time.Duration) {
	f.t.Helper()
	f.now = f.now.Add(advance)
	if err := f.e.Tick(f.ctx); err != nil {
		f.t.Fatal(err)
	}
}

func (f *alertFixture) alertsOf(ruleID string) []resource.Alert {
	f.t.Helper()
	all := mustV(f.alerts.List(f.ctx, store.ListOptions{OwnerID: ruleID}))
	return all
}

func (f *alertFixture) ruleNow(id string) resource.AlertRule {
	f.t.Helper()
	return mustV(f.rules.Get(f.ctx, id))
}

func TestAlerts_FireUpdateResolveReopen(t *testing.T) {
	f := newAlertFixture(t)
	web := f.component("web")
	r := f.rule("cpu", web, f.channel)

	f.provider.set(web, 5)
	f.tick(0) // sees the rule; its first evaluation is spread by the jitter
	f.tick(time.Minute)
	got := f.alertsOf(r.Metadata.ID)
	if len(got) != 1 || got[0].Status.State != resource.AlertFiring || got[0].Status.Value != 5 || got[0].Status.Threshold != 1 {
		t.Fatalf("after firing: %+v", got)
	}
	first := got[0]
	if ds := first.Status.Deliveries; len(ds) != 1 || ds[0].ChannelID != f.channel || ds[0].State != resource.DeliveryPending || ds[0].Event != resource.DeliveryEventFiring {
		t.Fatalf("deliveries = %+v", ds)
	}
	if first.Spec.Fingerprint != resource.AlertFingerprint(r.Metadata.ID, web, f.env.Metadata.ID) {
		t.Errorf("fingerprint = %q", first.Spec.Fingerprint)
	}

	// Still firing: the same Alert, its value updated at the flush.
	f.provider.set(web, 7)
	f.tick(time.Minute)
	got = f.alertsOf(r.Metadata.ID)
	if len(got) != 1 || got[0].Status.Value != 7 {
		t.Fatalf("still firing: %+v", got)
	}

	// Clear: resolved, with a resolution to the firing's channel.
	f.provider.set(web, 0)
	f.tick(time.Minute)
	a := mustV(f.alerts.Get(f.ctx, first.Metadata.ID))
	if a.Status.State != resource.AlertResolved || a.Status.ResolvedReason != resource.ResolvedClear || a.Status.ResolvedAt == nil {
		t.Fatalf("resolved: %+v", a.Status)
	}
	if n := len(a.Status.Deliveries); n != 2 || a.Status.Deliveries[1].Event != resource.DeliveryEventResolved {
		t.Fatalf("deliveries after resolve = %+v", a.Status.Deliveries)
	}

	// A firing within 5 minutes reopens the same Alert.
	f.provider.set(web, 9)
	f.tick(2 * time.Minute)
	got = f.alertsOf(r.Metadata.ID)
	if len(got) != 1 || got[0].Metadata.ID != first.Metadata.ID || got[0].Status.State != resource.AlertFiring || got[0].Status.ResolvedAt != nil {
		t.Fatalf("reopen: %+v", got)
	}

	// Resolved, then quiet past the window: the next firing is a new Alert.
	f.provider.set(web, 0)
	f.tick(time.Minute)
	f.tick(6 * time.Minute)
	f.provider.set(web, 4)
	f.tick(time.Minute)
	got = f.alertsOf(r.Metadata.ID)
	if len(got) != 2 {
		t.Fatalf("after the window, alerts = %d, want 2", len(got))
	}
}

func TestAlerts_ErrorLeavesFiringAlertFiring(t *testing.T) {
	f := newAlertFixture(t)
	web := f.component("web")
	r := f.rule("cpu", web, f.channel)
	f.provider.set(web, 5)
	f.tick(0) // sees the rule; its first evaluation is spread by the jitter
	f.tick(time.Minute)

	f.provider.err = errors.New("adapter down: http://10.0.0.5:8080/secret-path")
	f.tick(time.Minute)
	f.tick(time.Minute)
	got := f.alertsOf(r.Metadata.ID)
	if len(got) != 1 || got[0].Status.State != resource.AlertFiring || len(got[0].Status.Deliveries) != 1 {
		t.Fatalf("an error changed the Alert: %+v", got)
	}
	c := findCond(f.ruleNow(r.Metadata.ID).Status.Conditions, resource.AlertRuleConditionEvaluating)
	if c == nil || c.Status != resource.ConditionFalse || c.Reason != resource.ReasonQueryFailed {
		t.Fatalf("Evaluating = %+v, want False QueryFailed", c)
	}
	if c.Message != "the condition's query failed; see serendipd's log" {
		t.Errorf("the provider's error reached the rule: %q", c.Message)
	}

	// A refused condition says why on the rule, and changes nothing.
	f.provider.err = fmt.Errorf("wrapped: %w", &reconcile.ConditionRefusedError{Why: "the Component sets no CPU request"})
	f.tick(time.Minute)
	c = findCond(f.ruleNow(r.Metadata.ID).Status.Conditions, resource.AlertRuleConditionEvaluating)
	if c == nil || c.Reason != resource.ReasonConditionRefused || c.Message != "the Component sets no CPU request" {
		t.Errorf("Evaluating = %+v, want ConditionRefused with the reason", c)
	}
	if got := f.alertsOf(r.Metadata.ID); got[0].Status.State != resource.AlertFiring {
		t.Fatalf("a refused condition resolved the Alert")
	}

	// No data is an error too: never a resolution.
	f.provider.err = reconcile.ErrNoData
	f.tick(time.Minute)
	if got := f.alertsOf(r.Metadata.ID); got[0].Status.State != resource.AlertFiring {
		t.Fatalf("no data resolved the Alert")
	}
	// The package not ready: inactive, and still firing.
	f.provider.err = reconcile.ErrProviderNotReady
	f.tick(time.Minute)
	if rr := f.ruleNow(r.Metadata.ID); rr.Status.Active || rr.Status.ActiveReason != resource.ReasonPackageNotReady {
		t.Errorf("rule status = %+v, want inactive PackageNotReady", rr.Status)
	}
	if got := f.alertsOf(r.Metadata.ID); got[0].Status.State != resource.AlertFiring {
		t.Fatalf("not ready resolved the Alert")
	}
}

func TestAlerts_ComponentGoneAndDisabledResolve(t *testing.T) {
	f := newAlertFixture(t)
	web := f.component("web")
	r := f.rule("cpu", web, f.channel)
	f.provider.set(web, 5)
	f.tick(0) // sees the rule; its first evaluation is spread by the jitter
	f.tick(time.Minute)

	// Disabled: resolved as ruleDisabled.
	off := false
	cur := f.ruleNow(r.Metadata.ID)
	cur.Spec.Enabled = &off
	mustV(f.rules.Update(f.ctx, cur))
	f.tick(time.Minute)
	a := f.alertsOf(r.Metadata.ID)[0]
	if a.Status.State != resource.AlertResolved || a.Status.ResolvedReason != resource.ResolvedRuleDisabled {
		t.Fatalf("disabled: %+v", a.Status)
	}

	// The Component goes (removed under the fixture, as a cascade would):
	// its firing Alert resolves as componentGone.
	api := f.component("api")
	r2 := f.rule("cpu2", api, f.channel)
	f.provider.set(api, 5)
	f.tick(0)
	f.tick(time.Minute)
	if got := f.alertsOf(r2.Metadata.ID); len(got) != 1 || got[0].Status.State != resource.AlertFiring {
		t.Fatalf("api not firing: %+v", got)
	}
	if _, err := f.db.Exec(`DELETE FROM resources WHERE id = ?`, api); err != nil {
		t.Fatal(err)
	}
	f.tick(time.Minute)
	a = f.alertsOf(r2.Metadata.ID)[0]
	if a.Status.State != resource.AlertResolved || a.Status.ResolvedReason != resource.ResolvedComponentGone {
		t.Fatalf("component gone: %+v", a.Status)
	}
	if c := findCond(f.ruleNow(r2.Metadata.ID).Status.Conditions, resource.AlertRuleConditionEvaluating); c == nil || c.Reason != resource.ReasonComponentGone {
		t.Errorf("Evaluating = %+v, want ComponentGone", c)
	}
}

func TestAlerts_PackageRemovedResolves(t *testing.T) {
	f := newAlertFixture(t)
	web := f.component("web")
	r := f.rule("cpu", web, f.channel)
	f.provider.set(web, 5)
	f.tick(0) // sees the rule; its first evaluation is spread by the jitter
	f.tick(time.Minute)

	installed := false
	f.e, f.d = reconcile.NewAlertEvaluator(reconcile.AlertEvaluatorOptions{
		Stores: f.stores, Provider: f.provider,
		Installed: func(context.Context) (bool, error) { return installed, nil },
		Now:       func() time.Time { return f.now },
	}, reconcile.DeliveryOptions{Notifier: f.notifier})
	f.tick(time.Minute)
	a := f.alertsOf(r.Metadata.ID)[0]
	if a.Status.State != resource.AlertResolved || a.Status.ResolvedReason != resource.ResolvedPackageRemoved {
		t.Fatalf("package removed: %+v", a.Status)
	}
	// The resolution is delivered.
	f.d.Resume(f.ctx)
	f.d.Pass(f.ctx)
	if f.notifier.count() == 0 {
		t.Error("the packageRemoved resolution wasn't sent")
	}
}

// 200 rules at 1 minute: at most 2 queries at once, and no store write per
// evaluation once values are steady.
func TestAlerts_LoadIsBounded(t *testing.T) {
	f := newAlertFixture(t)
	f.provider.delay = 2 * time.Millisecond
	for i := range 200 {
		comp := f.component(fmt.Sprintf("c%03d", i))
		f.rule(fmt.Sprintf("r%03d", i), comp, f.channel)
		f.provider.set(comp, 0.5) // below the threshold: no Alerts
	}
	f.tick(0)
	f.tick(time.Minute) // first evaluation of every rule
	f.tick(time.Minute)
	before := f.writes.Load()
	evals := f.provider.calls.Load()
	for range 5 {
		f.tick(time.Minute)
	}
	if got := f.provider.calls.Load() - evals; got != 1000 {
		t.Errorf("evaluations over 5 minutes = %d, want 1000", got)
	}
	if w := f.writes.Load() - before; w != 0 {
		t.Errorf("store writes over 1000 steady evaluations = %d, want 0", w)
	}
	if m := f.provider.maxSeen.Load(); m > reconcile.AlertQueryWorkers {
		t.Errorf("queries at once = %d, want at most %d", m, reconcile.AlertQueryWorkers)
	}
}

// sqliteRetention is serendipd's retention purge over the fixture's db.
func sqliteRetention(f *alertFixture) func(context.Context, time.Time, int) (int, error) {
	return func(ctx context.Context, cutoff time.Time, limit int) (int, error) {
		return sqlite.PurgeResolvedAlerts(ctx, f.db, nil, cutoff, limit)
	}
}
