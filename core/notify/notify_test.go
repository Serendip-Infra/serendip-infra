package notify_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/notify"
	"github.com/serendip-infra/serendip-infra/core/resource"
)

const testToken = "1000000001:AAH-test_token_value_0123456789abcd"

// Until the cluster's ranges are known, a private address is refused
// even with the allowance: it could be the cluster's own (T006 review).
func TestCheckAddr_PrivateRefusedUntilRangesKnown(t *testing.T) {
	c := &notify.Checker{}
	lan := netip.MustParseAddr("192.168.1.30")
	if err := c.CheckAddr(lan, true); !errors.Is(err, notify.ErrRefusedAddress) || !strings.Contains(err.Error(), "aren't known yet") {
		t.Errorf("before the ranges: %v", err)
	}
	if err := c.CheckAddr(netip.MustParseAddr("1.1.1.1"), false); err != nil {
		t.Errorf("a public address before the ranges: %v", err)
	}
	c.SetClusterRanges(nil)
	if err := c.CheckAddr(lan, true); err != nil {
		t.Errorf("after the ranges: %v", err)
	}
}

func TestCheckAddr(t *testing.T) {
	c := &notify.Checker{}
	c.SetClusterRanges([]netip.Prefix{netip.MustParsePrefix("10.42.0.0/16"), netip.MustParsePrefix("10.43.0.0/16"), netip.MustParsePrefix("192.168.1.20/32"), netip.MustParsePrefix("fd42::/48")})
	tests := []struct {
		ip                  string
		refused, refusedLAN bool // refused without, and with, allowPrivateDestination
	}{
		{"1.1.1.1", false, false},
		{"8.8.8.8", false, false},
		{"172.32.0.1", false, false},
		{"2606:4700::1111", false, false},
		{"127.0.0.2", true, true},
		{"::ffff:127.0.0.2", true, true},
		{"::1", true, true},
		{"0.0.0.0", true, true},
		{"::", true, true},
		{"169.254.169.254", true, true},
		{"fe80::1", true, true},
		{"224.0.0.1", true, true},
		{"ff02::1", true, true},
		{"255.255.255.255", true, true},
		{"10.1.2.3", true, false},
		{"172.16.0.1", true, false},
		{"192.168.1.1", true, false},
		{"100.64.0.1", true, false},
		{"fd00::1", true, false},
		{"::ffff:10.1.2.3", true, false},
		{"::ffff:1.1.1.1", false, false},
		{"64:ff9b::a01:203", true, false},  // NAT64 of 10.1.2.3
		{"64:ff9b::7f00:2", true, true},    // NAT64 of 127.0.0.2
		{"2002:a01:203::1", true, false},   // 6to4 of 10.1.2.3
		{"64:ff9b::101:101", false, false}, // NAT64 of 1.1.1.1
		// The cluster's own ranges and the node stay refused even with
		// the allowance.
		{"10.42.3.4", true, true},
		{"10.43.0.10", true, true},
		{"192.168.1.20", true, true},
		{"::ffff:10.42.0.1", true, true},
		{"fd42::5", true, true},
		// go-reviewer: tunnel and deprecated forms, and a zoned address.
		{"2001::1", true, true},     // Teredo
		{"::a00:1", true, true},     // IPv4-compatible 10.0.0.1
		{"192.88.99.1", true, true}, // 6to4 relay anycast
		{"fe80::1%eth0", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := netip.MustParseAddr(tt.ip)
			if err := c.CheckAddr(ip, false); (err != nil) != tt.refused || (err != nil && !errors.Is(err, notify.ErrRefusedAddress)) {
				t.Errorf("CheckAddr(%s) = %v, refused want %v", tt.ip, err, tt.refused)
			}
			if err := c.CheckAddr(ip, true); (err != nil) != tt.refusedLAN {
				t.Errorf("CheckAddr(%s, allowPrivate) = %v, refused want %v", tt.ip, err, tt.refusedLAN)
			}
		})
	}
}

