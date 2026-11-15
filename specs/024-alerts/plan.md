# Plan: Spec 024 — Alert rules and notification channels

Status: Approved, 2026-11-15. T001 (spike) in progress.

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

**v1 ships three types, Telegram first** (the user, 2026-11-15: "if we can
just use Telegram then that's fine"). Each is free, sends outbound only,
is a plain `net/http` POST or a `net/smtp` send, and needs no public URL.

| Type | Free use | Notes |
|---|---|---|
| Telegram bot | yes | `sendMessage` with HTML formatting and an inline keyboard. A **URL button** only opens a link, so it needs no callback. About one message a second per chat, 20 a minute per group, 30 a second overall; a 429 carries `retry_after`. The bot token is the credential, and it sits in the URL path |
| Email (SMTP) | yes | Through a relay. **Gmail with an app password** is about 500 a day and needs no domain. **Brevo** allows 300 a day with no card. **Resend** allows 100 a day and 3,000 a month, but needs a domain. SES is cheap (about $0.10 per 1,000) but needs an AWS account and approval to leave its sandbox, so it isn't needed. A mail server on the laptop is ruled out: home IP addresses are on blocklists, port 25 is often blocked, and there's no reverse DNS |
| Webhook (Serendip's signed JSON) | yes | Also the integration point: n8n or Node-RED turns an alert into anything else |

**Through the webhook (n8n or Node-RED) until they get their own formats:**
- **Slack, Discord, Teams and Google Chat:** each accepts cards on a plain
  incoming webhook (Block Kit, embeds, Adaptive Cards, cards).
  - Teams' old connector URLs stopped working in May 2026. Its
    replacement is a Workflows flow.
  - Buttons that act need a public URL on Slack and Teams. Discord
    webhooks can't have buttons at all.
- **ntfy:** a free push app, which can break through a phone's silent
  mode.
- **Phone calls:** Twilio Voice from an n8n flow, at about 3¢ a minute in
  the UK. No API places calls for free.
  - **WhatsApp:** Meta's Business Calling API needs the user's opt-in, a
    live audio session and per-minute pricing (Research: unverified), so
    it isn't a "ring and read the alert" API.
  - **n8n's WhatsApp node** sends messages, and those are paid templates
    outside WhatsApp's 24-hour window.
- **SMS:**
  - Twilio, at about $0.056 a segment in the UK;
  - an Android phone acting as a gateway (sms-gate), costing only the SIM;
  - Sri Lankan gateways, at about LKR 0.6–1.0 a message.

**Rejected:**
- **whatsmeow and other unofficial WhatsApp clients:** they break
  WhatsApp's terms, and accounts get banned;
- **email-to-SMS gateways:** gone in the UK;
- **Textbelt's free tier:** one message a day.

**OTP, for a later identity spec (not 024):** see "ThunderID OTP" below.
The free options deliver nothing: TOTP authenticator apps, and passkeys.
The candidate for codes by message is ThunderID's "Custom" SMS sender
pointed at `serendipd`, which relays the code through this spec's
Telegram bot. Telegram Gateway is the paid fallback, at $0.01 a code.

## Spec amendments (agreed with this plan)

- **Already folded into spec.md:**
  - traces moved to spec 025;
  - AC 7 reads "status and measured cost", not "backend repaired";
  - the channel types are set: Telegram, email and the webhook;
  - a minimum severity per channel, and the heartbeat (spec IN 12), were
    added at review.
- **Open questions 1–6** are answered by the decisions below.

## Spike findings (T001, 2026-11-15, `serendip-t018`)

Queried through `kubectl proxy` to the plane's Service proxies, the path
`serendipd` uses. Recorded answers are in
`providers/observability/openchoreo/testdata/condition-*.json`; a scratch
test parsed each into the provider's types (metrics: 6 points, logs: 4
lines with `total` 11, restarts: one sample and an empty vector).

1. **Metric condition: as planned.** `POST /api/v1/metrics/query`,
   `metric=resource`, `step=1m`, a 5-minute window, scope with
   `namespace` and all three UIDs, for `cmp-00620e1f19` in
   `env-312daaeb07` (3 replicas): 200, six points per series (both ends
   included), values summed across the Deployment's pods (CPU limit
   0.3 = 3 × 0.1). Points are aligned to the query's end, not a fixed
   grid. **Amends decision 3:** a `for` of N minutes reads N + 1 points,
   and fires when all of them are above; the evaluator asks for
   `[now − for, now]`.
