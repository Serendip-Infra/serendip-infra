package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/api"
	"github.com/serendip-infra/serendip-infra/core/notify"
	"github.com/serendip-infra/serendip-infra/core/policy"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store/sqlite"
)

// Spec 024 T008: alerting through the CLI and serendip mcp.

// botToken is the channel's token, held only in its Secret.
const botToken = "123456789:AAHsecretTokenValue-abcdefghijklmnopq"

// fakeNotifier sends nothing. Deliver reads the token as the real sender
// would, so a test send has it in hand, and answers as Telegram does to a
// revoked token.
type fakeNotifier struct {
	mu    sync.Mutex
	reads int
}

func (f *fakeNotifier) Deliver(ctx context.Context, t notify.Target, _ notify.Message) notify.Result {
	tok, err := t.Secrets(ctx, t.Channel.Telegram.BotTokenSecretID)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil && tok == botToken {
		f.reads++
	}
	return notify.Result{Err: &notify.SendError{Host: "api.telegram.org", Status: 401, Msg: "Unauthorized"}}
}

func (*fakeNotifier) Beat(context.Context, notify.Health, resource.HeartbeatSpec, notify.SecretReader, bool) notify.HeartbeatResult {
	return notify.HeartbeatResult{}
}
func (*fakeNotifier) CheckURL(context.Context, string, bool, bool) error { return nil }
func (*fakeNotifier) CheckHost(context.Context, string, bool) error      { return nil }

type alertWorld struct {
	*world
	notifier                     *fakeNotifier
	projID, compID, secretID     string
	ruleID, channelID, alertsDir string
}

