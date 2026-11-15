package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/serendip-infra/serendip-infra/api"
	"github.com/serendip-infra/serendip-infra/core/notify"
	"github.com/serendip-infra/serendip-infra/core/policy"
	"github.com/serendip-infra/serendip-infra/core/resource"
)

// Spec 024 T007: channels, SMTP servers and the heartbeat through the API.

// fakeNotifier checks addresses with a real core/notify Client over a
// fake resolver. Its sends are recorded, not made, unless real is set:
// then Deliver is the Client's own, which is how a refused destination
// is reached (the dial-time check refuses before any packet leaves).
type fakeNotifier struct {
	*notify.Client
	mu     sync.Mutex
	hosts  map[string]netip.Addr
	real   bool
	sent   []notify.Message
	beats  int
	failed *notify.SendError
}

func newFakeNotifier() *fakeNotifier {
	f := &fakeNotifier{hosts: map[string]netip.Addr{
		"hooks.example.com": netip.MustParseAddr("93.184.216.34"),
		"lan.example":       netip.MustParseAddr("10.1.2.3"),
		"ping.example.com":  netip.MustParseAddr("93.184.216.35"),
		"smtp.example.com":  netip.MustParseAddr("93.184.216.36"),
	}}
	f.Client = notify.NewClient(notify.Options{Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if a, ok := f.hosts[host]; ok {
			return []netip.Addr{a}, nil
		}
		return nil, errors.New("no such host")
	}})
	// The cluster's own ranges are known, so an Owner's allowance works.
	f.Check.SetClusterRanges([]netip.Prefix{netip.MustParsePrefix("10.42.0.0/16"), netip.MustParsePrefix("10.43.0.0/16")})
	return f
}

func (f *fakeNotifier) resolve(host, addr string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hosts[host] = netip.MustParseAddr(addr)
}

func (f *fakeNotifier) Deliver(ctx context.Context, t notify.Target, m notify.Message) notify.Result {
	if f.real {
		return f.Client.Deliver(ctx, t, m)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	if f.failed != nil {
		return notify.Result{Err: f.failed}
	}
	return notify.Result{State: resource.DeliverySent}
}

func (f *fakeNotifier) Beat(ctx context.Context, h notify.Health, spec resource.HeartbeatSpec, s notify.SecretReader, force bool) notify.HeartbeatResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !force {
		return notify.HeartbeatResult{Paused: notify.PausedEvaluator}
	}
	f.beats++
	return notify.HeartbeatResult{Sent: true}
}

func noSecrets(context.Context, string) (string, error) {
	return "", errors.New("no Secrets in this test")
}

func newNotifyWorld(t *testing.T, grants func(w *authzWorld)) (*authzWorld, *fakeNotifier) {
	n := newFakeNotifier()
	return newAuthzWorld(t, grants, api.WithNotifier(n, noSecrets)), n
}

func webhookTo(name, project, url string, private *resource.Allowance) map[string]any {
	ch := webhookChannel(name, project, false, private)
	ch["spec"].(resource.NotificationChannelSpec).Webhook.URL = url
	return ch
}

func jsonDoc(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return doc
}

func TestNotifications_ChannelShowsNoSecretValue(t *testing.T) {
	w, _ := newNotifyWorld(t, func(w *authzWorld) {
		w.grant("tok-admin", policy.OwnerRoleID, "/")
		w.grant("tok-admin", policy.SecretsOfficerRoleID, "secret")
	})
	tg := map[string]any{
		"metadata": map[string]any{"name": "tg", "ownerRefs": []resource.OwnerRef{{Kind: resource.KindProject, ID: w.id["projA"]}}},
		"spec": resource.NotificationChannelSpec{Type: resource.ChannelTelegram,
			Telegram: &resource.TelegramSettings{ChatID: "-1001234", BotTokenSecretID: w.id["secret"]}},
	}
	id := w.mkAs("tok-admin", "/v1/notificationchannels", tg)
	path := "/v1/notificationchannels/" + id
	set := func() any {
		got := w.do("tok-admin", http.MethodGet, path, nil)
		if got.Code != http.StatusOK {
			t.Fatalf("get: %d %s", got.Code, got.Body)
		}
		return jsonDoc(t, got.Body.Bytes())["secretSet"].(map[string]any)["botTokenSecretId"]
	}
	if v := set(); v != false {
		t.Errorf("secretSet before a value is written = %v, want false", v)
	}
	const token = "123456:AAH-very-secret-bot-token"
	w.expect("tok-admin", http.MethodPost, "/v1/secrets/"+w.id["secret"]+"/value", map[string]any{"data": map[string]any{"value": token}}, http.StatusOK)
	if v := set(); v != true {
		t.Errorf("secretSet after the value is written = %v, want true", v)
	}
	for _, p := range []string{path, "/v1/notificationchannels"} {
		got := w.do("tok-admin", http.MethodGet, p, nil)
		if strings.Contains(got.Body.String(), token) || strings.Contains(got.Body.String(), "AAH-") {
			t.Errorf("GET %s shows the bot token: %s", p, got.Body)
		}
	}
}

