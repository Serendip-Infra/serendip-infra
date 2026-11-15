package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/serendip-infra/serendip-infra/core/audit"
	"github.com/serendip-infra/serendip-infra/core/graph"
	"github.com/serendip-infra/serendip-infra/core/notify"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store"
)

// The delivery loop (specs/024-alerts plan decision 7, cs-tools' separate
// loop): it sends the Alerts' pending deliveries through core/notify,
// retrying with backoff, at most one send a second per channel (and 20 a
// minute to a Telegram group), folding a chat channel's queue into one
// digest, and recording silences and severity routing on the delivery.
// Pending deliveries live on the Alert, so a restart resumes them.

// Delivery settings (decision 7).
const (
	MaxDeliveryAttempts = 5
	deliveryPoll        = 5 * time.Second
	channelGap          = time.Second
	telegramGroupPerMin = 20
	// digestLines bounds the Alerts a digest names.
	digestLines = 5
)

// deliveryBackoff is the wait after each failed attempt.
var deliveryBackoff = []time.Duration{10 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute}

// Notifier sends one notification (core/notify's Client).
type Notifier interface {
	Deliver(ctx context.Context, t notify.Target, m notify.Message) notify.Result
}

// DeliveryOptions wires the delivery loop.
type DeliveryOptions struct {
	Notifier Notifier
	// Secrets reads a channel's Secrets at send time.
	Secrets notify.SecretReader
	// ConsoleURL is the console's base URL, for "View in console"; empty
	// leaves the link out.
	ConsoleURL string
	// Source names the install in a notification's footer.
	Source string
}

// DeliveryLoop sends pending deliveries.
type DeliveryLoop struct {
	s      AlertStores
	writer *alertWriter
	o      DeliveryOptions
	now    func() time.Time

	mu      sync.Mutex
	queued  map[string]bool
	wake    chan struct{}
	lastRun atomic.Int64

	// Per-channel send history, in memory: a restart may send one more
	// than the rate in its first second, which the receiver's own limit
	// (honoured through Retry-After) covers.
	lastSend   map[string]time.Time
	groupSends map[string][]time.Time
}

func newDeliveryLoop(s AlertStores, w *alertWriter, o DeliveryOptions, now func() time.Time) *DeliveryLoop {
	return &DeliveryLoop{s: s, writer: w, o: o, now: now, queued: map[string]bool{}, wake: make(chan struct{}, 1),
		lastSend: map[string]time.Time{}, groupSends: map[string][]time.Time{}}
}

// Enqueue marks an Alert as having deliveries to send.
func (d *DeliveryLoop) Enqueue(alertID string) {
	d.mu.Lock()
	d.queued[alertID] = true
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// LastRun is when the loop last ran a pass (the heartbeat's health check).
func (d *DeliveryLoop) LastRun() time.Time {
	n := d.lastRun.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// Resume queues every Alert with a pending delivery: after a restart, a
// firing is sent again only if its delivery was still pending.
func (d *DeliveryLoop) Resume(ctx context.Context) error {
	all, err := d.s.Alerts.List(ctx, store.ListOptions{})
	if err != nil {
		return err
	}
	for _, a := range all {
		if slices.ContainsFunc(a.Status.Deliveries, func(x resource.AlertDelivery) bool { return x.State == resource.DeliveryPending }) {
			d.Enqueue(a.Metadata.ID)
		}
	}
	return nil
}

// Run resumes, then runs a pass whenever woken, and at least every few
// seconds, until ctx ends.
func (d *DeliveryLoop) Run(ctx context.Context) {
	ctx = audit.WithInterface(audit.WithActor(ctx, AlertsActor), InterfaceName)
	if err := d.Resume(ctx); err != nil {
		log.Printf("reconcile: alert delivery: resume: %v", err)
	}
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
		case <-t.C:
		}
		next := d.Pass(ctx)
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(next)
	}
}

// pendingItem is one pending delivery ready to send.
type pendingItem struct {
	alert resource.Alert
	d     resource.AlertDelivery
}

// itemResult is what happened to one delivery.
type itemResult struct {
	state, threadRef, lastErr string
	retryAt                   *time.Time
	attempted                 bool
}

