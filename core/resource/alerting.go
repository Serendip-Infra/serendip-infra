package resource

import (
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Alerting kinds (specs/024-alerts plan decision 1). KindAlert is the
// operational kind resource-model.md names; spec 024 gives it a shape.
const (
	KindAlert               Kind = "Alert"
	KindAlertRule           Kind = "AlertRule"
	KindNotificationChannel Kind = "NotificationChannel"
	KindSMTPServer          Kind = "SMTPServer"
	KindHeartbeat           Kind = "Heartbeat"
)

// Caps on rules, channels and the heartbeat (plan decisions 1, 2 and 15).
const (
	// MaxEnabledAlertRules bounds the evaluator's query load: 200 rules at
	// one minute is about 3.3 queries a second.
	MaxEnabledAlertRules = 200
	MinIntervalMinutes   = 1
	MaxIntervalMinutes   = 60
	// MaxCountAbove is the highest count a restarts or logMatch rule can
	// compare with: the logs adapter's page limit is 1000 (RP-073).
	MaxCountAbove          = 999
	MaxEmailRecipients     = 20
	MaxRuleChannels        = 20
	MaxAlertDeliveries     = 20
	MaxAlertSummaryLen     = 200
	MaxLogMatchTextLen     = 200
	MaxAllowanceReason     = 500
	MaxWebhookURLLen       = 2048
	MinHeartbeatMinutes    = 1
	MaxHeartbeatMinutes    = 10
	DefaultIntervalMinutes = 1
	MaxSilenceReason       = 500
	// MaxSilence is the longest a rule can be silenced in one go, so a
	// forgotten silence ends by itself (spec 024 T007).
	MaxSilence = 30 * 24 * time.Hour
)

// Severity is an alert's severity, and a channel's minimum.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// rank orders severities; 0 is not a severity.
func (s Severity) rank() int {
	switch s {
	case SeverityInfo:
		return 1
	case SeverityWarning:
		return 2
	case SeverityCritical:
		return 3
	}
	return 0
}

// Meets says whether s is at or above min. An empty min is info, so it
// lets every severity through (plan decision 6, severity routing).
func (s Severity) Meets(min Severity) bool {
	if min == "" {
		min = SeverityInfo
	}
	return s.rank() > 0 && s.rank() >= min.rank()
}

func validSeverity(s Severity) bool { return s.rank() > 0 }

// Condition types (plan decision 3). Each is a fixed query Serendip
// builds; none takes a query language.
const (
	ConditionTypeMetric      = "metric"
	ConditionTypeRestarts    = "restarts"
	ConditionTypeLogMatch    = "logMatch"
	ConditionTypePackageDisk = "packageDisk"
)

// Metric condition settings.
const (
	MetricCPU    = "cpu"
	MetricMemory = "memory"

	MetricAsValue            = "value"
	MetricAsPercentOfRequest = "percentOfRequest"
	MetricAsPercentOfLimit   = "percentOfLimit"
)

// AlertCondition is what a rule checks. Type names the one settings block
// that's set.
type AlertCondition struct {
	Type        string                `json:"type"`
	Metric      *MetricCondition      `json:"metric,omitempty"`
	Restarts    *RestartsCondition    `json:"restarts,omitempty"`
	LogMatch    *LogMatchCondition    `json:"logMatch,omitempty"`
	PackageDisk *PackageDiskCondition `json:"packageDisk,omitempty"`
}

// MetricCondition fires when every point over the last ForMinutes is
// above Above. As says what Above is measured in: millicores or MiB for
// "value" (the units Serendip's metrics are read in), a percentage for
// the other two.
type MetricCondition struct {
	Metric     string  `json:"metric"`
	As         string  `json:"as"`
	Above      float64 `json:"above"`
	ForMinutes int     `json:"forMinutes"`
}

// RestartsCondition fires when the Component's containers restarted more
// than Above times within the last WindowMinutes.
type RestartsCondition struct {
	Above         int `json:"above"`
	WindowMinutes int `json:"windowMinutes"`
}

// LogMatchCondition fires when more than Above lines containing Text were
// stored within the last WindowMinutes. Alerts carry the count, never the
// lines (RP-067).
type LogMatchCondition struct {
	Text          string `json:"text"`
	Above         int    `json:"above"`
	WindowMinutes int    `json:"windowMinutes"`
}

// PackageDiskCondition fires when the observability package's stored data
// is above PercentOfBudget of its disk budget. Built-in rules only (plan
// decision 4).
type PackageDiskCondition struct {
	PercentOfBudget float64 `json:"percentOfBudget"`
}

// AlertRuleSpec is a rule, owned by a Component or an Application (or,
// built in, by the observability package's ProviderInstance).
type AlertRuleSpec struct {
	// EnvironmentID is the Environment the rule watches. A built-in
	// rule has none.
	EnvironmentID string         `json:"environmentId,omitempty"`
	Condition     AlertCondition `json:"condition"`
	Severity      Severity       `json:"severity"`
	Summary       string         `json:"summary"`
	// ChannelIDs are the NotificationChannels it notifies; none means
	// the owning Project's default channels (the platform's, for a
	// built-in rule).
	ChannelIDs      []string `json:"channelIds,omitempty"`
	IntervalMinutes int      `json:"intervalMinutes,omitempty"`
	// Enabled defaults to true when absent.
	Enabled *bool `json:"enabled,omitempty"`
	// BuiltIn marks the rule serendipd keeps for a package (decision 4):
	// it can be changed, silenced or disabled, not deleted. Only
	// serendipd sets it.
	BuiltIn bool `json:"builtIn,omitempty"`
}

// KeptByOwner refuses deleting a built-in rule on its own (plan decision
// 4): it goes with its package.
func (s AlertRuleSpec) KeptByOwner() string {
	if s.BuiltIn {
		return "a built-in rule goes with its package; disable or silence it instead"
	}
	return ""
}

// IsEnabled is Enabled, defaulting to true.
func (s AlertRuleSpec) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

// Interval is the rule's evaluation interval, with the default applied.
func (s AlertRuleSpec) Interval() time.Duration {
	m := s.IntervalMinutes
	if m == 0 {
		m = DefaultIntervalMinutes
	}
	return time.Duration(m) * time.Minute
}

// AlertRule condition types and reasons (decision 2, spec IN 3).
const (
	AlertRuleConditionEvaluating = "Evaluating"

	ReasonPackageNotReady  = "PackageNotReady"
	ReasonComponentGone    = "ComponentGone"
	ReasonConditionRefused = "ConditionRefused"
	ReasonQueryFailed      = "QueryFailed"
	ReasonDisabled         = "Disabled"
	// ReasonNotDeployed: nothing of the rule's runs in its Environment.
	ReasonNotDeployed = "NotDeployed"
	ReasonNoData      = "NoData"
)

// AlertSilence suppresses a rule's deliveries, not its Alerts, until
// Until (spec AC 8).
type AlertSilence struct {
	By     string    `json:"by"`
	Reason string    `json:"reason"`
	Until  time.Time `json:"until"`
}

// ValidateSilence checks a silence asked for at now: a reason, and an
// end after now and at most MaxSilence away.
func ValidateSilence(s AlertSilence, now time.Time) error {
	if err := plainText("reason", s.Reason, MaxSilenceReason); err != nil {
		return err
	}
	switch {
	case s.Until.IsZero():
		return invalid("until is required (an RFC 3339 time)")
	case !s.Until.After(now):
		return invalid("until must be in the future")
	case s.Until.Sub(now) > MaxSilence:
		return invalid("until: at most %d days from now", int(MaxSilence/(24*time.Hour)))
	}
	return nil
}

// AlertRuleStatus is written by serendipd. LastValue is flushed at most
// once a minute (decision 2), not on every check. LastEvaluated goes with
// it; while the value holds, it's refreshed hourly, and the API answers
// the evaluator's live time (RP-080).
type AlertRuleStatus struct {
	Status
	Active        bool          `json:"active"`
	ActiveReason  string        `json:"activeReason,omitempty"`
	LastEvaluated *time.Time    `json:"lastEvaluated,omitempty"`
	LastValue     *float64      `json:"lastValue,omitempty"`
	Silence       *AlertSilence `json:"silence,omitempty"`
}

type AlertRule = Resource[AlertRuleSpec, AlertRuleStatus]

// AlertSpec is fixed when serendipd opens the Alert: one firing of one
// rule for one Component in one Environment. Alerts are written only by
// serendipd and have no generic create, update or delete route.
type AlertSpec struct {
	RuleID        string   `json:"ruleId"`
	ComponentID   string   `json:"componentId,omitempty"`
	EnvironmentID string   `json:"environmentId,omitempty"`
	Severity      Severity `json:"severity"`
	Summary       string   `json:"summary"`
	Fingerprint   string   `json:"fingerprint"`
}

// AlertFingerprint is the key a rule's firings fold under (decision 2):
// one open Alert per rule, Component and Environment.
func AlertFingerprint(ruleID, componentID, environmentID string) string {
	return ruleID + "|" + componentID + "|" + environmentID
}

// Alert states, resolution reasons, delivery events and delivery states.
const (
	AlertFiring   = "firing"
	AlertResolved = "resolved"

	ResolvedClear          = "clear"
	ResolvedRuleDeleted    = "ruleDeleted"
	ResolvedPackageRemoved = "packageRemoved"
	ResolvedComponentGone  = "componentGone"
	// ResolvedRuleDisabled: the rule was disabled while its Alert fired
	// (spec 024 T005), so nothing watches it any more.
	ResolvedRuleDisabled = "ruleDisabled"

	DeliveryEventFiring   = "firing"
	DeliveryEventResolved = "resolved"

	DeliveryPending          = "pending"
	DeliverySent             = "sent"
	DeliveryFailed           = "failed"
	DeliverySilenced         = "silenced"
	DeliveryBelowMinSeverity = "belowMinSeverity"
)

// AlertAck records who acknowledged an Alert.
type AlertAck struct {
	By string    `json:"by"`
	At time.Time `json:"at"`
}

// AlertDelivery is one notification of one event to one channel. A
// pending one is resumed after a restart (decision 7).
type AlertDelivery struct {
	ChannelID string `json:"channelId"`
	Event     string `json:"event"`
	State     string `json:"state"`
	Attempts  int    `json:"attempts,omitempty"`
	// LastError is cleaned down to the host before it's stored
	// (decision 8): never a token or a URL path.
	LastError   string     `json:"lastError,omitempty"`
	At          time.Time  `json:"at"`
	NextAttempt *time.Time `json:"nextAttempt,omitempty"`
	// ThreadRef is what a resolution replies to: Telegram's message_id
	// or the email's Message-ID.
	ThreadRef string `json:"threadRef,omitempty"`
}

// AlertStatus is the Alert's lifecycle, written by serendipd.
type AlertStatus struct {
	Status
	State          string          `json:"state"`
	StartedAt      time.Time       `json:"startedAt"`
	ResolvedAt     *time.Time      `json:"resolvedAt,omitempty"`
	ResolvedReason string          `json:"resolvedReason,omitempty"`
	Value          float64         `json:"value"`
	Threshold      float64         `json:"threshold"`
	Acknowledged   *AlertAck       `json:"acknowledged,omitempty"`
	Deliveries     []AlertDelivery `json:"deliveries,omitempty"`
}

type Alert = Resource[AlertSpec, AlertStatus]

// AddDelivery appends d, keeping at most MaxAlertDeliveries: the oldest
// finished deliveries go first, and pending ones are never dropped, so
// a burst of pending ones can briefly go past the cap.
func (s *AlertStatus) AddDelivery(d AlertDelivery) {
	s.Deliveries = append(s.Deliveries, d)
	for len(s.Deliveries) > MaxAlertDeliveries {
		i := 0
		for i < len(s.Deliveries) && s.Deliveries[i].State == DeliveryPending {
			i++
		}
		if i == len(s.Deliveries) {
			return // all pending: the delivery loop drains them
		}
		s.Deliveries = append(s.Deliveries[:i], s.Deliveries[i+1:]...)
	}
}

// Channel types (plan decision 6).
const (
	ChannelTelegram = "telegram"
	ChannelEmail    = "email"
	ChannelWebhook  = "webhook"
)

// Allowance is an Owner's recorded exception for one channel (decision
// 5): reaching a private address, or plain HTTP.
type Allowance struct {
	Reason string `json:"reason"`
}

// TelegramSettings: the host is fixed (the Bot API's), so no person-typed
// address is involved. The bot token is the whole credential (RP-069).
type TelegramSettings struct {
	ChatID           string `json:"chatId"`
	BotTokenSecretID string `json:"botTokenSecretId"`
}

// EmailSettings sends through a platform SMTPServer, so a Project can
// have email channels without holding SMTP credentials.
type EmailSettings struct {
	SMTPServerID string   `json:"smtpServerId"`
	To           []string `json:"to"`
}

// WebhookSettings: URL, or URLSecretID when the URL itself holds a token
// (an n8n webhook's path), with Host stored beside it so the address can
// be shown and checked before the value is read.
type WebhookSettings struct {
	URL             string `json:"url,omitempty"`
	URLSecretID     string `json:"urlSecretId,omitempty"`
	Host            string `json:"host,omitempty"`
	SigningSecretID string `json:"signingSecretId,omitempty"`
}

// NotificationChannelSpec is a channel, at the platform (no owner) or
// owned by a Project.
type NotificationChannelSpec struct {
	Type     string            `json:"type"`
	Telegram *TelegramSettings `json:"telegram,omitempty"`
	Email    *EmailSettings    `json:"email,omitempty"`
	Webhook  *WebhookSettings  `json:"webhook,omitempty"`
	// MinSeverity is the lowest severity it's sent; empty is info.
	MinSeverity Severity `json:"minSeverity,omitempty"`
	// Default channels are notified by rules that name none.
	Default                 bool       `json:"default,omitempty"`
	AllowPrivateDestination *Allowance `json:"allowPrivateDestination,omitempty"`
	AllowPlainHTTP          *Allowance `json:"allowPlainHTTP,omitempty"`
}

// NotificationChannelStatus records the last test send, with its
// conditions.
type NotificationChannelStatus struct {
	Status
	LastTest       *time.Time `json:"lastTest,omitempty"`
	LastTestResult string     `json:"lastTestResult,omitempty"`
}

type NotificationChannel = Resource[NotificationChannelSpec, NotificationChannelStatus]

// SMTP TLS modes. Plain SMTP is never offered: auth happens only after
// TLS (decision 5).
const (
	SMTPStartTLS = "starttls"
	SMTPImplicit = "implicit"
)

// SMTPServerSpec is a platform-only relay (Gmail with an app password,
// Brevo, ...). The credentials are Secrets.
type SMTPServerSpec struct {
	Host             string `json:"host"`
	Port             int    `json:"port"`
	TLS              string `json:"tls"`
	From             string `json:"from"`
	UsernameSecretID string `json:"usernameSecretId"`
	PasswordSecretID string `json:"passwordSecretId"`
}

type SMTPServerStatus struct {
	Status
}

type SMTPServer = Resource[SMTPServerSpec, SMTPServerStatus]

// HeartbeatSpec is the platform's one heartbeat (decision 15): a request
// to an outside monitor every interval, sent only while Serendip can
// alert. The URL is a Secret (a ping URL is its own credential).
type HeartbeatSpec struct {
	URLSecretID string `json:"urlSecretId"`
	Host        string `json:"host"`
	// Method is GET (the default) or POST, with an empty body.
	Method                  string     `json:"method,omitempty"`
	IntervalMinutes         int        `json:"intervalMinutes,omitempty"`
	AllowPrivateDestination *Allowance `json:"allowPrivateDestination,omitempty"`
}

// Interval is the heartbeat's interval, with the default applied.
func (s HeartbeatSpec) Interval() time.Duration {
	m := s.IntervalMinutes
	if m == 0 {
		m = MinHeartbeatMinutes
	}
	return time.Duration(m) * time.Minute
}

// HeartbeatConditionSending is False, with the reason, while sending is
// paused because Serendip can't alert.
const HeartbeatConditionSending = "Sending"

type HeartbeatStatus struct {
	Status
	LastSent   *time.Time `json:"lastSent,omitempty"`
	LastResult string     `json:"lastResult,omitempty"`
}

type Heartbeat = Resource[HeartbeatSpec, HeartbeatStatus]

// HeartbeatKey is the Heartbeat store's unique key: one per install.
func HeartbeatKey(HeartbeatSpec) string { return "heartbeat" }

// OwnerKept is a spec that may say it can be removed only with its owner
// (a cascade), not by a delete aimed at it: KeptByOwner returns why, or
// "" when it can be deleted alone.
type OwnerKept interface {
	KeptByOwner() string
}

// RefusesDeleteWhileUsed lists the kinds the store won't delete while
// something outside the delete still depends on them (decision 1): a
// Secret, channel or SMTP server that a channel, rule or heartbeat names.
func RefusesDeleteWhileUsed(k Kind) bool {
	switch k {
	case KindSecret, KindNotificationChannel, KindSMTPServer:
		return true
	}
	return false
}

// --- validation ---

var (
	// chatIDRE: a numeric chat ID (negative for groups and channels), or
	// a public channel's @username.
	chatIDRE = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)
	// hostnameRE: a DNS name of letters, digits, '-' and '.'.
	hostnameRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrValidation}, a...)...)
}