func TestCheckHostAndURL(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.resolve("public.example.com", "1.1.1.1", "2606:4700::1111")
	e.resolve("mixed.example.com", "1.1.1.1", "10.0.0.5")
	e.resolve("lan.example.com", "192.168.1.30")
	e.resolve("meta.example.com", "169.254.169.254")
	if err := e.client.CheckHost(ctx, "public.example.com:8443", false); err != nil {
		t.Errorf("public: %v", err)
	}
	// Refused if any of its addresses is.
	if err := e.client.CheckHost(ctx, "mixed.example.com", false); !errors.Is(err, notify.ErrRefusedAddress) {
		t.Errorf("a name with one private address: %v", err)
	}
	if err := e.client.CheckHost(ctx, "lan.example.com", false); !errors.Is(err, notify.ErrRefusedAddress) {
		t.Errorf("a LAN name: %v", err)
	}
	if err := e.client.CheckHost(ctx, "lan.example.com", true); err != nil {
		t.Errorf("a LAN name with the allowance: %v", err)
	}
	if err := e.client.CheckHost(ctx, "meta.example.com", true); !errors.Is(err, notify.ErrRefusedAddress) {
		t.Errorf("link-local with the allowance: %v", err)
	}
	if err := e.client.CheckHost(ctx, "[::1]:80", true); !errors.Is(err, notify.ErrRefusedAddress) {
		t.Errorf("an IPv6 loopback literal: %v", err)
	}
	if err := e.client.CheckURL(ctx, "http://public.example.com/x", false, false); err == nil || !strings.Contains(err.Error(), "HTTPS only") {
		t.Errorf("plain http: %v", err)
	}
	if err := e.client.CheckURL(ctx, "http://public.example.com/x", false, true); err != nil {
		t.Errorf("plain http allowed: %v", err)
	}
	if err := e.client.CheckHost(ctx, "nowhere.example.com", false); err == nil || strings.Contains(err.Error(), "DNSError") {
		t.Errorf("an unknown name: %v", err)
	}
}

// The Control hook checks the address the socket actually connects to.
func TestControlHook(t *testing.T) {
	e := newEnv(t)
	for addr, refused := range map[string]bool{"1.1.1.1:443": false, "127.0.0.2:443": true, "[::1]:443": true, "10.0.0.1:443": true, "[::ffff:169.254.169.254]:80": true} {
		if err := e.client.Control(addr, false); (err != nil) != refused {
			t.Errorf("Control(%s) = %v, refused want %v", addr, err, refused)
		}
	}
}

// A name re-pointed at a refused address after it was checked (DNS
// rebinding) is refused at the send.
func TestRebindingRefusedAtSend(t *testing.T) {
	e := newEnv(t)
	var hits atomic.Int32
	port := e.server(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	e.resolve("rebind.example.com", "1.1.1.1")
	ctx := context.Background()
	url := "https://rebind.example.com:" + port + "/hook"
	if err := e.client.CheckURL(ctx, url, false, false); err != nil {
		t.Fatalf("check at create: %v", err)
	}
	e.resolve("rebind.example.com", "127.0.0.2")
	res := e.client.Deliver(ctx, webhookTarget(url, ""), firing())
	if res.Err == nil || !res.Err.Refused() || res.Err.Retryable {
		t.Fatalf("send after rebinding: %+v", res)
	}
	if hits.Load() != 0 {
		t.Fatal("the receiver was reached")
	}
}

func webhookTarget(url, signingID string) notify.Target {
	return notify.Target{Channel: resource.NotificationChannelSpec{Type: resource.ChannelWebhook,
		Webhook: &resource.WebhookSettings{URL: url, SigningSecretID: signingID}},
		Secrets: secrets(map[string]string{"sig": "signing-key-0123456789"})}
}

func secrets(m map[string]string) notify.SecretReader {
	return func(_ context.Context, id string) (string, error) {
		if v, ok := m[id]; ok {
			return v, nil
		}
		return "", errors.New("vault: secret/data/" + id + " not found at 10.43.0.7:8200")
	}
}

func firing() notify.Message {
	return notify.Message{Event: notify.EventFiring, AlertID: "alr-0123456789", Rule: "cpu-high", Severity: resource.SeverityCritical,
		State: resource.AlertFiring, Summary: `queue <b> & </a> "x"`, Component: "checkout-api", Environment: "production",
		Detail: "CPU above 90% of limit for 5 min (now 97%)", Source: "Serendip spike T001",
		StartedAt: time.Date(2026, 11, 15, 3, 20, 0, 0, time.UTC), Value: 97, Threshold: 90,
		ConsoleURL: "https://console.example.com/alerts/alr-0123456789"}
}

func TestRedirectRefused(t *testing.T) {
	e := newEnv(t)
	var followed atomic.Int32
	other := e.server(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Add(1) }))
	port := e.server(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://hooks.example.com:"+other+"/elsewhere", http.StatusFound)
	}))
	res := e.client.Deliver(context.Background(), webhookTarget("https://hooks.example.com:"+port+"/hook", ""), firing())
	if res.Err == nil || res.Err.Status != http.StatusFound || res.Err.Retryable || !strings.Contains(res.Err.Msg, "redirect refused") {
		t.Fatalf("302: %+v", res.Err)
	}
	if followed.Load() != 0 {
		t.Fatal("the redirect was followed")
	}
}

