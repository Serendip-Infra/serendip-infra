package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"time"

	"github.com/serendip-infra/serendip-infra/core/audit"
	"github.com/serendip-infra/serendip-infra/core/notify"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store"
)

// Notification channels, SMTP servers and the heartbeat (specs/024-alerts
// plan decisions 6, 8, 12 and 15): the generic CRUD with views that show
// a Secret only as secretSet, the destination check at save, test sends,
// and the heartbeat singleton's routes.

// Notifier is what the API sends and checks destinations with
// (core/notify's Client): the same checked sender as the delivery loop.
type Notifier interface {
	Deliver(ctx context.Context, t notify.Target, m notify.Message) notify.Result
	Beat(ctx context.Context, health notify.Health, spec resource.HeartbeatSpec, secrets notify.SecretReader, force bool) notify.HeartbeatResult
	CheckURL(ctx context.Context, rawURL string, allowPrivate, plainHTTP bool) error
	CheckHost(ctx context.Context, host string, allowPrivate bool) error
	FindTelegramChats(ctx context.Context, token string) ([]notify.TelegramChat, error)
}

// WithNotifier lets the API test channels and the heartbeat, and check a
// destination's addresses when it's saved. secrets reads a Secret's value
// at send time. Without it, test sends answer 503 and a destination is
// checked only by its shape until the first send.
func WithNotifier(n Notifier, secrets notify.SecretReader) ServerOption {
	return func(o *serverOptions) { o.notifier, o.notifySecrets = n, secrets }
}

// testTimeout bounds a test send: an SMTP exchange can take 30 s.
const testTimeout = 40 * time.Second

// maxTestSends is how many test sends run at once; more answer 429, so a
// script can't hold the store's one connection and many sockets open.
const maxTestSends = 2

type notificationsHandler struct {
	channels     store.Store[resource.NotificationChannelSpec, resource.NotificationChannelStatus]
	smtp         store.Store[resource.SMTPServerSpec, resource.SMTPServerStatus]
	heartbeats   store.Store[resource.HeartbeatSpec, resource.HeartbeatStatus]
	secrets      store.Store[resource.SecretSpec, resource.SecretStatus]
	notifier     Notifier
	read         notify.SecretReader
	recordDenied dbForAudit
	now          func() time.Time
	// sending holds a slot per test send in flight.
	sending chan struct{}
}

// sendSlot takes a test-send slot, or answers 429 and reports false.
// The caller releases a taken slot with <-h.sending.
func (h *notificationsHandler) sendSlot(w http.ResponseWriter, r *http.Request) bool {
	select {
	case h.sending <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", "5")
		WriteProblem(w, http.StatusTooManyRequests, "too many test sends", "another test send is running; try again in a few seconds", audit.RequestIDFrom(r.Context()))
		return false
	}
}

// channelAuthorize is a channel's checks beyond its route's rule, then its
// destination's address check: decided first, so nobody the checks refuse
// can use a save to ask what a name resolves to.
func (h *notificationsHandler) channelAuthorize(ctx context.Context, r resource.NotificationChannel, current *resource.NotificationChannel) (context.Context, error) {
	ctx, err := channelChecks(ctx, r, current)
	if err != nil {
		return ctx, err
	}
	return ctx, h.checkChannel(ctx, r.Spec)
}

// --- views ---

// channelView is a channel as a response shows it: its Secrets' IDs with
// whether each holds a value, never the value (decision 8).
type channelView struct {
	resource.NotificationChannel
	SecretSet map[string]bool `json:"secretSet,omitempty"`
}

// platformChannelView is a platform default channel shown to a caller
// who may name it in a rule but not read it: its settings are hidden
// (decision 9).
type platformChannelView struct {
	Metadata        resource.Metadata `json:"metadata"`
	Spec            platformSpec      `json:"spec"`
	PlatformChannel bool              `json:"platformChannel"`
}