// plainText is non-empty, at most max runes, with no control characters.
func plainText(field, s string, max int) error {
	if strings.TrimSpace(s) == "" {
		return invalid("%s is required", field)
	}
	if len([]rune(s)) > max {
		return invalid("%s: at most %d characters", field, max)
	}
	if strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return invalid("%s contains a control character", field)
	}
	return nil
}

func minutesIn(field string, m, lo, hi int, optional bool) error {
	if optional && m == 0 {
		return nil
	}
	if m < lo || m > hi {
		return invalid("%s: %d to %d minutes", field, lo, hi)
	}
	return nil
}

func refID(field, id string, required bool) error {
	if id == "" {
		if required {
			return invalid("%s is required", field)
		}
		return nil
	}
	if !validRefID(id) {
		return invalid("%s: not a resource ID", field)
	}
	return nil
}

func validAllowance(field string, a *Allowance) error {
	if a == nil {
		return nil
	}
	if err := plainText(field+".reason", a.Reason, MaxAllowanceReason); err != nil {
		return err
	}
	return nil
}

// ValidateHost checks a host name or IP literal, with an optional port.
// Whether its addresses may be reached is the sender's check, not this
// one.
func ValidateHost(field, h string) error {
	if h == "" {
		return invalid("%s is required", field)
	}
	host := h
	if hh, port, err := net.SplitHostPort(h); err == nil {
		if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
			return invalid("%s: port must be 1 to 65535", field)
		}
		host = hh
	} else if strings.HasPrefix(h, "[") {
		return invalid("%s: not a host", field)
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	if len(host) > 253 || !hostnameRE.MatchString(host) {
		return invalid("%s: not a host name", field)
	}
	return nil
}

