# Plan: Spec 024 — Alert rules and notification channels

Status: Draft, 2026-11-15. Awaiting approval.

Upstream facts come from two research passes on 2026-11-15: oss-researcher
on OpenChoreo's alerting, and a channel-pricing review. The OpenChoreo
facts were read from the `main` branches of OpenChoreo and
community-modules. T001 checks them against what is pinned (plane chart
1.3.1, logs-openobserve 0.8.0) before code depends on them. "Research:"
marks a fact that hasn't been verified here yet.

## The decision that shapes everything: Serendip evaluates the rules

The spec's original leaning was that the backend would evaluate rules and
Serendip would only send the notifications. The research rules that out
for this install.

- **OpenChoreo's alerting path:**
  - `ObservabilityAlertRule` → the Observer → the logs or metrics adapter
    → an OpenObserve alert or a **`PrometheusRule`**.
  - A firing comes back through the adapter's webhook to the Observer.
    The Observer stores it in its own SQLite and **sends the notification
    itself**, from `ObservabilityAlertsNotificationChannel` CRs.
  - Those CRs hold SMTP settings and webhook headers as Kubernetes
    `secretKeyRef`s.
- **Metric rules need the Prometheus Operator and Alertmanager.** The
  metrics adapter only writes `PrometheusRule` CRs. Spec 023 removed the
  Operator and its 10 CRDs (ADR 0021). That saved about 450 MiB at idle,
  plus about 390 MiB that kube-apiserver kept holding after removal.
  Bringing them back for alerts would reverse a measured decision.
- **The Observer's sender:**
  - it would send to person-typed URLs from a pod inside the cluster,
    with no validation of ours (RP-062, made worse);
  - it would keep credentials outside the Secrets capability (spec IN 8);
  - it would create a second store of Alerts beside Serendip's.
- **OpenObserve sends no "resolved" event** (Research: from a
  third-party guide). Serendip would have to work out resolution anyway.

**Decided: `serendipd` evaluates every rule itself.**
- **How:** on each rule's interval it queries the metrics and logs
  adapters, and Prometheus for restarts. Serendip builds every query;
  none is user-written PromQL or SQL.
- **What it keeps:** the Alert state, and it sends the notifications
  itself.

**Why this is cheaper and safer:**
- Nothing new is installed for alerting: no Operator, no Alertmanager and
  no backend alert objects. So RP-066 doesn't grow (spec open question
  5).
- No firing has to reach `serendipd`, so there's no new listener (open
  question 3).
- Only `serendipd` ever contacts a person-typed address, through one
  validating client (spec IN 9).
- `core/` sees only rules and conditions, in its own words (constitution
  1).
- Resolution is simply the same evaluation coming back clear.

**What it costs:** query load, one query per rule per interval. The caps
in decision 2 bound it, and T011 measures it.

**cs-tools' design still applies** (spec "Prior art"):
- fingerprints and a reopen window for folding;
- a separate delivery loop with retries.

Its ingestion mappings aren't needed, because nothing is ingested.

## Channel research (spec IN 7, open question 6)

Each v1 type is free, outbound only, a plain `net/http` POST or a
`net/smtp` send, and needs no public URL.

| Type | Free use | Notes |
|---|---|---|
| Webhook, Serendip format | yes | Signed JSON body |
| Webhook, Slack-compatible | yes | `{"text": …}`. Reaches Slack, Mattermost, Rocket.Chat and Google Chat (Google Chat needs a Workspace account) |
| Webhook, Discord | yes | `content` and `embeds`. About 30 requests a minute per webhook (unofficial); honours 429 |
| Webhook, Teams | yes | Office 365 connectors stopped working in May 2026. Teams' replacement is a Workflows "when a webhook request is received" flow, taking an Adaptive Card |
| Email (SMTP) | yes | Through a relay, for example Gmail with an app password, Brevo (300 a day, no card) or Resend (100 a day) |
| ntfy | yes | `ntfy.sh` or self-hosted. Priority 5 ("urgent") overrides a phone's quiet mode: the free stand-in for a page |
| Telegram bot | yes | `sendMessage`, about one message a second per chat. Needs only outbound HTTPS |

