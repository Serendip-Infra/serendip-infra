// Words for spec 024's alerting: conditions, severities, states and
// deliveries, as the console says them. Pure functions, tested in
// lib.test.ts; the server decides everything they describe.
import type { Alert, AlertCondition, AlertDelivery, AlertRule, AlertSummary, ChannelListItem, NotificationChannel, PlatformChannel, Severity } from "../api/types";

export const severities: Severity[] = ["critical", "warning", "info"];
export const severityWord: Record<Severity, string> = { critical: "Critical", warning: "Warning", info: "Info" };

/** Actions the console checks ahead of time (core/policy; path-derived). */
export const alertActions = {
  ruleWrite: "serendip/alertrules/write",
  ruleDelete: "serendip/alertrules/delete",
  silence: "serendip/alertrules/silence/action",
  acknowledge: "serendip/alerts/acknowledge/action",
  channelWrite: "serendip/notificationchannels/write",
  channelDelete: "serendip/notificationchannels/delete",
  channelTest: "serendip/notificationchannels/test/action",
  /** Owner-only: Contributor has it in NotActions. Covers both allowances. */
  allowance: "serendip/notificationchannels/allowPrivateDestination/action",
  smtpWrite: "serendip/smtpservers/write",
  smtpDelete: "serendip/smtpservers/delete",
  heartbeatWrite: "serendip/heartbeat/write",
  heartbeatDelete: "serendip/heartbeat/delete",
  secretValueRead: "serendip/secrets/value/read",
};

/** The firing counts the bell shows, most severe first; zero counts are left out. */
export function bellCounts(s: AlertSummary | undefined): Array<{ severity: Severity; count: number }> {
  if (!s) return [];
  return severities.flatMap((severity) => {
    const count = s.firing?.[severity] ?? 0;
    return count > 0 ? [{ severity, count }] : [];
  });
}

/** "2 critical, 1 warning firing", or "No alerts firing". */
export function bellLabel(s: AlertSummary | undefined): string {
  const counts = bellCounts(s);
  if (counts.length === 0) return "No alerts firing";
  return `${counts.map((c) => `${c.count} ${c.severity}`).join(", ")} firing`;
}

const metricWord = { cpu: "CPU", memory: "Memory" } as const;
const minutes = (n: number) => (n === 1 ? "1 minute" : `${n} minutes`);

/** The value's unit for a condition: millicores, MiB or a percentage. */
export function valueUnit(c: AlertCondition | undefined): string {
  if (!c) return "";
  if (c.type === "packageDisk" || c.type === "requestErrorRate") return "%";
  if (c.type === "requestLatency") return " ms";
  if (c.type === "metric" && c.metric) {
    if (c.metric.as !== "value") return "%";
    return c.metric.metric === "cpu" ? "m" : " MiB";
  }
  return "";
}

/** A number as the condition reads it: "250m", "512 MiB", "85%", "3". */
export function formatValue(v: number, c: AlertCondition | undefined): string {
  const rounded = Math.abs(v) >= 10 ? Math.round(v) : Math.round(v * 10) / 10;
  return `${rounded.toLocaleString()}${valueUnit(c)}`;
}

/** "CPU above 80% of its request for 5 minutes". */
export function conditionText(c: AlertCondition): string {
  switch (c.type) {
    case "metric": {
      const m = c.metric;
      if (!m) return "A metric condition";
      const of = m.as === "percentOfRequest" ? " of its request" : m.as === "percentOfLimit" ? " of its limit" : "";
      return `${metricWord[m.metric] ?? m.metric} above ${formatValue(m.above, c)}${of} for ${minutes(m.forMinutes)}`;
    }
    case "restarts":
      return c.restarts ? `More than ${c.restarts.above} restarts in ${minutes(c.restarts.windowMinutes)}` : "Restarts";
    case "logMatch":
      return c.logMatch ? `More than ${c.logMatch.above} log lines containing “${c.logMatch.text}” in ${minutes(c.logMatch.windowMinutes)}` : "Log lines";
    case "packageDisk":
      return c.packageDisk ? `Stored data above ${c.packageDisk.percentOfBudget}% of the package's disk budget` : "Package disk";
    case "requestErrorRate": {
      const r = c.requestErrorRate;
      if (!r) return "Request error rate";
      const min = r.minRequests ? `, with at least ${r.minRequests} requests` : "";
      return `More than ${formatValue(r.abovePercent, c)} of requests failing in ${minutes(r.windowMinutes)}${min}`;
    }
    case "requestLatency":
      return c.requestLatency ? `p${c.requestLatency.percentile} request latency above ${formatValue(c.requestLatency.aboveMs, c)} over ${minutes(c.requestLatency.windowMinutes)}` : "Request latency";
    case "traceErrors":
      return c.traceErrors ? `More than ${c.traceErrors.above} traces with an error in ${minutes(c.traceErrors.windowMinutes)}` : "Traces with errors";
    default:
      return String((c as { type: string }).type);
  }
}