// ValidateCondition checks a rule's condition against its type. builtIn
// says whether the rule is serendipd's own.
func ValidateCondition(c AlertCondition, builtIn bool) error {
	set := 0
	for _, p := range []bool{c.Metric != nil, c.Restarts != nil, c.LogMatch != nil, c.PackageDisk != nil} {
		if p {
			set++
		}
	}
	if set != 1 {
		return invalid("condition: set exactly one of metric, restarts, logMatch or packageDisk, matching condition.type")
	}
	switch c.Type {
	case ConditionTypeMetric:
		m := c.Metric
		if m == nil {
			return invalid("condition.metric is required for type metric")
		}
		if m.Metric != MetricCPU && m.Metric != MetricMemory {
			return invalid("condition.metric.metric: cpu or memory")
		}
		switch m.As {
		case MetricAsValue:
			if m.Above <= 0 {
				return invalid("condition.metric.above: more than 0 (millicores for cpu, MiB for memory)")
			}
		case MetricAsPercentOfRequest:
			if m.Above <= 0 || m.Above > 1000 {
				return invalid("condition.metric.above: a percentage of the request, more than 0 and at most 1000")
			}
		case MetricAsPercentOfLimit:
			if m.Above <= 0 || m.Above > 100 {
				return invalid("condition.metric.above: a percentage of the limit, more than 0 and at most 100")
			}
		default:
			return invalid("condition.metric.as: value, percentOfRequest or percentOfLimit")
		}
		return minutesIn("condition.metric.forMinutes", m.ForMinutes, MinIntervalMinutes, MaxIntervalMinutes, false)
	case ConditionTypeRestarts:
		r := c.Restarts
		if r == nil {
			return invalid("condition.restarts is required for type restarts")
		}
		if r.Above < 0 || r.Above > MaxCountAbove {
			return invalid("condition.restarts.above: 0 to %d", MaxCountAbove)
		}
		return minutesIn("condition.restarts.windowMinutes", r.WindowMinutes, MinIntervalMinutes, MaxIntervalMinutes, false)
	case ConditionTypeLogMatch:
		l := c.LogMatch
		if l == nil {
			return invalid("condition.logMatch is required for type logMatch")
		}
		if err := plainText("condition.logMatch.text", l.Text, MaxLogMatchTextLen); err != nil {
			return err
		}
		if l.Above < 0 || l.Above > MaxCountAbove {
			return invalid("condition.logMatch.above: 0 to %d (the logs search counts at most 1000 lines)", MaxCountAbove)
		}
		return minutesIn("condition.logMatch.windowMinutes", l.WindowMinutes, MinIntervalMinutes, MaxIntervalMinutes, false)
	case ConditionTypePackageDisk:
		if !builtIn {
			return invalid("condition.type packageDisk is for the package's built-in rule only")
		}
		d := c.PackageDisk
		if d == nil {
			return invalid("condition.packageDisk is required for type packageDisk")
		}
		if d.PercentOfBudget <= 0 || d.PercentOfBudget > 100 {
			return invalid("condition.packageDisk.percentOfBudget: more than 0 and at most 100")
		}
		return nil
	}
	return invalid("condition.type: metric, restarts or logMatch")
}