func TestProxyEnvironmentIgnored(t *testing.T) {
	e := newEnv(t)
	var proxied, direct atomic.Int32
	proxy := e.server(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { proxied.Add(1) }))
	t.Setenv("HTTPS_PROXY", "https://127.0.0.1:"+proxy)
	t.Setenv("HTTP_PROXY", "https://127.0.0.1:"+proxy)
	port := e.server(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { direct.Add(1) }))
	res := e.client.Deliver(context.Background(), webhookTarget("https://hooks.example.com:"+port+"/hook", ""), firing())
	if res.Err != nil || direct.Load() != 1 || proxied.Load() != 0 {
		t.Fatalf("err %v, direct %d, proxied %d", res.Err, direct.Load(), proxied.Load())
	}
}

func TestPrivateAllowanceAndPlainHTTP(t *testing.T) {
	e := newEnv(t)
	e.resolve("lan.example.com", "192.168.1.30")
	ch := resource.NotificationChannelSpec{Type: resource.ChannelWebhook, Webhook: &resource.WebhookSettings{URL: "https://lan.example.com/hook"}}
	res := e.client.Deliver(context.Background(), notify.Target{Channel: ch}, firing())
	if res.Err == nil || !res.Err.Refused() {
		t.Fatalf("a LAN receiver without the allowance: %+v", res.Err)
	}
	// With the allowance the check passes (checked without dialing a
	// real LAN address).
	ch.AllowPrivateDestination = &resource.Allowance{Reason: "in-house receiver"}
	if err := e.client.CheckURL(context.Background(), ch.Webhook.URL, true, false); err != nil {
		t.Fatalf("with the allowance: %v", err)
	}
	ch.Webhook.URL = "http://lan.example.com/hook"
	res = e.client.Deliver(context.Background(), notify.Target{Channel: ch}, firing())
	if res.Err == nil || !strings.Contains(res.Err.Msg, "HTTPS only") {
		t.Fatalf("plain http without its allowance: %+v", res.Err)
	}
}

