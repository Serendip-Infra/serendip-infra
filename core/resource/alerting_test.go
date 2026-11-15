package resource_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/resource"
)

func wantErr(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		return
	}
	if !errors.Is(err, resource.ErrValidation) || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want a validation error containing %q", err, want)
	}
}

func metric(m, as string, above float64, forMin int) resource.AlertCondition {
	return resource.AlertCondition{Type: resource.ConditionTypeMetric, Metric: &resource.MetricCondition{Metric: m, As: as, Above: above, ForMinutes: forMin}}
}

func TestValidateCondition(t *testing.T) {
	restarts := func(above, win int) resource.AlertCondition {
		return resource.AlertCondition{Type: resource.ConditionTypeRestarts, Restarts: &resource.RestartsCondition{Above: above, WindowMinutes: win}}
	}
	logs := func(text string, above, win int) resource.AlertCondition {
		return resource.AlertCondition{Type: resource.ConditionTypeLogMatch, LogMatch: &resource.LogMatchCondition{Text: text, Above: above, WindowMinutes: win}}
	}
	disk := resource.AlertCondition{Type: resource.ConditionTypePackageDisk, PackageDisk: &resource.PackageDiskCondition{PercentOfBudget: 85}}
	tests := []struct {
		name    string
		c       resource.AlertCondition
		builtIn bool
		err     string
	}{
		{"cpu cores", metric("cpu", "value", 0.5, 5), false, ""},
		{"memory % of limit", metric("memory", "percentOfLimit", 90, 1), false, ""},
		{"cpu % of request above 100", metric("cpu", "percentOfRequest", 250, 60), false, ""},
		{"unknown metric", metric("disk", "value", 1, 5), false, "cpu or memory"},
		{"unknown as", metric("cpu", "ratio", 1, 5), false, "percentOfLimit"},
		{"zero value", metric("cpu", "value", 0, 5), false, "more than 0"},
		{"limit over 100", metric("memory", "percentOfLimit", 101, 5), false, "at most 100"},
		{"request over 1000", metric("cpu", "percentOfRequest", 1001, 5), false, "at most 1000"},
		{"for 0", metric("cpu", "value", 1, 0), false, "1 to 60"},
		{"for 61", metric("cpu", "value", 1, 61), false, "1 to 60"},
		{"restarts", restarts(3, 10), false, ""},
		{"any restart", restarts(0, 1), false, ""},
		{"restarts above cap", restarts(1000, 10), false, "0 to 999"},
		{"restarts negative", restarts(-1, 10), false, "0 to 999"},
		{"restarts window", restarts(1, 61), false, "1 to 60"},
		{"log match", logs("panic:", 0, 5), false, ""},
		{"log match at cap", logs("x", 999, 5), false, ""},
		{"log match above cap", logs("x", 1000, 5), false, "1000 lines"},
		{"log match no text", logs("  ", 1, 5), false, "text is required"},
		{"log match control char", logs("a\nb", 1, 5), false, "control character"},
		{"log match long text", logs(strings.Repeat("x", 201), 1, 5), false, "at most 200"},
		{"disk built in", disk, true, ""},
		{"disk by a person", disk, false, "built-in rule only"},
		{"disk over 100", resource.AlertCondition{Type: resource.ConditionTypePackageDisk, PackageDisk: &resource.PackageDiskCondition{PercentOfBudget: 120}}, true, "at most 100"},
		{"none set", resource.AlertCondition{Type: resource.ConditionTypeMetric}, false, "exactly one"},
		{"two set", resource.AlertCondition{Type: resource.ConditionTypeRestarts, Restarts: &resource.RestartsCondition{WindowMinutes: 1}, LogMatch: &resource.LogMatchCondition{Text: "x", WindowMinutes: 1}}, false, "exactly one"},
		{"type mismatch", resource.AlertCondition{Type: resource.ConditionTypeMetric, Restarts: &resource.RestartsCondition{WindowMinutes: 1}}, false, "metric is required"},
		{"unknown type", resource.AlertCondition{Type: "query", Restarts: &resource.RestartsCondition{WindowMinutes: 1}}, false, "condition.type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { wantErr(t, resource.ValidateCondition(tt.c, tt.builtIn), tt.err) })
	}
}

func TestValidateAlertRule(t *testing.T) {
	comp := resource.Metadata{Name: "r", OwnerRefs: []resource.OwnerRef{{Kind: resource.KindComponent, ID: "c1"}}}
	good := func() resource.AlertRuleSpec {
		return resource.AlertRuleSpec{EnvironmentID: "e1", Condition: metric("cpu", "value", 1, 5), Severity: resource.SeverityWarning, Summary: "CPU high", ChannelIDs: []string{"ch1"}}
	}
	builtIn := resource.Metadata{Name: "disk", OwnerRefs: []resource.OwnerRef{{Kind: resource.KindProviderInstance, ID: "pi"}}}
	diskRule := resource.AlertRuleSpec{BuiltIn: true, Condition: resource.AlertCondition{Type: resource.ConditionTypePackageDisk, PackageDisk: &resource.PackageDiskCondition{PercentOfBudget: 85}}, Severity: resource.SeverityWarning, Summary: "disk"}
	tests := []struct {
		name string
		m    resource.Metadata
		mod  func(*resource.AlertRuleSpec)
		err  string
	}{
		{"good", comp, nil, ""},
		{"owned by an Application", resource.Metadata{Name: "r", OwnerRefs: []resource.OwnerRef{{Kind: resource.KindApplication, ID: "a"}}}, nil, ""},
		{"no owner", resource.Metadata{Name: "r"}, nil, "exactly one owner"},
		{"owned by a Project", resource.Metadata{Name: "r", OwnerRefs: []resource.OwnerRef{{Kind: resource.KindProject, ID: "p"}}}, nil, "Component or an Application"},
		{"two owners", resource.Metadata{Name: "r", OwnerRefs: []resource.OwnerRef{{Kind: resource.KindComponent, ID: "a"}, {Kind: resource.KindComponent, ID: "b"}}}, nil, "exactly one owner"},
		{"claims built in", comp, func(s *resource.AlertRuleSpec) { s.BuiltIn = true }, "ProviderInstance"},
		{"no environment", comp, func(s *resource.AlertRuleSpec) { s.EnvironmentID = "" }, "environmentId is required"},
		{"bad environment", comp, func(s *resource.AlertRuleSpec) { s.EnvironmentID = "a/b" }, "not a resource ID"},
		{"no severity", comp, func(s *resource.AlertRuleSpec) { s.Severity = "" }, "severity"},
		{"bad severity", comp, func(s *resource.AlertRuleSpec) { s.Severity = "page" }, "severity"},
		{"no summary", comp, func(s *resource.AlertRuleSpec) { s.Summary = "" }, "summary is required"},
		{"long summary", comp, func(s *resource.AlertRuleSpec) { s.Summary = strings.Repeat("é", 201) }, "at most 200"},
		{"summary at cap", comp, func(s *resource.AlertRuleSpec) { s.Summary = strings.Repeat("é", 200) }, ""},
		{"no channels: Project defaults", comp, func(s *resource.AlertRuleSpec) { s.ChannelIDs = nil }, ""},
		{"channel twice", comp, func(s *resource.AlertRuleSpec) { s.ChannelIDs = []string{"a", "a"} }, "named twice"},
		{"bad channel ID", comp, func(s *resource.AlertRuleSpec) { s.ChannelIDs = []string{"../x"} }, "not a resource ID"},
		{"too many channels", comp, func(s *resource.AlertRuleSpec) {
			s.ChannelIDs = nil
			for i := range 21 {
				s.ChannelIDs = append(s.ChannelIDs, "ch"+strings.Repeat("x", i))
			}
		}, "at most 20"},
		{"interval 60", comp, func(s *resource.AlertRuleSpec) { s.IntervalMinutes = 60 }, ""},
		{"interval 61", comp, func(s *resource.AlertRuleSpec) { s.IntervalMinutes = 61 }, "1 to 60"},
		{"interval negative", comp, func(s *resource.AlertRuleSpec) { s.IntervalMinutes = -1 }, "1 to 60"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := good()
			if tt.mod != nil {
				tt.mod(&s)
			}
			wantErr(t, resource.ValidateAlertRule(tt.m, s), tt.err)
		})
	}
	t.Run("built in", func(t *testing.T) { wantErr(t, resource.ValidateAlertRule(builtIn, diskRule), "") })
	t.Run("built in with an environment", func(t *testing.T) {
		s := diskRule
		s.EnvironmentID = "e1"
		wantErr(t, resource.ValidateAlertRule(builtIn, s), "watches the package")
	})
	t.Run("built in on a Component", func(t *testing.T) { wantErr(t, resource.ValidateAlertRule(comp, diskRule), "ProviderInstance") })
}