// Pass sends what's due once and returns how soon to run again.
func (d *DeliveryLoop) Pass(ctx context.Context) time.Duration {
	now := d.now()
	defer d.lastRun.Store(now.UnixNano())
	d.mu.Lock()
	ids := make([]string, 0, len(d.queued))
	for id := range d.queued {
		ids = append(ids, id)
	}
	d.mu.Unlock()
	slices.Sort(ids)

	next := deliveryPoll
	byChannel := map[string][]pendingItem{}
	var channelOrder []string
	for _, id := range ids {
		a, err := d.s.Alerts.Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			d.dequeue(id)
			continue
		}
		if err != nil {
			log.Printf("reconcile: alert delivery: %s: %v", id, err)
			continue
		}
		left := false
		for _, x := range a.Status.Deliveries {
			if x.State != resource.DeliveryPending {
				continue
			}
			left = true
			if x.NextAttempt != nil && now.Before(*x.NextAttempt) {
				next = min(next, x.NextAttempt.Sub(now))
				continue
			}
			// A resolution waits for its firing to the same channel, so a
			// receiver never sees "resolved" before "firing".
			if x.Event == resource.DeliveryEventResolved && slices.ContainsFunc(a.Status.Deliveries, func(f resource.AlertDelivery) bool {
				return f.Event == resource.DeliveryEventFiring && f.ChannelID == x.ChannelID && f.State == resource.DeliveryPending
			}) {
				continue
			}
			if _, ok := byChannel[x.ChannelID]; !ok {
				channelOrder = append(channelOrder, x.ChannelID)
			}
			byChannel[x.ChannelID] = append(byChannel[x.ChannelID], pendingItem{alert: a, d: x})
		}
		if !left {
			d.dequeue(id)
		}
	}

	rules := map[string]*resource.AlertRule{}
	for _, chID := range channelOrder {
		items := byChannel[chID]
		results := make([]itemResult, len(items))
		ch, err := d.s.Channels.Get(ctx, chID)
		if errors.Is(err, store.ErrNotFound) {
			for i := range results {
				results[i] = itemResult{state: resource.DeliveryFailed, lastErr: "the channel no longer exists", attempted: true}
			}
			d.record(ctx, items, results, now)
			continue
		}
		if err != nil {
			log.Printf("reconcile: alert delivery: channel %s: %v", chID, err)
			continue
		}
		// Silences and severity routing first: they send nothing.
		var send []int
		for i, it := range items {
			r, ok := d.rule(ctx, rules, it.alert.Spec.RuleID)
			switch {
			case !ok:
				continue
			case r != nil && r.Status.Silence != nil && now.Before(r.Status.Silence.Until):
				results[i] = itemResult{state: resource.DeliverySilenced, attempted: true}
			case notify.Route(it.alert.Spec.Severity, it.d.Event, ch.Spec) != "":
				results[i] = itemResult{state: resource.DeliveryBelowMinSeverity, attempted: true}
			default:
				send = append(send, i)
			}
		}
		if len(send) > 0 {
			if wait := d.rateWait(ch, now); wait > 0 {
				next = min(next, wait)
			} else {
				target, terr := d.target(ctx, ch)
				var res notify.Result
				var group []int
				switch {
				case terr != nil:
					res = notify.Result{Err: &notify.SendError{Host: "channel", Msg: terr.Error()}}
					group = send
				case len(send) > 1 && isChat(ch.Spec):
					group = send
					res = d.o.Notifier.Deliver(ctx, target, d.digest(items, send))
				default:
					group = send[:1]
					res = d.o.Notifier.Deliver(ctx, target, d.message(ctx, items[send[0]]))
				}
				// Sends can take seconds: the rate and the backoff run from
				// when this one ended.
				after := d.now()
				d.sent(ch, after)
				for _, i := range group {
					results[i] = outcome(items[i].d, res, after)
				}
				if len(send) > len(group) {
					next = min(next, channelGap)
				}
			}
		}
		d.record(ctx, items, results, now)
	}
	return max(next, 100*time.Millisecond)
}

func (d *DeliveryLoop) dequeue(id string) {
	d.mu.Lock()
	delete(d.queued, id)
	d.mu.Unlock()
}

// rule reads an Alert's rule, for its silence. ok is false when it
// couldn't be read: the item waits rather than be sent past a silence.
func (d *DeliveryLoop) rule(ctx context.Context, cache map[string]*resource.AlertRule, id string) (r *resource.AlertRule, ok bool) {
	if r, hit := cache[id]; hit {
		return r, true
	}
	got, err := d.s.Rules.Get(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		cache[id] = nil
		return nil, true
	case err != nil:
		log.Printf("reconcile: alert delivery: rule %s: %v", id, err)
		return nil, false
	}
	cache[id] = &got
	return &got, true
}

