import { useState, type FormEvent, type ReactNode } from "react";
import { ApiError, apiFetch, apiList, isShown, type NetworkError } from "../api/client";
import type { Allowance, ChannelListItem, ChannelType, Heartbeat, HeartbeatSpec, NotificationChannel, NotificationChannelSpec, Project, Secret, Severity, SMTPServer, SMTPServerSpec, TelegramChat } from "../api/types";
import { ApiEquivalent, type ApiRequest } from "../components/ApiEquivalent";
import { ConditionsTable, KeyValueList } from "../components/Data";
import { DeleteConfirm } from "../components/Delete";
import { SelectField, TextField } from "../components/Field";
import { Guarded } from "../components/Guarded";
import { RequestError } from "../components/RequestError";
import { PageHeader } from "../components/Shell";
import { LoadingState, Notice } from "../components/States";
import { StatusText } from "../components/Status";
import { usePermissions, type Access } from "../hooks/usePermissions";
import { useNow, usePoll } from "../hooks/usePoll";
import { alertActions, channelTarget, channelTypeWord, isPlatformChannel, severities, severityWord } from "../lib/alerts";
import { navigate } from "../lib/router";
import { formatAge } from "../lib/time";
import { dnsLabelError, UpdatedLine } from "./common";

// Settings › Notifications (spec 024 plan decision 14): channels, SMTP
// servers and the heartbeat. Every credential is a Secret, chosen here by
// name; its value is never read or shown (secretSet only), and the console
// never calls /v1/secrets/{id}/value. The Telegram "Find chat" step asks
// the server to read the bot token for the caller and answers chats only.

const enc = encodeURIComponent;

/** Alert content leaves the laptop (RP-070): said wherever a destination is set. */
export function LeavesNotice() {
  return (
    <Notice>
      <p>
        Notifications carry alert content (summaries, component, environment and project names, values) to the service you choose: Telegram, your email relay or a webhook's
        receiver. It leaves this machine, so choose services you trust with that.
      </p>
    </Notice>
  );
}

/** "Value stored" or "No value yet", for a Secret a resource names. Never the value. */
export function SecretSetText({ set }: { set: boolean | undefined }) {
  if (set === undefined) return <StatusText kind="unknown">Unknown</StatusText>;
  return set ? <StatusText kind="ok">Value stored</StatusText> : <StatusText kind="warn">No value yet</StatusText>;
}

function Failure({ error, what }: { error: ApiError | NetworkError; what: string }) {
  return (
    <Notice tone="danger">
      <p>
        {error.message} {what}
      </p>
      {error instanceof ApiError && error.requestId && (
        <p className="request-id">
          Request ID <span className="mono">{error.requestId}</span>
        </p>
      )}
    </Notice>
  );
}

/** A Secret picker: names only. */
function SecretField({ id, label, hint, value, secrets, onChange, error }: { id: string; label: string; hint?: string; value: string; secrets: Secret[] | undefined; onChange: (v: string) => void; error?: string | undefined }) {
  const options = [{ value: "", label: secrets === undefined ? "Loading secrets…" : "Choose a Secret" }, ...(secrets ?? []).map((s) => ({ value: s.metadata.id, label: s.metadata.name }))];
  if (value && !(secrets ?? []).some((s) => s.metadata.id === value)) options.push({ value, label: value });
  return <SelectField id={id} label={label} {...(hint ? { hint } : {})} value={value} onChange={(e) => onChange(e.target.value)} options={options} {...(error ? { error } : {})} />;
}

/** An allowance's checkbox and reason, shown only to Owners (the action is in Contributor's NotActions). */
function AllowanceField({ id, label, hint, value, onChange, error }: { id: string; label: string; hint: string; value: Allowance | undefined; onChange: (v: Allowance | undefined) => void; error?: string | undefined }) {
  return (
    <div className="rows">
      <label className="checkbox">
        <input type="checkbox" checked={value !== undefined} onChange={(e) => onChange(e.target.checked ? { reason: "" } : undefined)} />
        {label}
      </label>
      {value !== undefined && <TextField id={id} label="Reason, recorded with the allowance" hint={hint} value={value.reason} maxLength={500} onChange={(e) => onChange({ reason: e.target.value })} {...(error ? { error } : {})} />}
    </div>
  );
}

/** One request and its failure, for test sends and Find chat. */
function useSend<T>() {
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ ok: true; value: T } | { ok: false; error: ApiError | NetworkError }>();
  async function run(path: string, done?: () => void) {
    setBusy(true);
    setResult(undefined);
    try {
      // A test send can take 30 s (an SMTP exchange); the server gives it 40.
      setResult({ ok: true, value: await apiFetch<T>("POST", path, undefined, undefined, 45_000) });
    } catch (err) {
      if (isShown(err)) setResult({ ok: false, error: err });
    } finally {
      setBusy(false);
      done?.();
    }
  }
  return { busy, result, run };
}

/** A test send's answer in words: 422 is the address check, 502 the receiver. */
function SendResult({ result }: { result: { ok: true } | { ok: false; error: ApiError | NetworkError } | undefined }) {
  if (!result) return null;
  if (result.ok) return <p className="field-hint" role="status"><StatusText kind="ok">Sent</StatusText> Accepted by the receiver; check that it arrived.</p>;
  const e = result.error;
  const why = e instanceof ApiError && e.status === 422 ? "The destination was refused by Serendip's address rules." : e instanceof ApiError && e.status === 502 ? "The receiver failed." : e instanceof ApiError && e.status === 503 ? "There's no sender: no cluster is configured." : "";
  return <Failure error={e} what={why} />;
}

// ---- Channels ----