func TestAlertRuleDefaultsAndCap(t *testing.T) {
	var s resource.AlertRuleSpec
	if !s.IsEnabled() || s.Interval() != time.Minute {
		t.Fatalf("defaults: enabled %v, interval %v", s.IsEnabled(), s.Interval())
	}
	off := false
	disabled := resource.AlertRuleSpec{Enabled: &off, IntervalMinutes: 5}
	if disabled.IsEnabled() || disabled.Interval() != 5*time.Minute {
		t.Fatal("enabled: false or the interval was ignored")
	}
	wantErr(t, resource.ValidateEnabledRuleCount(199, s), "")
	wantErr(t, resource.ValidateEnabledRuleCount(200, s), "at most 200 enabled rules")
	wantErr(t, resource.ValidateEnabledRuleCount(200, disabled), "")
}

func TestValidateNotificationChannel(t *testing.T) {
	platform := resource.Metadata{Name: "c"}
	project := resource.Metadata{Name: "c", OwnerRefs: []resource.OwnerRef{{Kind: resource.KindProject, ID: "p"}}}
	tg := func(chat string) resource.NotificationChannelSpec {
		return resource.NotificationChannelSpec{Type: resource.ChannelTelegram, Telegram: &resource.TelegramSettings{ChatID: chat, BotTokenSecretID: "s1"}}
	}
	mail := func(to ...string) resource.NotificationChannelSpec {
		return resource.NotificationChannelSpec{Type: resource.ChannelEmail, Email: &resource.EmailSettings{SMTPServerID: "smtp", To: to}}
	}
	hook := func(w resource.WebhookSettings) resource.NotificationChannelSpec {
		return resource.NotificationChannelSpec{Type: resource.ChannelWebhook, Webhook: &w}
	}
	with := func(s resource.NotificationChannelSpec, f func(*resource.NotificationChannelSpec)) resource.NotificationChannelSpec {
		f(&s)
		return s
	}
	allow := &resource.Allowance{Reason: "in-house receiver"}
	many := make([]string, 21)
	for i := range many {
		many[i] = "u" + strings.Repeat("x", i) + "@example.com"
	}
	tests := []struct {
		name string
		m    resource.Metadata
		s    resource.NotificationChannelSpec
		err  string
	}{
		{"telegram private chat", platform, tg("123456789"), ""},
		{"telegram group", project, tg("-1001234567890"), ""},
		{"telegram channel name", platform, tg("@serendip_alerts"), ""},
		{"telegram bad chat", platform, tg("12ab"), "chatId"},
		{"telegram no token", platform, with(tg("1"), func(s *resource.NotificationChannelSpec) { s.Telegram.BotTokenSecretID = "" }), "botTokenSecretId is required"},
		{"telegram with allowance", platform, with(tg("1"), func(s *resource.NotificationChannelSpec) { s.AllowPrivateDestination = allow }), "no allowances"},
		{"min severity critical", platform, with(tg("1"), func(s *resource.NotificationChannelSpec) { s.MinSeverity = resource.SeverityCritical }), ""},
		{"bad min severity", platform, with(tg("1"), func(s *resource.NotificationChannelSpec) { s.MinSeverity = "high" }), "minSeverity"},
		{"owned by a Component", resource.Metadata{Name: "c", OwnerRefs: []resource.OwnerRef{{Kind: resource.KindComponent, ID: "x"}}}, tg("1"), "one Project"},
		{"email", project, mail("ops@example.com", "dev@example.com"), ""},
		{"email no recipients", project, mail(), "1 to 20"},
		{"email 21 recipients", project, mail(many...), "1 to 20"},
		{"email 20 recipients", project, mail(many[:20]...), ""},
		{"email display name", project, mail("Ops <ops@example.com>"), "bare email address"},
		{"email header injection", project, mail("ops@example.com\r\nBcc: x@evil.test"), "line break"},
		{"email twice", project, mail("a@example.com", "A@example.com"), "listed twice"},
		{"email no server", project, with(mail("a@example.com"), func(s *resource.NotificationChannelSpec) { s.Email.SMTPServerID = "" }), "smtpServerId is required"},
		{"email with allowance", project, with(mail("a@example.com"), func(s *resource.NotificationChannelSpec) { s.AllowPlainHTTP = allow }), "no allowances"},
		{"webhook", platform, hook(resource.WebhookSettings{URL: "https://hooks.example.com/a?b=c", SigningSecretID: "sig"}), ""},
		{"webhook with port", platform, hook(resource.WebhookSettings{URL: "https://hooks.example.com:8443/a"}), ""},
		{"webhook plain http", platform, hook(resource.WebhookSettings{URL: "http://hooks.example.com/a"}), "HTTPS only"},
		{"webhook plain http allowed", platform, with(hook(resource.WebhookSettings{URL: "http://10.0.0.5/a"}), func(s *resource.NotificationChannelSpec) { s.AllowPlainHTTP = allow }), ""},
		{"allowance without reason", platform, with(hook(resource.WebhookSettings{URL: "http://10.0.0.5/a"}), func(s *resource.NotificationChannelSpec) { s.AllowPlainHTTP = &resource.Allowance{} }), "reason is required"},
		{"webhook other scheme", platform, hook(resource.WebhookSettings{URL: "ftp://hooks.example.com/a"}), "an https URL"},
		{"webhook credentials", platform, hook(resource.WebhookSettings{URL: "https://u:p@hooks.example.com/a"}), "no credentials"},
		{"webhook fragment", platform, hook(resource.WebhookSettings{URL: "https://hooks.example.com/a#x"}), "no fragment"},
		{"webhook no host", platform, hook(resource.WebhookSettings{URL: "https:///a"}), "is required"},
		{"webhook bad port", platform, hook(resource.WebhookSettings{URL: "https://hooks.example.com:99999/a"}), "port"},
		{"webhook long url", platform, hook(resource.WebhookSettings{URL: "https://hooks.example.com/" + strings.Repeat("a", 2048)}), "at most 2048"},
		{"webhook url and secret", platform, hook(resource.WebhookSettings{URL: "https://h.example.com", URLSecretID: "s", Host: "h.example.com"}), "set url, or urlSecretId"},
		{"webhook neither", platform, hook(resource.WebhookSettings{}), "set url, or urlSecretId"},
		{"webhook url secret", platform, hook(resource.WebhookSettings{URLSecretID: "s", Host: "n8n.example.com"}), ""},
		{"webhook url secret, IPv6 host", platform, hook(resource.WebhookSettings{URLSecretID: "s", Host: "[2001:db8::1]:8443"}), ""},
		{"webhook url secret, no host", platform, hook(resource.WebhookSettings{URLSecretID: "s"}), "webhook.host is required"},
		{"webhook url secret, bad host", platform, hook(resource.WebhookSettings{URLSecretID: "s", Host: "n8n.example.com/path"}), "not a host name"},
		{"webhook host beside url", platform, hook(resource.WebhookSettings{URL: "https://a.example.com", Host: "b.example.com"}), "taken from url"},
		{"no settings", platform, resource.NotificationChannelSpec{Type: resource.ChannelWebhook}, "exactly one"},
		{"settings mismatch", platform, resource.NotificationChannelSpec{Type: resource.ChannelEmail, Telegram: &resource.TelegramSettings{}}, "email is required"},
		{"unknown type", platform, resource.NotificationChannelSpec{Type: "slack", Webhook: &resource.WebhookSettings{}}, "type: telegram, email or webhook"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { wantErr(t, resource.ValidateNotificationChannel(tt.m, tt.s), tt.err) })
	}
}

