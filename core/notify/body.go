package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/serendip-infra/serendip-infra/core/resource"
)

// Events a notification can carry.
const (
	EventFiring   = resource.DeliveryEventFiring
	EventResolved = resource.DeliveryEventResolved
	EventTest     = "test"
)

// maxDetailLen caps the caller's condition line, so a card stays well
// under Telegram's 4,096 characters (T001: 4,097 gave 400).
const maxDetailLen = 300

// Message is one notification, in the sender's terms. The caller (the
// evaluator) composes Detail, the condition line, so this package knows
// nothing about conditions: "CPU above 90% of limit for 5 min (now
// 97%)", or for a resolution "CPU back under 90% (now 41%) after 7 min".
type Message struct {
	Event       string
	AlertID     string
	Rule        string
	Severity    resource.Severity
	State       string
	Summary     string
	Project     string
	Application string
	Component   string
	Environment string
	Detail      string
	// Source names the install in the footer.
	Source     string
	StartedAt  time.Time
	ResolvedAt *time.Time
	Value      float64
	Threshold  float64
	ConsoleURL string
	// ThreadRef is the firing's reference (Telegram message_id, email
	// Message-ID) that a resolution replies to.
	ThreadRef string
}

// oneLine keeps a value on one line, capped at max runes.
func oneLine(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = string(r[:max-1]) + "…"
	}
	return s
}

// escapeHTML escapes what Telegram's HTML mode and an HTML email part
// parse: &, < and >, and " for attribute values. T001: a summary holding
// <b>, & and </a> escaped this way shows as text.
func escapeHTML(s string) string {
	return htmlEscaper.Replace(s)
}

var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// attrEscaper also escapes quotes, for an href.
var attrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")

// WebhookVersion is the webhook body's version (operations.md).
const WebhookVersion = "serendip.alert.v1"

// WebhookBody is serendip.alert.v1.
type WebhookBody struct {
	Version string       `json:"version"`
	Event   string       `json:"event"`
	Alert   WebhookAlert `json:"alert"`
}

// WebhookAlert is the alert inside a serendip.alert.v1 body.
type WebhookAlert struct {
	ID          string     `json:"id"`
	Rule        string     `json:"rule"`
	Severity    string     `json:"severity"`
	Summary     string     `json:"summary"`
	State       string     `json:"state"`
	Project     string     `json:"project,omitempty"`
	Application string     `json:"application,omitempty"`
	Component   string     `json:"component,omitempty"`
	Environment string     `json:"environment,omitempty"`
	StartedAt   time.Time  `json:"startedAt"`
	ResolvedAt  *time.Time `json:"resolvedAt,omitempty"`
	Value       float64    `json:"value"`
	Threshold   float64    `json:"threshold"`
	ConsoleURL  string     `json:"consoleUrl,omitempty"`
}

// webhookBody renders m as serendip.alert.v1.
func webhookBody(m Message) ([]byte, error) {
	return json.Marshal(WebhookBody{Version: WebhookVersion, Event: m.Event, Alert: WebhookAlert{
		ID: m.AlertID, Rule: m.Rule, Severity: string(m.Severity), Summary: m.Summary, State: m.State,
		Project: m.Project, Application: m.Application, Component: m.Component, Environment: m.Environment,
		StartedAt: m.StartedAt.UTC(), ResolvedAt: utcPtr(m.ResolvedAt), Value: m.Value, Threshold: m.Threshold,
		ConsoleURL: m.ConsoleURL,
	}})
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// SignatureHeader carries the webhook's signature.
const SignatureHeader = "X-Serendip-Signature"

// Sign is the signature header's value: t=<unix>,v1=<hex HMAC-SHA256 of
// t + "." + body>, so a receiver can refuse a replay by its age.
func Sign(secret []byte, at time.Time, body []byte) string {
	t := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(t))
	mac.Write([]byte("."))
	mac.Write(body)
	return "t=" + t + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}