// newAlertWorld is newWorld with a notifier, a Component, the bot token's
// Secret, a Telegram channel and a rule created through the CLI, and n
// firing Alerts of the rule.
func newAlertWorld(t *testing.T, n int) *alertWorld {
	t.Helper()
	f := &fakeNotifier{}
	var aw *alertWorld
	secrets := func(_ context.Context, id string) (string, error) {
		if id != aw.secretID {
			return "", fmt.Errorf("no Secret %s", id)
		}
		return botToken, nil
	}
	aw = &alertWorld{world: newWorldWith(t, nil, api.WithNotifier(f, secrets)), notifier: f, alertsDir: t.TempDir()}
	w := aw.world
	ctx := context.Background()
	env, err := sqlite.NewStore[resource.EnvironmentSpec, resource.EnvironmentStatus](w.db, resource.KindEnvironment).Get(ctx, w.envID)
	if err != nil {
		t.Fatal(err)
	}
	aw.projID = env.Metadata.OwnerRefs[0].ID
	comp, err := sqlite.NewStore[resource.ComponentSpec, resource.ComponentStatus](w.db, resource.KindComponent).Create(ctx,
		resource.Component{Metadata: resource.Metadata{Name: "web", OwnerRefs: []resource.OwnerRef{{Kind: resource.KindApplication, ID: w.appID}}},
			Spec: resource.ComponentSpec{Image: "nginx"}})
	if err != nil {
		t.Fatal(err)
	}
	aw.compID = comp.Metadata.ID
	sec, err := sqlite.NewStore[resource.SecretSpec, resource.SecretStatus](w.db, resource.KindSecret).Create(ctx,
		resource.Secret{Metadata: resource.Metadata{Name: "bot-token", OwnerRefs: []resource.OwnerRef{{Kind: resource.KindProject, ID: aw.projID}}},
			Spec: resource.SecretSpec{Path: "alerts/bot"}})
	if err != nil {
		t.Fatal(err)
	}
	aw.secretID = sec.Metadata.ID
	// Naming a Secret in a channel needs reading its value (T003): the
	// person is Owner, and Secrets User too.
	if _, err := sqlite.NewStore[resource.RoleAssignmentSpec, resource.RoleAssignmentStatus](w.db, resource.KindRoleAssignment).Create(ctx,
		resource.RoleAssignment{Metadata: resource.Metadata{Name: "secrets-user"}, Spec: resource.RoleAssignmentSpec{
			Principal: resource.PrincipalRef{Type: resource.PrincipalPerson, ID: "sub-owner"}, RoleDefinitionID: policy.SecretsUserRoleID, Scope: resource.RootScope,
		}}); err != nil {
		t.Fatal(err)
	}
	w.signIn(t)

	aw.channelID = aw.create(t, "channels", fmt.Sprintf(`{"metadata":{"name":"ops-chat","ownerRefs":[{"kind":"Project","id":%q}]},
		"spec":{"type":"telegram","telegram":{"chatId":"-1001234","botTokenSecretId":%q}}}`, aw.projID, aw.secretID))
	aw.ruleID = aw.create(t, "alertrules", fmt.Sprintf(`{"metadata":{"name":"cpu-high","ownerRefs":[{"kind":"Component","id":%q}]},
		"spec":{"environmentId":%q,"severity":"warning","summary":"CPU over 500m","channelIds":[%q],
		"condition":{"type":"metric","metric":{"metric":"cpu","as":"value","above":500,"forMinutes":5}}}}`, aw.compID, w.envID, aw.channelID))

	alerts := sqlite.NewStore[resource.AlertSpec, resource.AlertStatus](w.db, resource.KindAlert)
	start := time.Now().Add(-time.Duration(n) * time.Minute).UTC()
	for i := range n {
		if _, err := alerts.Create(ctx, resource.Alert{
			// As the evaluator writes them: named after, and owned by, the rule.
			Metadata: resource.Metadata{Name: "cpu-high", OwnerRefs: []resource.OwnerRef{{Kind: resource.KindAlertRule, ID: aw.ruleID}}},
			Spec: resource.AlertSpec{RuleID: aw.ruleID, ComponentID: aw.compID, EnvironmentID: w.envID, Severity: resource.SeverityWarning,
				Summary: "CPU over 500m", Fingerprint: resource.AlertFingerprint(aw.ruleID, aw.compID, w.envID)},
			Status: resource.AlertStatus{State: resource.AlertFiring, StartedAt: start.Add(time.Duration(i) * time.Minute), Value: 640, Threshold: 500,
				Deliveries: []resource.AlertDelivery{{ChannelID: aw.channelID, Event: "firing", State: "failed", Attempts: 3,
					LastError: "api.telegram.org: 401 Unauthorized", At: start}}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return aw
}

// create runs `<group> create --file` with doc and answers the new ID.
func (aw *alertWorld) create(t *testing.T, group, doc string) string {
	t.Helper()
	f := filepath.Join(aw.alertsDir, group+".json")
	if err := os.WriteFile(f, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	r := aw.cli(nil, "", group, "create", "--file", f, "--json")
	var got struct {
		Metadata struct{ ID string } `json:"metadata"`
	}
	if r.code != exitOK || json.Unmarshal([]byte(r.stdout), &got) != nil || got.Metadata.ID == "" {
		t.Fatalf("%s create: %d %s %s", group, r.code, r.stdout, r.stderr)
	}
	return got.Metadata.ID
}

// dryRun is a --dry-run's output with its body, if any, compacted.
func dryRun(out string) string {
	line, body, _ := strings.Cut(strings.TrimSpace(out), "\n")
	if body == "" {
		return line
	}
	var b bytes.Buffer
	if json.Compact(&b, []byte(body)) != nil {
		return out
	}
	return line + "\n" + b.String()
}

// Each command of plan decision 13 makes the request it should, and a
// wrong use is a usage error before anything is sent.
func TestAlertingCommands(t *testing.T) {
	w := newWorld(t)
	u := w.api.URL
	until := "2026-11-20T00:00:00Z"
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"alerts", "list"}, "GET " + u + "/v1/alerts"},
		{[]string{"alerts", "list", "--state", "firing", "--severity", "critical", "--componentId", "c1", "--limit", "500"},
			"GET " + u + "/v1/alerts?componentId=c1&limit=500&severity=critical&state=firing"},
		{[]string{"alerts", "list", "--applicationId", "a1", "--environmentId", "e1"}, "GET " + u + "/v1/alerts?applicationId=a1&environmentId=e1"},
		{[]string{"alerts", "get", "al1"}, "GET " + u + "/v1/alerts/al1"},
		{[]string{"alerts", "summary"}, "GET " + u + "/v1/alerts/summary"},
		{[]string{"alerts", "ack", "al1"}, "POST " + u + "/v1/alerts/al1/acknowledge"},
		{[]string{"alertrules", "list"}, "GET " + u + "/v1/alertrules"},
		{[]string{"alertrules", "get", "r1"}, "GET " + u + "/v1/alertrules/r1"},
		{[]string{"alertrules", "delete", "r1"}, "DELETE " + u + "/v1/alertrules/r1"},
		{[]string{"alertrules", "silence", "r1", "--until", until, "--reason", "deploy window"},
			"POST " + u + "/v1/alertrules/r1/silence\n" + `{"reason":"deploy window","until":"` + until + `"}`},
		{[]string{"alertrules", "unsilence", "r1"}, "POST " + u + "/v1/alertrules/r1/unsilence"},
		{[]string{"channels", "list"}, "GET " + u + "/v1/notificationchannels"},
		{[]string{"channels", "get", "ch1"}, "GET " + u + "/v1/notificationchannels/ch1"},
		{[]string{"channels", "delete", "ch1"}, "DELETE " + u + "/v1/notificationchannels/ch1"},
		{[]string{"channels", "test", "ch1"}, "POST " + u + "/v1/notificationchannels/ch1/test"},
		{[]string{"smtp", "list"}, "GET " + u + "/v1/smtpservers"},
		{[]string{"smtp", "get", "s1"}, "GET " + u + "/v1/smtpservers/s1"},
		{[]string{"smtp", "delete", "s1"}, "DELETE " + u + "/v1/smtpservers/s1"},
		{[]string{"heartbeat", "get"}, "GET " + u + "/v1/heartbeat"},
		{[]string{"heartbeat", "test"}, "POST " + u + "/v1/heartbeat/test"},
		{[]string{"heartbeat", "delete"}, "DELETE " + u + "/v1/heartbeat"},
	} {
		r := w.cli(ops, "", append([]string{"--dry-run"}, tt.args...)...)
		if r.code != exitOK || dryRun(r.stdout) != tt.want {
			t.Errorf("%v: %d %q, want %q (%s)", tt.args, r.code, r.stdout, tt.want, r.stderr)
		}
	}

	// Writes with a document: create, update and set send it whole.
	doc := filepath.Join(t.TempDir(), "doc.json")
	os.WriteFile(doc, []byte(`{"spec":{"x":1}}`), 0o600)
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"alertrules", "create", "--file", doc}, "POST " + u + "/v1/alertrules"},
		{[]string{"alertrules", "update", "r1", "--file", doc}, "PUT " + u + "/v1/alertrules/r1"},
		{[]string{"channels", "create", "--file", doc}, "POST " + u + "/v1/notificationchannels"},
		{[]string{"channels", "update", "ch1", "--file", doc}, "PUT " + u + "/v1/notificationchannels/ch1"},
		{[]string{"smtp", "create", "--file", doc}, "POST " + u + "/v1/smtpservers"},
		{[]string{"smtp", "update", "s1", "--file", doc}, "PUT " + u + "/v1/smtpservers/s1"},
		{[]string{"heartbeat", "set", "--file", doc}, "PUT " + u + "/v1/heartbeat"},
	} {
		r := w.cli(ops, "", append([]string{"--dry-run"}, tt.args...)...)
		if r.code != exitOK || dryRun(r.stdout) != tt.want+"\n"+`{"spec":{"x":1}}` {
			t.Errorf("%v: %d %q, want %q and the document (%s)", tt.args, r.code, r.stdout, tt.want, r.stderr)
		}
	}

	before := len(w.sent())
	for _, args := range [][]string{
		{"alerts", "list", "--state", "open"},              // not firing or resolved
		{"alerts", "list", "--severity", "high"},           // not a severity
		{"alerts", "list", "--limit", "501"},               // over the API's cap
		{"alerts", "list", "--limit", "0"},                 // under 1
		{"alerts", "get"},                                  // no ID
		{"alerts", "ack", "al1", "al2"},                    // two IDs
		{"alertrules", "silence", "r1", "--until", until},  // no reason
		{"alertrules", "silence", "r1", "--reason", "why"}, // no until
		{"alertrules", "create"},                           // no document
		{"channels", "test"},                               // no ID
		{"channels", "show", "ch1"},                        // get, not show (decision 13 amended)
		{"smtp", "test", "s1"},                             // no such command
		{"heartbeat", "set"},                               // no document
		{"heartbeat", "get", "h1"},                         // a singleton has no ID
	} {
		if r := w.cli(ops, "", args...); r.code != exitUsage {
			t.Errorf("%v: %d, want usage (%s)", args, r.code, r.stderr)
		}
	}
	if len(w.sent()) != before {
		t.Errorf("a usage error sent a request: %v", w.sent()[before:])
	}
}