func TestValidateSMTPServer(t *testing.T) {
	good := resource.SMTPServerSpec{Host: "smtp.gmail.com", Port: 587, TLS: resource.SMTPStartTLS, From: "me@gmail.com", UsernameSecretID: "u", PasswordSecretID: "p"}
	tests := []struct {
		name string
		m    resource.Metadata
		mod  func(*resource.SMTPServerSpec)
		err  string
	}{
		{"good", resource.Metadata{Name: "s"}, nil, ""},
		{"implicit 465", resource.Metadata{Name: "s"}, func(s *resource.SMTPServerSpec) { s.Port, s.TLS = 465, resource.SMTPImplicit }, ""},
		{"owned", resource.Metadata{Name: "s", OwnerRefs: []resource.OwnerRef{{Kind: resource.KindProject, ID: "p"}}}, nil, "platform"},
		{"plain", resource.Metadata{Name: "s"}, func(s *resource.SMTPServerSpec) { s.TLS = "none" }, "plain SMTP isn't offered"},
		{"no tls", resource.Metadata{Name: "s"}, func(s *resource.SMTPServerSpec) { s.TLS = "" }, "starttls or implicit"},
		{"host with port", resource.Metadata{Name: "s"}, func(s *resource.SMTPServerSpec) { s.Host = "smtp.gmail.com:587" }, "without a port"},
		{"port 0", resource.Metadata{Name: "s"}, func(s *resource.SMTPServerSpec) { s.Port = 0 }, "1 to 65535"},
		{"bad from", resource.Metadata{Name: "s"}, func(s *resource.SMTPServerSpec) { s.From = "not an address" }, "bare email address"},
		{"no password", resource.Metadata{Name: "s"}, func(s *resource.SMTPServerSpec) { s.PasswordSecretID = "" }, "passwordSecretId is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := good
			if tt.mod != nil {
				tt.mod(&s)
			}
			wantErr(t, resource.ValidateSMTPServer(tt.m, s), tt.err)
		})
	}
}