/** Whether a rule is evaluating, and if not, why, in words. */
export function ruleStateWords(r: AlertRule, now: Date): { kind: "ok" | "warn" | "unknown"; word: string; detail?: string } {
  const silence = r.status.silence;
  const silenced = silence && Date.parse(silence.until) > now.getTime();
  if (r.spec.enabled === false) return { kind: "unknown", word: "Disabled" };
  if (!r.status.active) {
    const reasons: Record<string, string> = {
      PackageNotReady: "the observability package isn't ready",
      ComponentGone: "its component is gone",
      ConditionRefused: "the condition can't be evaluated as written",
      QueryFailed: "the last query failed",
      NotDeployed: "the component isn't deployed there",
      NoData: "there's no data yet",
    };
    // A refused condition's own words say what's missing, such as the tracing package (spec 025).
    const refused = r.status.activeReason === "ConditionRefused" ? r.status.conditions?.find((c) => c.type === "Evaluating")?.message : undefined;
    const why = refused || (r.status.activeReason ? (reasons[r.status.activeReason] ?? r.status.activeReason) : "not evaluated yet");
    return { kind: "warn", word: "Inactive", detail: why };
  }
  if (silenced) return { kind: "warn", word: "Silenced", detail: `until ${new Date(silence.until).toLocaleString()}: ${silence.reason}` };
  return { kind: "ok", word: "Active" };
}

/** Why an Alert resolved. */
export function resolvedWords(reason: string | undefined): string {
  const words: Record<string, string> = {
    clear: "the condition cleared",
    ruleDeleted: "its rule was deleted",
    packageRemoved: "package removed",
    componentGone: "its component is gone",
    ruleDisabled: "its rule was disabled",
  };
  return reason ? (words[reason] ?? reason) : "";
}

/** A delivery's state in words: "why wasn't I told" has an answer. */
export function deliveryWords(d: AlertDelivery): { kind: "ok" | "progress" | "warn" | "error" | "unknown"; word: string } {
  switch (d.state) {
    case "sent":
      return { kind: "ok", word: "Sent" };
    case "pending":
      return { kind: "progress", word: d.attempts ? `Retrying (attempt ${d.attempts + 1})` : "Sending" };
    case "failed":
      return { kind: "error", word: "Failed" };
    case "silenced":
      return { kind: "unknown", word: "Not sent: silenced" };
    case "belowMinSeverity":
      return { kind: "unknown", word: "Not sent: below the channel's minimum" };
    default:
      return { kind: "unknown", word: d.state };
  }
}

export const isPlatformChannel = (c: ChannelListItem): c is PlatformChannel => "platformChannel" in c && c.platformChannel === true;
export const fullChannel = (c: ChannelListItem): NotificationChannel | undefined => (isPlatformChannel(c) ? undefined : c);

/** Where a channel sends, in words that never hold a secret: the chat, the addresses, or the webhook's host. */
export function channelTarget(c: ChannelListItem): string {
  if (isPlatformChannel(c)) return "Platform channel";
  const s = c.spec;
  if (s.type === "telegram") return s.telegram ? `Telegram chat ${s.telegram.chatId}` : "Telegram";
  if (s.type === "email") return s.email ? s.email.to.join(", ") : "Email";
  if (s.webhook?.url) {
    try {
      return new URL(s.webhook.url).host;
    } catch {
      return s.webhook.url;
    }
  }
  return s.webhook?.host ?? "Webhook";
}

export const channelTypeWord = { telegram: "Telegram", email: "Email", webhook: "Webhook" } as const;

/** Silence presets: the server allows at most 30 days. */
export const silencePresets = [
  { value: "1", label: "1 hour" },
  { value: "4", label: "4 hours" },
  { value: "24", label: "1 day" },
  { value: "168", label: "1 week" },
  { value: "720", label: "30 days" },
];

/** A silence's end, hours from now. 30 days is a minute short, so it's never past the server's limit when it arrives. */
export function silenceUntil(hours: number, now: Date): string {
  const ms = hours * 3_600_000 - (hours >= 720 ? 60_000 : 0);
  return new Date(now.getTime() + ms).toISOString();
}

/** Alerts newest first, firing ahead of resolved. */
export function sortAlerts(alerts: Alert[]): Alert[] {
  return [...alerts].sort((a, b) => (a.status.state === b.status.state ? Date.parse(b.status.startedAt) - Date.parse(a.status.startedAt) : a.status.state === "firing" ? -1 : 1));
}