// Against the server: list and get Alerts, acknowledge one, silence and
// unsilence the rule, and read the summary.
func TestAlertingCommandsAgainstTheServer(t *testing.T) {
	aw := newAlertWorld(t, 3)
	r := aw.cli(nil, "", "alerts", "list")
	if r.code != exitOK || strings.Count(r.stdout, "cpu-high  firing") != 3 {
		t.Fatalf("alerts list: %d %q %s", r.code, r.stdout, r.stderr)
	}
	r = aw.cli(nil, "", "alerts", "list", "--json", "--limit", "2")
	var alerts []resource.Alert
	if r.code != exitOK || json.Unmarshal([]byte(r.stdout), &alerts) != nil || len(alerts) != 2 {
		t.Fatalf("alerts list --limit 2: %d %q", r.code, r.stdout)
	}
	id := alerts[0].Metadata.ID
	if r := aw.cli(nil, "", "alerts", "ack", id); r.code != exitOK {
		t.Errorf("ack: %d %s", r.code, r.stderr)
	}
	r = aw.cli(nil, "", "alerts", "get", id, "--json")
	var a resource.Alert
	if r.code != exitOK || json.Unmarshal([]byte(r.stdout), &a) != nil || a.Status.Acknowledged == nil {
		t.Errorf("get after ack: %d %q", r.code, r.stdout)
	}
	r = aw.cli(nil, "", "alerts", "summary", "--json")
	if r.code != exitOK || !strings.Contains(r.stdout, "warning") {
		t.Errorf("summary: %d %q %s", r.code, r.stdout, r.stderr)
	}
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	r = aw.cli(nil, "", "alertrules", "silence", aw.ruleID, "--until", until, "--reason", "deploy window", "--json")
	if r.code != exitOK || !strings.Contains(r.stdout, "deploy window") {
		t.Errorf("silence: %d %q %s", r.code, r.stdout, r.stderr)
	}
	if r := aw.cli(nil, "", "alertrules", "unsilence", aw.ruleID, "--json"); r.code != exitOK || strings.Contains(r.stdout, "deploy window") {
		t.Errorf("unsilence: %d %q %s", r.code, r.stdout, r.stderr)
	}
	if r := aw.cli(nil, "", "alerts", "get", "no-such-alert"); r.code != exitNotFound {
		t.Errorf("get missing: %d %s", r.code, r.stderr)
	}
}