func TestValidateHeartbeat(t *testing.T) {
	good := resource.HeartbeatSpec{URLSecretID: "u", Host: "hc-ping.com"}
	tests := []struct {
		name string
		mod  func(*resource.HeartbeatSpec)
		err  string
	}{
		{"good", nil, ""},
		{"post every 10", func(s *resource.HeartbeatSpec) { s.Method, s.IntervalMinutes = "POST", 10 }, ""},
		{"interval 11", func(s *resource.HeartbeatSpec) { s.IntervalMinutes = 11 }, "1 to 10"},
		{"put", func(s *resource.HeartbeatSpec) { s.Method = "PUT" }, "GET or POST"},
		{"no url", func(s *resource.HeartbeatSpec) { s.URLSecretID = "" }, "urlSecretId is required"},
		{"no host", func(s *resource.HeartbeatSpec) { s.Host = "" }, "host is required"},
		{"lan monitor", func(s *resource.HeartbeatSpec) {
			s.Host, s.AllowPrivateDestination = "192.168.1.30:8080", &resource.Allowance{Reason: "Gatus on the Pi"}
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := good
			if tt.mod != nil {
				tt.mod(&s)
			}
			wantErr(t, resource.ValidateHeartbeat(resource.Metadata{Name: "heartbeat"}, s), tt.err)
		})
	}
	wantErr(t, resource.ValidateHeartbeat(resource.Metadata{Name: "h", OwnerRefs: []resource.OwnerRef{{Kind: resource.KindProject, ID: "p"}}}, good), "platform")
	if good.Interval() != time.Minute || resource.HeartbeatKey(good) != resource.HeartbeatKey(resource.HeartbeatSpec{}) {
		t.Fatal("heartbeat default interval or singleton key")
	}
}