type platformSpec struct {
	Type        string            `json:"type"`
	MinSeverity resource.Severity `json:"minSeverity,omitempty"`
	Default     bool              `json:"default"`
}

type heartbeatView struct {
	resource.Heartbeat
	SecretSet map[string]bool `json:"secretSet,omitempty"`
}

type smtpView struct {
	resource.SMTPServer
	SecretSet map[string]bool `json:"secretSet,omitempty"`
}

// secretSet reports whether a Secret exists and has had a value written.
func (h *notificationsHandler) secretSet(ctx context.Context, id string) (bool, error) {
	s, err := h.secrets.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return s.Status.LastSyncedAt != nil, nil
}

// secretSets is secretSet for each named field whose ID isn't empty.
func (h *notificationsHandler) secretSets(ctx context.Context, fields map[string]string) (map[string]bool, error) {
	out := map[string]bool{}
	for f, id := range fields {
		if id == "" {
			continue
		}
		ok, err := h.secretSet(ctx, id)
		if err != nil {
			return nil, err
		}
		out[f] = ok
	}
	return out, nil
}

func (h *notificationsHandler) channelView(ctx context.Context, ch resource.NotificationChannel) (any, error) {
	fields := map[string]string{}
	switch {
	case ch.Spec.Telegram != nil:
		fields["botTokenSecretId"] = ch.Spec.Telegram.BotTokenSecretID
	case ch.Spec.Webhook != nil:
		fields["urlSecretId"] = ch.Spec.Webhook.URLSecretID
		fields["signingSecretId"] = ch.Spec.Webhook.SigningSecretID
	}
	set, err := h.secretSets(ctx, fields)
	if err != nil {
		return nil, err
	}
	return channelView{NotificationChannel: ch, SecretSet: set}, nil
}

func (h *notificationsHandler) smtpView(ctx context.Context, s resource.SMTPServer) (any, error) {
	set, err := h.secretSets(ctx, map[string]string{"usernameSecretId": s.Spec.UsernameSecretID, "passwordSecretId": s.Spec.PasswordSecretID})
	if err != nil {
		return nil, err
	}
	return smtpView{SMTPServer: s, SecretSet: set}, nil
}

func (h *notificationsHandler) heartbeatView(ctx context.Context, hb resource.Heartbeat) (any, error) {
	set, err := h.secretSets(ctx, map[string]string{"urlSecretId": hb.Spec.URLSecretID})
	if err != nil {
		return nil, err
	}
	return heartbeatView{Heartbeat: hb, SecretSet: set}, nil
}