2. **Log condition: the answer carries an exact `total`.** A search for
   `spike-crash` with `limit: 4` gave 4 lines and `"total": 11`; with
   `limit: 100`, 11 lines and `total` 11. **Amends decision 3:** query
   with `limit: 1` and compare `total` with `above`, so `Alert.value` is
   the real count, not "at least above + 1". The line itself is never put
   in a notification (RP-067). A scope missing `environmentUid` was
   accepted, not refused, which is why the scope always carries all
   three (spec 023's finding, unchanged).
3. **Restarts.**
   - **The counter wasn't collected.** Our kube-state-metrics
     `metricAllowlist` (`prometheus-values.yaml`) has no
     `kube_pod_container_status_restarts_total`; the plan's "the chart
     has it" was wrong. Added for the spike by patching the Deployment's
     args (restored afterwards). **T006 adds it to the allowlist**: one
     series per container, next to ~9,600 head series now.
   - **Pods match by label.** kube-state-metrics already exports the
     pods' `openchoreo.dev/*-uid` labels on `kube_pod_labels`
     (`metricLabelsAllowlist`), so no ReplicaSet lookup is needed:

     ```
     sum((max_over_time(kube_pod_container_status_restarts_total[W])
        - min_over_time(kube_pod_container_status_restarts_total[W]))
       * on (namespace, pod) group_left()
         max by (namespace, pod) (kube_pod_labels{
           label_openchoreo_dev_component_uid="C",
           label_openchoreo_dev_environment_uid="E",
           label_openchoreo_dev_project_uid="P"}))
     ```

   - **`increase()` overstates:** a 2-pod crash loop at 4 restarts each
     (6 inside the window) read 7.08, then 9.82. `max − min` read 6,
     an integer. It misses a new pod's restarts before its first scrape
     (here, 1 per pod); accepted, since a crash loop keeps counting.
     An empty vector (no pods, or none restarting yet) reads 0.
   - **Lag:** scrape interval is 1 minute. kubectl showed the third
     restart at 03:25:01; Prometheus at 03:25:42 (~40 s). Worst case is
     about one scrape plus one evaluation, ~2 minutes from the restart
     to the firing.
4. **Latency at the current store size** (Prometheus 44 MiB, 9,636 head
   series; logs store 3-day retention, not sized here):
   - metrics adapter: 10–22 ms (3 runs);
   - logs adapter: 101–464 ms (the first cold), 230 ms over 24 hours;
   - Prometheus restart query: 9–19 ms.

   Well inside decision 2's 20 s timeout; T011 measures 200 rules.
5. **Telegram** (a scratch bot, a private chat; the probe, kept outside
   the repo, wrote `core/notify/testdata/telegram-*.json` and
   `probe-report-telegram.txt`, with the token, chat ID, names and bot ID
   replaced):
   - **The card is accepted:** `sendMessage`, `parse_mode: HTML`, one
     URL button, link previews off: 200 in ~350 ms
     (`telegram-request-card.json` is the body T004 matches). A summary
     with `<b>`, `&` and `</a>` escaped to entities shows as text.
   - **Button URLs:** only the host name `localhost` is refused (400,
     "inline keyboard button URL … is invalid: Wrong HTTP URL").
     `http://127.0.0.1:8088`, `http://serendip.lan:8088`,
     `http://192.168.1.20:8088` and `https://console.localhost` are all
     accepted: plain `http` is fine. **Amends decision 6:** the card gets
     its button unless the console URL's host is `localhost`; then the
     URL goes in the text and there's no button. A LAN URL works only
     while the phone is on that LAN; Settings says so.
   - **Threading:** `reply_parameters: {message_id,
     allow_sending_without_reply: true}` gave 200, and the answer's
     `reply_to_message` is the firing.
   - **Errors:** a truncated JSON body reads as an empty request (400,
     "message text is empty"), not a parse error; unclosed HTML gives
     400 "can't parse entities"; 4,097 characters give 400 "message is
     too long". All 400s, never retried (decision 7). The card stays
     well under 4,096 characters; the summary is capped.
   - **Rate:** 40 messages sent one after another (~3 a second), then 30
     sent at once, all got 200: no 429 for a private chat at this
     volume. Not pushed further. The 429 body wasn't recorded, so T004's
     test uses the Bot API documentation's shape: `error_code: 429` with
     `parameters.retry_after` in seconds. Decision 7's per-channel rate
     and digests keep Serendip far below this anyway.
   - **Finding the chat:** one `getUpdates` call returned the private
     chat's ID and type after the person sent the bot a message, and
     nothing before (0 updates, no webhook set). The console's "Find
     chat" step tells the person to message the bot first.
6. **Email** (`probe-report-gmail.txt`, `probe-report-brevo.txt`):
   - **Gmail** (`smtp.gmail.com`, an app password): both ports sent one
     message, in 3.3–4.1 s.
     - **587:** no AUTH offered before STARTTLS (the safe behaviour);
       after TLS 1.3, `LOGIN PLAIN XOAUTH2 …`. **465:** implicit TLS 1.3,
       same AUTH list.
     - `SIZE` 34 MiB. Google's documented limit for a personal account
       is about 500 recipients a day; no limit was shown in the session.
     - Gmail signs its own mail, so DMARC passes: the recommended
       zero-cost default.
     - PLAIN over TLS is all Serendip needs; XOAUTH2 isn't used (an app
       password is simpler and needs no OAuth client).
   - **Brevo** (`smtp-relay.brevo.com`, free plan, a verified
     `@gmail.com` sender): both ports sent one message, in 1.6–2.0 s.
     - **587:** STARTTLS, then TLS 1.3. **It offers `AUTH PLAIN LOGIN
       CRAM-MD5` before STARTTLS too**, so a client that authenticates
       when AUTH is offered would send the key in clear text. Decision
       5's rule (STARTTLS required, auth only after TLS) is needed, not
       defensive; T004 tests it with a fake server that offers AUTH
       before TLS.
     - **465:** implicit TLS 1.3; it also lists STARTTLS inside TLS,
       which the client ignores.
     - `SIZE` 20 MiB; the card email is a few KiB.
     - **Free-mail senders don't pass DMARC through a relay.** Brevo
       flags a `@gmail.com` sender as "not compliant" (no DKIM for
       gmail.com, DMARC unaligned), so receivers may file it as spam or
       refuse it. Seen: to a Gmail inbox, `brevo` (587) arrived with
       the From name shown as `serendipinfra` (not rewritten), but
       `brevo-implicit` (465), the same message through the same relay a
       moment later, went to Spam (both `gmail` messages reached the
       inbox). The free-mail sender's unaligned DMARC makes filing a coin
       toss, which is what the warning predicted. And a relay's 250
       doesn't mean delivered: the channel's `lastResult` says "accepted by the
       relay", never "delivered". **Amends decision 6:** Settings recommends a Gmail app
       password (Gmail signs its own mail) as the zero-cost default, and
       a relay such as Brevo only with a domain the user owns and has
       authenticated (DKIM, DMARC). Brevo's "block unauthorized IPs" is
       documented as hardening for a fixed public IP (T013).