func TestAlertingRelationships(t *testing.T) {
	dep := func(k resource.Kind, id string) resource.Relationship {
		return resource.Relationship{Type: resource.EdgeDependsOn, TargetKind: k, TargetID: id}
	}
	tests := []struct {
		name string
		got  []resource.Relationship
		want []resource.Relationship
	}{
		{"rule to channels", resource.AlertRuleRelationships(resource.AlertRuleSpec{ChannelIDs: []string{"a", "b"}}),
			[]resource.Relationship{dep(resource.KindNotificationChannel, "a"), dep(resource.KindNotificationChannel, "b")}},
		{"rule with defaults", resource.AlertRuleRelationships(resource.AlertRuleSpec{}), []resource.Relationship{}},
		{"telegram to its token", resource.NotificationChannelRelationships(resource.NotificationChannelSpec{Telegram: &resource.TelegramSettings{BotTokenSecretID: "t"}}),
			[]resource.Relationship{dep(resource.KindSecret, "t")}},
		{"email to its server", resource.NotificationChannelRelationships(resource.NotificationChannelSpec{Email: &resource.EmailSettings{SMTPServerID: "s"}}),
			[]resource.Relationship{dep(resource.KindSMTPServer, "s")}},
		{"webhook to both secrets", resource.NotificationChannelRelationships(resource.NotificationChannelSpec{Webhook: &resource.WebhookSettings{URLSecretID: "u", SigningSecretID: "g"}}),
			[]resource.Relationship{dep(resource.KindSecret, "u"), dep(resource.KindSecret, "g")}},
		{"webhook by url", resource.NotificationChannelRelationships(resource.NotificationChannelSpec{Webhook: &resource.WebhookSettings{URL: "https://x"}}), nil},
		{"smtp to its secrets", resource.SMTPServerRelationships(resource.SMTPServerSpec{UsernameSecretID: "u", PasswordSecretID: "p"}),
			[]resource.Relationship{dep(resource.KindSecret, "u"), dep(resource.KindSecret, "p")}},
		{"smtp, one secret for both", resource.SMTPServerRelationships(resource.SMTPServerSpec{UsernameSecretID: "u", PasswordSecretID: "u"}),
			[]resource.Relationship{dep(resource.KindSecret, "u")}},
		{"heartbeat to its url", resource.HeartbeatRelationships(resource.HeartbeatSpec{URLSecretID: "u"}), []resource.Relationship{dep(resource.KindSecret, "u")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !slices.Equal(tt.got, tt.want) {
				t.Fatalf("got %+v, want %+v", tt.got, tt.want)
			}
		})
	}
	for _, k := range []resource.Kind{resource.KindSecret, resource.KindNotificationChannel, resource.KindSMTPServer} {
		if !resource.RefusesDeleteWhileUsed(k) {
			t.Errorf("%s should refuse deletion while used", k)
		}
	}
	for _, k := range []resource.Kind{resource.KindAlertRule, resource.KindComponent, resource.KindHeartbeat} {
		if resource.RefusesDeleteWhileUsed(k) {
			t.Errorf("%s shouldn't refuse deletion while used", k)
		}
	}
}