interface ChannelState {
  name: string;
  owner: string; // "" for the platform, else a Project ID
  type: ChannelType;
  minSeverity: Severity;
  isDefault: boolean;
  chatId: string;
  botTokenSecretId: string;
  smtpServerId: string;
  to: string;
  urlInSecret: boolean;
  url: string;
  urlSecretId: string;
  host: string;
  signingSecretId: string;
  allowPrivate: Allowance | undefined;
  allowPlain: Allowance | undefined;
}

function channelState(c: NotificationChannel | undefined, owner: string): ChannelState {
  const s = c?.spec;
  return {
    name: c?.metadata.name ?? "",
    owner,
    type: s?.type ?? "telegram",
    minSeverity: s?.minSeverity ?? "info",
    isDefault: s?.default ?? false,
    chatId: s?.telegram?.chatId ?? "",
    botTokenSecretId: s?.telegram?.botTokenSecretId ?? "",
    smtpServerId: s?.email?.smtpServerId ?? "",
    to: (s?.email?.to ?? []).join(", "),
    urlInSecret: Boolean(s?.webhook?.urlSecretId),
    url: s?.webhook?.url ?? "",
    urlSecretId: s?.webhook?.urlSecretId ?? "",
    host: s?.webhook?.host ?? "",
    signingSecretId: s?.webhook?.signingSecretId ?? "",
    allowPrivate: s?.allowPrivateDestination,
    allowPlain: s?.allowPlainHTTP,
  };
}

function toChannelSpec(s: ChannelState): NotificationChannelSpec {
  const spec: NotificationChannelSpec = { type: s.type, minSeverity: s.minSeverity, default: s.isDefault };
  if (s.type === "telegram") spec.telegram = { chatId: s.chatId.trim(), botTokenSecretId: s.botTokenSecretId };
  if (s.type === "email")
    spec.email = {
      smtpServerId: s.smtpServerId.trim(),
      to: s.to
        .split(",")
        .map((x) => x.trim())
        .filter(Boolean),
    };
  if (s.type === "webhook")
    spec.webhook = s.urlInSecret
      ? { urlSecretId: s.urlSecretId, host: s.host.trim(), ...(s.signingSecretId ? { signingSecretId: s.signingSecretId } : {}) }
      : { url: s.url.trim(), ...(s.signingSecretId ? { signingSecretId: s.signingSecretId } : {}) };
  if (s.allowPrivate) spec.allowPrivateDestination = { reason: s.allowPrivate.reason.trim() };
  if (s.allowPlain) spec.allowPlainHTTP = { reason: s.allowPlain.reason.trim() };
  return spec;
}

export function validateChannel(s: ChannelState, creating: boolean): Record<string, string> {
  const e: Record<string, string> = {};
  if (creating) {
    const n = dnsLabelError(s.name.trim(), "name");
    if (n) e.name = n;
  }
  if (s.type === "telegram") {
    if (!s.botTokenSecretId) e.botTokenSecretId = "Choose the Secret holding the bot token.";
    if (!/^-?\d+$|^@[A-Za-z0-9_]{5,}$/.test(s.chatId.trim())) e.chatId = "A chat ID (a number, negative for groups) or a public channel's @name. Use Find chat.";
  }
  if (s.type === "email") {
    if (!s.smtpServerId.trim()) e.smtpServerId = "Choose an SMTP server.";
    const to = s.to.split(",").map((x) => x.trim()).filter(Boolean);
    if (to.length === 0 || to.length > 20) e.to = "One to 20 addresses, separated by commas.";
    else if (to.some((a) => !/^[^\s@]+@[^\s@]+$/.test(a))) e.to = "Each address looks like name@example.com.";
  }
  if (s.type === "webhook") {
    if (s.urlInSecret) {
      if (!s.urlSecretId) e.urlSecretId = "Choose the Secret holding the URL.";
      if (!/^[A-Za-z0-9.-]+(:\d+)?$/.test(s.host.trim())) e.host = "The URL's host, for example n8n.example.com.";
    } else if (!/^https?:\/\/\S+$/.test(s.url.trim())) e.url = "An https:// URL.";
  }
  if (s.allowPrivate && s.allowPrivate.reason.trim() === "") e.allowPrivate = "Say why this channel may reach a private address.";
  if (s.allowPlain && s.allowPlain.reason.trim() === "") e.allowPlain = "Say why this channel may use plain HTTP.";
  return e;
}

/** Find chat: one getUpdates call, read by the server with the bot token. */
function FindChat({ secretId, onPick }: { secretId: string; onPick: (id: string) => void }) {
  const send = useSend<{ chats: TelegramChat[] }>();
  const path = `/v1/secrets/${secretId}/telegramchats`;
  return (
    <div className="rows">
      <p className="field-hint">
        To find the chat ID: send your bot a message in the chat you want (for a group, add the bot and send a message there), then press Find chat. It asks Telegram once for the chats
        the bot has seen; nothing is saved.
      </p>
      <div className="form-actions">
        <button type="button" className="button" disabled={!secretId || send.busy} onClick={() => void send.run(`/v1/secrets/${enc(secretId)}/telegramchats`)}>
          {send.busy ? "Asking Telegram…" : "Find chat"}
        </button>
      </div>
      {send.result?.ok === false && <Failure error={send.result.error} what={send.result.error instanceof ApiError && send.result.error.status === 409 ? "" : "No chats were read."} />}
      {send.result?.ok && (send.result.value.chats.length === 0 ? (
        <p className="field-hint" role="status">The bot hasn't seen a message yet. Send it one, then try again.</p>
      ) : (
        <ul className="plain-list" aria-label="Chats the bot has seen">
          {send.result.value.chats.map((c) => (
            <li key={c.id}>
              <button type="button" className="button" onClick={() => onPick(c.id)}>
                Use {c.title || c.id} ({c.type}, {c.id})
              </button>
            </li>
          ))}
        </ul>
      ))}
      {secretId && <ApiEquivalent request={{ method: "POST", path }} />}
    </div>
  );
}