Clean-up: the scratch namespace `t024-spike` (a 2-replica crash loop
with fake UIDs) is deleted, and kube-state-metrics' args are back to the
release's.

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
     - `minSeverity` (`info`, `warning` or `critical`; default `info`);
     - `default` (bool);
     - `allowPrivateDestination` and `allowPlainHTTP`, each with a
       `reason`.
   - **`SMTPServer`:**
     - platform-only;
     - fields: `host`, `port`, `tls` (`starttls` or `implicit`, never
       plain), `from`, `usernameSecretId` and `passwordSecretId`;
     - it's a separate kind so Projects can create email channels
       without holding SMTP credentials.
   - **`Heartbeat`:**
     - platform-only, and at most one (the store's unique key);
     - fields: `urlSecretId`, `host`, `interval` (1–10 minutes, default
       1), and `allowPrivateDestination` with a reason, for a monitor
       elsewhere on the LAN;
     - status: `lastSent` and `lastResult`, with conditions (decision
       15).
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
       resolution is delivered (spec IN 13).
   - **The interface:** `ObservabilityProvider` gains
     `EvaluateCondition(ctx, LogTarget, Condition, window) (Value,
     error)`. `core/` passes a condition in its own terms, and the
     provider builds the backend query.

3. **Conditions.** Each one is a fixed query that Serendip builds (no
   query language; spec OUT).

   | Type | Data | How it's evaluated |
   |---|---|---|
   | `metric` (`cpu` or `memory`; as `value`, `percentOfRequest` or `percentOfLimit`; `above`; `for`) | the metrics adapter (spec 023's path) | it reads the window's points, and fires when every point in `for` is above |
   | `restarts` (`above` within `window`) | Prometheus: kube-state-metrics' `kube_pod_container_status_restarts_total`, **added to `prometheus-values.yaml`'s `metricAllowlist`** (T001: it wasn't there) | `max_over_time − min_over_time` per container over the window, joined to `kube_pod_labels` on the three UIDs and summed (T001: not `increase`, which extrapolates) |
   | `logMatch` (`text`, `above` within `window`, `above` ≤ 999) | the logs adapter | a search with `limit = 1`, reading the answer's `total` (T001); if `total` is ever absent, `limit = above + 1` and a full page |
   | `packageDisk` (`percentOfBudget`) | spec 023's `usage` | built-in only |

   - **Restarts read Prometheus directly,** through the same Service
     proxy. That's the one new query path. The PromQL is a fixed template
     filled from validated IDs, never from user text.
   - **Scope always carries all three UIDs,** as spec 023 found it must,
     because an unknown or missing field widens a query silently.
   - **Amended in T006:**
     - a metric `value` threshold is in millicores or MiB, the units
       Serendip's metrics are read in (T002's message said cores or
       bytes);
     - a percentage pairs each point with the request or limit in force
       then (its latest point at or before it);
     - an empty restarts answer is 0 only when the pods' restart series
       exist (a second `count` query), else no data;
     - a condition that can't be evaluated as written (a percentage of
       a request that isn't set) reads `ConditionRefused`, with the
       reason;
     - a disk reading over 5 minutes old is no data.

4. **The built-in disk rule** (spec IN 2).
   - **Created** by `serendipd` when the package becomes Ready: one
     `AlertRule` with `builtIn: true`, owned by the `ProviderInstance`,
     firing at 85% of the budget.
   - **Notifies** every platform channel marked `default`.
   - **People can** silence it, change its threshold or disable it, but
     not delete it (409).
   - **It goes** with the package.
   - **As built (T006):**
     - a package declares its rules in the catalogue
       (`reconcile.Package.AlertRules`), so `core` names no product;
     - the reconciler creates a missing one after Ready is stored,
       matched by condition type, and never rewrites it;
     - the store refuses a delete aimed at it (`resource.OwnerKept`,
       `ErrInUse`, 409), and the API refuses setting or clearing
       `builtIn`;
     - it goes by cascade: **removing the package now needs
       `cascade=true`**, and the console sends it, so the Approval's
       blast radius lists the rule and its Alerts (RP-077).

5. **The sender** (`core/notify`, standard library only; open question
   2).
   - **Why in `core/`:** webhooks, SMTP and Telegram's Bot API are
     protocols or public HTTPS APIs, not infrastructure products Serendip
     installs.
     They're Serendip-owned delivery formats, like the webhook body.
     - Constitution 1's rule is about vendors in the domain model.
       `core/notify` names the formats it speaks, as `core/jsonschema`
       names JSON Schema.
     - A Notification capability with a provider registry would add an
       interface and a registry entry for a handful of HTTPS POSTs.
       contract-guardian reviews this reading (T012).
     - The deferred paid types (SMS, voice) are separate accounts and
       products, so they can arrive as providers when they come.
     - contract-guardian (T004): this reading holds only while there's
       no Notification capability. If a second chat protocol ever
       arrives as a pluggable provider, Telegram becomes a provider
       detail then. ADR 0022 records the deviation (T013).
   - **One `http.Client`:**
     - its `Dialer.Control` checks the address actually dialed, which
       defeats DNS rebinding;
     - `CheckRedirect` refuses every redirect;
     - `Proxy` is nil, so `HTTPS_PROXY` can't route around the check;
     - TLS 1.2 or later, verified;
     - a 10 s timeout, and a 64 KiB cap on the response it reads.
   - **SMTP** uses `net/smtp` over the same checked dialer. STARTTLS is
     required (or implicit TLS on 465), and auth happens only after TLS.
     An SMTP server has no private-destination allowance: it's a relay on
     the internet, and a LAN mail server is out of scope (T004).
   - **No connection pooling** (T004, go-reviewer): a pooled connection
     skips the dial-time check, and one opened under a channel's private
     allowance could carry another channel's send.
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
   - **Rate limits:** a 429 with `Retry-After` (or Telegram's
     `retry_after`) is honoured, up to 15 minutes. Each channel has its
     own send rate (decision 7).

6. **Channel types and their settings.** Every value that grants access
   is a `Secret` reference (spec 009), like builds' `credentialSecret`.

   | Type | Settings | Secret |
   |---|---|---|
   | `telegram` | `chatId` (a user, group or channel). The host is fixed to `api.telegram.org`, so no person-typed address is involved | `botTokenSecretId` |
   | `email` | `smtpServerId`, `to[]` (at most 20) | the SMTP server's |
   | `webhook` | `url` | `signingSecretId`; `urlSecretId` instead of `url` when the URL itself holds a token (an n8n webhook's path, for example) |

   - **When the URL is a Secret,** the channel stores only its host. The
     host is shown, and checked against the address rules before the value
     is read.
   - **The Telegram card** (`sendMessage`, `parse_mode=HTML`):
     - a first line with a severity marker and the state, for example
       "🔴 CRITICAL · firing" or "✅ resolved";
     - the summary in bold;
     - Component · Environment · Project;
     - the value against the threshold, and when it started (or how long
       it lasted);
     - an inline keyboard with one URL button, "View in console", opening
       the Alert.

     If Telegram refuses the console's URL (T001 checks `localhost` and
     plain `http`), the link goes in the text instead.
     - Every value from a rule or a Component is HTML-escaped.
     - A firing and its resolution are sent as replies in one thread
       (`reply_parameters` to the firing's `message_id`, kept on the
       delivery). A chat reads as one line per alert.
   - **The email:**
     - a subject of "[CRITICAL] firing: <summary>";
     - a plain-text body and a small HTML part, with the same fields and
       the console link;
     - `Message-ID` and `In-Reply-To`, so the resolution threads under the
       firing in mail clients.
   - **The webhook body** is `serendip.alert.v1`, documented in
     `operations.md`:
     - fields: `{version, event (firing|resolved|test), alert{id, rule,
       severity, summary, state, project, application, component,
       environment, startedAt, resolvedAt, value, threshold,
       consoleUrl}}`;
     - its signature: `X-Serendip-Signature: t=<unix>,v1=<hex
       HMAC-SHA256(secret, t + "." + body)>`, so receivers can refuse
       replays.
   - **The n8n recipe** (`docs/guides/alert-integrations.md`):
     - a Webhook node that checks the signature in a Code node;
     - three example branches: a Slack card, a Twilio Voice call for
       `critical`, and an ntfy push;
     - which of these cost money.

     It's checked once, live, in T010.
   - **Severity routing:** an alert goes to the rule's channels (or the
     Project's defaults) whose `minSeverity` it meets. Silences and
     routing are both recorded on the delivery (`silenced`,
     `belowMinSeverity`), so "why wasn't I told" has an answer.

7. **Delivery** (cs-tools' separate loop).
   - **Pending deliveries** live on the Alert (`deliveries[]`), so a
     restart resumes them.
   - **Retries:** up to 5 attempts, waiting 10 s, 30 s, 1 min and 5 min
     between them (about 7 minutes in all; T010 dropped an unused 15 min
     step). After that the delivery is `failed`, with the last error
     (spec IN 11). A 4xx other than 408 and 429 isn't retried.
   - **Per-channel rate:** at most one send a second, and 20 a minute to
     a Telegram group. Telegram's `retry_after` is honoured, up to 15
     minutes. When more are queued for the
     same channel, firings and resolutions are folded into one digest
     ("3 alerts firing, 1 resolved"), as cs-tools does for chat.
   - **Silences** suppress deliveries, not Alerts (spec AC 8). A
     suppressed delivery is recorded as `silenced`.

8. **Secrets** (spec IN 8).
   - The sender reads values at send time and never caches them beyond
     the send.
   - API responses show `secretSet: true|false`.
   - Errors are cleaned before they're stored:
     - only the host is kept, so the Telegram bot token in the URL path,
       or a token in a query, can't reach an Alert, an event or a log;
     - Go's `url.Error` is unwrapped, because its message carries the
       whole URL.
   - **The check:** after a full run, the known values are searched for
     in the store, audit rows, events and `serendipd`'s log (T010).

9. **Authorization** (spec IN 14). New actions:
   - `serendip/alertRules/{read,write,delete}` and
     `serendip/alertRules/silence/action`;
   - `serendip/alerts/read` and `serendip/alerts/acknowledge/action`;
   - `serendip/notificationChannels/{read,write,delete}`,
     `…/test/action` and `…/allowPrivateDestination/action`. The last is
     in Contributor's `NotActions`, so only Owner holds it;
   - `serendip/smtpServers/{read,write,delete}` and
     `serendip/heartbeat/{read,write,delete}`, at `/` only.

   How they apply:
   - **Using channels:** writing a rule needs `notificationChannels/read`
     on each channel it names. So a Contributor in Project A can't route
     alerts to Project B's channel.
   - **A channel's Secrets** (bot token, URL, signing key) need
     `serendip/secrets/value/read` where they're newly named, as a
     build's credential does (amended in T003). An email channel's SMTP
     server needs nothing: SMTP servers are platform relays. Changing
     where a channel with an allowance sends needs the allowance action.
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
    - **An Approval by policy** (amended in T005, contract-guardian:
      constitution 5 has no exception): this is system retention, not a
      person's delete, so nobody waits, but each pass records one
      Executed Approval per rule naming the removed Alerts, and each
      removed Alert an audit row naming it, under the system actor.
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
        `POST /v1/alertrules/{id}/unsilence` (amended in T003: a DELETE
        route is a delete, with its Approval, and unsilencing deletes
        nothing).
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
    - **Heartbeat:** decision 15.
    - **Amended in T007:**
      - **Status is the server's** on rules, channels and SMTP servers: a
        body's `status` is ignored, so a rule's silence changes only
        through `silence/action` (before this, the generic PUT stored it).
      - `GET /v1/alerts` takes `limit` (default 100, at most 500), newest
        first. The summary adds `total` and `unacknowledged`.
      - A silence needs a `reason`, and its end must be in the future and
        at most 30 days away (`resource.MaxSilence`).
      - **A destination is checked when it's saved** by the sender's
        address rules (webhook URLs and hosts, SMTP hosts, the
        heartbeat's host): a refused address is a 400 with the reason. A
        name that doesn't resolve yet is left to the send (RP-078).
      - **A test send** answers 200 when sent, 422 when the address check
        refused the destination, and 502 when the receiver failed, with
        the host-only error. It's recorded on the channel's
        `lastTest`/`lastTestResult`, whose write is its audit row. With
        no cluster there's no sender, so it answers 503.

13. **CLI and MCP.**
    - **CLI commands:**
      - `serendip alerts list|show|ack|summary`;
      - `serendip alertrules
        create|list|show|update|delete|silence|unsilence`;
      - `serendip channels create|list|show|update|delete|test`;
      - `serendip smtp create|list|show|update|delete`;
      - `serendip heartbeat set|show|test|delete`.
    - **MCP tools:**
      - read tools for Alerts and rules, summarised and capped at 50
        Alerts;
      - the write tools that follow from the catalogue, under the agent's
        roles.
    - **Amended in T007:**
      - `get` instead of `show`, as every other CLI command says;
      - the catalogue's MCP tools are the Alert reads, `alerts_acknowledge`,
        the rule CRUD and silence and unsilence. Channel, SMTP server and
        heartbeat writes, and the channel test, are left off MCP with a
        reason: people set them up, since they name Secrets and where
        alert content leaves the laptop (RP-070). AI agents read channels
        through `resources_list`.

14. **The console:**
    - **Component › Observability:** an Alerts section with the
      Component's rules and open Alerts, acknowledge and silence, and a
      rule form.
    - **Application:** its rules and firing Alerts.
    - **The top bar:** a bell with firing counts by severity, linking to
      an Alerts page with filters.
    - **Settings › Notifications:**
      - channels by type, with their minimum severity, a test send, and the
        private and plain-HTTP allowances for Owners. The Telegram form
        explains finding a `chatId` (send the bot a message, then press
        "Find chat", which reads `getUpdates` once);
      - SMTP servers, for Owners, with notes on Gmail app passwords and
        Brevo;
      - the heartbeat, for Owners, with its last result.
    - **The Packages page:** the removal wording, extended (spec IN 13).
    - **Amended in T009:** "Find chat" is `POST
      /v1/secrets/{id}/telegramchats`, decided by the Secret's
      `serendip/secrets/value/read` (the server reads the token for the
      caller) and answering chats only; CLI `channels find-chat`, off MCP.
      Acknowledge and silence are checked at the rule's scope, which an
      Alert inherits.
    - **Every view** shows its "Same request through the API".

15. **The heartbeat** (spec IN 12).
    - **A loop in `serendipd`** sends `GET` (or `POST`, with an empty
      body) to the heartbeat URL every `interval`, through the same
      checked client as channels.
    - **It sends only while Serendip can alert.** The evaluator must have
      completed a tick within the last 2 minutes, the delivery loop must
      have run, and the store must answer a read. If any of these fails,
      it sends nothing, and the outside monitor's own timeout raises the
      alarm.
      - It never sends a "fail" signal. Sending one depends on the same
        machine still working, so absence is the signal.
      - Some monitors take an explicit failure (healthchecks.io's
        `/fail`). That's left to the monitor's own grace period.
    - **Status:**
      - `lastSent`, `lastResult` and a `Sending` condition, shown on
        Settings › Notifications;
      - the reason when sending is paused because Serendip can't alert.
    - **Compatible monitors:** healthchecks.io's free tier (a ping URL),
      Uptime Kuma's push monitors, and an n8n Webhook node on another
      machine, or Gatus (suggested tools above). `docs/guides/alert-integrations.md` covers setting each up, and
      says plainly that the monitor must not run on the same laptop.
    - **The API, CLI and MCP:** `PUT /v1/heartbeat` (the singleton), `GET`,
      `DELETE` (through an Approval), and `POST /v1/heartbeat/test`;
      `serendip heartbeat set|get|test|delete`.
    - **Amended in T007:** the loop is `core/reconcile/heartbeat.go`,
      started with the evaluator. It checks every 15 s whether a beat is
      due. It writes status only when the result changes, or every
      10 minutes while steady, since each write is an audit row (RP-078).
      A test send skips the health gate and always writes.

## Suggested outside tools (not dependencies)

Serendip only sends a signed webhook and a heartbeat request, so any of
these works. The guide (`docs/guides/alert-integrations.md`) suggests
them with their free limits, as checked on 2026-11-15. None of them is
installed, bundled or required.

### By profile (the user, 2026-11-15)

The watcher must live outside the thing it watches: if the cluster is
down, its own alerting is down too.

- **`edge-1` (one person, one laptop): free SaaS, nothing to run.**
  - Alerts go to Telegram.
  - The heartbeat goes to **healthchecks.io's free tier**, which alerts on
    Telegram and email when the pings stop.
  - Email goes through a Gmail app password or Brevo.
- **`small-3` and `standard-5` (a small team): a self-hosted watcher on
  an always-free cloud VM.**
  - It's **Gatus**, a single Go binary with a YAML config:
    - it receives the cluster's heartbeat (`external-endpoints`);
    - it probes the console's URL from outside;
    - it alerts on Telegram, by email, or through Twilio for calls.
  - It's reached over HTTPS, or over a Tailscale or WireGuard link when
    the cluster has no public address. Gatus needs no inbound path to the
    cluster, because the cluster pings it.
  - **Watch the watcher:** Gatus itself pings healthchecks.io's free tier,
    so a reclaimed or crashed VM is noticed too.
  - Apprise API or Node-RED can sit beside it, for SMS or calls.
- **Always-free VMs, for the watcher** (2026-11-15; check before relying
  on them):
  - **Google Cloud e2-micro** (2 shared vCPUs, 1 GB RAM, 30 GB disk, in
    `us-west1`, `us-central1` or `us-east1`): **the suggested home for
    Gatus.**
    - Gatus needs tens of MB, and pings coming in are free.
    - The 1 GB a month of outbound traffic is far more than alert
      messages use.
    - It's not reclaimed for being idle.
  - **Oracle Cloud Always Free** (up to 4 Ampere cores and 24 GB in A1,
    or two 1 GB x86 micros; 200 GB of storage; 10 TB of traffic):
    - The most capacity, but **Oracle reclaims an Always Free instance
      that stays idle for 7 days** (CPU, network and, on A1, memory under
      20%). A watcher is idle by nature.
    - Converting the account to Pay As You Go keeps the free resources
      and avoids the reclaim.
    - A1 capacity is often short in popular regions.
    - It's the better choice if the VM also runs Node-RED or Uptime
      Kuma.
  - **Not always-free:**
    - AWS and Azure: 12 months, or credits for new AWS accounts since
      2025;
    - Render and Koyeb: free instances sleep, so a sleeping watcher
      misses the silence it's meant to catch.
  - **A serverless alternative, with no VM:** a Cloudflare Worker on the
    free plan (Research: unverified limits):
    - the heartbeat stores a timestamp in KV;
    - a once-a-minute Cron Trigger sends a Telegram message when the
      timestamp goes stale.

    It's about 40 lines of JavaScript. The guide could carry it, but
    Serendip wouldn't ship or maintain it.

- **Heartbeat monitors** (decision 15). The monitor must not run on the
  same laptop.
  - **healthchecks.io**, the suggested default:
    - its free tier has 20 checks, 5 SMS or WhatsApp a month, and
      Telegram and email alerts;
    - it supports any period with a grace time;
    - it's BSD-3, so it can be self-hosted later.
  - **Gatus,** for a second device (a Pi, or an Oracle Always Free VM):
    - a single Go binary with a YAML config;
    - its `external-endpoints` heartbeat alerts by Telegram, Twilio or
      ntfy.
  - **Uptime Kuma:** a friendlier UI and push monitors, but heavier
    (Node.js).
  - **Not suggested:**
    - UptimeRobot's free tier (heartbeats and Telegram are paid);
    - Better Stack's free tier (no Telegram; phone alerts are about $30 a
      month);
    - Cronitor (5 checks);
    - Site24x7 (its free plan is email-only, and heartbeats seem to be
      paid; unverified).
- **Turning a webhook into other channels** (WhatsApp, SMS, calls,
  Slack):
  - **Apprise API:** a small relay to 150+ services, and the lightest. It
    has no signature check, so it stays on a trusted network, or
    something checks the signature in front of it.
  - **Node-RED** (Apache-2.0, runs on a Pi): for flow logic.
  - **n8n Community Edition:**
    - it's fair-code under the Sustainable Use License, not open source;
    - its vendors suggest 2–4 GB, which is heavy beside edge-1;
    - n8n Cloud has a trial only.
  - **Hosted, free:** Activepieces Cloud (1,000 runs a month, accepts
    webhooks, MIT if self-hosted later), or Make (1,000 credits, two
    scenarios). Zapier and Pipedream's free tiers can't take production
    webhooks.

## ThunderID OTP (for a later identity spec)

From oss-researcher, 2026-11-15. It read ThunderID **v1.1.0**, whose repo
is now `thunder-id/thunderid`. ThunderID has released since ADR 0006's
watch note: v1.0.2 (Oct 27), v1.1.0 (Nov 2), v2.0.0-m1 (Nov 6). That
also answers PROGRESS.md's "re-check by 2026-11-15" item.

- **One-time codes: yes, as a second factor.** The `OTPExecutor` makes
  and checks a code, and a separate `SMSExecutor` or `EmailExecutor`
  sends it.
  - They're chained in the flow builder (`/flows`), for example after
    username and password, or in password recovery.
  - Passwordless use isn't documented.
  - Code length is 4–10 digits, valid for 30–600 s, with `maxAttempts`
    3 per generation.
  - No verify rate limit or resend cool-down was found.
- **TOTP (authenticator apps): no,** at v1.1.0. **Passkeys:** a
  `PasskeyAuthExecutor` exists, but isn't documented as a flow step.
- **Email OTP** uses `email.smtp` in `deployment.yaml`: STARTTLS only
  (port 587, not implicit TLS on 465), PLAIN auth, TLS 1.2 or later. So a
  Gmail app password or Brevo works as-is, and it can be the same relay
  as Serendip's `SMTPServer`.
- **SMS senders:** Twilio, Vonage, or a **Custom** sender, created with
  `POST /connections/sms-gateway`.
  - **What the Custom sender sends:** it POSTs (or GETs) the **rendered
    template text** to a URL, as JSON or as form lines. There are no
    fixed fields and no signature, just any static headers you
    configure.
  - **Its limits:**
    - those header values aren't stored as secrets;
    - there's a 10 s timeout, and no retry on a non-2xx answer;
    - the recipient isn't sent unless the template includes it
      (`{{ctx(mobile_number)}}`);
    - the destination must pass a phone-number pattern.
- **Codes over Telegram is possible, but it's a workaround:**
  - **How:** the SMS template carries the person's phone number and the
    code. The Custom sender posts them to a small relay route on
    `serendipd`. `serendipd` maps the phone number to that person's
    linked Telegram chat, and sends the code through this spec's bot.
  - **Costs:**
    - it's a new inbound route into `serendipd`, kept cluster-internal
      and guarded by a static header token that ThunderID stores in
      plain text;
    - every person needs a phone number on their ThunderID account;
    - and it depends on Telegram account linking (the follow-up spec).
- **The recommendation for that identity spec:**
  1. email OTP through ThunderID's SMTP first (free, no Serendip code);
  2. passkeys, once ThunderID documents them as a flow step;
  3. TOTP, if a ThunderID release adds it (watch v2.0);
  4. the Telegram relay, only if a person can't use email.

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
  - `webhook.go`, `smtp.go`, `telegram.go`;
  - `heartbeat.go`;
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
  - `docs/guides/alert-integrations.md`: the n8n recipe and heartbeat monitors;
  - `resource-model.md`, `capability-model.md` (its Alerting row),
    `application-platform.md`, `authorization.md`, `approvals.md`;
  - ADR 0022: Serendip evaluates alert rules and sends notifications
    itself; OpenChoreo's alerting path isn't used;
  - `build-vs-adopt.md`;
  - the risk register.

## Risks (to the risk register as they're confirmed)

- **RP-069:** the Telegram bot token is the whole credential. Anyone
  holding it can read the bot's chats and send as it. It's kept as a
  Secret, cleaned from errors, and seen only by `serendipd` and the
  Secrets provider. BotFather can revoke it.
- **RP-070:** alert content leaves the laptop for third parties:
  Telegram's servers, and the mail relay. That's the summary, the names,
  the values and a console link, but never log lines (decision 9). The
  console says so on the channel form.
- **RP-071:** resolved Alerts are removed after 30 days by system
  retention, through policy-decided Approvals.
- **RP-072:** evaluation polls, so:
  - a condition shorter than the interval can be missed;
  - a firing is seen up to one interval late (1 minute at best).
- **RP-074:** the heartbeat proves only that Serendip can alert, not that
  a message reached a person. A broken Telegram token is caught by
  delivery failures on the Alert, not by the heartbeat.
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
- **composite conditions** (agreed for later, 2026-11-15): a rule that
  fires on a combination of others, such as "CPU high *and* error logs",
  like AWS CloudWatch's composite alarms (Azure Monitor has no direct
  equivalent; its alert processing rules only route and suppress). A
  later spec adds a `composite` condition over existing rules' states;
  T005's fingerprints and evaluator are the base it builds on;
- OpenChoreo's `ObservabilityAlertRule`,
  `ObservabilityAlertsNotificationChannel` and `observability-alert-rule`
  trait, and the Observer's alert store;
- Alertmanager and the Prometheus Operator;
- notification templates set by people: each body is fixed and
  versioned;
- a Notification capability or provider registry entry (decision 5);
- reading replies or button taps from Telegram (a follow-up spec, with
  account linking);
- Slack, Discord, Teams, Google Chat and ntfy formats (reached through
  the webhook for now);
- hosting the heartbeat's monitor.
