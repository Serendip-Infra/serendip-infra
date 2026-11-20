package reconcile

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/serendip-infra/serendip-infra/core/audit"
	"github.com/serendip-infra/serendip-infra/core/notify"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store"
)

// The heartbeat loop (specs/024-alerts plan decision 15): a request to an
// outside monitor every interval, only while Serendip can alert. Absence
// is the signal, so nothing is sent when it can't.

// Beater sends one heartbeat (core/notify's Client).
type Beater interface {
	Beat(ctx context.Context, health notify.Health, spec resource.HeartbeatSpec, secrets notify.SecretReader, force bool) notify.HeartbeatResult
}

// HeartbeatStatusEvery is how often a steady heartbeat writes its
// lastSent: each write is an audit row, so a beat a minute writes only
// when its result changes, or this long after the last write.
const HeartbeatStatusEvery = 10 * time.Minute

// heartbeatTick is how often the loop checks whether a beat is due.
const heartbeatTick = 15 * time.Second

// Heartbeat results, shown as lastResult and the Sending condition's
// reason.
const (
	HeartbeatSent       = "Sent"
	HeartbeatSendFailed = "SendFailed"
)

// HeartbeatLoop sends the platform's heartbeat.
type HeartbeatLoop struct {
	beats   store.Store[resource.HeartbeatSpec, resource.HeartbeatStatus]
	beater  Beater
	health  notify.Health
	secrets notify.SecretReader
	now     func() time.Time
	// next is when the next beat is due; zero means now. Only Run's
	// goroutine touches it.
	next time.Time
}

// heartbeatRetry is how soon a failed send is tried again, when that's
// sooner than the interval.
const heartbeatRetry = time.Minute

// NewHeartbeatLoop returns the loop over beats, sending with beater.
func NewHeartbeatLoop(beats store.Store[resource.HeartbeatSpec, resource.HeartbeatStatus], beater Beater, health notify.Health, secrets notify.SecretReader) *HeartbeatLoop {
	return &HeartbeatLoop{beats: beats, beater: beater, health: health, secrets: secrets, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock makes the loop read the time from now (tests).
func (l *HeartbeatLoop) WithClock(now func() time.Time) *HeartbeatLoop {
	l.now = now
	return l
}

// Run beats until ctx ends.
func (l *HeartbeatLoop) Run(ctx context.Context) {
	ctx = audit.WithInterface(audit.WithActor(ctx, AlertsActor), InterfaceName)
	t := time.NewTicker(heartbeatTick)
	defer t.Stop()
	for {
		if err := l.Tick(ctx); err != nil && ctx.Err() == nil {
			log.Printf("reconcile: heartbeat: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick sends a beat if one is due, and records its result when it
// changed. A sent beat waits the interval; a failed one is retried within
// a minute; while paused, every tick checks again, so the first beat
// after Serendip recovers isn't held back an interval (the monitor's
// grace period may be shorter).
func (l *HeartbeatLoop) Tick(ctx context.Context) error {
	list, err := l.beats.List(ctx, store.ListOptions{})
	if err != nil || len(list) == 0 {
		return err
	}
	hb := list[0]
	now := l.now()
	if now.Before(l.next) {
		return nil
	}
	res := l.beater.Beat(ctx, l.health, hb.Spec, l.secrets, false)
	if ctx.Err() != nil {
		// Stopping: a beat cut short isn't a failure to record (T012
		// review). The sender's own timeouts bound a beat otherwise.
		return nil
	}
	switch {
	case res.Sent:
		l.next = now.Add(hb.Spec.Interval())
	case res.Paused == "":
		l.next = now.Add(min(hb.Spec.Interval(), heartbeatRetry))
	}
	for attempt := 0; attempt < 2; attempt++ {
		if !RecordHeartbeat(&hb.Status, res, now) {
			return nil
		}
		_, err = l.beats.Update(ctx, hb)
		if !errors.Is(err, store.ErrConflict) {
			break
		}
		// A person's write or test meanwhile: record on a fresh read. The
		// beat itself stays sent, so the schedule is kept.
		if hb, err = l.beats.Get(ctx, hb.Metadata.ID); err != nil {
			break
		}
	}
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
		return nil // removed, or still contended: the next change records
	}
	return err
}

// RecordHeartbeat applies one beat's result to st and reports whether
// anything worth writing changed: the result, the Sending condition, or
// a lastSent older than HeartbeatStatusEvery.
func RecordHeartbeat(st *resource.HeartbeatStatus, res notify.HeartbeatResult, now time.Time) bool {
	cond := resource.Condition{Type: resource.HeartbeatConditionSending, LastTransitionTime: now}
	var result string
	switch {
	case res.Sent:
		result = HeartbeatSent
		cond.Status, cond.Reason = resource.ConditionTrue, HeartbeatSent
	case res.Paused != "":
		result = "paused: " + res.Paused
		cond.Status, cond.Reason = resource.ConditionFalse, res.Paused
		cond.Message = "Serendip can't alert now, so nothing is sent; the monitor's own timeout raises the alarm"
	default:
		msg := "unknown error"
		if res.Err != nil {
			msg = res.Err.Error() // cleaned to the host by core/notify
		}
		result = "failed: " + msg
		cond.Status, cond.Reason, cond.Message = resource.ConditionFalse, HeartbeatSendFailed, msg
	}
	changed := st.LastResult != result
	st.LastResult = result
	if prev := findCondition(st.Conditions, cond.Type); prev == nil || prev.Status != cond.Status || prev.Reason != cond.Reason || prev.Message != cond.Message {
		// A transition is a change of status: a new failure message
		// alone keeps the time it began failing.
		if prev != nil && prev.Status == cond.Status {
			cond.LastTransitionTime = prev.LastTransitionTime
		}
		st.Conditions = upsertCondition(st.Conditions, cond)
		changed = true
	}
	if res.Sent && (changed || st.LastSent == nil || now.Sub(*st.LastSent) >= HeartbeatStatusEvery) {
		t := now
		st.LastSent = &t
		changed = true
	}
	return changed
}