// ValidateAlertRule checks a rule's owner and spec. A rule is owned by
// one Component or Application; a built-in rule by its ProviderInstance.
func ValidateAlertRule(m Metadata, s AlertRuleSpec) error {
	if len(m.OwnerRefs) != 1 {
		return invalid("metadata.ownerRefs: a rule has exactly one owner, its Component or Application")
	}
	switch owner := m.OwnerRefs[0].Kind; {
	case s.BuiltIn && owner != KindProviderInstance:
		return invalid("metadata.ownerRefs: a built-in rule is owned by its package's ProviderInstance")
	case !s.BuiltIn && owner != KindComponent && owner != KindApplication:
		return invalid("metadata.ownerRefs: a rule is owned by a Component or an Application")
	}
	if s.BuiltIn {
		if s.EnvironmentID != "" {
			return invalid("environmentId: a built-in rule watches the package, not an Environment")
		}
	} else if err := refID("environmentId", s.EnvironmentID, true); err != nil {
		return err
	}
	if err := ValidateCondition(s.Condition, s.BuiltIn); err != nil {
		return err
	}
	if !validSeverity(s.Severity) {
		return invalid("severity: critical, warning or info")
	}
	if err := plainText("summary", s.Summary, MaxAlertSummaryLen); err != nil {
		return err
	}
	if len(s.ChannelIDs) > MaxRuleChannels {
		return invalid("channelIds: at most %d", MaxRuleChannels)
	}
	seen := map[string]bool{}
	for i, id := range s.ChannelIDs {
		if err := refID(fmt.Sprintf("channelIds[%d]", i), id, true); err != nil {
			return err
		}
		if seen[id] {
			return invalid("channelIds: %s is named twice", id)
		}
		seen[id] = true
	}
	return minutesIn("intervalMinutes", s.IntervalMinutes, MinIntervalMinutes, MaxIntervalMinutes, true)
}