// listChannels is the channels the caller may read, and the platform
// default channels it may not, with their settings hidden: any principal
// who may write a rule may name those (decision 9).
func (h *notificationsHandler) listChannels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	all, err := h.channels.List(ctx, listOptions(r))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	readable, err := filterReadable(ctx, all)
	if err != nil {
		WriteProblem(w, http.StatusInternalServerError, "authorization check failed", "the list couldn't be filtered; retry later", audit.RequestIDFrom(ctx))
		return
	}
	full := map[string]bool{}
	for _, ch := range readable {
		full[ch.Metadata.ID] = true
	}
	out := make([]any, 0, len(all))
	for _, ch := range all {
		switch {
		case full[ch.Metadata.ID]:
			v, err := h.channelView(ctx, ch)
			if err != nil {
				writeStoreErr(w, r, err)
				return
			}
			out = append(out, v)
		case ch.Spec.Default && len(ch.Metadata.OwnerRefs) == 0:
			out = append(out, platformChannelView{
				Metadata:        resource.Metadata{ID: ch.Metadata.ID, Name: ch.Metadata.Name},
				Spec:            platformSpec{Type: ch.Spec.Type, MinSeverity: ch.Spec.MinSeverity, Default: true},
				PlatformChannel: true,
			})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// --- destination checks at save ---

// refusedDestination turns the sender's address refusal into a 400 with
// its reason. Other failures (a name that doesn't resolve yet) are left
// to the send, which checks again every time.
func refusedDestination(field string, err error) error {
	if err == nil || !errors.Is(err, notify.ErrRefusedAddress) {
		return nil
	}
	return fmt.Errorf("%w: %s: destination refused: %v", resource.ErrValidation, field, err)
}

// checkChannel checks a webhook channel's destination with the sender's
// address rules. A Telegram channel's host is fixed, and an email
// channel's is its SMTP server's, checked when that's saved.
func (h *notificationsHandler) checkChannel(ctx context.Context, s resource.NotificationChannelSpec) error {
	if h.notifier == nil || s.Webhook == nil {
		return nil
	}
	allow, plain := s.AllowPrivateDestination != nil, s.AllowPlainHTTP != nil
	if s.Webhook.URL != "" {
		return refusedDestination("webhook.url", h.notifier.CheckURL(ctx, s.Webhook.URL, allow, plain))
	}
	if s.Webhook.Host != "" {
		return refusedDestination("webhook.host", h.notifier.CheckHost(ctx, s.Webhook.Host, allow))
	}
	return nil
}

// checkSMTP checks an SMTP server's host. It never takes an allowance.
func (h *notificationsHandler) checkSMTP(ctx context.Context, _ string, s resource.SMTPServerSpec) error {
	if h.notifier == nil || s.Host == "" {
		return nil
	}
	return refusedDestination("host", h.notifier.CheckHost(ctx, s.Host, false))
}

// --- test sends ---

// testMessage is what a test send carries: plainly a test, at info, so
// it reaches every channel whatever its minimum severity.
func testMessage(channelName string, now time.Time) notify.Message {
	return notify.Message{
		Event: notify.EventTest, Severity: resource.SeverityInfo, State: notify.EventTest,
		Rule: "test", Summary: "Test notification from Serendip",
		Detail: "If you can read this, the channel " + channelName + " works.",
		Source: "Serendip", StartedAt: now,
	}
}

// testChannel sends a test notification and records the result on the
// channel's status (its audit row is the test send's record).
func (h *notificationsHandler) testChannel(w http.ResponseWriter, r *http.Request) {
	if h.notifier == nil {
		WriteProblem(w, http.StatusServiceUnavailable, "sending not configured", "this server has no notification sender (no cluster is configured)", audit.RequestIDFrom(r.Context()))
		return
	}
	if !h.sendSlot(w, r) {
		return
	}
	defer func() { <-h.sending }()
	ch, err := h.channels.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	t := notify.Target{Channel: ch.Spec, Secrets: h.read}
	if e := ch.Spec.Email; e != nil {
		srv, err := h.smtp.Get(r.Context(), e.SMTPServerID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			writeStoreErr(w, r, err)
			return
		}
		if err == nil {
			t.SMTP = &srv.Spec
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), testTimeout)
	defer cancel()
	now := h.now()
	res := h.notifier.Deliver(ctx, t, testMessage(ch.Metadata.Name, now))
	result := "sent"
	if res.Err != nil {
		result = "failed: " + res.Err.Error()
	}
	// Recorded with the request's context: a slow receiver mustn't cost
	// the record. The send has happened either way, so its answer stands.
	if err := h.recordTest(r.Context(), ch.Metadata.ID, now, result); err != nil {
		log.Printf("api: record channel %s's test send (request %s): %v", ch.Metadata.ID, audit.RequestIDFrom(r.Context()), err)
	}
	answerSend(w, r, res.Err)
}

func (h *notificationsHandler) recordTest(ctx context.Context, id string, at time.Time, result string) error {
	for attempt := 0; ; attempt++ {
		ch, err := h.channels.Get(ctx, id)
		if err != nil {
			return err
		}
		t := at.UTC()
		ch.Status.LastTest, ch.Status.LastTestResult = &t, result
		_, err = h.channels.Update(ctx, ch)
		if errors.Is(err, store.ErrConflict) && attempt < 2 {
			continue
		}
		return err
	}
}

// findTelegramChats is the console's "Find chat" step (plan decision 14):
// one getUpdates call with the bot token held by Secret {id}, answering
// the chats the bot has seen a message from. The route's rule is the
// Secret's value read, since the token is read for the caller; the token
// itself is never answered.
func (h *notificationsHandler) findTelegramChats(w http.ResponseWriter, r *http.Request) {
	reqID := audit.RequestIDFrom(r.Context())
	if h.notifier == nil || h.read == nil {
		WriteProblem(w, http.StatusServiceUnavailable, "sending not configured", "this server has no notification sender (no cluster is configured)", reqID)
		return
	}
	id := r.PathValue("id")
	set, err := h.secretSet(r.Context(), id)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	if !set {
		if _, err := h.secrets.Get(r.Context(), id); err != nil {
			writeStoreErr(w, r, err)
			return
		}
		WriteProblem(w, http.StatusConflict, "no value", "the Secret has no value yet: write the bot token to it first", reqID)
		return
	}
	if !h.sendSlot(w, r) {
		return
	}
	defer func() { <-h.sending }()
	ctx, cancel := context.WithTimeout(r.Context(), testTimeout)
	defer cancel()
	token, err := h.read(ctx, id)
	if err != nil {
		log.Printf("api: read Secret %s for Find chat (request %s): %v", id, reqID, err)
		WriteProblem(w, http.StatusServiceUnavailable, "secret unavailable", "the Secret's value couldn't be read", reqID)
		return
	}
	chats, err := h.notifier.FindTelegramChats(ctx, token)
	if err != nil {
		// core/notify's error, cleaned down to the host and Telegram's reason.
		WriteProblem(w, http.StatusBadGateway, "Telegram refused", err.Error(), reqID)
		return
	}
	if chats == nil {
		chats = []notify.TelegramChat{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"chats": chats})
}

// answerSend answers a test send: 200 when sent, 422 when the address
// check refused the destination, 502 when the receiver failed. The error
// is core/notify's, cleaned down to the host.
func answerSend(w http.ResponseWriter, r *http.Request, se *notify.SendError) {
	reqID := audit.RequestIDFrom(r.Context())
	switch {
	case se == nil:
		writeJSON(w, http.StatusOK, map[string]string{"result": "sent"})
	case se.Refused():
		WriteProblem(w, http.StatusUnprocessableEntity, "destination refused", se.Error(), reqID)
	default:
		WriteProblem(w, http.StatusBadGateway, "test send failed", se.Error(), reqID)
	}
}

// --- the heartbeat ---

func (h *notificationsHandler) singleton(ctx context.Context) (*resource.Heartbeat, error) {
	list, err := h.heartbeats.List(ctx, store.ListOptions{})
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

func (h *notificationsHandler) getHeartbeat(w http.ResponseWriter, r *http.Request) {
	hb, err := h.singleton(r.Context())
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	if hb == nil {
		WriteProblem(w, http.StatusNotFound, "not found", "no heartbeat is set", audit.RequestIDFrom(r.Context()))
		return
	}
	h.writeHeartbeat(w, r, http.StatusOK, *hb)
}

func (h *notificationsHandler) writeHeartbeat(w http.ResponseWriter, r *http.Request, status int, hb resource.Heartbeat) {
	v, err := h.heartbeatView(r.Context(), hb)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	writeJSON(w, status, v)
}

// putHeartbeat sets the singleton: created when there's none, replaced
// otherwise (with a conflict check when the body names a version). Its
// status stays the server's.
func (h *notificationsHandler) putHeartbeat(w http.ResponseWriter, r *http.Request) {
	reqID := audit.RequestIDFrom(r.Context())
	var hb resource.Heartbeat
	if !decodeBody(w, r, &hb) {
		return
	}
	hb.Status = resource.HeartbeatStatus{}
	if hb.Metadata.Name == "" {
		hb.Metadata.Name = "heartbeat"
	}
	current, err := h.singleton(r.Context())
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	// Decided before the body is validated, as every route decides before
	// it answers about what's there.
	ctx, err := heartbeatChecks(r.Context(), hb, current)
	if err != nil {
		var rf *deployRefusal
		if errors.As(err, &rf) {
			auditRefusal(r, rf, resource.KindHeartbeat, h.recordDenied)
			WriteProblem(w, rf.status, rf.title, rf.detail, reqID)
			return
		}
		writeStoreErr(w, r, err)
		return
	}
	if err := resource.ValidateHeartbeat(hb.Metadata, hb.Spec); err != nil {
		WriteProblem(w, http.StatusBadRequest, "validation failed", err.Error(), reqID)
		return
	}
	hb.Metadata.Relationships = resource.HeartbeatRelationships(hb.Spec)
	if h.notifier != nil {
		if err := refusedDestination("host", h.notifier.CheckHost(ctx, hb.Spec.Host, hb.Spec.AllowPrivateDestination != nil)); err != nil {
			WriteProblem(w, http.StatusBadRequest, "validation failed", err.Error(), reqID)
			return
		}
	}
	if current == nil {
		hb.Metadata.ID = ""
		created, err := h.heartbeats.Create(ctx, hb)
		if err != nil {
			writeStoreErr(w, r, err)
			return
		}
		h.writeHeartbeat(w, r, http.StatusCreated, created)
		return
	}
	// Without a version, "set" replaces whatever is there: a status write
	// meanwhile (a beat, a test) is retried on a fresh read, as long as the
	// spec the checks compared with is still the one stored.
	checkVersion := hb.Metadata.ResourceVersion != 0
	hb.Metadata.ID = current.Metadata.ID
	for attempt := 0; ; attempt++ {
		if !checkVersion {
			hb.Metadata.ResourceVersion = current.Metadata.ResourceVersion
		}
		hb.Status = current.Status
		updated, err := h.heartbeats.Update(ctx, hb)
		if errors.Is(err, store.ErrConflict) && !checkVersion && attempt < 2 {
			fresh, ferr := h.heartbeats.Get(ctx, current.Metadata.ID)
			if ferr == nil && reflect.DeepEqual(fresh.Spec, current.Spec) {
				current = &fresh
				continue
			}
		}
		if err != nil {
			writeStoreErr(w, r, err)
			return
		}
		h.writeHeartbeat(w, r, http.StatusOK, updated)
		return
	}
}

// heartbeatChecks are the heartbeat's checks beyond its route's rule (at
// "/"): naming its URL's Secret needs reading the value, as a channel's
// does, and an allowance, or moving a heartbeat that has one, needs the
// Owner-only allowance action at "/".
func heartbeatChecks(ctx context.Context, hb resource.Heartbeat, current *resource.Heartbeat) (context.Context, error) {
	s := hb.Spec
	var was resource.HeartbeatSpec
	if current != nil {
		was = current.Spec
	}
	if current == nil || s.URLSecretID != was.URLSecretID || s.Host != was.Host {
		at := resource.Scope{Kind: resource.KindSecret, ID: s.URLSecretID}
		var ok bool
		var err error
		ctx, ok, err = allowedNoted(ctx, secretValueRead, at)
		if err != nil {
			return ctx, err
		}
		if !ok {
			return ctx, deployRefuse(http.StatusForbidden, "forbidden",
				"missing %s at %s: the heartbeat sends to the URL its Secret holds, so naming one needs reading its value", secretValueRead.Name, at)
		}
	}
	if s.AllowPrivateDestination == nil {
		return ctx, nil
	}
	moved := current != nil && (s.Host != was.Host || s.URLSecretID != was.URLSecretID)
	if current == nil || grants(s.AllowPrivateDestination, was.AllowPrivateDestination) || moved {
		return allowancesAt(ctx, []resource.Scope{resource.RootScope})
	}
	return ctx, nil
}

func (h *notificationsHandler) deleteHeartbeat(w http.ResponseWriter, r *http.Request) {
	hb, err := h.singleton(r.Context())
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	if hb == nil {
		WriteProblem(w, http.StatusNotFound, "not found", "no heartbeat is set", audit.RequestIDFrom(r.Context()))
		return
	}
	if err := h.heartbeats.Delete(r.Context(), hb.Metadata.ID, false); err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testHeartbeat sends one heartbeat now, whatever Serendip's health (a
// person checking the monitor receives it), and records the result.
func (h *notificationsHandler) testHeartbeat(w http.ResponseWriter, r *http.Request) {
	reqID := audit.RequestIDFrom(r.Context())
	if h.notifier == nil {
		WriteProblem(w, http.StatusServiceUnavailable, "sending not configured", "this server has no notification sender (no cluster is configured)", reqID)
		return
	}
	hb, err := h.singleton(r.Context())
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	if hb == nil {
		WriteProblem(w, http.StatusNotFound, "not found", "no heartbeat is set", reqID)
		return
	}
	if !h.sendSlot(w, r) {
		return
	}
	defer func() { <-h.sending }()
	ctx, cancel := context.WithTimeout(r.Context(), testTimeout)
	defer cancel()
	res := h.notifier.Beat(ctx, notify.Health{}, hb.Spec, h.read, true)
	// The send has happened either way, so its answer stands.
	if err := h.recordHeartbeatTest(r.Context(), hb.Metadata.ID, res); err != nil {
		log.Printf("api: record the heartbeat test (request %s): %v", reqID, err)
	}
	answerSend(w, r, res.Err)
}

// recordHeartbeatTest writes a test beat's result, re-reading on a
// conflict with the heartbeat loop. It's written even when unchanged: its
// audit row records the test.
func (h *notificationsHandler) recordHeartbeatTest(ctx context.Context, id string, res notify.HeartbeatResult) error {
	now := h.now()
	for attempt := 0; ; attempt++ {
		hb, err := h.heartbeats.Get(ctx, id)
		if err != nil {
			return err
		}
		reconcile.RecordHeartbeat(&hb.Status, res, now)
		_, err = h.heartbeats.Update(ctx, hb)
		if errors.Is(err, store.ErrConflict) && attempt < 2 {
			continue
		}
		return err
	}
}

// registerChannels serves channels through the generic handler, with the
// channel list of listChannels.
func (h *notificationsHandler) registerChannels(mux Mux, rh *ResourceHandler[resource.NotificationChannelSpec, resource.NotificationChannelStatus]) {
	mux.HandleFunc("POST /v1/notificationchannels", rh.Create)
	mux.HandleFunc("GET /v1/notificationchannels", h.listChannels)
	mux.HandleFunc("GET /v1/notificationchannels/{id}", rh.Get)
	mux.HandleFunc("PUT /v1/notificationchannels/{id}", rh.Update)
	mux.HandleFunc("DELETE /v1/notificationchannels/{id}", rh.Delete)
	mux.HandleFunc("POST /v1/notificationchannels/{id}/test", h.testChannel)
	mux.HandleFunc("POST /v1/secrets/{id}/telegramchats", h.findTelegramChats)
}

func (h *notificationsHandler) registerHeartbeat(mux Mux) {
	mux.HandleFunc("GET /v1/heartbeat", h.getHeartbeat)
	mux.HandleFunc("PUT /v1/heartbeat", h.putHeartbeat)
	mux.HandleFunc("DELETE /v1/heartbeat", h.deleteHeartbeat)
	mux.HandleFunc("POST /v1/heartbeat/test", h.testHeartbeat)
}