func TestWebhookSignature(t *testing.T) {
	e := newEnv(t)
	at := time.Date(2026, 11, 15, 4, 0, 0, 0, time.UTC)
	e.client.SetNow(func() time.Time { return at })
	type got struct {
		sig, event, ctype string
		body              []byte
	}
	ch := make(chan got, 1)
	port := e.server(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- got{r.Header.Get("X-Serendip-Signature"), r.Header.Get("X-Serendip-Event"), r.Header.Get("Content-Type"), b}
	}))
	res := e.client.Deliver(context.Background(), webhookTarget("https://hooks.example.com:"+port+"/hook", "sig"), firing())
	if res.Err != nil || res.State != resource.DeliverySent {
		t.Fatalf("send: %+v", res)
	}
	g := <-ch
	// A receiver's own check: parse t and v1, recompute, compare in
	// constant time.
	var ts, v1 string
	for _, part := range strings.Split(g.sig, ",") {
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			v1 = v
		}
	}
	mac := hmac.New(sha256.New, []byte("signing-key-0123456789"))
	mac.Write([]byte(ts + "." + string(g.body)))
	want := hex.EncodeToString(mac.Sum(nil))
	if ts != strconv.FormatInt(at.Unix(), 10) || !hmac.Equal([]byte(v1), []byte(want)) {
		t.Fatalf("signature %q doesn't verify (want t=%d,v1=%s)", g.sig, at.Unix(), want)
	}
	var body notify.WebhookBody
	if err := json.Unmarshal(g.body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Version != "serendip.alert.v1" || body.Event != "firing" || body.Alert.ID != "alr-0123456789" || body.Alert.Summary != `queue <b> & </a> "x"` ||
		body.Alert.Value != 97 || body.Alert.Threshold != 90 || g.event != "firing" || g.ctype != "application/json" {
		t.Fatalf("body %+v, event %q, type %q", body, g.event, g.ctype)
	}
	// A body changed in transit doesn't verify.
	mac = hmac.New(sha256.New, []byte("signing-key-0123456789"))
	mac.Write([]byte(ts + "." + strings.Replace(string(g.body), "97", "10", 1)))
	if hmac.Equal([]byte(v1), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		t.Fatal("a changed body verified")
	}
}