// ValidateEnabledRuleCount refuses enabling a rule past the install's cap.
// enabledOthers counts the other enabled rules.
func ValidateEnabledRuleCount(enabledOthers int, s AlertRuleSpec) error {
	if s.IsEnabled() && enabledOthers >= MaxEnabledAlertRules {
		return invalid("at most %d enabled rules per install; disable or delete one first", MaxEnabledAlertRules)
	}
	return nil
}

// AlertRuleRelationships are a rule's depends-on edges to the channels
// it names, so deleting a channel still in use is refused.
func AlertRuleRelationships(s AlertRuleSpec) []Relationship {
	out := make([]Relationship, 0, len(s.ChannelIDs))
	for _, id := range s.ChannelIDs {
		out = append(out, Relationship{Type: EdgeDependsOn, TargetKind: KindNotificationChannel, TargetID: id})
	}
	return out
}

// ValidateNotificationChannel checks a channel's owner (none, or one
// Project) and its settings.
func ValidateNotificationChannel(m Metadata, s NotificationChannelSpec) error {
	if len(m.OwnerRefs) > 1 || (len(m.OwnerRefs) == 1 && m.OwnerRefs[0].Kind != KindProject) {
		return invalid("metadata.ownerRefs: a channel belongs to the platform (no owner) or to one Project")
	}
	if s.MinSeverity != "" && !validSeverity(s.MinSeverity) {
		return invalid("minSeverity: info, warning or critical")
	}
	if err := validAllowance("allowPrivateDestination", s.AllowPrivateDestination); err != nil {
		return err
	}
	if err := validAllowance("allowPlainHTTP", s.AllowPlainHTTP); err != nil {
		return err
	}
	set := 0
	for _, p := range []bool{s.Telegram != nil, s.Email != nil, s.Webhook != nil} {
		if p {
			set++
		}
	}
	if set != 1 {
		return invalid("set exactly one of telegram, email or webhook, matching type")
	}
	switch s.Type {
	case ChannelTelegram:
		t := s.Telegram
		if t == nil {
			return invalid("telegram is required for type telegram")
		}
		if s.AllowPrivateDestination != nil || s.AllowPlainHTTP != nil {
			return invalid("a telegram channel's host is fixed, so it takes no allowances")
		}
		if !chatIDRE.MatchString(t.ChatID) {
			return invalid("telegram.chatId: a numeric chat ID or a channel's @username")
		}
		return refID("telegram.botTokenSecretId", t.BotTokenSecretID, true)
	case ChannelEmail:
		e := s.Email
		if e == nil {
			return invalid("email is required for type email")
		}
		if s.AllowPrivateDestination != nil || s.AllowPlainHTTP != nil {
			return invalid("an email channel's destination is its SMTP server's, so it takes no allowances")
		}
		if err := refID("email.smtpServerId", e.SMTPServerID, true); err != nil {
			return err
		}
		if len(e.To) == 0 || len(e.To) > MaxEmailRecipients {
			return invalid("email.to: 1 to %d addresses", MaxEmailRecipients)
		}
		seen := map[string]bool{}
		for i, to := range e.To {
			if err := validAddress(fmt.Sprintf("email.to[%d]", i), to); err != nil {
				return err
			}
			if k := strings.ToLower(to); seen[k] {
				return invalid("email.to: %s is listed twice", to)
			} else {
				seen[k] = true
			}
		}
		return nil
	case ChannelWebhook:
		w := s.Webhook
		if w == nil {
			return invalid("webhook is required for type webhook")
		}
		if err := refID("webhook.signingSecretId", w.SigningSecretID, false); err != nil {
			return err
		}
		switch {
		case (w.URL == "") == (w.URLSecretID == ""):
			return invalid("webhook: set url, or urlSecretId and host when the URL holds a token")
		case w.URL != "":
			if w.Host != "" {
				return invalid("webhook.host is taken from url; set it only with urlSecretId")
			}
			return ValidateWebhookURL("webhook.url", w.URL, s.AllowPlainHTTP != nil)
		default:
			if err := refID("webhook.urlSecretId", w.URLSecretID, true); err != nil {
				return err
			}
			return ValidateHost("webhook.host", w.Host)
		}
	}
	return invalid("type: telegram, email or webhook")
}

