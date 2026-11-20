package reconcile_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/notify"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store"
	"github.com/serendip-infra/serendip-infra/core/store/sqlite"
)

// openWith stores a firing Alert of rule r with the given deliveries.
func (f *alertFixture) openWith(r resource.AlertRule, comp string, ds ...resource.AlertDelivery) resource.Alert {
	f.t.Helper()
	return mustV(f.alerts.Create(f.ctx, resource.Alert{
		Metadata: resource.Metadata{Name: r.Metadata.Name, OwnerRefs: []resource.OwnerRef{{Kind: resource.KindAlertRule, ID: r.Metadata.ID}}},
		Spec: resource.AlertSpec{RuleID: r.Metadata.ID, ComponentID: comp, EnvironmentID: f.env.Metadata.ID, Severity: r.Spec.Severity,
			Summary: r.Spec.Summary, Fingerprint: resource.AlertFingerprint(r.Metadata.ID, comp, f.env.Metadata.ID)},
		Status: resource.AlertStatus{State: resource.AlertFiring, StartedAt: f.now, Value: 5, Threshold: 1, Deliveries: ds},
	}))
}

func (f *alertFixture) pending(ch, event string) resource.AlertDelivery {
	return resource.AlertDelivery{ChannelID: ch, Event: event, State: resource.DeliveryPending, At: f.now}
}

func (f *alertFixture) pass(advance time.Duration) {
	f.now = f.now.Add(advance)
	f.d.Pass(f.ctx)
}

func (f *alertFixture) delivery(id string, i int) resource.AlertDelivery {
	f.t.Helper()
	return mustV(f.alerts.Get(f.ctx, id)).Status.Deliveries[i]
}

func (f *alertFixture) telegram(chat string) string {
	f.t.Helper()
	return mustV(f.channels.Create(f.ctx, resource.NotificationChannel{
		Metadata: resource.Metadata{Name: "tg" + chat},
		Spec: resource.NotificationChannelSpec{Type: resource.ChannelTelegram,
			Telegram: &resource.TelegramSettings{ChatID: chat, BotTokenSecretID: "s1"}},
	})).Metadata.ID
}

// restart replaces the evaluator and loop, as a restart of serendipd does.
func (f *alertFixture) restart() {
	f.e, f.d = reconcile.NewAlertEvaluator(reconcile.AlertEvaluatorOptions{Stores: f.stores, Provider: f.provider,
		Installed: func(context.Context) (bool, error) { return true, nil }, Now: func() time.Time { return f.now }},
		reconcile.DeliveryOptions{Notifier: f.notifier})
}