**Deferred, as opt-in channel types for a later spec** (spec OUT):
- **SMS:** Twilio (UK about $0.056 a segment), an Android phone as a
  gateway (sms-gate, costing only the SIM), Sri Lankan gateways (about
  LKR 0.6–1.0 a message);
- **voice calls:** no free API exists. Twilio Voice in the UK is about
  3¢ a minute, and ntfy's `X-Call` is on Pro plans only;
- **the WhatsApp Cloud API:** messages outside the 24-hour window are
  paid templates, and it needs Meta Business verification;
- **Pushover** ($4.99 once), **Matrix** and **Gotify** (free, self-hosted).

**Rejected:**
- **whatsmeow and other unofficial WhatsApp clients:** they break
  WhatsApp's terms, and accounts get banned;
- **email-to-SMS gateways:** gone in the UK;
- **Textbelt's free tier:** one message a day.

**OTP, for a later identity spec (not 024):**
- the free options deliver nothing: TOTP authenticator apps, and passkeys
  (ThunderID has a passkey executor);
- email OTP goes through ThunderID's own SMTP setting;
- ThunderID's SMS senders are Twilio, Vonage and a "Custom" sender that
  POSTs to a URL. `serendipd` could be that URL, to send codes over
  Telegram or an Android gateway (Research: the request shape is
  unchecked);
- Telegram Gateway costs $0.01 a code.

These findings go in `build-vs-adopt.md` and PROGRESS.md's "Maybe" list.

## Spec amendments (agreed with this plan)

- **Already folded into spec.md:**
  - traces moved to spec 025;
  - AC 7 reads "status and measured cost", not "backend repaired";
  - the channel types are set.
- **Open questions 1–6** are answered by the decisions below.

## Decisions

1. **The new kinds** (`core/resource`), all with status conditions.
   - **`AlertRule`:**
     - owned by a Component or an Application, with an `environmentId`;
     - spec: `condition` (decision 3), `severity`, `summary`,
       `channelIds`, `interval` (1–60 min, default 1) and `enabled`;
     - status: `active` (with its reason), `lastEvaluated`,
       `lastValue`, and `silence` (who, why, until).
   - **`Alert`**, replacing spec 001's placeholder `AlertSpec`:
     - **owned by its rule,** so deleting a rule lists its Alerts in the
       Approval's blast radius;
     - **written only by `serendipd`,** like `Approval`: it isn't in
       `resourcePaths`, so it has no generic `PUT` or `DELETE`;
     - **spec, fixed at creation:** rule, Component, Environment,
       severity, summary, fingerprint;
     - **status:**
       - `state` (`firing` or `resolved`);
       - `startedAt`, and `resolvedAt` with its reason (`clear`,
         `ruleDeleted`, `packageRemoved` or `componentGone`);
       - `value` and `threshold`;
       - `acknowledged` (who, when);
       - `deliveries[]` (channel, event, attempts, state, last error,
         time), capped at 20.
   - **`NotificationChannel`:**
     - at the platform (no owner) or owned by a Project;
     - `type` and its settings (decision 6);
     - `default` (bool);
     - `allowPrivateDestination` and `allowPlainHTTP`, each with a
       `reason`.
   - **`SMTPServer`:**
     - platform-only;
     - fields: `host`, `port`, `tls` (`starttls` or `implicit`, never
       plain), `from`, `usernameSecretId` and `passwordSecretId`;
     - it's a separate kind so Projects can create email channels
       without holding SMTP credentials.
   - **Graph edges:**
     - rule → Component or Application, and rule → channel;
     - Alert → rule;
     - channel → Secret, and channel → SMTP server;
     - SMTP server → Secret.

     Deleting a channel, SMTP server or Secret that something still
     uses is refused with 409, as for other references.