// ValidateWebhookURL checks a webhook or heartbeat URL's shape: HTTPS
// (plain HTTP only with the allowance), a host, no credentials and no
// fragment. The sender checks it again, with the host's addresses, at
// every send.
func ValidateWebhookURL(field, raw string, plainHTTP bool) error {
	if len(raw) > MaxWebhookURLLen {
		return invalid("%s: at most %d characters", field, MaxWebhookURLLen)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return invalid("%s: not a URL", field)
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && plainHTTP:
	case u.Scheme == "http":
		return invalid("%s: HTTPS only, unless an Owner allows plain HTTP for this channel", field)
	default:
		return invalid("%s: an https URL", field)
	}
	if u.User != nil {
		return invalid("%s: no credentials in the URL; put a URL holding a token in a Secret (urlSecretId)", field)
	}
	if u.Fragment != "" || u.Opaque != "" {
		return invalid("%s: no fragment", field)
	}
	return ValidateHost(field+" host", u.Host)
}

// validAddress is one bare address, with no display name and no line
// break.
func validAddress(field, a string) error {
	if strings.ContainsAny(a, "\r\n") {
		return invalid("%s contains a line break", field)
	}
	p, err := mail.ParseAddress(a)
	if err != nil || p.Address != a || p.Name != "" {
		return invalid("%s: a bare email address", field)
	}
	return nil
}

