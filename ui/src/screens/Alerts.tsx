import { useState, type FormEvent } from "react";
import { apiFetch, apiList, isShown, type ApiError, type NetworkError } from "../api/client";
import type { Alert, AlertCondition, AlertRule, AlertRuleSpec, Application, ChannelListItem, Component, Environment, OwnerRef, Severity } from "../api/types";
import { ApiEquivalent, type ApiRequest } from "../components/ApiEquivalent";
import { KeyValueList } from "../components/Data";
import { DeleteConfirm } from "../components/Delete";
import { SelectField, TextField } from "../components/Field";
import { Guarded } from "../components/Guarded";
import { RequestError } from "../components/RequestError";
import { PageHeader } from "../components/Shell";
import { EmptyState, LoadingState, Notice } from "../components/States";
import { StatusText, type StatusKind } from "../components/Status";
import { useOptional } from "../hooks/useOptional";
import { usePermissions, type Access } from "../hooks/usePermissions";
import { useNow, usePoll } from "../hooks/usePoll";
import {
  alertActions,
  channelTarget,
  channelTypeWord,
  conditionText,
  deliveryWords,
  formatValue,
  resolvedWords,
  ruleStateWords,
  severities,
  severityWord,
  silencePresets,
  silenceUntil,
  sortAlerts,
} from "../lib/alerts";
import { availability } from "../lib/health";
import { ownerOf } from "../lib/pipeline";
import { Link, navigate } from "../lib/router";
import { formatAge } from "../lib/time";
import { dnsLabelError, UpdatedLine } from "./common";

// Spec 024's console (plan decision 14): a Component's and an
// Application's rules and Alerts, acknowledge and silence, the rule form,
// and the Alerts page the bell links to. The evaluator and the delivery
// loop are serendipd's; these screens only read and write through the API.

const enc = encodeURIComponent;
export const alertPath = (id: string) => `/alerts/${enc(id)}`;

const severityKind: Record<Severity, StatusKind> = { critical: "error", warning: "warn", info: "unknown" };

export function SeverityText({ severity }: { severity: Severity }) {
  return <StatusText kind={severityKind[severity] ?? "unknown"}>{severityWord[severity] ?? severity}</StatusText>;
}

export function AlertStateText({ alert }: { alert: Alert }) {
  if (alert.status.state === "resolved") return <StatusText kind="ok">Resolved</StatusText>;
  return <StatusText kind={severityKind[alert.spec.severity] ?? "warn"}>Firing</StatusText>;
}

function RuleState({ rule, now }: { rule: AlertRule; now: Date }) {
  const s = ruleStateWords(rule, now);
  return (
    <>
      <StatusText kind={s.kind}>{s.word}</StatusText>
      {s.detail && <span className="field-hint"> {s.detail}</span>}
    </>
  );
}

/** An inline failure: what the server said, and the request ID. */
function Failure({ error, what }: { error: ApiError | NetworkError; what: string }) {
  return (
    <Notice tone="danger">
      <p>
        {error.message} {what}
      </p>
      {"requestId" in error && error.requestId && (
        <p className="request-id">
          Request ID <span className="mono">{error.requestId}</span>
        </p>
      )}
    </Notice>
  );
}

/** One POST with no answer worth keeping (acknowledge, silence, unsilence). */
function useAction() {
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<ApiError | NetworkError>();
  async function run(path: string, body: unknown, done: () => void) {
    setBusy(true);
    setFailure(undefined);
    try {
      await apiFetch("POST", path, body);
      done();
    } catch (err) {
      if (isShown(err)) setFailure(err);
    } finally {
      setBusy(false);
    }
  }
  return { busy, failure, run };
}

// ---- Alerts ----

function AckButton({ alert, access, onDone }: { alert: Alert; access: Access; onDone: () => void }) {
  const a = useAction();
  if (alert.status.acknowledged) return <span className="field-hint">Acknowledged by {alert.status.acknowledged.by.replace(/^person:/, "")}</span>;
  if (alert.status.state !== "firing") return null;
  return (
    <>
      <Guarded allowed={access.can(alertActions.acknowledge)} label="Acknowledge" reason="Your roles don't allow acknowledging alerts here.">
        <button type="button" className="button" disabled={a.busy} onClick={() => void a.run(`/v1/alerts/${enc(alert.metadata.id)}/acknowledge`, undefined, onDone)}>
          {a.busy ? "Acknowledging…" : "Acknowledge"}
        </button>
      </Guarded>
      {a.failure && <Failure error={a.failure} what="It wasn't acknowledged." />}
    </>
  );
}

/**
 * Acknowledge decided at the Alert's rule, for a list that spans scopes
 * (the Alerts page). An Alert inherits its rule's access, and the
 * permissions route takes only kinds with routes of their own.
 */
function ScopedAck({ alert, onDone }: { alert: Alert; onDone: () => void }) {
  const access = usePermissions({ kind: "AlertRule", id: alert.spec.ruleId });
  return <AckButton alert={alert} access={access} onDone={onDone} />;
}

/**
 * AlertTable lists Alerts with their state, value and acknowledge. names
 * maps Component and Environment IDs to names when known. Without access,
 * each firing Alert's acknowledge is decided at the Alert.
 */
