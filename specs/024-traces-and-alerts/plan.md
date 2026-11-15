# Plan: Spec 024 — Traces, alert rules and notification channels

Status: Draft, 2026-11-15. Awaiting approval.

Upstream facts are from oss-researcher (2026-11-15), read from OpenChoreo's
and community-modules' `main` branches. T001 checks each one against what
is pinned (plane chart 1.3.1, logs-openobserve 0.8.0) before code depends
on it. "Research:" marks a fact that is still unverified here.

## The decision that shapes everything: Serendip evaluates the rules

The spec's leaning (open question 1) was that the backend evaluates rules
and Serendip only sends the notifications. The research rules that out for
this install:

- **OpenChoreo's alerting path** is `ObservabilityAlertRule` → the Observer
  → the logs or metrics adapter → an OpenObserve alert or a
  **`PrometheusRule`**. A firing goes back through the adapter's webhook to
  the Observer, which stores it in its own SQLite and **sends the
  notification itself**, from `ObservabilityAlertsNotificationChannel` CRs.
  Those CRs hold SMTP settings and webhook headers as Kubernetes
  `secretKeyRef`s.
- **Metric rules need the Prometheus Operator and Alertmanager.** The
  metrics adapter writes only `PrometheusRule` CRs. Spec 023 removed the
  Operator and its 10 CRDs, which saved about 450 MiB idle and about
  390 MiB of kube-apiserver after removal (ADR 0021). Bringing them back
  for alerts reverses a measured decision.
- **Using the Observer's sender** puts a person-typed URL in a pod inside
  the cluster, with no validation of ours. That is RP-062's exposure made
  worse. It also keeps credentials outside the Secrets capability (spec
  IN 13) and creates a second store of Alerts beside Serendip's.
- **OpenObserve sends no "resolved" event** (Research: from a third-party
  guide, not OpenObserve's docs). Serendip would have to work out
  resolution anyway.

**Decided: `serendipd` evaluates every rule itself.** It queries the
adapters (and Prometheus, for restarts) on each rule's interval, with
queries Serendip builds; nothing is user-written PromQL or SQL. It keeps
the Alert state and sends notifications itself.

- **Why this is cheaper and safer:**
  - nothing new is installed for alerting: no Operator, no Alertmanager,
    no backend alert objects, so RP-066 doesn't grow (open question 7);
  - no firing has to reach `serendipd`, so there is no new listener
    (open question 4);
  - only `serendipd` contacts a person-typed address, through one
    validating client (spec IN 14);
  - `core/` sees only rules and conditions in its own words (constitution
    1);
  - resolution is the same evaluation coming back clear.
- **Its cost** is query load: one query per rule per interval. That is
  bounded by caps (decision 6) and measured in T013.
- **This changes the spec** in two places (see "Spec amendments"). There
  is no backend object to repair by hand, and evaluation is the loop
  that reconciliation covers.
- **cs-tools' design still applies** (spec "Prior art"): fingerprints
  and a reopen window for folding, and a separate delivery loop with
  retries. Its webhook mappings don't apply, because nothing is
  ingested.

## Spec amendments (agreed with this plan)

1. **IN 8 / AC 9, "reconciled to the backend".** These become: a rule's
   status says whether it is being evaluated, and why not; a resync with
   nothing changed adds near-zero CPU; evaluation's own cost is measured
   (T013). Nothing is created in the backend, so there is nothing to
   repair there.
2. **AC 13, "turning traces off needs an Approval where it deletes
   data".** Turning traces off removes the collector and the tracing
   adapter. It does not delete stored traces, which age out with the 3-day
   retention and reappear if traces are turned back on. So it deletes no
   data and needs no Approval. Removing the package still needs one.
3. **Open question 4 is closed:** polling, no inbound path.

## Decisions

1. **Traces: OpenChoreo's `observability-tracing-openobserve` module, on
   the existing OpenObserve** (open question 2).
   - **What it installs:** an OpenTelemetry Collector Deployment
     (`opentelemetry-collector`, OTLP on 4317 gRPC and 4318 HTTP) and a
     tracing adapter on :9100. Its bundled OpenObserve is turned off; the
     collector exports to the logs module's instance at `/api/<org>/traces`.
     Research: about 100 MiB of requests, plus the extra store load.
   - **Version:** the newest release that supports OpenChoreo 1.2 or later
     (Research: 0.3.x). T001 pins it in `VERSIONS.md`.
   - **Its admin Secret** (`openobserve-admin-credentials`, keys
     `ZO_ROOT_USER_EMAIL` and `ZO_ROOT_USER_PASSWORD`) is meant to come
     from External Secrets Operator. Serendip doesn't install ESO
     (build-vs-adopt.md). T001 checks whether the logs module's Secret of
     that name serves, or whether the installer writes one.
   - **Tail sampling is turned off.** Research says the module keeps 100
     traces and 10 spans a second by default, which would silently drop
     the spans criterion 2 looks for. A memory limit on the collector and
     the 3-day retention bound it instead. The measured cost (T013) is
     stated with that load, and RP-069 records what an unbounded sender
     could do.
   - **The plane's `observer.tracingAdapter.enabled`** (Research: a
     prerequisite) is set only if T001 shows the module needs it when the
     Observer isn't queried.
