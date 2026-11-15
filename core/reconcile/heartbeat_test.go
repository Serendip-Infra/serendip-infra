package reconcile_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/notify"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store/sqlite"
)

// fakeBeater answers each beat with next, counting the beats.
type fakeBeater struct {
	next  notify.HeartbeatResult
	beats int
}

func (f *fakeBeater) Beat(context.Context, notify.Health, resource.HeartbeatSpec, notify.SecretReader, bool) notify.HeartbeatResult {
	f.beats++
	return f.next
}

func TestHeartbeatLoop(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "hb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	beats := sqlite.NewStore[resource.HeartbeatSpec, resource.HeartbeatStatus](db, resource.KindHeartbeat).SetUniqueKey(resource.HeartbeatKey)
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	b := &fakeBeater{next: notify.HeartbeatResult{Sent: true}}
	loop := reconcile.NewHeartbeatLoop(beats, b, notify.Health{}, nil).WithClock(func() time.Time { return now })

	// Nothing set: nothing sent.
	if err := loop.Tick(ctx); err != nil || b.beats != 0 {
		t.Fatalf("no heartbeat: err %v, beats %d", err, b.beats)
	}
	hb, err := beats.Create(ctx, resource.Heartbeat{Metadata: resource.Metadata{Name: "heartbeat"},
		Spec: resource.HeartbeatSpec{URLSecretID: "s", Host: "hc-ping.com", IntervalMinutes: 2}})
	if err != nil {
		t.Fatal(err)
	}
	version := func() int64 {
		got, err := beats.Get(ctx, hb.Metadata.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.Metadata.ResourceVersion
	}
	step := func(d time.Duration) {
		t.Helper()
		now = now.Add(d)
		if err := loop.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}

	step(0)
	v1 := version()
	if b.beats != 1 || v1 == hb.Metadata.ResourceVersion {
		t.Fatalf("first beat: beats %d, written %v", b.beats, v1 != hb.Metadata.ResourceVersion)
	}
	// Within the interval: no beat.
	step(time.Minute)
	if b.beats != 1 {
		t.Errorf("beat inside the interval: %d", b.beats)
	}
	// Steady sends write nothing until lastSent is HeartbeatStatusEvery old.
	for i := 0; i < 4; i++ {
		step(2 * time.Minute)
	}
	if b.beats != 5 || version() != v1 {
		t.Errorf("steady beats: %d beats, status rewritten %v; want 5 and no write", b.beats, version() != v1)
	}
	step(2 * time.Minute)
	if version() == v1 {
		t.Error("lastSent wasn't refreshed after HeartbeatStatusEvery")
	}
	// Pausing is written at once, with its reason.
	b.next = notify.HeartbeatResult{Paused: notify.PausedEvaluator}
	step(2 * time.Minute)
	got, _ := beats.Get(ctx, hb.Metadata.ID)
	c := got.Status.Conditions
	if len(c) != 1 || c[0].Type != resource.HeartbeatConditionSending || c[0].Status != resource.ConditionFalse || c[0].Reason != notify.PausedEvaluator {
		t.Errorf("paused condition = %+v", c)
	}
	if got.Status.LastResult != "paused: "+notify.PausedEvaluator {
		t.Errorf("lastResult = %q", got.Status.LastResult)
	}
	// While paused every tick checks again, so recovery beats at once.
	n := b.beats
	step(15 * time.Second)
	if b.beats != n+1 {
		t.Errorf("a paused loop waited for its interval: %d beats, want %d", b.beats, n+1)
	}
	b.next = notify.HeartbeatResult{Sent: true}
	step(15 * time.Second)
	if b.beats != n+2 {
		t.Errorf("no beat right after recovery: %d beats, want %d", b.beats, n+2)
	}
	// A failed send is retried within a minute, not the interval.
	b.next = notify.HeartbeatResult{Err: &notify.SendError{Host: "hc-ping.com", Status: 502, Msg: "Bad Gateway"}}
	step(2 * time.Minute)
	n = b.beats
	step(30 * time.Second)
	if b.beats != n {
		t.Errorf("a failure was retried within 30 s")
	}
	step(30 * time.Second)
	if b.beats != n+1 {
		t.Errorf("a failure wasn't retried after a minute: %d beats, want %d", b.beats, n+1)
	}
}

func TestRecordHeartbeat_Failure(t *testing.T) {
	var st resource.HeartbeatStatus
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	res := notify.HeartbeatResult{Err: &notify.SendError{Host: "hc-ping.com", Status: 503, Msg: "Service Unavailable"}}
	if !reconcile.RecordHeartbeat(&st, res, now) {
		t.Fatal("a first failure isn't a change")
	}
	if st.LastSent != nil || st.Conditions[0].Reason != reconcile.HeartbeatSendFailed || st.LastResult != "failed: hc-ping.com: 503 Service Unavailable" {
		t.Errorf("status = %+v", st)
	}
	if reconcile.RecordHeartbeat(&st, res, now.Add(time.Minute)) {
		t.Error("the same failure again is a change")
	}
}