// NotificationChannelRelationships are a channel's depends-on edges to
// the Secrets and SMTP server it uses.
func NotificationChannelRelationships(s NotificationChannelSpec) []Relationship {
	var out []Relationship
	add := func(k Kind, id string) {
		if id != "" {
			out = append(out, Relationship{Type: EdgeDependsOn, TargetKind: k, TargetID: id})
		}
	}
	switch {
	case s.Telegram != nil:
		add(KindSecret, s.Telegram.BotTokenSecretID)
	case s.Email != nil:
		add(KindSMTPServer, s.Email.SMTPServerID)
	case s.Webhook != nil:
		add(KindSecret, s.Webhook.URLSecretID)
		add(KindSecret, s.Webhook.SigningSecretID)
	}
	return out
}

// ValidateSMTPServer checks a platform-only SMTP server.
func ValidateSMTPServer(m Metadata, s SMTPServerSpec) error {
	if len(m.OwnerRefs) > 0 {
		return invalid("metadata.ownerRefs: an SMTP server belongs to the platform and has no owner")
	}
	if net.ParseIP(s.Host) == nil && (len(s.Host) > 253 || !hostnameRE.MatchString(s.Host)) {
		return invalid("host: a host name or IP address, without a port")
	}
	if s.Port < 1 || s.Port > 65535 {
		return invalid("port: 1 to 65535")
	}
	if s.TLS != SMTPStartTLS && s.TLS != SMTPImplicit {
		return invalid("tls: starttls or implicit; plain SMTP isn't offered")
	}
	if err := validAddress("from", s.From); err != nil {
		return err
	}
	if err := refID("usernameSecretId", s.UsernameSecretID, true); err != nil {
		return err
	}
	return refID("passwordSecretId", s.PasswordSecretID, true)
}