2. **The evaluator** (`core/reconcile/alerts.go`). One goroutine in
   `serendipd`, beside the engine. It's an ops loop, not a resource
   reconcile, the same way package jobs are separate.
   - **Each tick** (15 s), it takes the rules that are due, with a little
     jitter. It runs at most 2 queries at once, each with a 20 s timeout.
   - **Caps:** 200 enabled rules per install, and a 1-minute minimum
     interval. That's at most about 3.3 queries a second. Anything beyond
     the caps is refused, with the reason.
   - **No write per evaluation.** `lastValue` and `lastEvaluated` are held
     in memory. They're flushed at most once a minute, and only for rules
     that changed. So the store sees a write on a state change, not on
     every check (SQLite on a T470).
   - **Folding** (cs-tools' design):
     - the fingerprint is `rule|component|environment`;
     - while that fingerprint's Alert is firing, evaluations update it in
       memory;
     - a firing within 5 minutes of the fingerprint's last resolution
       reopens the same Alert. So a flapping rule makes one record, not
       one per flap.
   - **Errors** (an adapter down, a timeout) don't change an Alert's
     state.
     - They set the rule's `Evaluating=False` condition, with the reason,
       shown in the console.
     - No "resolved" is ever sent because data is missing.
   - **On restart,** open Alerts are read from the store and evaluation
     resumes. A firing is re-sent only if its delivery was still pending.
   - **When the package isn't Ready, or is removed:**
     - rules read `active: false, PackageNotReady`;
     - on removal, open Alerts resolve with `packageRemoved`, and that
       resolution is delivered (spec IN 12).
   - **The interface:** `ObservabilityProvider` gains
     `EvaluateCondition(ctx, LogTarget, Condition, window) (Value,
     error)`. `core/` passes a condition in its own terms, and the
     provider builds the backend query.

3. **Conditions.** Each one is a fixed query that Serendip builds (no
   query language; spec OUT).

   | Type | Data | How it's evaluated |
   |---|---|---|
   | `metric` (`cpu` or `memory`; as `value`, `percentOfRequest` or `percentOfLimit`; `above`; `for`) | the metrics adapter (spec 023's path) | it reads the window's points, and fires when every point in `for` is above |
   | `restarts` (`above` within `window`) | Prometheus: kube-state-metrics' `kube_pod_container_status_restarts_total` (the chart has it, `prometheus-values.yaml`) | `increase(...)` over the window, across the Deployment's pods. T001 checks whether pods can be matched by label; if not, they're named by owner |
   | `logMatch` (`text`, `above` within `window`, `above` ≤ 999) | the logs adapter | a search with `limit = above + 1`; it fires when the page comes back full |
   | `packageDisk` (`percentOfBudget`) | spec 023's `usage` | built-in only |

   - **Restarts read Prometheus directly,** through the same Service
     proxy. That's the one new query path. The PromQL is a fixed template
     filled from validated IDs, never from user text.
   - **Scope always carries all three UIDs,** as spec 023 found it must,
     because an unknown or missing field widens a query silently.

4. **The built-in disk rule** (spec IN 2).
   - **Created** by `serendipd` when the package becomes Ready: one
     `AlertRule` with `builtIn: true`, owned by the `ProviderInstance`,
     firing at 85% of the budget.
   - **Notifies** every platform channel marked `default`.
   - **People can** silence it, change its threshold or disable it, but
     not delete it (409).
   - **It goes** with the package.

5. **The sender** (`core/notify`, standard library only; open question
   2).
   - **Why in `core/`:** webhooks, SMTP, ntfy and Telegram are protocols
     or public HTTPS APIs, not infrastructure products Serendip installs.
     They're Serendip-owned delivery formats, like the webhook body.
     - Constitution 1's rule is about vendors in the domain model.
       `core/notify` names the formats it speaks, as `core/jsonschema`
       names JSON Schema.
     - A Notification capability with a provider registry would add an
       interface and a registry entry for a handful of HTTPS POSTs.
       contract-guardian reviews this reading (T012).
     - The deferred paid types (SMS, voice) are separate accounts and
       products, so they can arrive as providers when they come.
   - **One `http.Client`:**
     - its `Dialer.Control` checks the address actually dialed, which
       defeats DNS rebinding;
     - `CheckRedirect` refuses every redirect;
     - `Proxy` is nil, so `HTTPS_PROXY` can't route around the check;
     - TLS 1.2 or later, verified;
     - a 10 s timeout, and a 64 KiB cap on the response it reads.
   - **SMTP** uses `net/smtp` over the same checked dialer. STARTTLS is
     required (or implicit TLS on 465), and auth happens only after TLS.
   - **The address check** (spec IN 9) refuses:
     - loopback, unspecified, private (RFC 1918, ULA), link-local, CGNAT
       (100.64/10), multicast and broadcast;
     - the cluster's pod and Service CIDRs;
     - the node's own addresses, read from the Node objects and refreshed
       with the cluster.

     It covers IPv4 and IPv6, including IPv4-mapped IPv6. It runs at
     create, at update, at test send and at every delivery.
   - **`allowPrivateDestination`** is for an in-house receiver. It skips
     only the private-range check, for that one channel.
     - It needs the action
       `serendip/notificationChannels/allowPrivateDestination/action`,
       which only Owners hold (decision 9), and a recorded reason.
     - Loopback, link-local and the cluster's own ranges stay refused
       even then: an in-house receiver is reached by its own address, not
       through the cluster.
   - **`allowPlainHTTP`** needs the same action and a reason.
   - **Rate limits:** a 429 with `Retry-After` is honoured, up to 15
     minutes. Each channel has its own send rate (decision 7).

6. **Channel types and their settings.** Every value that grants access
   is a `Secret` reference (spec 009), like builds' `credentialSecret`.

   | Type | Settings | Secret |
   |---|---|---|
   | `webhook` | `format` (`serendip`, `slack`, `discord` or `teams`), `url` | `urlSecretId`: the whole URL, for Slack, Discord and Teams, whose URLs are the token; or `signingSecretId` for the `serendip` format |
   | `email` | `smtpServerId`, `to[]` (at most 20) | the SMTP server's |
   | `ntfy` | `server` (default `https://ntfy.sh`) | `topicSecretId`: on `ntfy.sh` a topic name is effectively its password; optional `tokenSecretId` |
   | `telegram` | `chatId` | `botTokenSecretId`; the host is fixed to `api.telegram.org` |

   - **When the URL is a Secret,** the channel stores its host only, for
     display and for the address check before the value is read.
   - **The bodies:**
     - **`serendip`** is `serendip.alert.v1`: `{version, event
       (firing|resolved|test), alert{id, rule, severity, summary, state,
       project, application, component, environment, startedAt,
       resolvedAt, value, threshold, consoleUrl}}`, documented in
       `operations.md`. Its signature is `X-Serendip-Signature:
       t=<unix>,v1=<hex HMAC-SHA256(secret, t + "." + body)>`, so
       receivers can refuse replays.
     - **`slack`, `discord` and `teams`** get a short text from the same
       fields (an Adaptive Card for Teams), with the console link.
     - **ntfy** gets a title and message. Severity sets the priority
       (critical 5, warning 4, info 3) and a tag; the console link is in
       `Click`.
     - **Telegram** gets `sendMessage` with HTML formatting. Any value
       from a rule or a Component is escaped.
   - **Text from people** (a summary, a Component's name) is escaped for
     each format. A summary can't inject a mention (`@everyone`, `<!channel>`):
     Discord's `allowed_mentions` is empty, and Slack's `<!…>` and `<@…>`
     sequences are escaped.

7. **Delivery** (cs-tools' separate loop).
   - **Pending deliveries** live on the Alert (`deliveries[]`), so a
     restart resumes them.
   - **Retries:** up to 5 attempts, backing off 10 s, 30 s, 1 min, 5 min
     and 15 min. After that the delivery is `failed`, with the last error
     (spec IN 11). A 4xx other than 408 and 429 isn't retried.
   - **Per-channel rate:** at most one send a second (Telegram and Slack
     limits), and 20 a minute for Discord. When more are queued for the
     same channel, firings and resolutions are folded into one digest
     ("3 alerts firing, 1 resolved"), as cs-tools does for chat.
   - **Silences** suppress deliveries, not Alerts (spec AC 8). A
     suppressed delivery is recorded as `silenced`.

8. **Secrets** (spec IN 8).
   - The sender reads values at send time and never caches them beyond
     the send.
   - API responses show `secretSet: true|false`.
   - Errors are cleaned before they're stored:
     - only the host is kept, so a Telegram token in a URL path, or a
       token in a query, can't reach an Alert, an event or a log;
     - Go's `url.Error` is unwrapped, because its message carries the
       whole URL.
   - **The check:** after a full run, the known values are searched for
     in the store, audit rows, events and `serendipd`'s log (T010).

9. **Authorization** (spec IN 13). New actions:
   - `serendip/alertRules/{read,write,delete}` and
     `serendip/alertRules/silence/action`;
   - `serendip/alerts/read` and `serendip/alerts/acknowledge/action`;
   - `serendip/notificationChannels/{read,write,delete}`,
     `…/test/action` and `…/allowPrivateDestination/action`. The last is
     in Contributor's `NotActions`, so only Owner holds it;
   - `serendip/smtpServers/{read,write,delete}`, at `/` only.

   How they apply:
   - **Using channels:** writing a rule needs `notificationChannels/read`
     on each channel it names. So a Contributor in Project A can't route
     alerts to Project B's channel.
   - **Platform `default` channels** can be named by any principal who
     can write a rule. Their settings are shown only to readers at `/`;
     everyone else sees "platform channel".
   - **Reader** gets the new reads through `*/read`. Alerts carry no log
     text: a `logMatch` Alert records the count, not the lines.
   - **Every route** gets a row in the route-table test.
   - **Agents** use the same roles. Acknowledging is a write, not a
     delete, so it needs no Approval.

10. **Deletes** of `AlertRule`, `NotificationChannel` and `SMTPServer` go
    through spec 019's Approvals, with their blast radius (a rule's
    Alerts). `Alert`s have no delete route.

11. **Alert retention** (open question 4).
    - **What:** `serendipd` removes resolved Alerts older than 30 days, at
      most 500 rows a pass.
    - **No Approval:** this is system retention, not a person's delete.
      Each pass writes one audit row, with the count, under the system
      actor.
    - **Why it's recorded:** spec 019's RP-003 left retention of
      operational records unsettled, so RP-071 records this choice.
    - **Growth:** about 2 KiB per Alert, with deliveries capped at 20. A
      flapping rule is one Alert (decision 2), so 200 rules can't grow
      the store faster than their state changes.

12. **The API** (`api/alerts_handler.go`, `api/notifications_handler.go`).
    Every route is in `api/catalog`, so CLI and MCP parity follows.
    - **Rules:**
      - the generic CRUD on `/v1/alertrules`;
      - `POST /v1/alertrules/{id}/silence` (`until`, `reason`), and
        `DELETE …/silence`.
    - **Alerts:**
      - `GET /v1/alerts`, filtered by `componentId`, `applicationId`,
        `environmentId`, `state` or `severity`;
      - `GET /v1/alerts/{id}`;
      - `POST /v1/alerts/{id}/acknowledge`;
      - `GET /v1/alerts/summary`, the firing counts by severity for the
        top bar.

      Every Alert route is scoped to what the caller can read.
    - **Channels:** CRUD on `/v1/notificationchannels`, and
      `POST …/{id}/test`.
    - **SMTP servers:** CRUD on `/v1/smtpservers`.

13. **CLI and MCP.**
    - **CLI commands:**
      - `serendip alerts list|show|ack|summary`;
      - `serendip alertrules
        create|list|show|update|delete|silence|unsilence`;
      - `serendip channels create|list|show|update|delete|test`;
      - `serendip smtp create|list|show|update|delete`.
    - **MCP tools:**
      - read tools for Alerts and rules, summarised and capped at 50
        Alerts;
      - the write tools that follow from the catalogue, under the agent's
        roles.

14. **The console:**
    - **Component › Observability:** an Alerts section with the
      Component's rules and open Alerts, acknowledge and silence, and a
      rule form.
    - **Application:** its rules and firing Alerts.
    - **The top bar:** a bell with firing counts by severity, linking to
      an Alerts page with filters.
    - **Settings › Notifications:**
      - channels by type, with a test send, and the private and plain
        HTTP allowances for Owners;
      - SMTP servers, for Owners.
    - **The Packages page:** the removal wording, extended (spec IN 12).
    - **Every view** shows its "Same request through the API".

## Carried to spec 025 (traces)

The research found these facts, to be checked there:

- **The module:** OpenChoreo's `observability-tracing-openobserve`,
  0.3.x for OpenChoreo 1.2 and later.
  - It installs a Collector Deployment on OTLP 4317 and 4318, and a
    tracing adapter on :9100.
  - It can use the existing OpenObserve, with its bundled store off. It
    needs `openobserve-admin-credentials`, which normally comes from ESO.
  - Tail sampling is on by default, keeping 100 traces and 10 spans a
    second.
- **Queries:**
  - `POST /api/v1alpha1/traces/query` and `…/traces/{id}/spans/query`
    exist;
  - scope is by **names**, not UIDs;
  - there's no duration filter;
  - spans carry the pods' `openchoreo.dev/*-uid` labels as resource
    attributes.
- **To check there:**
  - whether name scoping widens;
  - whether a workload can claim another Component's identity in its
    spans;
  - whether the plane needs `observer.tracingAdapter.enabled`.
- **Trace-based conditions** (error count, p95 and p99 latency) join this
  spec's rule model as two more condition types.

This plan's earlier draft (git history, `02c86b4`) has the full trace
decisions.

## Files touched (outline)

- **`core/resource`:**
  - `alerting.go`: `AlertRule`, `Alert`, `NotificationChannel`,
    `SMTPServer`, and their validation;
  - `ops.go`: the placeholder `AlertSpec` is removed.
- **`core/reconcile`:**
  - `observability.go`: `EvaluateCondition`;
  - `alerts.go`: the evaluator, folding and retention;
  - `delivery.go`;
  - `providerinstance.go`: the built-in rule.
- **`core/notify`:**
  - `dialer.go`: the address check;
  - `client.go`;
  - `webhook.go`: the four formats;
  - `smtp.go`, `ntfy.go`, `telegram.go`;
  - `body.go`, `escape.go`.
- **`core/policy`:** `builtins.go`, for the actions and `NotActions`.
- **`providers/observability/openchoreo`:**
  - `conditions.go`: the fixed queries;
  - a Prometheus query client beside `observer.go`;
  - `VERSIONS.md`.
- **`api`:** `alerts_handler.go`, `notifications_handler.go`, the authz
  route table, `catalog`.
- **Clients:** the commands in `cmd/serendip`, the tools in `mcp/`, and
  wiring the evaluator and sender in `cmd/serendipd`.
- **`ui/src`:**
  - `screens/Observability.tsx` (alerts);
  - `screens/Alerts.tsx`, `screens/Notifications.tsx`;
  - the top-bar bell.
- **`profiles/edge-1/bench`:** a rule-evaluation phase (200 rules).
- **Docs:**
  - `operations.md`: rules, Alerts, channels, the webhook body;
  - `resource-model.md`, `capability-model.md` (its Alerting row),
    `application-platform.md`, `authorization.md`, `approvals.md`;
  - ADR 0022: Serendip evaluates alert rules and sends notifications
    itself; OpenChoreo's alerting path isn't used;
  - `build-vs-adopt.md`;
  - the risk register.

## Risks (to the risk register as they're confirmed)

- **RP-069:** an ntfy topic on `ntfy.sh` is readable by anyone who knows
  it. It's kept as a Secret and never shown, but a person who picks a
  guessable topic leaks their alerts. The console warns, and suggests a
  generated name.
- **RP-070:** Slack, Discord and Teams URLs are bearer tokens to post in
  that channel. They're kept as Secrets; only `serendipd` and the
  Secrets provider see them.
- **RP-071:** resolved Alerts are removed after 30 days by system
  retention, with no Approval.
- **RP-072:** evaluation polls, so:
  - a condition shorter than the interval can be missed;
  - a firing is seen up to one interval late (1 minute at best).
- **RP-073:** a `logMatch` threshold is capped at 999. The adapter's page
  limit is 1000, so "more than N lines" can't count higher.
- **RP-062, revisited:** OpenObserve's guard stays off for the logs
  module's own webhook, but no person-typed destination ever reaches
  OpenObserve now. Its "Revisit" condition is met, and its mitigation
  becomes decision 5.
- **RP-066:** unchanged; no `observabilityalertrules` objects are made.
- **RP-068:** the built-in rule closes its "no alert" residual.
- **Query load:** 200 rules at one-minute intervals against OpenObserve,
  which spec 023 found slows as data grows (RP-055). T011 measures the
  evaluation latency at 3 days of data.

## Explicitly NOT done

Everything in the spec's Scope OUT, and also:
- OpenChoreo's `ObservabilityAlertRule`,
  `ObservabilityAlertsNotificationChannel` and `observability-alert-rule`
  trait, and the Observer's alert store;
- Alertmanager and the Prometheus Operator;
- notification templates set by people: each body is fixed and
  versioned;
- a Notification capability or provider registry entry (decision 5);
- reading replies from Telegram or other chats (no acknowledge-by-chat).
