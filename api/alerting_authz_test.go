package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/serendip-infra/serendip-infra/core/policy"
	"github.com/serendip-infra/serendip-infra/core/resource"
)

// Spec 024 T003: who may write rules and channels (plan decision 9).

func webhookChannel(name, project string, def bool, private *resource.Allowance) map[string]any {
	meta := map[string]any{"name": name}
	if project != "" {
		meta["ownerRefs"] = []resource.OwnerRef{{Kind: resource.KindProject, ID: project}}
	}
	spec := resource.NotificationChannelSpec{
		Type: resource.ChannelWebhook, Webhook: &resource.WebhookSettings{URL: "https://hooks.example.com/" + name},
		Default: def, AllowPrivateDestination: private,
	}
	return map[string]any{"metadata": meta, "spec": spec}
}

func alertRule(name string, owner resource.Scope, env string, channels ...string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": name, "ownerRefs": []resource.OwnerRef{{Kind: owner.Kind, ID: owner.ID}}},
		"spec": resource.AlertRuleSpec{
			EnvironmentID: env, Severity: resource.SeverityWarning, Summary: "CPU high", ChannelIDs: channels,
			Condition: resource.AlertCondition{Type: resource.ConditionTypeMetric, Metric: &resource.MetricCondition{Metric: resource.MetricCPU, As: "value", Above: 1, ForMinutes: 5}},
		},
	}
}