func TestSeverityMeets(t *testing.T) {
	tests := []struct {
		s, min resource.Severity
		want   bool
	}{
		{resource.SeverityInfo, "", true},
		{resource.SeverityInfo, resource.SeverityWarning, false},
		{resource.SeverityWarning, resource.SeverityWarning, true},
		{resource.SeverityCritical, resource.SeverityWarning, true},
		{resource.SeverityWarning, resource.SeverityCritical, false},
		{"", resource.SeverityInfo, false},
	}
	for _, tt := range tests {
		if got := tt.s.Meets(tt.min); got != tt.want {
			t.Errorf("%q.Meets(%q) = %v", tt.s, tt.min, got)
		}
	}
}

func TestValidateAlertAndDeliveries(t *testing.T) {
	m := resource.Metadata{Name: "a", OwnerRefs: []resource.OwnerRef{{Kind: resource.KindAlertRule, ID: "r"}}}
	spec := resource.AlertSpec{RuleID: "r", ComponentID: "c", EnvironmentID: "e", Severity: resource.SeverityCritical, Summary: "x",
		Fingerprint: resource.AlertFingerprint("r", "c", "e")}
	st := resource.AlertStatus{State: resource.AlertFiring}
	wantErr(t, resource.ValidateAlert(m, spec, st), "")
	wantErr(t, resource.ValidateAlert(resource.Metadata{Name: "a"}, spec, st), "owned by its rule")
	bad := spec
	bad.Fingerprint = "r|c|other"
	wantErr(t, resource.ValidateAlert(m, bad, st), "fingerprint")
	wantErr(t, resource.ValidateAlert(m, spec, resource.AlertStatus{State: "open"}), "state")

	// The cap keeps every pending delivery and drops the oldest finished.
	s := resource.AlertStatus{State: resource.AlertFiring}
	s.AddDelivery(resource.AlertDelivery{ChannelID: "pending-0", State: resource.DeliveryPending})
	for i := range resource.MaxAlertDeliveries + 5 {
		s.AddDelivery(resource.AlertDelivery{ChannelID: "sent-" + string(rune('a'+i)), State: resource.DeliverySent})
	}
	if len(s.Deliveries) != resource.MaxAlertDeliveries || s.Deliveries[0].ChannelID != "pending-0" || s.Deliveries[1].ChannelID != "sent-g" {
		t.Fatalf("deliveries after the cap: %d, first %q, second %q", len(s.Deliveries), s.Deliveries[0].ChannelID, s.Deliveries[1].ChannelID)
	}
	wantErr(t, resource.ValidateAlert(m, spec, s), "")
	all := resource.AlertStatus{State: resource.AlertFiring}
	for range resource.MaxAlertDeliveries + 1 {
		all.AddDelivery(resource.AlertDelivery{State: resource.DeliveryPending})
	}
	if len(all.Deliveries) != resource.MaxAlertDeliveries+1 {
		t.Fatalf("a pending delivery was dropped: %d left", len(all.Deliveries))
	}
	wantErr(t, resource.ValidateAlert(m, spec, all), "")
	over := resource.AlertStatus{State: resource.AlertResolved}
	for range resource.MaxAlertDeliveries + 1 {
		over.Deliveries = append(over.Deliveries, resource.AlertDelivery{State: resource.DeliveryFailed})
	}
	wantErr(t, resource.ValidateAlert(m, spec, over), "at most 20 finished")
}