function ChannelForm({ channel, projects, secrets, smtp, onDone, onCancel }: {
  channel?: NotificationChannel;
  projects: Project[] | undefined;
  secrets: Secret[] | undefined;
  smtp: SMTPServer[] | undefined;
  onDone: () => void;
  onCancel: () => void;
}) {
  const creating = channel === undefined;
  const ownerOfChannel = channel?.metadata.ownerRefs?.find((o) => o.kind === "Project")?.id ?? "";
  const [s, setS] = useState(() => channelState(channel, ownerOfChannel));
  const [submitted, setSubmitted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<ApiError | NetworkError>();
  const ownerScope = s.owner ? { kind: "Project", id: s.owner } : {};
  const access = usePermissions(channel ? { kind: "NotificationChannel", id: channel.metadata.id } : ownerScope);
  const owner = access.can(alertActions.allowance);
  const update = (p: Partial<ChannelState>) => setS((x) => ({ ...x, ...p }));
  const errors = submitted ? validateChannel(s, creating) : {};
  const err = (k: string) => (errors[k] ? { error: errors[k] } : {});
  const spec = toChannelSpec(s);
  const request: ApiRequest = creating
    ? { method: "POST", path: "/v1/notificationchannels", body: { metadata: { name: s.name.trim(), ...(s.owner ? { ownerRefs: [{ kind: "Project", id: s.owner }] } : {}) }, spec } }
    : { method: "PUT", path: `/v1/notificationchannels/${channel.metadata.id}`, body: { metadata: channel.metadata, spec } };

  async function submit(e: FormEvent) {
    e.preventDefault();
    setSubmitted(true);
    if (Object.keys(validateChannel(s, creating)).length > 0) return;
    setBusy(true);
    setFailure(undefined);
    try {
      await apiFetch(request.method as "POST" | "PUT", creating ? request.path : `/v1/notificationchannels/${enc(channel.metadata.id)}`, request.body);
      onDone();
    } catch (er) {
      if (isShown(er)) setFailure(er);
      setBusy(false);
    }
  }

  return (
    <form className="panel form" onSubmit={submit} noValidate aria-label={creating ? "New channel" : `Edit ${channel.metadata.name}`}>
      {Object.keys(errors).length > 0 && <Notice tone="danger">Some fields need attention. Nothing was sent.</Notice>}
      <LeavesNotice />
      {creating && (
        <div className="form-row">
          <TextField id="ch-name" label="Name" value={s.name} maxLength={63} onChange={(e) => update({ name: e.target.value })} {...err("name")} />
          <SelectField
            id="ch-owner"
            label="Belongs to"
            hint="A Project's rules can use its channels; platform channels are set up by platform Owners."
            value={s.owner}
            onChange={(e) => update({ owner: e.target.value })}
            options={[{ value: "", label: "The platform" }, ...(projects ?? []).map((p) => ({ value: p.metadata.id, label: `Project ${p.metadata.name}` }))]}
          />
          <SelectField
            id="ch-type"
            label="Type"
            value={s.type}
            onChange={(e) => update({ type: e.target.value as ChannelType })}
            options={(["telegram", "email", "webhook"] as const).map((t) => ({ value: t, label: channelTypeWord[t] }))}
          />
        </div>
      )}
      {s.type === "telegram" && (
        <>
          <SecretField id="ch-bot" label="Bot token" hint="The Secret holding the token BotFather gave you. Its value is never shown here." value={s.botTokenSecretId} secrets={secrets} onChange={(v) => update({ botTokenSecretId: v })} error={errors.botTokenSecretId} />
          <TextField id="ch-chat" label="Chat ID" mono value={s.chatId} maxLength={64} onChange={(e) => update({ chatId: e.target.value })} {...err("chatId")} />
          <FindChat secretId={s.botTokenSecretId} onPick={(chatId) => update({ chatId })} />
        </>
      )}
      {s.type === "email" && (
        <>
          {smtp ? (
            <SelectField
              id="ch-smtp"
              label="SMTP server"
              value={s.smtpServerId}
              onChange={(e) => update({ smtpServerId: e.target.value })}
              options={[{ value: "", label: "Choose a server" }, ...smtp.map((x) => ({ value: x.metadata.id, label: `${x.metadata.name} (${x.spec.host})` }))]}
              {...err("smtpServerId")}
            />
          ) : (
            <TextField id="ch-smtp" label="SMTP server ID" hint="SMTP servers are platform relays; ask a platform Owner for the ID." mono value={s.smtpServerId} onChange={(e) => update({ smtpServerId: e.target.value })} {...err("smtpServerId")} />
          )}
          <TextField id="ch-to" label="To" hint="Up to 20 addresses, separated by commas." value={s.to} onChange={(e) => update({ to: e.target.value })} {...err("to")} />
        </>
      )}
      {s.type === "webhook" && (
        <>
          <label className="checkbox">
            <input type="checkbox" checked={s.urlInSecret} onChange={(e) => update({ urlInSecret: e.target.checked })} />
            The URL holds a token (an n8n webhook's path, for example), so keep it in a Secret
          </label>
          {s.urlInSecret ? (
            <div className="form-row">
              <SecretField id="ch-urlsecret" label="URL" value={s.urlSecretId} secrets={secrets} onChange={(v) => update({ urlSecretId: v })} error={errors.urlSecretId} />
              <TextField id="ch-host" label="Its host" hint="Shown, and checked against the address rules before the URL is read." mono value={s.host} onChange={(e) => update({ host: e.target.value })} {...err("host")} />
            </div>
          ) : (
            <TextField id="ch-url" label="URL" mono value={s.url} maxLength={2048} onChange={(e) => update({ url: e.target.value })} {...err("url")} />
          )}
          <SecretField
            id="ch-signing"
            label="Signing key (optional)"
            hint="Signs each body (X-Serendip-Signature: HMAC-SHA256), so the receiver can refuse forgeries and replays."
            value={s.signingSecretId}
            secrets={secrets}
            onChange={(v) => update({ signingSecretId: v })}
          />
        </>
      )}
      <div className="form-row">
        <SelectField id="ch-min" label="Minimum severity" hint="Alerts below it aren't sent here; that's recorded on the Alert." value={s.minSeverity} onChange={(e) => update({ minSeverity: e.target.value as Severity })} options={[...severities].reverse().map((v) => ({ value: v, label: severityWord[v] }))} />
      </div>
      <label className="checkbox">
        <input type="checkbox" checked={s.isDefault} onChange={(e) => update({ isDefault: e.target.checked })} />
        A default channel: notified by rules that name no channel
      </label>
      {owner && (
        <fieldset>
          <legend>Allowances (Owners only)</legend>
          <AllowanceField id="ch-private" label="May reach a private address (on the LAN)" hint="For an in-house receiver on the LAN, for example. Loopback, the cloud metadata address and the cluster's own addresses are never allowed." value={s.allowPrivate} onChange={(v) => update({ allowPrivate: v })} error={errors.allowPrivate} />
          {s.type === "webhook" && <AllowanceField id="ch-plain" label="May use plain HTTP" hint="The body crosses the network unencrypted." value={s.allowPlain} onChange={(v) => update({ allowPlain: v })} error={errors.allowPlain} />}
        </fieldset>
      )}
      {failure && <Failure error={failure} what="The channel wasn't saved." />}
      <div className="form-actions">
        <button type="submit" className="button button--primary" disabled={busy}>
          {busy ? "Saving…" : creating ? "Create channel" : "Save channel"}
        </button>
        <button type="button" className="button" onClick={onCancel} disabled={busy}>
          Cancel
        </button>
      </div>
      <ApiEquivalent request={request} />
    </form>
  );
}

type Open = { kind: "edit" | "delete"; id: string } | { kind: "new" } | undefined;

function ChannelRow({ c, projects, secrets, smtp, open, setOpen, onChanged, now }: {
  c: NotificationChannel;
  projects: Project[] | undefined;
  secrets: Secret[] | undefined;
  smtp: SMTPServer[] | undefined;
  open: Open;
  setOpen: (o: Open) => void;
  onChanged: () => void;
  now: Date;
}) {
  const access = usePermissions({ kind: "NotificationChannel", id: c.metadata.id });
  const test = useSend<{ result: string }>();
  const id = c.metadata.id;
  const project = c.metadata.ownerRefs?.find((o) => o.kind === "Project")?.id;
  const projectName = project ? (projects?.find((p) => p.metadata.id === project)?.metadata.name ?? project) : undefined;
  const secretFields = Object.entries(c.secretSet ?? {});
  const mine = open && open.kind !== "new" && open.id === id;
  return (
    <li className="panel">
      <div className="pipeline-head">
        <h3 className="subsection-title">
          {c.metadata.name} · {channelTypeWord[c.spec.type] ?? c.spec.type}
        </h3>
        {c.spec.default && <StatusText kind="ok">Default</StatusText>}
      </div>
      <KeyValueList
        items={[
          ["Sends to", channelTarget(c)],
          ["Belongs to", projectName ? `Project ${projectName}` : "The platform"],
          ["Minimum severity", severityWord[c.spec.minSeverity ?? "info"]],
          ...secretFields.map(([field, set]) => [secretFieldWord(field), <SecretSetText set={set} />] as [string, ReactNode]),
          ...(c.spec.allowPrivateDestination ? ([["Private address allowed", c.spec.allowPrivateDestination.reason]] as Array<[string, string]>) : []),
          ...(c.spec.allowPlainHTTP ? ([["Plain HTTP allowed", c.spec.allowPlainHTTP.reason]] as Array<[string, string]>) : []),
          ["Last test", c.status.lastTest ? `${c.status.lastTestResult ?? ""}, ${formatAge(c.status.lastTest, now)}` : "Never"],
        ]}
      />
      <div className="form-actions">
        <Guarded allowed={access.can(alertActions.channelTest)} label="Send a test" reason="Your roles don't allow test sends to this channel.">
          <button type="button" className="button" disabled={test.busy} onClick={() => void test.run(`/v1/notificationchannels/${enc(id)}/test`, onChanged)}>
            {test.busy ? "Sending…" : "Send a test"}
          </button>
        </Guarded>
        <Guarded allowed={access.can(alertActions.channelWrite)} label="Edit" reason="Your roles don't allow changing this channel.">
          <button type="button" className="button" disabled={Boolean(mine)} onClick={() => setOpen({ kind: "edit", id })}>
            Edit
          </button>
        </Guarded>
        <Guarded allowed={access.can(alertActions.channelDelete)} label="Delete" reason="Your roles don't allow deleting this channel.">
          <button type="button" className="button" disabled={Boolean(mine)} onClick={() => setOpen({ kind: "delete", id })}>
            Delete
          </button>
        </Guarded>
      </div>
      <SendResult result={test.result} />
      {(test.busy || test.result) && <ApiEquivalent request={{ method: "POST", path: `/v1/notificationchannels/${id}/test` }} />}
      {mine && open.kind === "edit" && <ChannelForm channel={c} projects={projects} secrets={secrets} smtp={smtp} onDone={() => (setOpen(undefined), onChanged())} onCancel={() => setOpen(undefined)} />}
      {mine && open.kind === "delete" && (
        <DeleteConfirm
          target={{ kind: "NotificationChannel", id, name: c.metadata.name }}
          cascade={false}
          noun="channel"
          note="A channel a rule still names can't be deleted: take it off those rules first."
          onDeleted={(approval) => (approval ? navigate(`/approvals/${enc(approval)}`) : (setOpen(undefined), onChanged()))}
          onCancel={() => setOpen(undefined)}
        />
      )}
    </li>
  );
}

function secretFieldWord(field: string): string {
  const words: Record<string, string> = {
    botTokenSecretId: "Bot token",
    urlSecretId: "URL",
    signingSecretId: "Signing key",
    usernameSecretId: "Username",
    passwordSecretId: "Password",
  };
  return words[field] ?? field;
}

function Channels({ projects, secrets, smtp }: { projects: Project[] | undefined; secrets: Secret[] | undefined; smtp: SMTPServer[] | undefined }) {
  const channels = usePoll((s) => apiList<ChannelListItem>("/v1/notificationchannels", s), 30_000, "settings-channels");
  const [open, setOpen] = useState<Open>();
  const now = useNow();
  const readable = (channels.data ?? []).filter((c): c is NotificationChannel => !isPlatformChannel(c));
  const platform = (channels.data ?? []).filter(isPlatformChannel);
  return (
    <section className="section" aria-labelledby="set-channels">
      <div className="pipeline-head">
        <h2 id="set-channels" className="section-title">
          Channels
        </h2>
        <button type="button" className="button" onClick={() => setOpen({ kind: "new" })} disabled={open?.kind === "new"}>
          New channel
        </button>
      </div>
      <p className="field-hint">Telegram, email through an SMTP relay, or a signed webhook (n8n, Node-RED, Apprise and others take it from there). Creating one needs the right to write channels where it belongs; the server decides.</p>
      {open?.kind === "new" && <ChannelForm projects={projects} secrets={secrets} smtp={smtp} onDone={() => (setOpen(undefined), channels.refresh())} onCancel={() => setOpen(undefined)} />}
      {channels.error ? (
        <RequestError error={channels.error} what="channels" />
      ) : !channels.data ? (
        <LoadingState what="channels" />
      ) : channels.data.length === 0 ? (
        <p className="state-body">No channels yet. Alerts still open and show in the console; nothing is sent.</p>
      ) : (
        <ul className="plain-list" aria-label="Channels">
          {readable.map((c) => (
            <ChannelRow key={c.metadata.id} c={c} projects={projects} secrets={secrets} smtp={smtp} open={open} setOpen={setOpen} onChanged={channels.refresh} now={now} />
          ))}
          {platform.map((c) => (
            <li key={c.metadata.id} className="panel">
              <h3 className="subsection-title">
                {c.metadata.name} · {channelTypeWord[c.spec.type] ?? c.spec.type}
              </h3>
              <p className="field-hint">A platform default channel: your rules can use it. Its settings are shown only to platform readers.</p>
            </li>
          ))}
        </ul>
      )}
      <ApiEquivalent request={{ method: "GET", path: "/v1/notificationchannels" }} />
    </section>
  );
}

// ---- SMTP servers ----

function SMTPForm({ server, secrets, onDone, onCancel }: { server?: SMTPServer; secrets: Secret[] | undefined; onDone: () => void; onCancel: () => void }) {
  const creating = server === undefined;
  const [name, setName] = useState(server?.metadata.name ?? "");
  const [spec, setSpec] = useState<SMTPServerSpec>(server?.spec ?? { host: "smtp.gmail.com", port: 587, tls: "starttls", from: "", usernameSecretId: "", passwordSecretId: "" });
  const [port, setPort] = useState(String(server?.spec.port ?? 587));
  const [submitted, setSubmitted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<ApiError | NetworkError>();
  const body = { ...spec, host: spec.host.trim(), from: spec.from.trim(), port: Number(port) };
  const e: Record<string, string> = {};
  if (submitted) {
    if (creating) {
      const n = dnsLabelError(name.trim(), "name");
      if (n) e.name = n;
    }
    if (!/^[A-Za-z0-9.-]+$/.test(body.host)) e.host = "A host name, for example smtp.gmail.com.";
    if (!Number.isInteger(body.port) || body.port < 1 || body.port > 65535) e.port = "A port, usually 587 (STARTTLS) or 465 (TLS).";
    if (!/^[^\s@]+@[^\s@]+$/.test(body.from)) e.from = "The sender address, for example alerts@example.com.";
    if (!body.usernameSecretId) e.usernameSecretId = "Choose the Secret holding the username.";
    if (!body.passwordSecretId) e.passwordSecretId = "Choose the Secret holding the password.";
  }
  const request: ApiRequest = creating ? { method: "POST", path: "/v1/smtpservers", body: { metadata: { name: name.trim() }, spec: body } } : { method: "PUT", path: `/v1/smtpservers/${server.metadata.id}`, body: { metadata: server.metadata, spec: body } };
  async function submit(ev: FormEvent) {
    ev.preventDefault();
    setSubmitted(true);
    // Validation runs on the next render too; this repeats it for the click.
    const ok = (!creating || !dnsLabelError(name.trim(), "name")) && /^[A-Za-z0-9.-]+$/.test(body.host) && /^[^\s@]+@[^\s@]+$/.test(body.from) && body.usernameSecretId && body.passwordSecretId && Number.isInteger(body.port) && body.port >= 1 && body.port <= 65535;
    if (!ok) return;
    setBusy(true);
    setFailure(undefined);
    try {
      await apiFetch(request.method as "POST" | "PUT", creating ? request.path : `/v1/smtpservers/${enc(server.metadata.id)}`, request.body);
      onDone();
    } catch (er) {
      if (isShown(er)) setFailure(er);
      setBusy(false);
    }
  }
  return (
    <form className="panel form" onSubmit={submit} noValidate aria-label={creating ? "New SMTP server" : `Edit ${server.metadata.name}`}>
      <div className="rows">
        <p className="field-hint">
          Free choices: a Gmail account with 2-Step Verification and an app password (smtp.gmail.com, 587 with STARTTLS or 465 with TLS; about 500 messages a day). Or a relay such as
          Brevo (smtp-relay.brevo.com, 587, 300 a day free), which needs a domain you own and have authenticated, or mail lands in spam.
        </p>
        <p className="field-hint">Serendip reports “accepted by the relay”, never “delivered”: what happens after the relay is the relay's.</p>
      </div>
      {creating && <TextField id="smtp-name" label="Name" value={name} maxLength={63} onChange={(ev) => setName(ev.target.value)} {...(e.name ? { error: e.name } : {})} />}
      <div className="form-row">
        <TextField id="smtp-host" label="Host" mono value={spec.host} onChange={(ev) => setSpec({ ...spec, host: ev.target.value })} {...(e.host ? { error: e.host } : {})} />
        <TextField id="smtp-port" label="Port" inputMode="numeric" value={port} onChange={(ev) => setPort(ev.target.value)} {...(e.port ? { error: e.port } : {})} />
        <SelectField
          id="smtp-tls"
          label="Encryption"
          hint="Plain SMTP isn't offered: the password is sent only over TLS."
          value={spec.tls}
          onChange={(ev) => setSpec({ ...spec, tls: ev.target.value as SMTPServerSpec["tls"] })}
          options={[
            { value: "starttls", label: "STARTTLS (usually 587)" },
            { value: "implicit", label: "TLS (usually 465)" },
          ]}
        />
      </div>
      <TextField id="smtp-from" label="From" value={spec.from} onChange={(ev) => setSpec({ ...spec, from: ev.target.value })} {...(e.from ? { error: e.from } : {})} />
      <div className="form-row">
        <SecretField id="smtp-user" label="Username" value={spec.usernameSecretId} secrets={secrets} onChange={(v) => setSpec({ ...spec, usernameSecretId: v })} error={e.usernameSecretId} />
        <SecretField id="smtp-pass" label="Password" hint="For Gmail, the app password, not the account's." value={spec.passwordSecretId} secrets={secrets} onChange={(v) => setSpec({ ...spec, passwordSecretId: v })} error={e.passwordSecretId} />
      </div>
      {failure && <Failure error={failure} what="The server wasn't saved." />}
      <div className="form-actions">
        <button type="submit" className="button button--primary" disabled={busy}>
          {busy ? "Saving…" : creating ? "Create SMTP server" : "Save SMTP server"}
        </button>
        <button type="button" className="button" onClick={onCancel} disabled={busy}>
          Cancel
        </button>
      </div>
      <ApiEquivalent request={request} />
    </form>
  );
}

function SMTPServers({ access, smtp, secrets, onChanged }: { access: Access; smtp: { data: SMTPServer[] | undefined; error: ApiError | NetworkError | undefined }; secrets: Secret[] | undefined; onChanged: () => void }) {
  const [open, setOpen] = useState<Open>();
  return (
    <section className="section" aria-labelledby="set-smtp">
      <div className="pipeline-head">
        <h2 id="set-smtp" className="section-title">
          SMTP servers
        </h2>
        <Guarded allowed={access.can(alertActions.smtpWrite)} label="New SMTP server" reason="SMTP servers are platform relays, set up by platform Owners.">
          <button type="button" className="button" onClick={() => setOpen({ kind: "new" })} disabled={open?.kind === "new"}>
            New SMTP server
          </button>
        </Guarded>
      </div>
      <p className="field-hint">Email channels send through one of these, so Projects can email without holding the relay's credentials.</p>
      {open?.kind === "new" && <SMTPForm secrets={secrets} onDone={() => (setOpen(undefined), onChanged())} onCancel={() => setOpen(undefined)} />}
      {smtp.error ? (
        smtp.error instanceof ApiError && smtp.error.status === 403 ? (
          <p className="state-body">Your roles don't allow reading the platform's SMTP servers.</p>
        ) : (
          <RequestError error={smtp.error} what="SMTP servers" />
        )
      ) : !smtp.data ? (
        <LoadingState what="SMTP servers" />
      ) : smtp.data.length === 0 ? (
        <p className="state-body">No SMTP servers yet.</p>
      ) : (
        <ul className="plain-list" aria-label="SMTP servers">
          {smtp.data.map((x) => {
            const mine = open && open.kind !== "new" && open.id === x.metadata.id;
            return (
              <li key={x.metadata.id} className="panel">
                <h3 className="subsection-title">{x.metadata.name}</h3>
                <KeyValueList
                  items={[
                    ["Relay", `${x.spec.host}:${x.spec.port} (${x.spec.tls === "implicit" ? "TLS" : "STARTTLS"})`],
                    ["From", x.spec.from],
                    ["Username", <SecretSetText set={x.secretSet?.usernameSecretId} />],
                    ["Password", <SecretSetText set={x.secretSet?.passwordSecretId} />],
                  ]}
                />
                <div className="form-actions">
                  <Guarded allowed={access.can(alertActions.smtpWrite)} label="Edit" reason="Only platform Owners change SMTP servers.">
                    <button type="button" className="button" disabled={Boolean(mine)} onClick={() => setOpen({ kind: "edit", id: x.metadata.id })}>
                      Edit
                    </button>
                  </Guarded>
                  <Guarded allowed={access.can(alertActions.smtpDelete)} label="Delete" reason="Only platform Owners delete SMTP servers.">
                    <button type="button" className="button" disabled={Boolean(mine)} onClick={() => setOpen({ kind: "delete", id: x.metadata.id })}>
                      Delete
                    </button>
                  </Guarded>
                </div>
                {mine && open.kind === "edit" && <SMTPForm server={x} secrets={secrets} onDone={() => (setOpen(undefined), onChanged())} onCancel={() => setOpen(undefined)} />}
                {mine && open.kind === "delete" && (
                  <DeleteConfirm
                    target={{ kind: "SMTPServer", id: x.metadata.id, name: x.metadata.name }}
                    cascade={false}
                    noun="SMTP server"
                    note="A server an email channel still uses can't be deleted."
                    onDeleted={(approval) => (approval ? navigate(`/approvals/${enc(approval)}`) : (setOpen(undefined), onChanged()))}
                    onCancel={() => setOpen(undefined)}
                  />
                )}
              </li>
            );
          })}
        </ul>
      )}
      <ApiEquivalent request={{ method: "GET", path: "/v1/smtpservers" }} />
    </section>
  );
}

// ---- The heartbeat ----

function HeartbeatForm({ hb, secrets, owner, onDone, onCancel }: { hb: Heartbeat | undefined; secrets: Secret[] | undefined; owner: boolean | undefined; onDone: () => void; onCancel: () => void }) {
  const [spec, setSpec] = useState<HeartbeatSpec>(hb?.spec ?? { urlSecretId: "", host: "hc-ping.com", method: "GET", intervalMinutes: 1 });
  const [interval, setIntervalText] = useState(String(hb?.spec.intervalMinutes ?? 1));
  const [submitted, setSubmitted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<ApiError | NetworkError>();
  const body: HeartbeatSpec = { ...spec, host: spec.host.trim(), intervalMinutes: Number(interval) };
  if (body.allowPrivateDestination) body.allowPrivateDestination = { reason: body.allowPrivateDestination.reason.trim() };
  const check = () => {
    const e: Record<string, string> = {};
    if (!body.urlSecretId) e.url = "Choose the Secret holding the ping URL.";
    if (!/^[A-Za-z0-9.-]+(:\d+)?$/.test(body.host)) e.host = "The URL's host, for example hc-ping.com.";
    if (!Number.isInteger(body.intervalMinutes) || body.intervalMinutes! < 1 || body.intervalMinutes! > 10) e.interval = "Whole minutes, 1 to 10.";
    if (body.allowPrivateDestination && body.allowPrivateDestination.reason === "") e.allow = "Say why the monitor is on a private address.";
    return e;
  };
  const e = submitted ? check() : {};
  const request: ApiRequest = { method: "PUT", path: "/v1/heartbeat", body: { ...(hb ? { metadata: hb.metadata } : {}), spec: body } };
  async function submit(ev: FormEvent) {
    ev.preventDefault();
    setSubmitted(true);
    if (Object.keys(check()).length > 0) return;
    setBusy(true);
    setFailure(undefined);
    try {
      await apiFetch("PUT", "/v1/heartbeat", request.body);
      onDone();
    } catch (er) {
      if (isShown(er)) setFailure(er);
      setBusy(false);
    }
  }
  return (
    <form className="panel form" onSubmit={submit} noValidate aria-label="Heartbeat settings">
      <div className="form-row">
        <SecretField id="hb-url" label="Ping URL" hint="The monitor's ping URL holds its key, so it's a Secret." value={spec.urlSecretId} secrets={secrets} onChange={(v) => setSpec({ ...spec, urlSecretId: v })} error={e.url} />
        <TextField id="hb-host" label="Its host" mono value={spec.host} onChange={(ev) => setSpec({ ...spec, host: ev.target.value })} {...(e.host ? { error: e.host } : {})} />
      </div>
      <div className="form-row">
        <SelectField id="hb-method" label="Method" value={spec.method ?? "GET"} onChange={(ev) => setSpec({ ...spec, method: ev.target.value as "GET" | "POST" })} options={[{ value: "GET", label: "GET" }, { value: "POST", label: "POST, empty body" }]} />
        <TextField id="hb-interval" label="Every, minutes" inputMode="numeric" value={interval} onChange={(ev) => setIntervalText(ev.target.value)} {...(e.interval ? { error: e.interval } : {})} />
      </div>
      {owner && (
        <AllowanceField
          id="hb-private"
          label="The monitor is on a private address (Gatus on a second device on the LAN, for example)"
          hint="The cloud metadata address is never allowed."
          value={spec.allowPrivateDestination}
          onChange={(v) => setSpec(v ? { ...spec, allowPrivateDestination: v } : (({ allowPrivateDestination: _, ...rest }) => rest)(spec))}
          error={e.allow}
        />
      )}
      {failure && <Failure error={failure} what="The heartbeat wasn't saved." />}
      <div className="form-actions">
        <button type="submit" className="button button--primary" disabled={busy}>
          {busy ? "Saving…" : "Save heartbeat"}
        </button>
        <button type="button" className="button" onClick={onCancel} disabled={busy}>
          Cancel
        </button>
      </div>
      <ApiEquivalent request={request} />
    </form>
  );
}

function HeartbeatSection({ access, secrets }: { access: Access; secrets: Secret[] | undefined }) {
  const hb = usePoll(
    (s) =>
      apiFetch<Heartbeat>("GET", "/v1/heartbeat", undefined, s).catch((err: unknown) => {
        if (err instanceof ApiError && err.status === 404) return null;
        throw err;
      }),
    30_000,
    "settings-heartbeat",
  );
  const [open, setOpen] = useState<"edit" | "delete">();
  const test = useSend<{ result: string }>();
  const now = useNow();
  const h = hb.data ?? undefined;
  const canWrite = access.can(alertActions.heartbeatWrite);
  return (
    <section className="section" aria-labelledby="set-heartbeat">
      <div className="pipeline-head">
        <h2 id="set-heartbeat" className="section-title">
          Heartbeat
        </h2>
      </div>
      <p className="field-hint">
        Serendip pings an outside monitor while it can alert: the evaluator has run in the last 2 minutes, deliveries are running and the store answers. When the pings stop, the monitor
        raises the alarm. The monitor must not run on this machine. healthchecks.io's free tier, Uptime Kuma's push monitors or Gatus on a second device all work.
      </p>
      {hb.error ? (
        hb.error instanceof ApiError && hb.error.status === 403 ? (
          <p className="state-body">Your roles don't allow reading the heartbeat: it's a platform setting.</p>
        ) : (
          <RequestError error={hb.error} what="the heartbeat" />
        )
      ) : hb.data === undefined ? (
        <LoadingState what="the heartbeat" />
      ) : !h ? (
        <p className="state-body">No heartbeat is set. If this machine stops, nothing outside it will notice.</p>
      ) : (
        <>
          <KeyValueList
            items={[
              ["Pings", `${h.spec.method ?? "GET"} ${h.spec.host}, every ${h.spec.intervalMinutes ?? 1} min`],
              ["Ping URL", <SecretSetText set={h.secretSet?.urlSecretId} />],
              ["Last result", h.status.lastResult ?? "Nothing sent yet"],
              ["Last sent", h.status.lastSent ? `${new Date(h.status.lastSent).toLocaleString()} (${formatAge(h.status.lastSent, now)})` : "Never"],
              ...(h.spec.allowPrivateDestination ? ([["Private address allowed", h.spec.allowPrivateDestination.reason]] as Array<[string, string]>) : []),
            ]}
          />
          <ConditionsTable conditions={h.status.conditions} now={now} />
        </>
      )}
      {hb.data !== undefined && (
        <div className="form-actions">
          <Guarded allowed={canWrite} label={h ? "Edit" : "Set up a heartbeat"} reason="Only platform Owners set the heartbeat.">
            <button type="button" className="button" disabled={open !== undefined} onClick={() => setOpen("edit")}>
              {h ? "Edit" : "Set up a heartbeat"}
            </button>
          </Guarded>
          {h && (
            <>
              <Guarded allowed={canWrite} label="Send one now" reason="Only platform Owners test the heartbeat.">
                <button type="button" className="button" disabled={test.busy} onClick={() => void test.run("/v1/heartbeat/test", hb.refresh)}>
                  {test.busy ? "Sending…" : "Send one now"}
                </button>
              </Guarded>
              <Guarded allowed={access.can(alertActions.heartbeatDelete)} label="Stop the heartbeat" reason="Only platform Owners stop the heartbeat.">
                <button type="button" className="button" disabled={open !== undefined} onClick={() => setOpen("delete")}>
                  Stop the heartbeat
                </button>
              </Guarded>
            </>
          )}
        </div>
      )}
      <SendResult result={test.result} />
      {open === "edit" && <HeartbeatForm hb={h} secrets={secrets} owner={access.can(alertActions.allowance)} onDone={() => (setOpen(undefined), hb.refresh())} onCancel={() => setOpen(undefined)} />}
      {open === "delete" && h && (
        <DeleteConfirm
          target={{ kind: "Heartbeat", id: h.metadata.id, name: h.metadata.name }}
          cascade={false}
          noun="heartbeat"
          path="/v1/heartbeat"
          note="The outside monitor will raise its alarm when the pings stop: pause it there first."
          onDeleted={(approval) => (approval ? navigate(`/approvals/${enc(approval)}`) : (setOpen(undefined), hb.refresh()))}
          onCancel={() => setOpen(undefined)}
        />
      )}
      <ApiEquivalent request={{ method: "GET", path: "/v1/heartbeat" }} />
    </section>
  );
}

/** NotificationsPage: Settings › Notifications. */
export function NotificationsPage() {
  const now = useNow();
  const access = usePermissions({});
  const projects = usePoll((s) => apiList<Project>("/v1/projects", s), 60_000, "settings-projects");
  const secrets = usePoll((s) => apiList<Secret>("/v1/secrets", s), 60_000, "settings-secrets");
  const smtp = usePoll((s) => apiList<SMTPServer>("/v1/smtpservers", s), 60_000, "settings-smtp");
  return (
    <>
      <PageHeader crumbs={[{ label: "Settings", to: "/settings/notifications" }]} title="Notifications" meta={<UpdatedLine at={smtp.updatedAt} now={now} />} />
      <LeavesNotice />
      <Channels projects={projects.data} secrets={secrets.data} smtp={smtp.data} />
      <SMTPServers access={access} smtp={smtp} secrets={secrets.data} onChanged={smtp.refresh} />
      <HeartbeatSection access={access} secrets={secrets.data} />
    </>
  );
}