// For an AI agent, alerts_list answers at most 50 Alerts, by default and
// whatever it asks (plan decision 13); the CLI keeps the API's 100.
func TestMCPAlertsCapped(t *testing.T) {
	aw := newAlertWorld(t, 60)
	_, got := aw.session(t, nil, nil,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+meta+`}}`,
		call(2, "alerts_list", `{}`),
		call(3, "alerts_list", `{"limit":10}`),
		call(4, "alerts_list", `{"limit":51}`),
		call(5, "alerts_summary", `{}`),
	)
	for _, tl := range got[1]["result"].(map[string]any)["tools"].([]any) {
		tool := tl.(map[string]any)
		if tool["name"] != "alerts_list" {
			continue
		}
		limit := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)["limit"].(map[string]any)
		if limit["maximum"] != float64(50) {
			t.Errorf("alerts_list's limit schema = %v, want maximum 50", limit)
		}
	}
	count := func(id int) int {
		text, isErr := toolText(t, got[id])
		var list []resource.Alert
		if isErr || json.Unmarshal([]byte(text), &list) != nil {
			t.Fatalf("call %d: %v %q", id, isErr, text)
		}
		return len(list)
	}
	if n := count(2); n != 50 {
		t.Errorf("alerts_list with no limit: %d Alerts, want 50", n)
	}
	if n := count(3); n != 10 {
		t.Errorf("alerts_list limit 10: %d Alerts", n)
	}
	if text, isErr := toolText(t, got[4]); !isErr || !strings.Contains(text, "from 1 to 50") {
		t.Errorf("alerts_list limit 51: %v %q, want refused", isErr, text)
	}
	if text, isErr := toolText(t, got[5]); isErr || !strings.Contains(text, "60") {
		t.Errorf("alerts_summary counts every firing Alert: %v %q", isErr, text)
	}
	var mcpLimits []string
	for _, s := range aw.sent() {
		if strings.HasPrefix(s, "GET /v1/alerts ") {
			mcpLimits = append(mcpLimits, s)
		}
	}
	if len(mcpLimits) != 2 {
		t.Errorf("alerts_list requests = %v, want two (the refused one sends nothing)", mcpLimits)
	}

	r := aw.cli(nil, "", "alerts", "list", "--json")
	var list []resource.Alert
	if r.code != exitOK || json.Unmarshal([]byte(r.stdout), &list) != nil || len(list) != 60 {
		t.Errorf("the CLI's alerts list: %d Alerts, want all 60 (%s)", len(list), r.stderr)
	}
}