// mkAs creates body at path as token, wanting 201, and returns its ID.
func (w *authzWorld) mkAs(token, path string, body any) string {
	w.t.Helper()
	got := w.do(token, http.MethodPost, path, body)
	if got.Code != http.StatusCreated {
		w.t.Fatalf("create %s as %s: %d %s", path, people[token].Name, got.Code, got.Body)
	}
	var doc struct {
		Metadata resource.Metadata `json:"metadata"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &doc); err != nil {
		w.t.Fatal(err)
	}
	return doc.Metadata.ID
}

func TestAuthz_AlertRulesNeedReadOnTheirChannels(t *testing.T) {
	w := newAuthzWorld(t, func(w *authzWorld) {
		w.grant("tok-contrib", policy.ContributorRoleID, "projA")
		w.grant("tok-reader", policy.ReaderRoleID, "projA")
	})
	chA := w.mkAs("key-ci", "/v1/notificationchannels", webhookChannel("a", w.id["projA"], false, nil))
	chB := w.mkAs("key-ci", "/v1/notificationchannels", webhookChannel("b", w.id["projB"], false, nil))
	platformDefault := w.mkAs("key-ci", "/v1/notificationchannels", webhookChannel("all", "", true, nil))
	platformOther := w.mkAs("key-ci", "/v1/notificationchannels", webhookChannel("ops", "", false, nil))
	missing := "01a1dda3-0000-7000-8000-00000000dead"
	web, env := w.scope("web"), w.id["prod"]

	tests := []struct {
		name     string
		token    string
		channels []string
		want     int
	}{
		{"a Reader can't write a rule", "tok-reader", []string{chA}, http.StatusForbidden},
		{"a Reader can't write one with no channels either", "tok-reader", nil, http.StatusForbidden},
		{"Project A's channel", "tok-contrib", []string{chA}, http.StatusCreated},
		{"no channels: the defaults", "tok-contrib", nil, http.StatusCreated},
		{"Project B's channel", "tok-contrib", []string{chB}, http.StatusForbidden},
		{"Project B's channel beside Project A's", "tok-contrib", []string{chA, chB}, http.StatusForbidden},
		{"a platform default channel", "tok-contrib", []string{platformDefault}, http.StatusCreated},
		{"a platform channel that isn't a default", "tok-contrib", []string{platformOther}, http.StatusForbidden},
		{"a missing channel, unreadable", "tok-contrib", []string{missing}, http.StatusForbidden},
		{"a missing channel, as an Owner", "key-ci", []string{missing}, http.StatusBadRequest},
		{"Project B's channel, as an Owner", "key-ci", []string{chB}, http.StatusCreated},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w.t = t
			w.expect(tt.token, http.MethodPost, "/v1/alertrules", alertRule("r"+string(rune('a'+i)), web, env, tt.channels...), tt.want)
		})
	}
	w.t = t

	// An update is checked on the channels it newly names: a Contributor
	// can edit a rule an Owner pointed at Project B, but not add B to one.
	ownerRule := w.mkAs("key-ci", "/v1/alertrules", alertRule("owners", web, env, chB))
	doc := w.current("/v1/alertrules/"+ownerRule, "")
	doc["spec"].(map[string]any)["summary"] = "edited"
	w.expect("tok-contrib", http.MethodPut, "/v1/alertrules/"+ownerRule, doc, http.StatusOK)

	contribRule := w.mkAs("tok-contrib", "/v1/alertrules", alertRule("mine", web, env, chA))
	doc = w.current("/v1/alertrules/"+contribRule, "")
	doc["spec"].(map[string]any)["channelIds"] = []string{chA, chB}
	w.expect("tok-contrib", http.MethodPut, "/v1/alertrules/"+contribRule, doc, http.StatusForbidden)
}

func TestAuthz_PrivateDestinationIsOwnersOnly(t *testing.T) {
	w := newAuthzWorld(t, func(w *authzWorld) {
		w.grant("tok-contrib", policy.ContributorRoleID, "projA")
		w.grant("tok-owner", policy.OwnerRoleID, "projA")
	})
	private := &resource.Allowance{Reason: "in-house receiver on the LAN"}
	projA := w.id["projA"]

	w.expect("tok-contrib", http.MethodPost, "/v1/notificationchannels", webhookChannel("c1", projA, false, private), http.StatusForbidden)
	w.expect("tok-owner", http.MethodPost, "/v1/notificationchannels", webhookChannel("o1", projA, false, private), http.StatusCreated)
	// A Project Owner can't grant it to a platform channel.
	w.expect("tok-owner", http.MethodPost, "/v1/notificationchannels", webhookChannel("o2", "", false, private), http.StatusForbidden)

	// Adding it on update is refused too; an Owner's grant can be kept
	// while editing other fields, and removed.
	id := w.mkAs("tok-contrib", "/v1/notificationchannels", webhookChannel("c2", projA, false, nil))
	path := "/v1/notificationchannels/" + id
	doc := w.current(path, "")
	doc["spec"].(map[string]any)["allowPrivateDestination"] = private
	w.expect("tok-contrib", http.MethodPut, path, doc, http.StatusForbidden)
	w.expect("tok-owner", http.MethodPut, path, doc, http.StatusOK)

	doc = w.current(path, "")
	doc["spec"].(map[string]any)["minSeverity"] = "warning"
	w.expect("tok-contrib", http.MethodPut, path, doc, http.StatusOK)
	doc = w.current(path, "")
	doc["spec"].(map[string]any)["allowPrivateDestination"] = &resource.Allowance{Reason: "a new reason"}
	w.expect("tok-contrib", http.MethodPut, path, doc, http.StatusForbidden)
	delete(doc["spec"].(map[string]any), "allowPrivateDestination")
	w.expect("tok-contrib", http.MethodPut, path, doc, http.StatusOK)
}

// SMTP servers are written at "/" whatever the body names.
func TestAuthz_SMTPServersAreDecidedAtRoot(t *testing.T) {
	w := newAuthzWorld(t, func(w *authzWorld) { w.grant("tok-owner", policy.OwnerRoleID, "projA") })
	body := map[string]any{
		"metadata": map[string]any{"name": "relay", "ownerRefs": []resource.OwnerRef{{Kind: resource.KindProject, ID: w.id["projA"]}}},
		"spec":     map[string]any{},
	}
	w.expect("tok-owner", http.MethodPost, "/v1/smtpservers", body, http.StatusForbidden)
}

// Deleting a rule, channel or SMTP server goes through an Approval, like
// every delete (decision 10); one still in use is refused with none.
func TestAuthz_AlertingDeletesHaveApprovals(t *testing.T) {
	w := newAuthzWorld(t, func(w *authzWorld) { w.grant("tok-contrib", policy.ContributorRoleID, "projA") })
	smtp := w.mkAs("key-ci", "/v1/smtpservers", map[string]any{
		"metadata": map[string]any{"name": "gmail"},
		"spec": resource.SMTPServerSpec{Host: "smtp.gmail.com", Port: 587, TLS: resource.SMTPStartTLS, From: "alerts@example.com",
			UsernameSecretID: w.id["secret"], PasswordSecretID: w.id["secret"]},
	})
	ch := w.mkAs("key-ci", "/v1/notificationchannels", webhookChannel("a", w.id["projA"], false, nil))
	rule := w.mkAs("tok-contrib", "/v1/alertrules", alertRule("cpu", w.scope("web"), w.id["prod"], ch))

	chScope := resource.Scope{Kind: resource.KindNotificationChannel, ID: ch}
	w.expect("tok-contrib", http.MethodDelete, "/v1/notificationchannels/"+ch, nil, http.StatusConflict)
	if n := len(approvalsOf(t, w, chScope)); n != 0 {
		t.Errorf("a refused delete left %d approvals", n)
	}
	// SMTP servers are platform-only: a Contributor in a Project can't.
	w.expect("tok-contrib", http.MethodDelete, "/v1/smtpservers/"+smtp, nil, http.StatusForbidden)
	for _, d := range []struct {
		token, path string
		target      resource.Scope
	}{
		{"tok-contrib", "/v1/alertrules/" + rule, resource.Scope{Kind: resource.KindAlertRule, ID: rule}},
		{"tok-contrib", "/v1/notificationchannels/" + ch, chScope},
		{"key-ci", "/v1/smtpservers/" + smtp, resource.Scope{Kind: resource.KindSMTPServer, ID: smtp}},
	} {
		got := w.do(d.token, http.MethodDelete, d.path, nil)
		if got.Code != http.StatusNoContent {
			t.Fatalf("DELETE %s: %d %s", d.path, got.Code, got.Body)
		}
		list := approvalsOf(t, w, d.target)
		if len(list) != 1 || list[0].Status.Phase != resource.ApprovalExecuted || got.Header().Get("Serendip-Approval-Id") != list[0].Metadata.ID {
			t.Errorf("DELETE %s: approvals %+v, want one Executed, named in the answer", d.path, list)
		}
	}
}

// Review fixes: an allowance covers one destination, a channel's Secrets
// need their values readable, and a moved rule names its channels anew.
func TestAuthz_ChannelDestinationsAndSecrets(t *testing.T) {
	w := newAuthzWorld(t, func(w *authzWorld) {
		w.grant("tok-contrib", policy.ContributorRoleID, "projA")
		w.grant("tok-owner", policy.OwnerRoleID, "projA")
		w.grant("tok-mixed", policy.ContributorRoleID, "projA")
		w.grant("tok-mixed", policy.SecretsUserRoleID, "secret")
	})
	projA := w.id["projA"]

	// A Contributor can't point an Owner's private allowance elsewhere.
	id := w.mkAs("tok-owner", "/v1/notificationchannels", webhookChannel("lan", projA, false, &resource.Allowance{Reason: "in-house receiver"}))
	path := "/v1/notificationchannels/" + id
	doc := w.current(path, "")
	doc["spec"].(map[string]any)["webhook"].(map[string]any)["url"] = "https://other.example.com/x"
	w.expect("tok-contrib", http.MethodPut, path, doc, http.StatusForbidden)
	w.expect("tok-owner", http.MethodPut, path, doc, http.StatusOK)

	// A bot token Secret needs its value readable by whoever names it.
	telegram := func(name string) map[string]any {
		return map[string]any{
			"metadata": map[string]any{"name": name, "ownerRefs": []resource.OwnerRef{{Kind: resource.KindProject, ID: projA}}},
			"spec": resource.NotificationChannelSpec{Type: resource.ChannelTelegram,
				Telegram: &resource.TelegramSettings{ChatID: "-1001234", BotTokenSecretID: w.id["secret"]}},
		}
	}
	w.expect("tok-contrib", http.MethodPost, "/v1/notificationchannels", telegram("t1"), http.StatusForbidden)
	tg := w.mkAs("tok-mixed", "/v1/notificationchannels", telegram("t2"))
	// Once named, the channel's other fields are editable by a Contributor.
	doc = w.current("/v1/notificationchannels/"+tg, "")
	doc["spec"].(map[string]any)["minSeverity"] = "critical"
	w.expect("tok-contrib", http.MethodPut, "/v1/notificationchannels/"+tg, doc, http.StatusOK)

	// A rule an Owner pointed at Project B's channel can't be moved by a
	// Contributor to another Component and keep it.
	chB := w.mkAs("key-ci", "/v1/notificationchannels", webhookChannel("b", w.id["projB"], false, nil))
	rule := w.mkAs("key-ci", "/v1/alertrules", alertRule("cpu", w.scope("web"), w.id["prod"], chB))
	doc = w.current("/v1/alertrules/"+rule, "")
	doc["metadata"].(map[string]any)["ownerRefs"] = []resource.OwnerRef{{Kind: resource.KindComponent, ID: w.id["devweb"]}}
	doc["spec"].(map[string]any)["environmentId"] = w.id["dev"]
	got := w.do("tok-contrib", http.MethodPut, "/v1/alertrules/"+rule, doc)
	if got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "notificationchannels/read") {
		t.Errorf("moving a rule naming Project B's channel: %d %s, want 403 for the channel", got.Code, got.Body)
	}
	// The same move with a channel they can read is fine.
	chA := w.mkAs("key-ci", "/v1/notificationchannels", webhookChannel("a", projA, false, nil))
	doc["spec"].(map[string]any)["channelIds"] = []string{chA}
	w.expect("tok-contrib", http.MethodPut, "/v1/alertrules/"+rule, doc, http.StatusOK)
}