2. **Trace queries go to the tracing adapter** through the API server's
   Service proxy, as for logs and metrics (spec 023 spike finding 4).
   - **Routes** (Research): `POST /api/v1alpha1/traces/query` and
     `POST /api/v1alpha1/traces/{traceId}/spans/query`.
   - **Scope is by names, not UIDs** (Research). Spec 023 found that an
     unknown scope field widens a query silently. So T001 tests the
     names, including a missing one and a wrong one. And Serendip checks
     every answer against the spans' `openchoreo.dev/*-uid` resource
     attributes, which the collector copies from pod labels. A trace with
     no span from the target Component and Environment is dropped from a
     list. If T001 finds name scoping exact, this check stays anyway, as
     a guard.
   - **One trace** is read only through a Deployment:
     `GET /v1/deployments/{id}/traces/{traceId}`. Serendip refuses it with
     404 unless a span of that trace belongs to the Deployment's
     Component and Environment. A trace ID alone never opens a trace.
   - **Hidden spans** (spec IN 4): Serendip checks the caller's read on
     each span's Component and Environment, and blanks the ones it can't
     read. It keeps their timing, so the timeline stays true, and drops
     their names, attributes and status message. A span with no Serendip
     identity (from outside OpenChoreo's labels) is hidden too.
   - **Filters:** minimum duration and errors only are applied by
     Serendip, because the adapter has none (Research). So a page can
     come back short, and the page carries `truncated` and a cursor,
     like log search.
   - **Spoofed identity** is checked in T001: whether a workload can send
     spans whose resource attributes claim another Component's UIDs, and
     whether the collector's `k8sattributes` overrides them from the
     sender's pod. If it doesn't, the collector config is set to
     override, or RP-070 records it.
3. **The collector's ingest is the one new path from workloads** (spec IN
   21). Serendip's NetworkPolicies admit workload namespaces to 4317 and
   4318 only. The collector's health, metrics and zpages ports, the tracing
   adapter and OpenObserve stay closed to them. T011 checks it from a
   workload pod, as spec 023 T013 did for Fluent Bit's `:2020`.
4. **Traces are a package option, not a second package** (open question
   6).
   - `ProviderInstance.spec.options` gains `traces: true|false`, default
     false. Changing it is an update, audited, under the existing
     install action at `/`.
   - **The package job** installs or removes the tracing module when the
     option changes. The applied fingerprint (`status.applied`, spec 022)
     includes the option, so a resync with nothing changed stays cheap.
   - **`provider.Requirements` gains `Options`:** a named map of extra
     costs, each `{IdleMemoryMiB, …, Disk}` on top of the base. The
     catalogue shows "traces: +N MiB idle, +M MiB disk a day".