func TestNotifications_PrivateDestinations(t *testing.T) {
	w, n := newNotifyWorld(t, func(w *authzWorld) {
		w.grant("tok-contrib", policy.ContributorRoleID, "projA")
		w.grant("tok-owner", policy.OwnerRoleID, "projA")
	})
	projA := w.id["projA"]
	lan := &resource.Allowance{Reason: "in-house receiver on the LAN"}

	// Refused at create, with the reason.
	for _, tt := range []struct{ name, url, want string }{
		{"a name resolving to a private address", "https://lan.example/hook", "10.1.2.3 is a private address"},
		{"a private IP literal", "https://192.168.1.20/hook", "192.168.1.20 is a private address"},
		{"loopback", "https://127.0.0.1/hook", "loopback"},
		{"the cluster's own range", "https://10.43.0.10/hook", "cluster's own ranges"},
	} {
		got := w.do("tok-contrib", http.MethodPost, "/v1/notificationchannels", webhookTo("c", projA, tt.url, nil))
		if got.Code != http.StatusBadRequest || !strings.Contains(got.Body.String(), tt.want) {
			t.Errorf("%s: %d %s, want 400 naming %q", tt.name, got.Code, got.Body, tt.want)
		}
	}
	// An Owner's allowance is accepted, but never for the cluster itself.
	w.mkAs("tok-owner", "/v1/notificationchannels", webhookTo("lan", projA, "https://lan.example/hook", lan))
	w.expect("tok-owner", http.MethodPost, "/v1/notificationchannels", webhookTo("svc", projA, "https://10.43.0.10/hook", lan), http.StatusBadRequest)
	// A name that doesn't resolve yet is left to the send.
	w.mkAs("tok-contrib", "/v1/notificationchannels", webhookTo("later", projA, "https://not-yet.example.com/hook", nil))

	// Refused at test, with the reason: the name now points inside.
	id := w.mkAs("tok-contrib", "/v1/notificationchannels", webhookTo("pub", projA, "https://hooks.example.com/hook", nil))
	test := "/v1/notificationchannels/" + id + "/test"
	got := w.do("tok-contrib", http.MethodPost, test, nil)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"sent"`) {
		t.Fatalf("test send: %d %s, want 200 sent", got.Code, got.Body)
	}
	if len(n.sent) != 1 || n.sent[0].Event != notify.EventTest || n.sent[0].Severity != resource.SeverityInfo {
		t.Errorf("sent %+v, want one test message at info", n.sent)
	}
	n.resolve("hooks.example.com", "10.9.9.9")
	n.real = true
	got = w.do("tok-contrib", http.MethodPost, test, nil)
	if got.Code != http.StatusUnprocessableEntity || !strings.Contains(got.Body.String(), "10.9.9.9 is a private address") {
		t.Errorf("test send to a name now resolving privately: %d %s, want 422 with the reason", got.Code, got.Body)
	}
	if strings.Contains(got.Body.String(), "/hook") {
		t.Errorf("the refusal names the URL's path: %s", got.Body)
	}
	st := jsonDoc(t, w.do("key-ci", http.MethodGet, "/v1/notificationchannels/"+id, nil).Body.Bytes())["status"].(map[string]any)
	if r, _ := st["lastTestResult"].(string); !strings.HasPrefix(r, "failed: ") || st["lastTest"] == nil {
		t.Errorf("status after a refused test = %v, want lastTest and a failed result", st)
	}
	// A Reader may not test.
	w.grant("tok-reader", policy.ReaderRoleID, "projA")
	w.expect("tok-reader", http.MethodPost, test, nil, http.StatusForbidden)
}

func TestNotifications_StatusIsTheServers(t *testing.T) {
	w, _ := newNotifyWorld(t, func(w *authzWorld) { w.grant("tok-contrib", policy.ContributorRoleID, "projA") })
	id := w.mkAs("tok-contrib", "/v1/notificationchannels", webhookTo("pub", w.id["projA"], "https://hooks.example.com/x", nil))
	path := "/v1/notificationchannels/" + id
	doc := w.current(path, "")
	doc["status"] = map[string]any{"lastTestResult": "sent (forged)"}
	w.expect("tok-contrib", http.MethodPut, path, doc, http.StatusOK)
	if st := jsonDoc(t, w.do("key-ci", http.MethodGet, path, nil).Body.Bytes())["status"].(map[string]any); st["lastTestResult"] != nil {
		t.Errorf("a PUT's status was stored: %v", st)
	}

	// A rule's silence can't be set by writing the rule.
	rule := alertRule("cpu", w.scope("web"), w.id["prod"])
	rule["status"] = map[string]any{"silence": map[string]any{"by": "me", "reason": "x", "until": "2099-01-01T00:00:00Z"}}
	rid := w.mkAs("tok-contrib", "/v1/alertrules", rule)
	rdoc := w.current("/v1/alertrules/"+rid, "")
	if st, _ := rdoc["status"].(map[string]any); st["silence"] != nil {
		t.Errorf("a create's silence was stored: %v", st)
	}
	rdoc["status"] = rule["status"]
	w.expect("tok-contrib", http.MethodPut, "/v1/alertrules/"+rid, rdoc, http.StatusOK)
	if st, _ := w.current("/v1/alertrules/"+rid, "")["status"].(map[string]any); st["silence"] != nil {
		t.Errorf("an update's silence was stored: %v", st)
	}
}

func TestNotifications_PlatformChannelsHiddenBelowRoot(t *testing.T) {
	w, _ := newNotifyWorld(t, func(w *authzWorld) { w.grant("tok-contrib", policy.ContributorRoleID, "projA") })
	def := webhookChannel("ops-default", "", true, nil)
	w.mkAs("key-ci", "/v1/notificationchannels", def)
	w.mkAs("key-ci", "/v1/notificationchannels", webhookChannel("ops-private", "", false, nil))
	w.mkAs("key-ci", "/v1/notificationchannels", webhookChannel("mine", w.id["projA"], false, nil))

	got := w.do("tok-contrib", http.MethodGet, "/v1/notificationchannels", nil)
	var items []map[string]any
	if err := json.Unmarshal(got.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	names := map[string]map[string]any{}
	for _, it := range items {
		names[it["metadata"].(map[string]any)["name"].(string)] = it
	}
	if _, ok := names["ops-private"]; ok || len(items) != 2 {
		t.Errorf("a Contributor's channels = %v, want mine and ops-default only", got.Body)
	}
	d := names["ops-default"]
	if d == nil || d["platformChannel"] != true || strings.Contains(got.Body.String(), "hooks.example.com/ops-default") {
		t.Errorf("the platform default shown below / = %v, want platformChannel and no settings", d)
	}
	if names["mine"]["spec"].(map[string]any)["webhook"] == nil {
		t.Errorf("the Project's own channel lost its settings: %v", names["mine"])
	}
	if all := w.do("key-ci", http.MethodGet, "/v1/notificationchannels", nil).Body.String(); !strings.Contains(all, "hooks.example.com/ops-default") {
		t.Errorf("a reader at / doesn't see the platform channel's settings: %s", all)
	}
}

func TestNotifications_SMTPHostChecked(t *testing.T) {
	w, _ := newNotifyWorld(t, func(w *authzWorld) {})
	smtp := func(host string) map[string]any {
		return map[string]any{"metadata": map[string]any{"name": "relay-" + host}, "spec": resource.SMTPServerSpec{
			Host: host, Port: 587, TLS: resource.SMTPStartTLS, From: "alerts@example.com",
			UsernameSecretID: w.id["secret"], PasswordSecretID: w.id["secret"]}}
	}
	got := w.do("key-ci", http.MethodPost, "/v1/smtpservers", smtp("lan.example"))
	if got.Code != http.StatusBadRequest || !strings.Contains(got.Body.String(), "private address") {
		t.Errorf("a private SMTP host: %d %s, want 400", got.Code, got.Body)
	}
	got = w.do("key-ci", http.MethodPost, "/v1/smtpservers", smtp("smtp.example.com"))
	if got.Code != http.StatusCreated || jsonDoc(t, got.Body.Bytes())["secretSet"] == nil {
		t.Errorf("an SMTP server: %d %s, want 201 with secretSet", got.Code, got.Body)
	}
}

func TestNotifications_Heartbeat(t *testing.T) {
	w, n := newNotifyWorld(t, func(w *authzWorld) {
		w.grant("tok-admin", policy.OwnerRoleID, "/")
		w.grant("tok-admin", policy.SecretsUserRoleID, "secret")
		w.grant("tok-contrib", policy.ContributorRoleID, "/")
		w.grant("tok-contrib", policy.SecretsUserRoleID, "secret")
		w.grant("tok-owner", policy.OwnerRoleID, "projA")
	})
	body := func(host string, allow *resource.Allowance) map[string]any {
		return map[string]any{"spec": resource.HeartbeatSpec{URLSecretID: w.id["secret"], Host: host, AllowPrivateDestination: allow}}
	}
	w.expect("tok-admin", http.MethodGet, "/v1/heartbeat", nil, http.StatusNotFound)
	w.expect("tok-admin", http.MethodPost, "/v1/heartbeat/test", nil, http.StatusNotFound)
	// Decided at "/": a Project Owner can't set it.
	w.expect("tok-owner", http.MethodPut, "/v1/heartbeat", body("ping.example.com", nil), http.StatusForbidden)
	w.expect("tok-admin", http.MethodPut, "/v1/heartbeat", body("lan.example", nil), http.StatusBadRequest)
	w.expect("tok-admin", http.MethodPut, "/v1/heartbeat", body("ping.example.com", nil), http.StatusCreated)
	w.expect("tok-admin", http.MethodPut, "/v1/heartbeat", body("ping.example.com", nil), http.StatusOK)

	// The allowance at "/" is an Owner's.
	lan := &resource.Allowance{Reason: "Gatus on the second laptop"}
	w.expect("tok-contrib", http.MethodPut, "/v1/heartbeat", body("lan.example", lan), http.StatusForbidden)
	w.expect("tok-admin", http.MethodPut, "/v1/heartbeat", body("lan.example", lan), http.StatusOK)

	got := w.do("tok-admin", http.MethodPost, "/v1/heartbeat/test", nil)
	if got.Code != http.StatusOK || n.beats != 1 {
		t.Fatalf("heartbeat test: %d %s (beats %d)", got.Code, got.Body, n.beats)
	}
	doc := jsonDoc(t, w.do("tok-admin", http.MethodGet, "/v1/heartbeat", nil).Body.Bytes())
	if st := doc["status"].(map[string]any); st["lastResult"] != "Sent" || st["lastSent"] == nil {
		t.Errorf("status after a test = %v", st)
	}
	if _, ok := doc["secretSet"].(map[string]any)["urlSecretId"]; !ok {
		t.Errorf("no secretSet for the URL: %v", doc)
	}

	del := w.do("tok-admin", http.MethodDelete, "/v1/heartbeat", nil)
	if del.Code != http.StatusNoContent || del.Header().Get("Serendip-Approval-Id") == "" {
		t.Errorf("delete: %d %s, want 204 with its Approval", del.Code, del.Body)
	}
	w.expect("tok-admin", http.MethodGet, "/v1/heartbeat", nil, http.StatusNotFound)
}

func TestNotifications_WithoutASender(t *testing.T) {
	w := newAuthzWorld(t, func(w *authzWorld) {})
	id := w.mkAs("key-ci", "/v1/notificationchannels", webhookChannel("c", w.id["projA"], false, nil))
	w.expect("key-ci", http.MethodPost, "/v1/notificationchannels/"+id+"/test", nil, http.StatusServiceUnavailable)
}