// SMTPServerRelationships are its depends-on edges to its Secrets.
func SMTPServerRelationships(s SMTPServerSpec) []Relationship {
	var out []Relationship
	for _, id := range []string{s.UsernameSecretID, s.PasswordSecretID} {
		if id != "" && (len(out) == 0 || out[0].TargetID != id) {
			out = append(out, Relationship{Type: EdgeDependsOn, TargetKind: KindSecret, TargetID: id})
		}
	}
	return out
}

// ValidateHeartbeat checks the platform's heartbeat.
func ValidateHeartbeat(m Metadata, s HeartbeatSpec) error {
	if len(m.OwnerRefs) > 0 {
		return invalid("metadata.ownerRefs: the heartbeat belongs to the platform and has no owner")
	}
	if err := refID("urlSecretId", s.URLSecretID, true); err != nil {
		return err
	}
	if err := ValidateHost("host", s.Host); err != nil {
		return err
	}
	if s.Method != "" && s.Method != "GET" && s.Method != "POST" {
		return invalid("method: GET or POST")
	}
	if err := validAllowance("allowPrivateDestination", s.AllowPrivateDestination); err != nil {
		return err
	}
	return minutesIn("intervalMinutes", s.IntervalMinutes, MinHeartbeatMinutes, MaxHeartbeatMinutes, true)
}

// HeartbeatRelationships is its depends-on edge to its URL's Secret.
func HeartbeatRelationships(s HeartbeatSpec) []Relationship {
	if s.URLSecretID == "" {
		return nil
	}
	return []Relationship{{Type: EdgeDependsOn, TargetKind: KindSecret, TargetID: s.URLSecretID}}
}

// ValidateAlert checks what serendipd writes to an Alert.
func ValidateAlert(m Metadata, s AlertSpec, st AlertStatus) error {
	if len(m.OwnerRefs) != 1 || m.OwnerRefs[0].Kind != KindAlertRule || m.OwnerRefs[0].ID != s.RuleID {
		return invalid("metadata.ownerRefs: an Alert is owned by its rule")
	}
	if s.Fingerprint != AlertFingerprint(s.RuleID, s.ComponentID, s.EnvironmentID) {
		return invalid("fingerprint: rule|component|environment")
	}
	if !validSeverity(s.Severity) {
		return invalid("severity: critical, warning or info")
	}
	if st.State != AlertFiring && st.State != AlertResolved {
		return invalid("state: firing or resolved")
	}
	finished := 0
	for _, d := range st.Deliveries {
		if d.State != DeliveryPending {
			finished++
		}
	}
	if finished > MaxAlertDeliveries {
		return invalid("deliveries: at most %d finished (AddDelivery trims them)", MaxAlertDeliveries)
	}
	return nil
}