function AlertTable({ alerts, names, access, now, onChanged, caption }: { alerts: Alert[]; names: Map<string, string>; access?: Access; now: Date; onChanged: () => void; caption: string }) {
  return (
    <div className="table-wrap">
      <table className="table">
        <caption className="visually-hidden">{caption}</caption>
        <thead>
          <tr>
            <th scope="col">Alert</th>
            <th scope="col">Severity</th>
            <th scope="col">State</th>
            <th scope="col">Where</th>
            <th scope="col">Value</th>
            <th scope="col">Started</th>
            <th scope="col">Acknowledge</th>
          </tr>
        </thead>
        <tbody>
          {sortAlerts(alerts).map((a) => (
            <tr key={a.metadata.id}>
              <td>
                <Link to={alertPath(a.metadata.id)}>{a.spec.summary}</Link>
              </td>
              <td>
                <SeverityText severity={a.spec.severity} />
              </td>
              <td>
                <AlertStateText alert={a} />
              </td>
              <td>{[a.spec.componentId && (names.get(a.spec.componentId) ?? a.spec.componentId), a.spec.environmentId && (names.get(a.spec.environmentId) ?? a.spec.environmentId)].filter(Boolean).join(" · ") || "Platform"}</td>
              <td className="mono">
                {formatValue(a.status.value, undefined)} (threshold {formatValue(a.status.threshold, undefined)})
              </td>
              <td>
                <time dateTime={a.status.startedAt} title={a.status.startedAt}>
                  {formatAge(a.status.startedAt, now)}
                </time>
              </td>
              <td>
                {access || a.status.state !== "firing" || a.status.acknowledged ? <AckButton alert={a} access={access ?? { can: () => undefined }} onDone={onChanged} /> : <ScopedAck alert={a} onDone={onChanged} />}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ---- Rules ----

function SilenceForm({ rule, onDone, onCancel }: { rule: AlertRule; onDone: () => void; onCancel: () => void }) {
  const [hours, setHours] = useState("4");
  const [reason, setReason] = useState("");
  const [submitted, setSubmitted] = useState(false);
  const a = useAction();
  const path = `/v1/alertrules/${rule.metadata.id}/silence`;
  const body = { until: silenceUntil(Number(hours), new Date()), reason: reason.trim() };
  const reasonError = submitted && body.reason === "" ? "Say why, so others know." : undefined;
  function submit(e: FormEvent) {
    e.preventDefault();
    setSubmitted(true);
    if (body.reason === "") return;
    void a.run(`/v1/alertrules/${enc(rule.metadata.id)}/silence`, body, onDone);
  }
  return (
    <form className="panel" onSubmit={submit} noValidate aria-label={`Silence ${rule.metadata.name}`}>
      <p>Silencing stops this rule's notifications. Its Alerts still open and resolve here.</p>
      <div className="form-row">
        <SelectField id={`silence-${rule.metadata.id}-for`} label="For" value={hours} onChange={(e) => setHours(e.target.value)} options={silencePresets} />
        <TextField id={`silence-${rule.metadata.id}-reason`} label="Reason" value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} {...(reasonError ? { error: reasonError } : {})} />
      </div>
      {a.failure && <Failure error={a.failure} what="The rule wasn't silenced." />}
      <div className="form-actions">
        <button type="submit" className="button button--primary" disabled={a.busy}>
          {a.busy ? "Silencing…" : "Silence"}
        </button>
        <button type="button" className="button" onClick={onCancel} disabled={a.busy}>
          Cancel
        </button>
      </div>
      <ApiEquivalent request={{ method: "POST", path, body }} />
    </form>
  );
}

type RuleOpen = { kind: "silence" | "edit" | "delete"; id: string } | undefined;

function RuleActions({ rule, access, open, setOpen, onChanged }: { rule: AlertRule; access: Access; open: RuleOpen; setOpen: (o: RuleOpen) => void; onChanged: () => void }) {
  const now = new Date();
  const silenced = rule.status.silence && Date.parse(rule.status.silence.until) > now.getTime();
  const un = useAction();
  const busy = open?.id === rule.metadata.id;
  return (
    <div className="form-actions">
      {silenced ? (
        <Guarded allowed={access.can(alertActions.silence)} label="Unsilence" reason="Your roles don't allow silencing this rule.">
          <button type="button" className="button" disabled={un.busy} onClick={() => void un.run(`/v1/alertrules/${enc(rule.metadata.id)}/unsilence`, undefined, onChanged)}>
            {un.busy ? "Unsilencing…" : "Unsilence"}
          </button>
        </Guarded>
      ) : (
        <Guarded allowed={access.can(alertActions.silence)} label="Silence" reason="Your roles don't allow silencing this rule.">
          <button type="button" className="button" disabled={busy} onClick={() => setOpen({ kind: "silence", id: rule.metadata.id })}>
            Silence
          </button>
        </Guarded>
      )}
      <Guarded allowed={access.can(alertActions.ruleWrite)} label="Edit" reason="Your roles don't allow changing this rule.">
        <button type="button" className="button" disabled={busy} onClick={() => setOpen({ kind: "edit", id: rule.metadata.id })}>
          Edit
        </button>
      </Guarded>
      {!rule.spec.builtIn && (
        <Guarded allowed={access.can(alertActions.ruleDelete)} label="Delete" reason="Your roles don't allow deleting this rule.">
          <button type="button" className="button" disabled={busy} onClick={() => setOpen({ kind: "delete", id: rule.metadata.id })}>
            Delete
          </button>
        </Guarded>
      )}
      {un.failure && <Failure error={un.failure} what="The silence is still on." />}
    </div>
  );
}

/** RuleList: rules with their state, and silence, edit and delete under the caller's roles at scope. */
function RuleList({ rules, envNames, access, channels, owner, envs, onChanged, idPrefix }: {
  rules: AlertRule[];
  envNames: Map<string, string>;
  access: Access;
  channels: ChannelListItem[] | undefined;
  owner: OwnerRef;
  envs: Environment[];
  onChanged: () => void;
  idPrefix: string;
}) {
  const now = useNow();
  const [open, setOpen] = useState<RuleOpen>();
  const done = () => (setOpen(undefined), onChanged());
  if (rules.length === 0) return <p className="state-body">No alert rules yet.</p>;
  return (
    <ul className="plain-list" aria-label="Alert rules">
      {rules.map((r) => (
        <li key={r.metadata.id} className="panel">
          <div className="pipeline-head">
            <h3 className="subsection-title">{r.spec.summary}</h3>
            <span role="status">
              <RuleState rule={r} now={now} />
            </span>
          </div>
          <KeyValueList
            items={[
              ["Condition", conditionText(r.spec.condition)],
              ["Severity", <SeverityText severity={r.spec.severity} />],
              ["Environment", r.spec.environmentId ? (envNames.get(r.spec.environmentId) ?? r.spec.environmentId) : "The package"],
              ["Last value", r.status.lastValue !== undefined ? `${formatValue(r.status.lastValue, r.spec.condition)}${r.status.lastEvaluated ? `, ${formatAge(r.status.lastEvaluated, now)}` : ""}` : "Not evaluated yet"],
              ["Notifies", channelNames(r.spec.channelIds, channels)],
            ]}
          />
          <RuleActions rule={r} access={access} open={open} setOpen={setOpen} onChanged={onChanged} />
          {open?.id === r.metadata.id && open.kind === "silence" && <SilenceForm rule={r} onDone={done} onCancel={() => setOpen(undefined)} />}
          {open?.id === r.metadata.id && open.kind === "edit" && <RuleForm idPrefix={`${idPrefix}-edit`} owner={owner} envs={envs} channels={channels} rule={r} onDone={done} onCancel={() => setOpen(undefined)} />}
          {open?.id === r.metadata.id && open.kind === "delete" && (
            <DeleteConfirm
              target={{ kind: "AlertRule", id: r.metadata.id, name: r.metadata.name }}
              cascade={true}
              noun="alert rule"
              note="Its Alerts, listed above, are deleted with it."
              onDeleted={(approval) => (approval ? navigate(`/approvals/${enc(approval)}`) : done())}
              onCancel={() => setOpen(undefined)}
            />
          )}
        </li>
      ))}
    </ul>
  );
}

function channelNames(ids: string[] | undefined, channels: ChannelListItem[] | undefined): string {
  if (!ids || ids.length === 0) return "The default channels";
  return ids.map((id) => channels?.find((c) => c.metadata.id === id)?.metadata.name ?? id).join(", ");
}

// ---- The rule form ----

/** What a condition needs beyond the observability package; without it the rule is inactive with the reason. */
const conditionNeeds: Partial<Record<AlertCondition["type"], string>> = {
  requestErrorRate: "Needs request metrics from the cluster's network layer (Cilium). Without them the rule stays inactive and says why.",
  requestLatency: "Needs request metrics from the cluster's network layer (Cilium). Without them the rule stays inactive and says why.",
  traceErrors: "Needs the tracing package, and components sending traces. Without it the rule stays inactive and says why.",
};

interface RuleState {
  name: string;
  summary: string;
  severity: Severity;
  environmentId: string;
  type: AlertCondition["type"];
  metric: "cpu" | "memory";
  percentile: "95" | "99";
  minRequests: string;
  as: "value" | "percentOfRequest" | "percentOfLimit";
  above: string;
  minutes: string;
  text: string;
  intervalMinutes: string;
  enabled: boolean;
  channelIds: string[];
}

function initialState(rule: AlertRule | undefined, envs: Environment[]): RuleState {
  const c = rule?.spec.condition;
  const above =
    c?.metric?.above ?? c?.restarts?.above ?? c?.logMatch?.above ?? c?.packageDisk?.percentOfBudget ?? c?.requestErrorRate?.abovePercent ?? c?.requestLatency?.aboveMs ?? c?.traceErrors?.above;
  const mins = c?.metric?.forMinutes ?? c?.restarts?.windowMinutes ?? c?.logMatch?.windowMinutes ?? c?.requestErrorRate?.windowMinutes ?? c?.requestLatency?.windowMinutes ?? c?.traceErrors?.windowMinutes;
  return {
    name: rule?.metadata.name ?? "",
    summary: rule?.spec.summary ?? "",
    severity: rule?.spec.severity ?? "warning",
    environmentId: rule?.spec.environmentId ?? envs[0]?.metadata.id ?? "",
    type: c?.type ?? "metric",
    metric: c?.metric?.metric ?? "cpu",
    percentile: c?.requestLatency?.percentile === 99 ? "99" : "95",
    minRequests: c?.requestErrorRate?.minRequests !== undefined ? String(c.requestErrorRate.minRequests) : "",
    as: c?.metric?.as ?? "percentOfLimit",
    above: above !== undefined ? String(above) : "80",
    minutes: mins !== undefined ? String(mins) : "5",
    text: c?.logMatch?.text ?? "",
    intervalMinutes: String(rule?.spec.intervalMinutes ?? 1),
    enabled: rule?.spec.enabled !== false,
    channelIds: rule?.spec.channelIds ?? [],
  };
}

function toCondition(s: RuleState): AlertCondition {
  const above = Number(s.above);
  const mins = Number(s.minutes);
  switch (s.type) {
    case "metric":
      return { type: "metric", metric: { metric: s.metric, as: s.as, above, forMinutes: mins } };
    case "restarts":
      return { type: "restarts", restarts: { above, windowMinutes: mins } };
    case "logMatch":
      return { type: "logMatch", logMatch: { text: s.text.trim(), above, windowMinutes: mins } };
    case "packageDisk":
      return { type: "packageDisk", packageDisk: { percentOfBudget: above } };
    case "requestErrorRate":
      return { type: "requestErrorRate", requestErrorRate: { abovePercent: above, ...(s.minRequests.trim() !== "" ? { minRequests: Number(s.minRequests) } : {}), windowMinutes: mins } };
    case "requestLatency":
      return { type: "requestLatency", requestLatency: { percentile: s.percentile === "99" ? 99 : 95, aboveMs: above, windowMinutes: mins } };
    case "traceErrors":
      return { type: "traceErrors", traceErrors: { above, windowMinutes: mins } };
  }
}

export function validateRule(s: RuleState, creating: boolean): Record<string, string> {
  const e: Record<string, string> = {};
  if (creating) {
    const n = dnsLabelError(s.name.trim(), "name");
    if (n) e.name = n;
  }
  if (s.summary.trim() === "") e.summary = "Enter a summary: it's the notification's headline.";
  else if (s.summary.length > 200) e.summary = "At most 200 characters.";
  const above = Number(s.above);
  if (s.above.trim() === "" || !Number.isFinite(above) || above < 0) e.above = "Enter a number, 0 or more.";
  else if ((s.type === "restarts" || s.type === "logMatch" || s.type === "traceErrors") && (!Number.isInteger(above) || above > 999)) e.above = "A whole number up to 999.";
  else if (s.type === "requestErrorRate" && above >= 100) e.above = "A percentage of requests, under 100.";
  else if (s.type === "requestLatency" && (above <= 0 || above > 600_000)) e.above = "Milliseconds, above 0 and at most 600000 (ten minutes).";
  else if (s.type === "packageDisk" && (above <= 0 || above > 100)) e.above = "A percentage, above 0 and at most 100.";
  if (s.type !== "packageDisk") {
    const m = Number(s.minutes);
    if (!Number.isInteger(m) || m < 1 || m > 60) e.minutes = "Whole minutes, 1 to 60.";
    if (s.environmentId === "") e.environmentId = "Choose an Environment.";
  }
  if (s.type === "logMatch" && s.text.trim() === "") e.text = "Enter the text to count.";
  if (s.type === "requestErrorRate" && s.minRequests.trim() !== "") {
    const n = Number(s.minRequests);
    if (!Number.isInteger(n) || n < 0 || n > 999) e.minRequests = "A whole number up to 999, or empty.";
  }
  const iv = Number(s.intervalMinutes);
  if (!Number.isInteger(iv) || iv < 1 || iv > 60) e.intervalMinutes = "Whole minutes, 1 to 60.";
  return e;
}

/**
 * RuleForm creates a rule owned by owner (a Component or an Application),
 * or edits rule. A built-in rule keeps its condition type; only its
 * threshold, severity, channels, interval and whether it's on change.
 */
export function RuleForm({ owner, envs, channels, rule, idPrefix, onDone, onCancel }: {
  owner: OwnerRef;
  envs: Environment[];
  channels: ChannelListItem[] | undefined;
  rule?: AlertRule;
  idPrefix: string;
  onDone: () => void;
  onCancel: () => void;
}) {
  const creating = rule === undefined;
  const [s, setS] = useState(() => initialState(rule, envs));
  const [submitted, setSubmitted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<ApiError | NetworkError>();
  const update = (p: Partial<RuleState>) => setS((x) => ({ ...x, ...p }));
  const builtIn = rule?.spec.builtIn === true;
  const errors = submitted ? validateRule(s, creating) : {};
  const err = (k: string) => (errors[k] ? { error: errors[k] } : {});

  const spec: AlertRuleSpec = {
    ...(builtIn ? {} : { environmentId: s.environmentId }),
    condition: toCondition(s),
    severity: s.severity,
    summary: s.summary.trim(),
    ...(s.channelIds.length > 0 ? { channelIds: s.channelIds } : {}),
    intervalMinutes: Number(s.intervalMinutes),
    enabled: s.enabled,
    ...(builtIn ? { builtIn: true } : {}),
  };
  const request: ApiRequest = creating
    ? { method: "POST", path: "/v1/alertrules", body: { metadata: { name: s.name.trim(), ownerRefs: [owner] }, spec } }
    : { method: "PUT", path: `/v1/alertrules/${rule.metadata.id}`, body: { metadata: rule.metadata, spec } };

  async function submit(e: FormEvent) {
    e.preventDefault();
    setSubmitted(true);
    if (Object.keys(validateRule(s, creating)).length > 0) return;
    setBusy(true);
    setFailure(undefined);
    try {
      await apiFetch(request.method as "POST" | "PUT", creating ? request.path : `/v1/alertrules/${enc(rule.metadata.id)}`, request.body);
      onDone();
    } catch (er) {
      if (isShown(er)) setFailure(er);
      setBusy(false);
    }
  }

  const id = (k: string) => `${idPrefix}-${k}`;
  const aboveLabel: Record<RuleState["type"], string> = {
    metric: s.as === "value" ? (s.metric === "cpu" ? "Above, millicores" : "Above, MiB") : "Above, %",
    packageDisk: "Above, % of the disk budget",
    restarts: "More than, restarts",
    logMatch: "More than, lines",
    requestErrorRate: "Above, % of requests failing",
    requestLatency: "Above, milliseconds",
    traceErrors: "More than, traces with an error",
  };
  return (
    <form className="panel form" onSubmit={submit} noValidate aria-label={creating ? "New alert rule" : `Edit ${rule.metadata.name}`}>
      {Object.keys(errors).length > 0 && <Notice tone="danger">Some fields need attention. Nothing was sent.</Notice>}
      {creating && <TextField id={id("name")} label="Name" hint="Lowercase letters, digits and hyphens." value={s.name} maxLength={63} onChange={(e) => update({ name: e.target.value })} {...err("name")} />}
      <TextField id={id("summary")} label="Summary" hint="The notification's headline, for example “web is using most of its memory”." value={s.summary} maxLength={200} onChange={(e) => update({ summary: e.target.value })} {...err("summary")} />
      <div className="form-row">
        <SelectField id={id("severity")} label="Severity" value={s.severity} onChange={(e) => update({ severity: e.target.value as Severity })} options={severities.map((v) => ({ value: v, label: severityWord[v] }))} />
        {!builtIn && (
          <SelectField
            id={id("env")}
            label="Environment"
            value={s.environmentId}
            onChange={(e) => update({ environmentId: e.target.value })}
            options={envs.map((e) => ({ value: e.metadata.id, label: e.metadata.name }))}
            {...err("environmentId")}
          />
        )}
      </div>
      {!builtIn && (
        <SelectField
          id={id("type")}
          label="Condition"
          value={s.type}
          onChange={(e) => update({ type: e.target.value as RuleState["type"] })}
          options={[
            { value: "metric", label: "CPU or memory" },
            { value: "restarts", label: "Restarts" },
            { value: "logMatch", label: "Log lines containing text" },
            { value: "requestErrorRate", label: "Request error rate" },
            { value: "requestLatency", label: "Request latency" },
            { value: "traceErrors", label: "Traces with errors" },
          ]}
        />
      )}
      {conditionNeeds[s.type] && <p className="field-hint">{conditionNeeds[s.type]}</p>}
      {s.type === "requestLatency" && (
        <SelectField
          id={id("percentile")}
          label="Percentile"
          value={s.percentile}
          onChange={(e) => update({ percentile: e.target.value as RuleState["percentile"] })}
          options={[
            { value: "95", label: "p95" },
            { value: "99", label: "p99" },
          ]}
        />
      )}
      {s.type === "metric" && (
        <div className="form-row">
          <SelectField id={id("metric")} label="Metric" value={s.metric} onChange={(e) => update({ metric: e.target.value as RuleState["metric"] })} options={[{ value: "cpu", label: "CPU" }, { value: "memory", label: "Memory" }]} />
          <SelectField
            id={id("as")}
            label="Measured as"
            value={s.as}
            onChange={(e) => update({ as: e.target.value as RuleState["as"] })}
            options={[
              { value: "percentOfLimit", label: "% of its limit" },
              { value: "percentOfRequest", label: "% of its request" },
              { value: "value", label: s.metric === "cpu" ? "Millicores" : "MiB" },
            ]}
          />
        </div>
      )}
      {s.type === "logMatch" && <TextField id={id("text")} label="Containing" hint="Plain text, matched as a phrase. Alerts record the count, never the lines." value={s.text} maxLength={200} onChange={(e) => update({ text: e.target.value })} {...err("text")} />}
      <div className="form-row">
        <TextField id={id("above")} label={aboveLabel[s.type]} inputMode="decimal" value={s.above} onChange={(e) => update({ above: e.target.value })} {...err("above")} />
        {s.type !== "packageDisk" && (
          <TextField id={id("minutes")} label={s.type === "metric" ? "For, minutes" : "Within, minutes"} inputMode="numeric" value={s.minutes} onChange={(e) => update({ minutes: e.target.value })} {...err("minutes")} />
        )}
        {s.type === "requestErrorRate" && (
          <TextField id={id("min-requests")} label="At least, requests" hint="Optional: below this many requests in the window, the rule doesn't fire." inputMode="numeric" value={s.minRequests} onChange={(e) => update({ minRequests: e.target.value })} {...err("minRequests")} />
        )}
        <TextField id={id("interval")} label="Checked every, minutes" inputMode="numeric" value={s.intervalMinutes} onChange={(e) => update({ intervalMinutes: e.target.value })} {...err("intervalMinutes")} />
      </div>
      <fieldset>
        <legend>Notify</legend>
        {channels === undefined ? (
          <p className="field-hint">Loading channels…</p>
        ) : channels.length === 0 ? (
          <p className="field-hint">
            No channels you can use. Set one up in <Link to="/settings/notifications">Settings › Notifications</Link>.
          </p>
        ) : (
          <div className="rows">
            {channels.map((c) => (
              <label key={c.metadata.id} className="checkbox">
                <input
                  type="checkbox"
                  checked={s.channelIds.includes(c.metadata.id)}
                  onChange={(e) => update({ channelIds: e.target.checked ? [...s.channelIds, c.metadata.id] : s.channelIds.filter((x) => x !== c.metadata.id) })}
                />
                {c.metadata.name} ({channelTypeWord[c.spec.type] ?? c.spec.type}, {channelTarget(c)})
              </label>
            ))}
          </div>
        )}
        <p className="field-hint">With none chosen, the Project's and the platform's default channels are notified.</p>
      </fieldset>
      <label className="checkbox">
        <input type="checkbox" checked={s.enabled} onChange={(e) => update({ enabled: e.target.checked })} />
        Evaluated (turn off to stop the rule without deleting it)
      </label>
      {failure && <Failure error={failure} what="The rule wasn't saved." />}
      <div className="form-actions">
        <button type="submit" className="button button--primary" disabled={busy}>
          {busy ? "Saving…" : creating ? "Create rule" : "Save rule"}
        </button>
        <button type="button" className="button" onClick={onCancel} disabled={busy}>
          Cancel
        </button>
      </div>
      <ApiEquivalent request={request} />
    </form>
  );
}

// ---- Component and Application sections ----

/** The channels a rule can name: the caller's readable ones, and platform defaults. */
function useChannels() {
  return usePoll((s) => apiList<ChannelListItem>("/v1/notificationchannels", s), 60_000, "alert-channels");
}

/**
 * ComponentAlerts: the Component's rules and open Alerts, in the
 * Observability tab (decision 14). Shown once the package is ready, since
 * rules evaluate stored metrics and logs.
 */
export function ComponentAlerts({ comp, envs }: { comp: Component; envs: Environment[] }) {
  const id = comp.metadata.id;
  const access = usePermissions({ kind: "Component", id });
  const rules = usePoll((s) => apiList<AlertRule>(`/v1/alertrules?ownerId=${enc(id)}`, s), 30_000, `comp-rules-${id}`);
  const alerts = usePoll((s) => apiList<Alert>(`/v1/alerts?componentId=${enc(id)}&state=firing`, s), 30_000, `comp-alerts-${id}`);
  const channels = useChannels();
  const [creating, setCreating] = useState(false);
  const now = useNow();
  const names = new Map<string, string>([[id, comp.metadata.name], ...envs.map((e) => [e.metadata.id, e.metadata.name] as [string, string])]);
  const refresh = () => (rules.refresh(), alerts.refresh());
  return (
    <section className="section" aria-labelledby="comp-alerts">
      <div className="pipeline-head">
        <h2 id="comp-alerts" className="section-title">
          Alerts
        </h2>
        <Guarded allowed={access.can(alertActions.ruleWrite)} label="New rule" reason="Your roles here don't allow creating alert rules.">
          <button type="button" className="button" onClick={() => setCreating(true)} disabled={creating}>
            New rule
          </button>
        </Guarded>
      </div>
      {creating && <RuleForm idPrefix="rule-new" owner={{ kind: "Component", id }} envs={envs} channels={channels.data} onDone={() => (setCreating(false), refresh())} onCancel={() => setCreating(false)} />}
      <h3 className="subsection-title">Firing</h3>
      {alerts.error ? (
        <RequestError error={alerts.error} what="alerts" />
      ) : !alerts.data ? (
        <LoadingState what="alerts" />
      ) : alerts.data.length === 0 ? (
        <p className="state-body">Nothing firing.</p>
      ) : (
        <AlertTable alerts={alerts.data} names={names} access={access} now={now} onChanged={refresh} caption={`Alerts firing for ${comp.metadata.name}`} />
      )}
      <h3 className="subsection-title">Rules</h3>
      {rules.error ? (
        <RequestError error={rules.error} what="alert rules" />
      ) : !rules.data ? (
        <LoadingState what="alert rules" />
      ) : (
        <RuleList idPrefix="comp-rule" rules={rules.data} envNames={names} access={access} channels={channels.data} owner={{ kind: "Component", id }} envs={envs} onChanged={refresh} />
      )}
      <ApiEquivalent request={{ method: "GET", path: `/v1/alerts?componentId=${id}&state=firing` }} />
    </section>
  );
}

/** ApplicationAlerts: an Application's own rules and every firing Alert of its Components. */
export function ApplicationAlerts({ app, comps }: { app: Application; comps: Component[] | undefined }) {
  const opt = useOptional("observability");
  // Rules evaluate stored metrics and logs; without the package the log search's gate says so.
  if (!opt.loaded || availability(opt.readiness) !== "ready") return null;
  return <AppAlerts app={app} comps={comps} />;
}

function AppAlerts({ app, comps }: { app: Application; comps: Component[] | undefined }) {
  const id = app.metadata.id;
  const projectId = ownerOf(app.metadata, "Project");
  const access = usePermissions({ kind: "Application", id });
  const rules = usePoll((s) => apiList<AlertRule>(`/v1/alertrules?ownerId=${enc(id)}`, s), 30_000, `app-rules-${id}`);
  const alerts = usePoll((s) => apiList<Alert>(`/v1/alerts?applicationId=${enc(id)}&state=firing`, s), 30_000, `app-alerts-${id}`);
  const envs = usePoll((s) => (projectId ? apiList<Environment>(`/v1/environments?ownerId=${enc(projectId)}`, s) : Promise.resolve([])), 60_000, `app-alert-envs-${projectId ?? ""}`);
  const channels = useChannels();
  const now = useNow();
  const names = new Map<string, string>([...(comps ?? []).map((c) => [c.metadata.id, c.metadata.name] as [string, string]), ...(envs.data ?? []).map((e) => [e.metadata.id, e.metadata.name] as [string, string])]);
  const refresh = () => (rules.refresh(), alerts.refresh());
  return (
    <section className="section" aria-labelledby="app-alerts">
      <h2 id="app-alerts" className="section-title">
        Alerts
      </h2>
      {alerts.error ? (
        <RequestError error={alerts.error} what="alerts" />
      ) : !alerts.data ? (
        <LoadingState what="alerts" />
      ) : alerts.data.length === 0 ? (
        <p className="state-body">Nothing firing in this application.</p>
      ) : (
        <AlertTable alerts={alerts.data} names={names} access={access} now={now} onChanged={refresh} caption={`Alerts firing in ${app.metadata.name}`} />
      )}
      {rules.data && rules.data.length > 0 && (
        <>
          <h3 className="subsection-title">The application's rules</h3>
          <RuleList idPrefix="app-rule" rules={rules.data} envNames={names} access={access} channels={channels.data} owner={{ kind: "Application", id }} envs={envs.data ?? []} onChanged={refresh} />
        </>
      )}
      <p className="field-hint">A component's own rules are on its Observability tab.</p>
      <ApiEquivalent request={{ method: "GET", path: `/v1/alerts?applicationId=${id}&state=firing` }} />
    </section>
  );
}

// ---- The Alerts page ----

/** AlertsPage: every Alert the caller can read, filtered by state and severity; the bell links here. */
export function AlertsPage({ query }: { query: URLSearchParams }) {
  const state = query.get("state") ?? "firing";
  const severity = query.get("severity") ?? "";
  const params = new URLSearchParams();
  if (state) params.set("state", state);
  if (severity) params.set("severity", severity);
  const path = `/v1/alerts${params.toString() ? `?${params}` : ""}`;
  const alerts = usePoll((s) => apiList<Alert>(path, s), 15_000, `alerts-${path}`);
  const builtIn = usePoll(async (s) => (await apiList<AlertRule>("/v1/alertrules", s)).filter((r) => r.spec.builtIn), 60_000, "alerts-builtin");
  const channels = useChannels();
  const rootAccess = usePermissions({});
  const now = useNow();
  // Names for "Where": the Components and Environments the caller can
  // read. Anything else stays an ID (spec 024 T010).
  const places = usePoll(
    async (s) => {
      const [comps, envs] = await Promise.all([apiList<Component>("/v1/components", s), apiList<Environment>("/v1/environments", s)]);
      return new Map<string, string>([...comps, ...envs].map((r) => [r.metadata.id, r.metadata.name] as [string, string]));
    },
    60_000,
    "alerts-places",
  );

  function setFilter(k: string, v: string) {
    const q = new URLSearchParams(query);
    q.set(k, v);
    navigate(`/alerts?${q}`, { replace: true });
  }

  return (
    <>
      <PageHeader title="Alerts" meta={<UpdatedLine at={alerts.updatedAt} now={now} />} />
      <div className="form-row">
        <SelectField
          id="alerts-state"
          label="State"
          value={state}
          onChange={(e) => setFilter("state", e.target.value)}
          options={[
            { value: "firing", label: "Firing" },
            { value: "resolved", label: "Resolved" },
            { value: "", label: "Any" },
          ]}
        />
        <SelectField id="alerts-severity" label="Severity" value={severity} onChange={(e) => setFilter("severity", e.target.value)} options={[{ value: "", label: "Any" }, ...severities.map((v) => ({ value: v, label: severityWord[v] }))]} />
      </div>
      {alerts.error ? (
        <RequestError error={alerts.error} what="alerts" />
      ) : !alerts.data ? (
        <LoadingState what="alerts" />
      ) : alerts.data.length === 0 ? (
        <EmptyState title={state === "firing" ? "Nothing firing" : "No alerts"}>Alerts you can read appear here, from the rules on components' Observability tabs and the packages' built-in rules.</EmptyState>
      ) : (
        <AlertTable alerts={alerts.data} names={places.data ?? new Map()} now={now} onChanged={alerts.refresh} caption="Alerts" />
      )}
      <ApiEquivalent request={{ method: "GET", path }} />
      {builtIn.data && builtIn.data.length > 0 && (
        <section className="section" aria-labelledby="alerts-builtin">
          <h2 id="alerts-builtin" className="section-title">
            Packages' built-in rules
          </h2>
          <p className="field-hint">They go with their package. Change the threshold, silence or turn them off; they can't be deleted.</p>
          <RuleList idPrefix="builtin-rule" rules={builtIn.data} envNames={new Map()} access={rootAccess} channels={channels.data} owner={{ kind: "ProviderInstance", id: "" }} envs={[]} onChanged={builtIn.refresh} />
        </section>
      )}
    </>
  );
}

/** AlertDetail: one Alert, what was sent about it and to where; the notifications' "View in console" opens it. */
export function AlertDetail({ id }: { id: string }) {
  const now = useNow();
  const alert = usePoll((s) => apiFetch<Alert>("GET", `/v1/alerts/${enc(id)}`, undefined, s), 15_000, `alert-${id}`);
  if (alert.error) return <RequestError error={alert.error} what="alert" action={<Link to="/alerts">Back to alerts</Link>} />;
  if (!alert.data) return <LoadingState what="the alert" />;
  return <AlertView a={alert.data} id={id} updatedAt={alert.updatedAt} refresh={alert.refresh} now={now} />;
}

function AlertView({ a, id, updatedAt, refresh, now }: { a: Alert; id: string; updatedAt: Date | undefined; refresh: () => void; now: Date }) {
  // An Alert inherits its rule's access (see ScopedAck).
  const access = usePermissions({ kind: "AlertRule", id: a.spec.ruleId });
  const channels = useChannels();
  const rule = usePoll((s) => apiFetch<AlertRule>("GET", `/v1/alertrules/${enc(a.spec.ruleId)}`, undefined, s), 60_000, `alert-rule-${a.spec.ruleId}`);
  const deliveries = a.status.deliveries ?? [];
  return (
    <>
      <PageHeader
        crumbs={[{ label: "Alerts", to: "/alerts" }]}
        title={a.spec.summary}
        status={
          <span role="status">
            <AlertStateText alert={a} />
          </span>
        }
        actions={<AckButton alert={a} access={access} onDone={refresh} />}
        meta={<UpdatedLine at={updatedAt} now={now} />}
      />
      <section className="section" aria-labelledby="alert-facts">
        <h2 id="alert-facts" className="section-title">
          What happened
        </h2>
        <KeyValueList
          items={[
            ["Severity", <SeverityText severity={a.spec.severity} />],
            ["Value", `${formatValue(a.status.value, undefined)} (threshold ${formatValue(a.status.threshold, undefined)})`],
            ["Started", `${new Date(a.status.startedAt).toLocaleString()} (${formatAge(a.status.startedAt, now)})`],
            ...(a.status.resolvedAt ? ([["Resolved", `${new Date(a.status.resolvedAt).toLocaleString()}: ${resolvedWords(a.status.resolvedReason)}`]] as Array<[string, string]>) : []),
            ["Acknowledged", a.status.acknowledged ? `by ${a.status.acknowledged.by.replace(/^person:/, "")}, ${formatAge(a.status.acknowledged.at, now)}` : "No"],
            ["Rule", rule.data?.metadata?.name ?? <span className="mono">{a.spec.ruleId}</span>],
          ]}
        />
      </section>
      <section className="section" aria-labelledby="alert-deliveries">
        <h2 id="alert-deliveries" className="section-title">
          Notifications
        </h2>
        {deliveries.length === 0 ? (
          <p className="state-body">Nothing sent yet.</p>
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">Channel</th>
                  <th scope="col">Event</th>
                  <th scope="col">State</th>
                  <th scope="col">Last error</th>
                  <th scope="col">When</th>
                </tr>
              </thead>
              <tbody>
                {deliveries.map((d, i) => {
                  const w = deliveryWords(d);
                  return (
                    <tr key={`${d.channelId}-${d.event}-${i}`}>
                      <td>{channels.data?.find((c) => c.metadata.id === d.channelId)?.metadata.name ?? d.channelId}</td>
                      <td>{d.event === "firing" ? "Firing" : "Resolved"}</td>
                      <td>
                        <StatusText kind={w.kind}>{w.word}</StatusText>
                      </td>
                      <td>{d.lastError ?? ""}</td>
                      <td>
                        <time dateTime={d.at} title={d.at}>
                          {formatAge(d.at, now)}
                        </time>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>
      <ApiEquivalent request={{ method: "GET", path: `/v1/alerts/${id}` }} />
    </>
  );
}