// A restart resumes a pending delivery and never re-sends a sent one; the
// resolution threads under the firing.
func TestDelivery_RestartResumesPendingOnly(t *testing.T) {
	f := newAlertFixture(t)
	web := f.component("web")
	r := f.rule("cpu", web, f.channel)
	sent := f.pending(f.channel, resource.DeliveryEventFiring)
	sent.State, sent.ThreadRef, sent.Attempts = resource.DeliverySent, "msg-41", 1
	a := f.openWith(r, web, sent, f.pending(f.channel, resource.DeliveryEventResolved))

	f.restart()
	if err := f.d.Resume(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.pass(0)
	if n := f.notifier.count(); n != 1 {
		t.Fatalf("sends after restart = %d, want 1 (the pending resolution)", n)
	}
	m := f.notifier.sent[0]
	if m.Event != notify.EventResolved || m.ThreadRef != "msg-41" || m.AlertID != a.Metadata.ID {
		t.Errorf("sent %+v, want the resolution threaded under msg-41", m)
	}
	if d := f.delivery(a.Metadata.ID, 1); d.State != resource.DeliverySent || d.Attempts != 1 {
		t.Errorf("resolution delivery = %+v", d)
	}
	f.pass(time.Minute)
	if n := f.notifier.count(); n != 1 {
		t.Errorf("sends after a second pass = %d, want still 1", n)
	}
}

func TestDelivery_RetriesStopAtFiveThenFail(t *testing.T) {
	f := newAlertFixture(t)
	web := f.component("web")
	r := f.rule("cpu", web, f.channel)
	a := f.openWith(r, web, f.pending(f.channel, resource.DeliveryEventFiring))
	for range 10 {
		f.notifier.answers = append(f.notifier.answers, notify.Result{Err: &notify.SendError{Host: "hooks.example.com", Status: 503, Retryable: true, Msg: "unavailable"}})
	}
	f.d.Enqueue(a.Metadata.ID)
	f.pass(0)
	for range 10 {
		f.pass(16 * time.Minute)
	}
	if n := f.notifier.count(); n != reconcile.MaxDeliveryAttempts {
		t.Errorf("attempts = %d, want %d", n, reconcile.MaxDeliveryAttempts)
	}
	d := f.delivery(a.Metadata.ID, 0)
	if d.State != resource.DeliveryFailed || d.Attempts != reconcile.MaxDeliveryAttempts || d.LastError != "hooks.example.com: 503 unavailable" {
		t.Errorf("delivery = %+v, want failed after %d with the cleaned error", d, reconcile.MaxDeliveryAttempts)
	}
}

func TestDelivery_BackoffWaits(t *testing.T) {
	f := newAlertFixture(t)
	web := f.component("web")
	r := f.rule("cpu", web, f.channel)
	a := f.openWith(r, web, f.pending(f.channel, resource.DeliveryEventFiring))
	f.notifier.answers = []notify.Result{{Err: &notify.SendError{Host: "h", Retryable: true, Msg: "timeout"}}}
	f.d.Enqueue(a.Metadata.ID)
	f.pass(0)
	f.pass(5 * time.Second) // before the 10 s backoff
	if n := f.notifier.count(); n != 1 {
		t.Fatalf("sent %d before the backoff ended, want 1", n)
	}
	f.pass(6 * time.Second)
	if n := f.notifier.count(); n != 2 {
		t.Errorf("sent %d after the backoff, want 2", n)
	}
}

func TestDelivery_A400IsNotRetried(t *testing.T) {
	f := newAlertFixture(t)
	web := f.component("web")
	r := f.rule("cpu", web, f.channel)
	a := f.openWith(r, web, f.pending(f.channel, resource.DeliveryEventFiring))
	f.notifier.answers = []notify.Result{{Err: &notify.SendError{Host: "hooks.example.com", Status: 400, Msg: "bad request"}}}
	f.d.Enqueue(a.Metadata.ID)
	f.pass(0)
	f.pass(time.Hour)
	if n := f.notifier.count(); n != 1 {
		t.Errorf("attempts = %d, want 1", n)
	}
	if d := f.delivery(a.Metadata.ID, 0); d.State != resource.DeliveryFailed || d.Attempts != 1 {
		t.Errorf("delivery = %+v, want failed after one attempt", d)
	}
}

// Ten queued for one Telegram chat go as one digest.
func TestDelivery_QueueFoldsIntoADigest(t *testing.T) {
	f := newAlertFixture(t)
	tg := f.telegram("-1001234")
	var ids []string
	for i := range 10 {
		comp := f.component(fmt.Sprintf("c%d", i))
		r := f.rule(fmt.Sprintf("r%d", i), comp, tg)
		a := f.openWith(r, comp, f.pending(tg, resource.DeliveryEventFiring))
		ids = append(ids, a.Metadata.ID)
		f.d.Enqueue(a.Metadata.ID)
	}
	f.pass(0)
	if n := f.notifier.count(); n != 1 {
		t.Fatalf("sends = %d, want one digest", n)
	}
	if m := f.notifier.sent[0]; m.Summary != "10 alerts firing, 0 resolved" || m.AlertID != "" {
		t.Errorf("digest = %+v", m)
	}
	for _, id := range ids {
		if d := f.delivery(id, 0); d.State != resource.DeliverySent {
			t.Errorf("alert %s delivery = %+v, want sent in the digest", id, d)
		}
	}
}

// A webhook gets one body per Alert, at most one a second.
func TestDelivery_WebhookIsRateLimited(t *testing.T) {
	f := newAlertFixture(t)
	for i := range 3 {
		comp := f.component(fmt.Sprintf("c%d", i))
		r := f.rule(fmt.Sprintf("r%d", i), comp, f.channel)
		f.d.Enqueue(f.openWith(r, comp, f.pending(f.channel, resource.DeliveryEventFiring)).Metadata.ID)
	}
	f.pass(0)
	f.pass(100 * time.Millisecond)
	if n := f.notifier.count(); n != 1 {
		t.Fatalf("sends within a second = %d, want 1", n)
	}
	f.pass(time.Second)
	f.pass(time.Second)
	if n := f.notifier.count(); n != 3 {
		t.Errorf("sends after 2 more seconds = %d, want 3", n)
	}
}

func TestDelivery_SilenceRecordsSilenced(t *testing.T) {
	f := newAlertFixture(t)
	web := f.component("web")
	r := f.rule("cpu", web, f.channel)
	r.Status.Silence = &resource.AlertSilence{By: "person:ana", Reason: "deploying", Until: f.now.Add(time.Hour)}
	r = mustV(f.rules.Update(f.ctx, r))
	a := f.openWith(r, web, f.pending(f.channel, resource.DeliveryEventFiring))
	f.d.Enqueue(a.Metadata.ID)
	f.pass(0)
	if n := f.notifier.count(); n != 0 {
		t.Errorf("a silenced rule sent %d", n)
	}
	if d := f.delivery(a.Metadata.ID, 0); d.State != resource.DeliverySilenced || d.Attempts != 0 {
		t.Errorf("delivery = %+v, want silenced", d)
	}
}

func TestDelivery_BelowMinSeverityIsRecorded(t *testing.T) {
	f := newAlertFixture(t)
	ch := mustV(f.channels.Get(f.ctx, f.channel))
	ch.Spec.MinSeverity = resource.SeverityCritical
	mustV(f.channels.Update(f.ctx, ch))
	web := f.component("web")
	r := f.rule("cpu", web, f.channel)
	r.Spec.Severity = resource.SeverityWarning
	r = mustV(f.rules.Update(f.ctx, r))
	a := f.openWith(r, web, f.pending(f.channel, resource.DeliveryEventFiring))
	f.d.Enqueue(a.Metadata.ID)
	f.pass(0)
	if d := f.delivery(a.Metadata.ID, 0); d.State != resource.DeliveryBelowMinSeverity || f.notifier.count() != 0 {
		t.Errorf("delivery = %+v, sends %d; want belowMinSeverity, none", d, f.notifier.count())
	}
}

// After a restart the evaluator resumes the open Alert, not a new one.
func TestAlerts_RestartResumesOpenAlert(t *testing.T) {
	f := newAlertFixture(t)
	web := f.component("web")
	r := f.rule("cpu", web, f.channel)
	f.provider.set(web, 5)
	f.tick(0)
	f.tick(time.Minute)
	f.restart()
	f.tick(time.Minute)
	f.tick(time.Minute)
	if got := f.alertsOf(r.Metadata.ID); len(got) != 1 || got[0].Status.State != resource.AlertFiring {
		t.Errorf("after a restart: %d Alerts, want the one still firing", len(got))
	}
}

// Retention removes resolved Alerts older than 30 days, with one audit row.
func TestAlerts_RetentionPurgesOldResolved(t *testing.T) {
	f := newAlertFixture(t)
	web := f.component("web")
	r := f.rule("cpu", web, f.channel)
	old := f.now.Add(-31 * 24 * time.Hour)
	recent := f.now.Add(-time.Hour)
	for _, at := range []time.Time{old, old, recent} {
		a := f.openWith(r, web)
		a.Status.State, a.Status.ResolvedAt, a.Status.ResolvedReason = resource.AlertResolved, &at, resource.ResolvedClear
		mustV(f.alerts.Update(f.ctx, a))
	}
	f.openWith(r, web) // firing: never purged
	n, err := sqliteRetention(f)(f.ctx, f.now.Add(-reconcile.AlertRetention), reconcile.AlertRetentionBatch)
	if err != nil || n != 2 {
		t.Fatalf("purged %d (%v), want 2", n, err)
	}
	if left := len(f.alertsOf(r.Metadata.ID)); left != 2 {
		t.Errorf("left %d Alerts, want the recent and the firing one", left)
	}
	// Like every delete, it's recorded as an Approval: one for the rule,
	// decided by policy and Executed, naming both Alerts.
	approvals := mustV(sqlite.NewStore[resource.ApprovalSpec, resource.ApprovalStatus](f.db, resource.KindApproval).List(f.ctx, store.ListOptions{}))
	if len(approvals) != 1 {
		t.Fatalf("approvals = %d, want 1", len(approvals))
	}
	ap := approvals[0]
	if ap.Status.Phase != resource.ApprovalExecuted || ap.Status.Decision == nil || !ap.Status.Decision.ByPolicy || len(ap.Status.BlastRadius) != 2 {
		t.Errorf("approval = %+v", ap.Status)
	}
	if len(ap.Status.OwnerChain) == 0 || ap.Status.OwnerChain[0] != (resource.Scope{Kind: resource.KindAlertRule, ID: r.Metadata.ID}) {
		t.Errorf("owner chain = %v, want the rule first (so its readers can read it)", ap.Status.OwnerChain)
	}
	var rows int
	if err := f.db.QueryRow(`SELECT count(*) FROM audit_log WHERE kind = 'Alert' AND after IS NULL AND policy_decision LIKE 'approved: ' || ? || '%'`, ap.Metadata.ID).Scan(&rows); err != nil || rows != 2 {
		t.Errorf("per-Alert audit rows naming the Approval = %d (%v), want 2", rows, err)
	}
}

// A resolution waits while its firing to the same channel is still
// being retried: a receiver never sees "resolved" first.
func TestDelivery_ResolutionWaitsForItsFiring(t *testing.T) {
	f := newAlertFixture(t)
	web := f.component("web")
	r := f.rule("cpu", web, f.channel)
	firing := f.pending(f.channel, resource.DeliveryEventFiring)
	later := f.now.Add(time.Minute)
	firing.NextAttempt, firing.Attempts = &later, 1
	f.now = f.now.Add(time.Second)
	a := f.openWith(r, web, firing, f.pending(f.channel, resource.DeliveryEventResolved))
	f.d.Enqueue(a.Metadata.ID)
	f.pass(0)
	if n := f.notifier.count(); n != 0 {
		t.Fatalf("sent %d while the firing waits, want 0", n)
	}
	f.pass(2 * time.Minute)
	f.pass(2 * time.Second)
	if n := f.notifier.count(); n != 2 || f.notifier.sent[0].Event != notify.EventFiring || f.notifier.sent[1].Event != notify.EventResolved {
		t.Errorf("sent %d, want firing then resolved", n)
	}
}

// A backlog is purged in batches, not one batch an hour.
func TestAlerts_RetentionCatchesUp(t *testing.T) {
	f := newAlertFixture(t)
	calls := 0
	f.e, f.d = reconcile.NewAlertEvaluator(reconcile.AlertEvaluatorOptions{Stores: f.stores, Provider: f.provider,
		Installed: func(context.Context) (bool, error) { return true, nil }, Now: func() time.Time { return f.now },
		Purge: func(context.Context, time.Time, int) (int, error) {
			calls++
			if calls < 3 {
				return reconcile.AlertRetentionBatch, nil
			}
			return 7, nil
		}}, reconcile.DeliveryOptions{Notifier: f.notifier})
	f.tick(0)
	if calls != 3 {
		t.Errorf("purge batches = %d, want 3 (two full, then the rest)", calls)
	}
	f.tick(time.Minute)
	if calls != 3 {
		t.Errorf("purged again within the hour: %d", calls)
	}
}