// isChat: chat channels fold a queue into a digest; a webhook's receiver
// expects one serendip.alert.v1 body per Alert.
func isChat(ch resource.NotificationChannelSpec) bool {
	return ch.Type == resource.ChannelTelegram || ch.Type == resource.ChannelEmail
}

// rateWait is how long until ch may be sent to again.
func (d *DeliveryLoop) rateWait(ch resource.NotificationChannel, now time.Time) time.Duration {
	id := ch.Metadata.ID
	wait := time.Duration(0)
	if last, ok := d.lastSend[id]; ok {
		wait = max(wait, last.Add(channelGap).Sub(now))
	}
	if isTelegramGroup(ch.Spec) {
		recent := d.groupSends[id][:0]
		for _, t := range d.groupSends[id] {
			if now.Sub(t) < time.Minute {
				recent = append(recent, t)
			}
		}
		d.groupSends[id] = recent
		if len(recent) >= telegramGroupPerMin {
			wait = max(wait, recent[0].Add(time.Minute).Sub(now))
		}
	}
	return wait
}

func (d *DeliveryLoop) sent(ch resource.NotificationChannel, now time.Time) {
	d.lastSend[ch.Metadata.ID] = now
	if isTelegramGroup(ch.Spec) {
		d.groupSends[ch.Metadata.ID] = append(d.groupSends[ch.Metadata.ID], now)
	}
}

// isTelegramGroup: groups and channels have negative chat IDs (or a
// public @username); a person's chat is a positive ID.
func isTelegramGroup(ch resource.NotificationChannelSpec) bool {
	return ch.Telegram != nil && (strings.HasPrefix(ch.Telegram.ChatID, "-") || strings.HasPrefix(ch.Telegram.ChatID, "@"))
}

// target is ch with what it refers to.
func (d *DeliveryLoop) target(ctx context.Context, ch resource.NotificationChannel) (notify.Target, error) {
	t := notify.Target{Channel: ch.Spec, Secrets: d.o.Secrets}
	if ch.Spec.Email != nil {
		srv, err := d.s.SMTP.Get(ctx, ch.Spec.Email.SMTPServerID)
		if err != nil {
			return t, errors.New("the channel's SMTP server couldn't be read")
		}
		t.SMTP = &srv.Spec
	}
	return t, nil
}

// outcome turns a send's result into the delivery's next state.
func outcome(x resource.AlertDelivery, res notify.Result, now time.Time) itemResult {
	if res.Err == nil {
		return itemResult{state: res.State, threadRef: res.ThreadRef, attempted: true}
	}
	attempts := x.Attempts + 1
	if !res.Err.Retryable || attempts >= MaxDeliveryAttempts {
		return itemResult{state: resource.DeliveryFailed, lastErr: res.Err.Error(), attempted: true}
	}
	wait := max(deliveryBackoff[min(attempts-1, len(deliveryBackoff)-1)], res.Err.RetryAfter)
	at := now.Add(wait)
	return itemResult{state: resource.DeliveryPending, lastErr: res.Err.Error(), retryAt: &at, attempted: true}
}

// record writes each delivery's result to its Alert, matching it by
// channel, event and queue time.
func (d *DeliveryLoop) record(ctx context.Context, items []pendingItem, results []itemResult, now time.Time) {
	byAlert := map[string][]int{}
	var order []string
	for i, it := range items {
		if !results[i].attempted {
			continue
		}
		if _, ok := byAlert[it.alert.Metadata.ID]; !ok {
			order = append(order, it.alert.Metadata.ID)
		}
		byAlert[it.alert.Metadata.ID] = append(byAlert[it.alert.Metadata.ID], i)
	}
	// What was sent is recorded even as serendipd stops: a send left
	// unrecorded would be sent again after the restart.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	for _, id := range order {
		idx := byAlert[id]
		_, err := d.writer.update(ctx, id, func(a *resource.Alert) bool {
			changed := false
			for _, i := range idx {
				x, r := items[i].d, results[i]
				for k := range a.Status.Deliveries {
					cur := &a.Status.Deliveries[k]
					if cur.State != resource.DeliveryPending || cur.ChannelID != x.ChannelID || cur.Event != x.Event || !cur.At.Equal(x.At) {
						continue
					}
					cur.State, cur.NextAttempt = r.state, r.retryAt
					if r.lastErr != "" {
						cur.LastError = r.lastErr
					}
					if r.threadRef != "" {
						cur.ThreadRef = r.threadRef
					}
					if r.state != resource.DeliverySilenced && r.state != resource.DeliveryBelowMinSeverity {
						cur.Attempts++
					}
					changed = true
					break
				}
			}
			return changed
		})
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			log.Printf("reconcile: alert delivery: couldn't record Alert %s's deliveries; a sent one may be sent again: %v", id, err)
		}
	}
}