// A channel's token is in its Secret only: no CLI command or MCP tool
// prints it, whether reading the channel, testing it (the sender had the
// token in hand) or reading the Alerts it was sent for.
func TestChannelTokenNeverPrinted(t *testing.T) {
	aw := newAlertWorld(t, 2)
	var outputs []string
	keep := func(what string, r result) {
		outputs = append(outputs, what+"\n"+r.stdout+"\n"+r.stderr)
	}
	for _, args := range [][]string{
		{"channels", "list"},
		{"channels", "list", "--json"},
		{"channels", "get", aw.channelID},
		{"channels", "get", aw.channelID, "--json"},
		{"channels", "test", aw.channelID},
		{"channels", "test", aw.channelID, "--json"},
		{"alertrules", "get", aw.ruleID, "--json"},
		{"alerts", "list", "--json"},
		{"resources", "get", "notificationchannels", aw.channelID},
	} {
		r := aw.cli(nil, "", args...)
		keep(strings.Join(args, " "), r)
		if args[1] == "test" && (r.code != exitServer || !strings.Contains(r.stdout+r.stderr, "api.telegram.org")) {
			t.Errorf("%v: %d %q, want the send's failure named by host", args, r.code, r.stderr)
		}
	}
	aw.notifier.mu.Lock()
	if aw.notifier.reads != 2 {
		t.Errorf("the sender read the token %d times, want 2 (one a test send)", aw.notifier.reads)
	}
	aw.notifier.mu.Unlock()

	r, got := aw.session(t, nil, nil,
		call(1, "resources_get", fmt.Sprintf(`{"kind":"notificationchannels","id":%q}`, aw.channelID)),
		call(2, "resources_list", `{"kind":"notificationchannels"}`),
		call(3, "alerts_list", `{}`),
		call(4, "alertrules_get", fmt.Sprintf(`{"id":%q}`, aw.ruleID)),
		call(5, "notificationchannels_test", fmt.Sprintf(`{"id":%q}`, aw.channelID)),
	)
	keep("serendip mcp", r)
	for id := 1; id <= 4; id++ {
		if text, isErr := toolText(t, got[id]); isErr {
			t.Errorf("MCP call %d: %q", id, text)
		}
	}
	if e, _ := got[5]["error"].(map[string]any); e == nil || !strings.Contains(fmt.Sprint(e["message"]), "unknown tool") {
		t.Errorf("notificationchannels_test is CLI-only: %v", got[5])
	}
	if text, _ := toolText(t, got[1]); !strings.Contains(text, "secretSet") {
		t.Errorf("resources_get shows the channel without its secretSet: %q", text)
	}

	// The outputs did show the channel: its Secret by ID.
	if shown := strings.Join(outputs, "\n"); strings.Count(shown, aw.secretID) < 4 {
		t.Errorf("the channel's Secret ID shown %d times, want the channel in each read", strings.Count(shown, aw.secretID))
	}
	secret := strings.SplitN(botToken, ":", 2)[1]
	for _, out := range outputs {
		for _, leak := range []string{botToken, secret, url.QueryEscape(botToken)} {
			if strings.Contains(out, leak) {
				t.Errorf("the token reached the output of %s", strings.SplitN(out, "\n", 2)[0])
			}
		}
	}
}