5. **The new kinds** (`core/resource`), all with status conditions:
   - **`AlertRule`.** Owned by a Component or an Application, with an
     `environmentId`. Its spec holds a `condition` (decision 7),
     `severity`, `summary`, `channelIds`, `interval` (1–60 min, default
     1) and `enabled`. Status: `active` (with reason), `lastEvaluated`,
     `lastValue`, `silencedUntil`, and `silence` (who, why, until).
   - **`Alert`** (replacing spec 001's placeholder `AlertSpec`). Owned by
     its rule, so deleting a rule's Approval lists its Alerts in the
     blast radius. It is written only by `serendipd`, like `Approval`: it
     isn't in `resourcePaths`, so there is no generic `PUT` or `DELETE`.
     - **Spec (fixed at creation):** rule, Component, Environment,
       severity, summary, fingerprint.
     - **Status:** `state` (`firing` or `resolved`), `startedAt`,
       `resolvedAt` with a reason (`clear`, `ruleDeleted`,
       `packageRemoved`, `componentGone`), `value`, `threshold`,
       `acknowledged` (who, when), and `deliveries[]` (channel, event,
       attempts, state, last error, time).
   - **`NotificationChannel`.** At the platform (no owner) or owned by a
     Project. Types `webhook` (`url`, `secretId` for a signing secret,
     `urlSecretId` when the URL itself holds a token) and `email`
     (`smtpServerId`, `to[]`). Also `default` (bool) and
     `allowPrivateDestination` with a `reason`.
   - **`SMTPServer`.** Platform-only: `host`, `port`, `tls` (`starttls`
     or `implicit`, never plain), `from`, `usernameSecretId`,
     `passwordSecretId`. A separate kind lets Projects make email
     channels without holding SMTP credentials.
   - **Graph edges:** rule → Component or Application, rule → channel,
     Alert → rule, channel → Secret, channel → SMTP server, SMTP server →
     Secret. Deleting a channel or Secret that something uses is refused
     (409), as for other references.
6. **The evaluator** (`core/reconcile/alerts.go`). One goroutine in
   `serendipd`, beside the engine (it's an ops loop, not a resource
   reconcile, the same way package jobs are separate).
   - **Each tick** (15 s) it takes the rules that are due, with a little
     jitter, and runs at most 2 queries at once, each with a 20 s timeout.
   - **Caps:** 200 enabled rules per install and a 1-minute minimum
     interval, so the worst case is about 3.3 queries a second. More is
     refused with the reason.
   - **No write per evaluation.** `lastValue` and `lastEvaluated` stay in
     memory and are flushed at most once a minute, and only for rules that
     changed. The store sees a write on a state change, not on every
     check (SQLite on a T470).
   - **Folding** (cs-tools' design): the fingerprint is
     `rule|component|environment`. While an Alert for a fingerprint is
     firing, evaluations update it in memory. A firing within 5 minutes of
     that fingerprint's last resolution reopens the same Alert, so a
     flapping rule makes one record, not one per flap.
   - **Errors** (adapter down, timeout) don't change an Alert's state.
     They set the rule's condition `Evaluating=False` with the reason,
     shown in the console. No "resolved" is sent because data is missing.
   - **On restart,** open Alerts are read from the store and evaluation
     resumes. A firing isn't re-sent unless its delivery was still
     pending.
   - **Package not Ready or removed:** rules read `active: false,
     PackageNotReady`. On removal, open Alerts resolve with
     `packageRemoved` (spec IN 18) and that is delivered.
7. **Conditions**, each one built by Serendip into a fixed query (no
   query language, spec OUT):
   | Type | Data | Query |
   |---|---|---|
   | `metric` (`cpu` or `memory`; `value`, `percentOfRequest` or `percentOfLimit`; `above`; `for`) | metrics adapter (spec 023's path) | the window's points; fires when every point in `for` is above |
   | `restarts` (`above` within `window`) | Prometheus, kube-state-metrics' `kube_pod_container_status_restarts_total` | `increase(...)` over the window, by the Deployment's pods. T001 checks that the pods' labels can be matched, or names pods by owner instead |
   | `logMatch` (`text`, `above` within `window`, `above` ≤ 999) | logs adapter | the search with `limit = above + 1`; fires when it comes back full |
   | `traceErrors` / `traceLatency` (`p95` or `p99`, `aboveMs`, within `window`) | tracing adapter | errored traces counted, or the percentile computed, over at most the newest 1000 traces in the window (the rule's status says when the cap was hit) |
   | `packageDisk` (`percentOfBudget`) | spec 023's `usage` | built-in only |

   A trace condition on a package without traces is refused at create,
   and goes inactive if traces are turned off.
   - **Restarts read Prometheus directly**, through the same Service proxy.
     That is one new query path. The PromQL is a fixed template filled
     from validated IDs, never user text.
8. **The built-in disk rule** (spec IN 7). When the package becomes Ready,
   `serendipd` creates one `AlertRule` (`builtIn: true`, owned by the
   `ProviderInstance`, at 85% of the budget). It notifies every platform
   channel marked `default`. People can silence it, change the threshold
   or disable it, but can't delete it; removing the package removes it.
9. **The sender** (`core/notify`, standard library only; open question 3).
   - **Why in `core/`:** webhook and SMTP are protocols, not products, so
     constitution 1 allows it. A Notification capability with providers
     would add a registry entry and an interface for two stdlib clients.
     Chat products come later as webhook formats (spec OUT), so there's
     still no provider to swap.
   - **One `http.Client`:**
     - its `Dialer.Control` checks the address actually dialed, which
       defeats DNS rebinding;
     - `CheckRedirect` refuses every redirect;
     - `Proxy` is nil, so `HTTPS_PROXY` can't route around the check;
     - TLS 1.2 or later, verified;
     - a 10 s timeout and a 64 KiB cap on the response it reads.
   - **SMTP** uses `net/smtp` over the same checked dialer: STARTTLS
     required (or implicit TLS on 465), auth only after TLS.
   - **The address check** (spec IN 14) refuses loopback, unspecified,
     private (RFC 1918, ULA), link-local, CGNAT (100.64/10), multicast and
     broadcast. It also refuses the cluster's pod and Service CIDRs and
     the node's own addresses (read from the Node objects, refreshed with
     the cluster). Both IPv4 and IPv6, including IPv4-mapped IPv6.
     - **It runs at create, at update, at test send and at every
       delivery.**
     - **`allowPrivateDestination`** skips only the private-range check,
       for that channel. It needs the separate action
       `serendip/notificationChannels/allowPrivateDestination/action`,
       which only Owners hold (decision 12), and a recorded reason.
       Loopback, link-local and the cluster's own ranges stay refused
       even then (an in-house receiver is reached by its own address, not
       through the cluster).
   - **Plain HTTP** needs the same allowance and its reason.
   - **The webhook body** is `serendip.alert.v1`: `{version, event
     (firing|resolved|test), alert{id, rule, severity, summary, state,
     project, application, component, environment, startedAt,
     resolvedAt, value, threshold, consoleUrl}}`. It is documented in
     `operations.md`.
   - **The signature** is `X-Serendip-Signature: t=<unix>,v1=<hex
     HMAC-SHA256(secret, t + "." + body)>`, so receivers can refuse
     replays.
10. **Delivery** (cs-tools' separate loop).
    - **Pending deliveries** live on the Alert (`deliveries[]`), so a
      restart resumes them.
    - **Retries:** up to 5 attempts, backing off 10 s, 30 s, 1 min, 5 min
      and 15 min, then `failed` with the last error (spec IN 16). 4xx
      answers other than 408 and 429 aren't retried.
    - **Per-channel limit:** at most one send a second. When more are
      queued for the same channel, firings and resolutions are folded into
      one digest ("3 alerts firing, 1 resolved"), as cs-tools does for
      chat webhooks.
    - **Silences** suppress deliveries, not Alerts (spec AC 10). A
      suppressed delivery is recorded as `silenced`.
11. **Secrets** (spec IN 13). Channel and SMTP credentials are `Secret`
    references (spec 009), like builds' `credentialSecret`.
    - The sender reads the values at send time and never caches them
      beyond the send.
    - API responses show `secretSet: true|false`.
    - Delivery errors are cleaned before they're stored: the URL's path
      and query are cut, leaving only the host, so a token in a URL can't
      reach an Alert, an event or a log.
    - **AC 8's check:** a known token is grepped for in the store, audit
      rows, events and `serendipd`'s log after a full run (T012).
12. **Authorization** (spec IN 19). New actions:
    - `serendip/components/traces/read`. Traces carry request data, so
      they're guarded like logs: Reader, User Access Administrator and RBAC
      Administrator get it in `NotActions`; Log Reader gets it through
      `*/read`.
    - `serendip/alertRules/{read,write,delete}` and
      `serendip/alertRules/silence/action`;
    - `serendip/alerts/read` and `serendip/alerts/acknowledge/action`;
    - `serendip/notificationChannels/{read,write,delete}`,
      `…/test/action` and `…/allowPrivateDestination/action`. Only Owner
      holds the last one: it's in Contributor's `NotActions`.
    - `serendip/smtpServers/{read,write,delete}`, at `/` only.
    - **Using channels:** a rule's write needs `notificationChannels/read`
      on each channel it names, so a Contributor in Project A can't route
      alerts to Project B's channel. Platform `default` channels are
      readable by any signed-in principal for that purpose. Their URLs are
      shown only to those with read at `/`, and to everyone else as
      "platform channel".
    - Every route gets a row in the route-table test.
    - **Agents** use the same roles. Acknowledging is a write, not a
      delete, so it needs no Approval.
13. **Deletes** of `AlertRule`, `NotificationChannel` and `SMTPServer` go
    through spec 019's Approvals, with their blast radius (a rule's Alerts).
    `Alert`s have no delete route.
14. **Alert retention** (open question 5). Resolved Alerts older than 30
    days are removed by `serendipd`, at most 500 rows a pass.
    - This is system retention, not a person's delete, so no Approval is
      made. It's one audit row per pass, with the count, under the system
      actor. As spec 019's RP-003 notes, retention of operational records
      wasn't settled. RP-071 records the choice.
    - **Growth:** about 2 KiB per Alert, and deliveries capped at 20 per
      Alert. A flapping rule is one Alert (decision 6), so 200 rules can't
      grow the store faster than their state changes.
15. **The API** (`api/alerts_handler.go`, `api/traces_handler.go`). Every
    route is in `api/catalog`, so CLI and MCP parity follows.
    - **Traces:** `GET /v1/deployments/{id}/traces`,
      `GET /v1/applications/{id}/traces?environmentId=` (both with
      `from`, `to`, `minDurationMs`, `errorsOnly`, `limit`, `before`), and
      `GET /v1/deployments/{id}/traces/{traceId}`.
    - **Rules:** the generic CRUD on `/v1/alertrules`, plus
      `POST /v1/alertrules/{id}/silence` (`until`, `reason`) and
      `DELETE …/silence`.
    - **Alerts:** `GET /v1/alerts` (filters: `componentId`,
      `applicationId`, `environmentId`, `state`, `severity`) and
      `GET /v1/alerts/{id}`, scoped to what the caller can read;
      `POST /v1/alerts/{id}/acknowledge`. `GET /v1/alerts/summary` gives
      firing counts by severity for the top bar.
    - **Channels:** CRUD on `/v1/notificationchannels`, plus
      `POST …/{id}/test`. CRUD on `/v1/smtpservers`.
    - **Packages:** `PUT` on the `ProviderInstance` with `options.traces`.
16. **CLI and MCP:**
    - **CLI:** `serendip traces list|show`, `serendip alerts
      list|show|ack|summary`, `serendip alertrules
      create|list|show|update|delete|silence|unsilence`, `serendip
      channels create|list|show|update|delete|test`, `serendip smtp
      create|list|show|update|delete`, and `serendip packages set
      observability --traces=on|off`.
    - **MCP:** read tools for traces (a summarised list and one trace,
      capped at 200 spans) and Alerts, and the write tools that follow
      from the catalogue under the agent's roles. A trace's span attributes
      are capped at 2 KiB per span in the MCP result.
17. **The console:**
    - **Component › Observability:** a Traces section (list with filters,
      and a trace timeline with hidden spans shown as grey bars) and an
      Alerts section (the Component's rules and open Alerts, with
      acknowledge and silence).
    - **Application:** traces across Components; its rules.
    - **A top-bar bell** with firing counts by severity, linking to an
      Alerts page (filters).
    - **Settings › Notifications:** channels (test send, the private
      allowance for Owners), SMTP servers (Owners).
    - **The Packages page:** a traces switch with the stated extra cost,
      and the removal wording extended (spec IN 18).
    - The traces timeline follows the dataviz skill (spec 023's
      `MetricChart` tokens). Each view shows its "Same request through the
      API".

## Files touched (outline)

- **`core/resource`:** `alerting.go` (`AlertRule`, `Alert`,
  `NotificationChannel`, `SMTPServer`, validation), replacing `ops.go`'s
  placeholder `AlertSpec`; `capability.go` (`ProviderInstance` options).
- **`core/provider`:** `requirements.go` (`Options`).
- **`core/reconcile`:** `observability.go` (`ObservabilityProvider` gains
  `SearchTraces`, `TraceSpans`, `EvaluateCondition`), `alerts.go`
  (evaluator, folding, retention), `delivery.go`, `providerinstance.go`
  (options in the fingerprint, built-in rule).
- **`core/notify`:** `dialer.go` (the address check), `webhook.go`,
  `smtp.go`, `body.go`.
- **`core/policy`:** `builtins.go` (actions, `NotActions`).
- **`providers/observability/openchoreo`:** `tracing.go` (module install,
  values, collector config), `traces.go` (adapter client, UID check),
  `conditions.go` (the fixed queries), `policies.go` (ingest rule),
  `requirements.go` (`Options`), `VERSIONS.md`.
- **`api`:** `traces_handler.go`, `alerts_handler.go`,
  `notifications_handler.go`, `authz` route table, `catalog`.
- **`cmd/serendip`:** the commands. **`mcp/`:** the tools.
  **`cmd/serendipd`:** wiring the evaluator and sender.
- **`ui/src`:** `screens/Observability.tsx` (traces, alerts),
  `screens/Alerts.tsx`, `screens/Notifications.tsx`, the top-bar bell,
  `components/TraceTimeline.tsx`, Packages.
- **`profiles/edge-1/bench`:** a trace load (two instrumented Components
  calling each other), and a rule-evaluation load (200 rules).
- **Docs:** `operations.md` (rules, Alerts, channels, the webhook body),
  `resource-model.md`, `capability-model.md` (Alerting row),
  `application-platform.md`, `authorization.md`, `approvals.md` (the new
  delete targets), ADR 0022 (Serendip evaluates alert rules and sends
  notifications; OpenChoreo's alerting path not used),
  `build-vs-adopt.md`, the risk register.

## Risks (to the risk register as they're confirmed)

- **RP-069:** with tail sampling off, a noisy sender fills the trace
  store faster than retention. It's bounded by OpenObserve's volume, as
  RP-068 is for logs, and the disk rule now warns.
- **RP-070 (if T001 confirms):** a workload can label its spans with
  another Component's identity.
- **RP-071:** resolved Alerts are removed after 30 days by system
  retention, with no Approval.
- **RP-072:** evaluation polls. A condition shorter than the interval can
  be missed, and a firing is seen up to one interval late (1 minute at
  best).
- **RP-073:** a trace percentile over the newest 1000 traces in the window
  isn't a percentile of all of them on a busy Component (the rule's status
  says so).
- **RP-062 is revisited:** OpenObserve's guard stays off for the logs
  module's own webhook, but no person-typed destination ever reaches
  OpenObserve now. Its "Revisit" is met; its mitigation becomes decision 9.
- **RP-066:** unchanged. No `observabilityalertrules` objects are made.
- **RP-068:** its residual ("no alert") is closed by the built-in rule.
- **Query load:** 200 rules at one-minute intervals, against OpenObserve,
  which spec 023 found slows as data grows (RP-055). T013 measures
  evaluation latency at 3 days of data.

## Explicitly NOT done

Everything in the spec's Scope OUT, and also:
- OpenChoreo's `ObservabilityAlertRule`, `ObservabilityAlertsNotificationChannel`,
  the `observability-alert-rule` trait and the Observer's alert store;
- Alertmanager and the Prometheus Operator;
- the collector's tail sampling;
- trace search by attribute or by operation name (the adapter's raw query
  is a query language);
- notification templates set by people (the body is fixed and versioned);
- a Notification capability or provider registry entry (decision 9).