func readJSON(t *testing.T, name string) any {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func asJSON(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// The card and the threaded resolution match what T001 sent and
// Telegram accepted.
func TestTelegramBodyMatchesRecording(t *testing.T) {
	b, err := notify.TelegramBody("123456789", firing())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := asJSON(t, b), readJSON(t, "telegram-request-card.json"); !reflect.DeepEqual(got, want) {
		t.Fatalf("card:\n got %s\nwant %v", b, want)
	}
	res := notify.Message{Event: notify.EventResolved, Severity: resource.SeverityCritical, Component: "checkout-api", Environment: "production",
		Detail: "CPU back under 90% (now 41%) after 7 min", ThreadRef: "3", ConsoleURL: "https://console.example.com/alerts/alr-0123456789"}
	b, _ = notify.TelegramBody("123456789", res)
	if got, want := asJSON(t, b), readJSON(t, "telegram-request-resolution.json"); !reflect.DeepEqual(got, want) {
		t.Fatalf("resolution:\n got %s\nwant %v", b, want)
	}
	// The summary's markup is escaped, not parsed.
	if !strings.Contains(firingText(t), "Summary: queue &lt;b&gt; &amp; &lt;/a&gt; \"x\"") {
		t.Fatal("the summary's markup reached the card unescaped")
	}
	// A localhost console URL (refused by Telegram as a button) goes in
	// the text instead.
	m := firing()
	m.ConsoleURL = "http://localhost:8088/alerts/alr-0123456789"
	b, _ = notify.TelegramBody("123456789", m)
	var req map[string]any
	_ = json.Unmarshal(b, &req)
	if _, ok := req["reply_markup"]; ok || !strings.HasSuffix(req["text"].(string), "\nhttp://localhost:8088/alerts/alr-0123456789") {
		t.Fatalf("localhost console URL: %s", b)
	}
	m.ConsoleURL = "http://192.168.1.20:8088/alerts/a"
	b, _ = notify.TelegramBody("123456789", m)
	if !strings.Contains(string(b), `"reply_markup"`) {
		t.Fatalf("a LAN console URL lost its button: %s", b)
	}
}

func firingText(t *testing.T) string {
	b, _ := notify.TelegramBody("1", firing())
	var req map[string]any
	_ = json.Unmarshal(b, &req)
	return req["text"].(string)
}

func telegramEnv(t *testing.T, h http.HandlerFunc) *env {
	e := newEnv(t)
	port := e.server(h)
	e.client.SetTelegramBase("https://tg.example.com:" + port)
	return e
}

func telegramTarget() notify.Target {
	return notify.Target{Channel: resource.NotificationChannelSpec{Type: resource.ChannelTelegram,
		Telegram: &resource.TelegramSettings{ChatID: "123456789", BotTokenSecretID: "tok"}},
		Secrets: secrets(map[string]string{"tok": testToken})}
}

func TestTelegramSend(t *testing.T) {
	ok, _ := os.ReadFile("testdata/telegram-sendMessage-ok.json")
	var path string
	e := telegramEnv(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write(ok)
	})
	res := e.client.Deliver(context.Background(), telegramTarget(), firing())
	if res.Err != nil || res.State != resource.DeliverySent || res.ThreadRef != "3" {
		t.Fatalf("send: %+v", res)
	}
	if path != "/bot"+testToken+"/sendMessage" {
		t.Fatalf("path %q", path)
	}
}

func TestTelegramErrors(t *testing.T) {
	bad, _ := os.ReadFile("testdata/telegram-bad-html.json")
	tests := []struct {
		name      string
		status    int
		body      string
		retryable bool
		after     time.Duration
		msg       string
	}{
		{"bad html", 400, string(bad), false, 0, "can't parse entities"},
		{"rate limited", 429, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 7","parameters":{"retry_after":7}}`, true, 7 * time.Second, "Too Many Requests"},
		{"rate limited for long", 429, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 7200","parameters":{"retry_after":7200}}`, true, notify.MaxRetryAfter, ""},
		{"server error", 502, `<html>bad gateway</html>`, true, 0, "Bad Gateway"},
		{"token echoed", 401, `{"ok":false,"error_code":401,"description":"Unauthorized: bot` + testToken + `"}`, false, 0, "[redacted]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := telegramEnv(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})
			res := e.client.Deliver(context.Background(), telegramTarget(), firing())
			se := res.Err
			if se == nil || se.Status != tt.status || se.Retryable != tt.retryable || se.RetryAfter != tt.after || !strings.Contains(se.Msg, tt.msg) {
				t.Fatalf("err %+v", se)
			}
			assertClean(t, se.Error())
		})
	}
}

// assertClean fails if s holds a token, a URL path or an internal
// address.
func assertClean(t *testing.T, s string) {
	t.Helper()
	for _, bad := range []string{testToken, "AAH-test", "/bot", "/hook", "s3cr3t-path", "signing-key", "10.43.0.7", "secret/data", "smtp-pass-value"} {
		if strings.Contains(s, bad) {
			t.Fatalf("error %q holds %q", s, bad)
		}
	}
}

func TestNoSecretInErrors(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// A webhook URL held as a Secret, to a port nothing listens on: the
	// dial error must not carry the URL's path.
	ch := resource.NotificationChannelSpec{Type: resource.ChannelWebhook, Webhook: &resource.WebhookSettings{URLSecretID: "url", Host: "hooks.example.com", SigningSecretID: "sig"}}
	sec := secrets(map[string]string{"url": "https://hooks.example.com:1/hook/s3cr3t-path?token=s3cr3t-path", "sig": "signing-key-0123456789"})
	res := e.client.Deliver(ctx, notify.Target{Channel: ch, Secrets: sec}, firing())
	if res.Err == nil || !res.Err.Retryable || res.Err.Host != "hooks.example.com" {
		t.Fatalf("dial failure: %+v", res.Err)
	}
	assertClean(t, res.Err.Error())

	// The Secret's value points elsewhere than the host shown.
	sec = secrets(map[string]string{"url": "https://other.example.com/hook/s3cr3t-path"})
	res = e.client.Deliver(ctx, notify.Target{Channel: ch, Secrets: sec}, firing())
	if res.Err == nil || !strings.Contains(res.Err.Msg, "different host") {
		t.Fatalf("a moved URL: %+v", res.Err)
	}
	assertClean(t, res.Err.Error())

	// A Secret that can't be read: its provider's error isn't passed on.
	res = e.client.Deliver(ctx, telegramTargetWith(secrets(nil)), firing())
	if res.Err == nil {
		t.Fatal("an unreadable token was sent")
	}
	assertClean(t, res.Err.Error())

	// A token that isn't one can't change the request path.
	res = e.client.Deliver(ctx, telegramTargetWith(secrets(map[string]string{"tok": "123:abc/../../evil"})), firing())
	if res.Err == nil || !strings.Contains(res.Err.Msg, "doesn't hold a bot token") {
		t.Fatalf("a malformed token: %+v", res.Err)
	}

	// A Telegram dial failure carries neither the token nor the path.
	e.client.SetTelegramBase("https://tg.example.com:1")
	res = e.client.Deliver(ctx, telegramTarget(), firing())
	if res.Err == nil || res.Err.Host != "api.telegram.org" {
		t.Fatalf("telegram dial failure: %+v", res.Err)
	}
	assertClean(t, res.Err.Error())
}

func telegramTargetWith(r notify.SecretReader) notify.Target {
	tg := telegramTarget()
	tg.Secrets = r
	return tg
}

func TestWebhookRetryAfter(t *testing.T) {
	for _, tt := range []struct {
		header string
		want   time.Duration
	}{{"30", 30 * time.Second}, {"3600", notify.MaxRetryAfter}, {"", 0}, {time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat), 2 * time.Minute}} {
		e := newEnv(t)
		port := e.server(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tt.header != "" {
				w.Header().Set("Retry-After", tt.header)
			}
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		res := e.client.Deliver(context.Background(), webhookTarget("https://hooks.example.com:"+port+"/hook", ""), firing())
		if res.Err == nil || !res.Err.Retryable || res.Err.RetryAfter < tt.want-2*time.Second || res.Err.RetryAfter > tt.want {
			t.Errorf("Retry-After %q: %+v", tt.header, res.Err)
		}
	}
	// A 400 isn't retried; a 408 is.
	for status, retry := range map[int]bool{400: false, 404: false, 408: true, 500: true, 503: true} {
		e := newEnv(t)
		port := e.server(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		res := e.client.Deliver(context.Background(), webhookTarget("https://hooks.example.com:"+port+"/hook", ""), firing())
		if res.Err == nil || res.Err.Retryable != retry {
			t.Errorf("%d: %+v", status, res.Err)
		}
	}
}

func TestBelowMinSeverityNotSent(t *testing.T) {
	e := newEnv(t)
	var hits atomic.Int32
	port := e.server(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	tgt := webhookTarget("https://hooks.example.com:"+port+"/hook", "")
	tgt.Channel.MinSeverity = resource.SeverityCritical
	m := firing()
	m.Severity = resource.SeverityWarning
	if res := e.client.Deliver(context.Background(), tgt, m); res.State != resource.DeliveryBelowMinSeverity || res.Err != nil {
		t.Fatalf("warning to a critical-only channel: %+v", res)
	}
	if hits.Load() != 0 {
		t.Fatal("it was sent")
	}
	m.Event = notify.EventTest
	if res := e.client.Deliver(context.Background(), tgt, m); res.State != resource.DeliverySent {
		t.Fatalf("a test send: %+v", res)
	}
	m.Event, m.Severity = notify.EventFiring, resource.SeverityCritical
	if res := e.client.Deliver(context.Background(), tgt, m); res.State != resource.DeliverySent || hits.Load() != 2 {
		t.Fatalf("critical: %+v, hits %d", res, hits.Load())
	}
}

func smtpTarget(f *fakeSMTP, tlsMode string) notify.Target {
	port, _ := strconv.Atoi(f.port)
	srv := &resource.SMTPServerSpec{Host: "smtp.example.com", Port: port, TLS: tlsMode, From: "Serendip <alerts@example.com>", UsernameSecretID: "user", PasswordSecretID: "pass"}
	return notify.Target{
		Channel: resource.NotificationChannelSpec{Type: resource.ChannelEmail, Email: &resource.EmailSettings{SMTPServerID: "smtp", To: []string{"ops@example.com", "dev@example.com"}}},
		SMTP:    srv,
		Secrets: secrets(map[string]string{"user": "alerts@example.com", "pass": "smtp-pass-value"}),
	}
}

func TestEmailThreadsResolution(t *testing.T) {
	for _, mode := range []string{resource.SMTPStartTLS, resource.SMTPImplicit} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			f := &fakeSMTP{cert: e.cert, offerSTARTTLS: true, implicit: mode == resource.SMTPImplicit}
			f.start(t)
			tgt := smtpTarget(f, mode)
			res := e.client.Deliver(context.Background(), tgt, firing())
			if res.Err != nil || res.State != resource.DeliverySent || !strings.HasPrefix(res.ThreadRef, "<alr-0123456789.firing.") {
				t.Fatalf("firing: %+v", res)
			}
			resolved := firing()
			resolved.Event, resolved.ThreadRef = notify.EventResolved, res.ThreadRef
			now := time.Now()
			resolved.ResolvedAt = &now
			res2 := e.client.Deliver(context.Background(), tgt, resolved)
			if res2.Err != nil {
				t.Fatalf("resolution: %+v", res2.Err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.data) != 2 || f.authUser != "alerts@example.com" || f.authPass != "smtp-pass-value" {
				t.Fatalf("%d messages, auth %q", len(f.data), f.authUser)
			}
			first, second := f.data[0], f.data[1]
			// The fake server's DotReader turns CRLF into LF.
			if !strings.Contains(first, "\nSubject: [CRITICAL] firing: queue <b> & </a> \"x\"\n") || !strings.Contains(second, "\nSubject: [RESOLVED] queue") {
				t.Errorf("firing subject:\n%s", first)
			}
			if !strings.Contains(second, "\nIn-Reply-To: "+res.ThreadRef+"\n") || !strings.Contains(second, "\nReferences: "+res.ThreadRef+"\n") {
				t.Errorf("resolution doesn't thread under %s:\n%s", res.ThreadRef, second)
			}
			if !strings.Contains(first, "text/plain") || !strings.Contains(first, "text/html") || !strings.Contains(first, "&lt;b&gt; &amp; &lt;/a&gt;") || strings.Contains(first, "<b>queue") {
				t.Errorf("firing parts or escaping:\n%s", first)
			}
			for _, s := range f.steps {
				if s == "AUTH(plain)" {
					t.Fatalf("authenticated before TLS: %v", f.steps)
				}
			}
		})
	}
}

// T001's Brevo on 587 offers AUTH before STARTTLS: the client still
// upgrades first. A server offering no STARTTLS never gets the password.
func TestSMTPAuthOnlyAfterTLS(t *testing.T) {
	e := newEnv(t)
	f := &fakeSMTP{cert: e.cert, offerSTARTTLS: true, authBeforeTLS: true}
	f.start(t)
	if res := e.client.Deliver(context.Background(), smtpTarget(f, resource.SMTPStartTLS), firing()); res.Err != nil {
		t.Fatalf("send: %+v", res.Err)
	}
	f.mu.Lock()
	steps := strings.Join(f.steps, ",")
	f.mu.Unlock()
	if !strings.HasPrefix(steps, "EHLO,STARTTLS,EHLO,AUTH(tls)") {
		t.Fatalf("steps %s", steps)
	}

	plain := &fakeSMTP{cert: e.cert, authBeforeTLS: true}
	plain.start(t)
	res := e.client.Deliver(context.Background(), smtpTarget(plain, resource.SMTPStartTLS), firing())
	if res.Err == nil || !strings.Contains(res.Err.Msg, "STARTTLS") || res.Err.Retryable {
		t.Fatalf("a server without STARTTLS: %+v", res.Err)
	}
	plain.mu.Lock()
	defer plain.mu.Unlock()
	if plain.authPass != "" || strings.Contains(strings.Join(plain.steps, ","), "AUTH") {
		t.Fatalf("the password was offered in clear: %v", plain.steps)
	}
	assertClean(t, res.Err.Error())
}

func TestHeartbeat(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 15, 5, 0, 0, 0, time.UTC)
	e.client.SetNow(func() time.Time { return now })
	var hits atomic.Int32
	var method string
	port := e.server(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); method = r.Method }))
	spec := resource.HeartbeatSpec{URLSecretID: "hb", Host: "hc.example.com"}
	sec := secrets(map[string]string{"hb": "https://hc.example.com:" + port + "/ping/uuid-s3cr3t-path"})
	tick, run := now.Add(-30*time.Second), now.Add(-10*time.Second)
	var storeErr error
	health := notify.Health{
		EvaluatorTick: func() time.Time { return tick },
		DeliveryRun:   func() time.Time { return run },
		StoreOK:       func(context.Context) error { return storeErr },
	}
	ctx := context.Background()
	if r := e.client.Beat(ctx, health, spec, sec, false); !r.Sent || r.Err != nil || hits.Load() != 1 || method != http.MethodGet {
		t.Fatalf("healthy: %+v, hits %d, %s", r, hits.Load(), method)
	}
	spec.Method = http.MethodPost
	if r := e.client.Beat(ctx, health, spec, sec, false); !r.Sent || method != http.MethodPost {
		t.Fatalf("POST: %+v, %s", r, method)
	}
	tick = now.Add(-3 * time.Minute)
	if r := e.client.Beat(ctx, health, spec, sec, false); r.Sent || r.Paused != notify.PausedEvaluator || hits.Load() != 2 {
		t.Fatalf("evaluator stalled: %+v, hits %d", r, hits.Load())
	}
	// A person's test send skips the gate.
	if r := e.client.Beat(ctx, health, spec, sec, true); !r.Sent || hits.Load() != 3 {
		t.Fatalf("forced: %+v", r)
	}
	tick = now
	run = now.Add(-5 * time.Minute)
	if r := e.client.Beat(ctx, health, spec, sec, false); r.Sent || r.Paused != notify.PausedDelivery {
		t.Fatalf("delivery stalled: %+v", r)
	}
	run, storeErr = now, errors.New("database is locked")
	if r := e.client.Beat(ctx, health, spec, sec, false); r.Sent || r.Paused != notify.PausedStore || hits.Load() != 3 {
		t.Fatalf("store failing: %+v, hits %d", r, hits.Load())
	}
	// A failing monitor: the error names the host only.
	storeErr = nil
	sec = secrets(map[string]string{"hb": "https://hc.example.com:1/ping/uuid-s3cr3t-path"})
	r := e.client.Beat(ctx, health, spec, sec, false)
	if r.Sent || r.Err == nil || r.Err.Host != "hc.example.com" {
		t.Fatalf("monitor down: %+v", r)
	}
	assertClean(t, r.Err.Error())
}