// message is one Alert's notification.
func (d *DeliveryLoop) message(ctx context.Context, it pendingItem) notify.Message {
	a := it.alert
	m := notify.Message{
		Event: it.d.Event, AlertID: a.Metadata.ID, Rule: a.Metadata.Name, Severity: a.Spec.Severity, State: a.Status.State,
		Summary: a.Spec.Summary, Source: d.o.Source, StartedAt: a.Status.StartedAt, ResolvedAt: a.Status.ResolvedAt,
		Value: a.Status.Value, Threshold: a.Status.Threshold,
	}
	if d.o.ConsoleURL != "" {
		m.ConsoleURL = strings.TrimRight(d.o.ConsoleURL, "/") + "/alerts/" + a.Metadata.ID
	}
	d.names(ctx, a, &m)
	if r, err := d.s.Rules.Get(ctx, a.Spec.RuleID); err == nil {
		lasted := time.Duration(0)
		if a.Status.ResolvedAt != nil {
			lasted = a.Status.ResolvedAt.Sub(a.Status.StartedAt)
		}
		m.Detail = conditionDetail(r.Spec.Condition, it.d.Event, a.Status.Value, a.Status.Threshold, lasted)
	}
	if it.d.Event == resource.DeliveryEventResolved {
		// The resolution replies to the firing in the same thread.
		for _, x := range a.Status.Deliveries {
			if x.ChannelID == it.d.ChannelID && x.Event == resource.DeliveryEventFiring && x.ThreadRef != "" {
				m.ThreadRef = x.ThreadRef
			}
		}
	}
	return m
}

// names fills in the Component, Environment, Application and Project.
func (d *DeliveryLoop) names(ctx context.Context, a resource.Alert, m *notify.Message) {
	if a.Spec.ComponentID != "" {
		if c, err := d.s.Components.Get(ctx, a.Spec.ComponentID); err == nil {
			m.Component = c.Metadata.Name
			for _, o := range c.Metadata.OwnerRefs {
				if o.Kind == resource.KindApplication && d.s.Applications != nil {
					if app, err := d.s.Applications.Get(ctx, o.ID); err == nil {
						m.Application = app.Metadata.Name
					}
				}
			}
		}
	}
	if a.Spec.EnvironmentID != "" {
		if env, err := d.s.Environments.Get(ctx, a.Spec.EnvironmentID); err == nil {
			m.Environment = env.Metadata.Name
		}
	}
	if d.s.Owners != nil && d.s.Projects != nil {
		if p, err := projectOf(ctx, d.s.Owners, graph.ResourceRef{Kind: resource.KindAlertRule, ID: a.Spec.RuleID}); err == nil && p != "" {
			if proj, err := d.s.Projects.Get(ctx, p); err == nil {
				m.Project = proj.Metadata.Name
			}
		}
	}
}

// digest folds a chat channel's queue into one message: "3 alerts
// firing, 1 resolved", naming the first few.
func (d *DeliveryLoop) digest(items []pendingItem, idx []int) notify.Message {
	firing, resolved := 0, 0
	sev := resource.SeverityInfo
	var lines []string
	for _, i := range idx {
		it := items[i]
		if it.d.Event == resource.DeliveryEventFiring {
			firing++
		} else {
			resolved++
		}
		if it.alert.Spec.Severity.Meets(sev) {
			sev = it.alert.Spec.Severity
		}
		if len(lines) < digestLines {
			lines = append(lines, it.d.Event+": "+it.alert.Spec.Summary)
		}
	}
	if more := len(idx) - len(lines); more > 0 {
		lines = append(lines, fmt.Sprintf("and %d more", more))
	}
	event, state := resource.DeliveryEventFiring, resource.AlertFiring
	if firing == 0 {
		event, state = resource.DeliveryEventResolved, resource.AlertResolved
	}
	m := notify.Message{
		Event: event, State: state, Severity: sev, Source: d.o.Source, StartedAt: d.now(),
		Summary: fmt.Sprintf("%d alerts firing, %d resolved", firing, resolved),
		Detail:  strings.Join(lines, "; "),
	}
	if d.o.ConsoleURL != "" {
		m.ConsoleURL = strings.TrimRight(d.o.ConsoleURL, "/") + "/alerts"
	}
	return m
}