func TestFindTelegramChats(t *testing.T) {
	updates, err := os.ReadFile("testdata/telegram-getUpdates.json")
	if err != nil {
		t.Fatal(err)
	}
	e := telegramEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getUpdates") {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write(updates)
	})
	chats, err := e.client.FindTelegramChats(context.Background(), testToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 || chats[0].ID != "123456789" || chats[0].Type != "private" {
		t.Fatalf("chats %+v", chats)
	}
}

// A DNS timeout is worth a retry; an unknown name and a cancelled send
// (serendipd stopping) aren't.
func TestFailureClassification(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	slow := notify.NewClient(notify.Options{Lookup: func(context.Context, string) ([]netip.Addr, error) {
		return nil, &net.DNSError{Err: "i/o timeout", Name: "hooks.example.com", IsTimeout: true}
	}})
	res := slow.Deliver(ctx, webhookTarget("https://hooks.example.com/hook", ""), firing())
	if res.Err == nil || !res.Err.Retryable || res.Err.Refused() {
		t.Errorf("DNS timeout: %+v", res.Err)
	}
	res = e.client.Deliver(ctx, webhookTarget("https://nowhere.example.com/hook", ""), firing())
	if res.Err == nil || res.Err.Retryable || !strings.Contains(res.Err.Msg, "no such host") {
		t.Errorf("unknown name: %+v", res.Err)
	}
	port := e.server(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	cctx, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	res = e.client.Deliver(cctx, webhookTarget("https://hooks.example.com:"+port+"/hook", ""), firing())
	if res.Err == nil || res.Err.Retryable || res.Err.Msg != "cancelled" {
		t.Errorf("cancelled: %+v", res.Err)
	}
	// An SMTP server has no private allowance, and says so.
	e.resolve("smtp.lan.example.com", "192.168.1.25")
	f := &fakeSMTP{cert: e.cert, offerSTARTTLS: true}
	f.start(t)
	tgt := smtpTarget(f, resource.SMTPStartTLS)
	tgt.SMTP.Host = "smtp.lan.example.com"
	res = e.client.Deliver(ctx, tgt, firing())
	if res.Err == nil || !res.Err.Refused() || !strings.Contains(res.Err.Msg, "public address") {
		t.Errorf("a LAN SMTP server: %+v", res.Err)
	}
}
