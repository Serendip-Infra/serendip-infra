# Operations

Serendip owns incident, alerting, and SLO semantics natively — this is one
of the areas the project is explicitly meant to differentiate on, rather
than embed a third-party ITSM/observability product wholesale
(`capability-model.md`'s native/external split).

## Incident lifecycle

```
Alert → Incident → Diagnosis → Recommendation → Approval → Action → Verification → Postmortem
```

- **Alert** — a single detected anomaly (metric threshold, log pattern,
  synthetic check). Built in spec 024: "Alerts (spec 024)" below.
- **Incident** — one or more correlated Alerts, triaged with a severity and
  affected-resource set. First-class Serendip resource (`resource-model.md`).
- **Diagnosis** — AI or human analysis of probable cause, using the
  resource graph and recent-change history (see below).
- **Recommendation** — a proposed remediation, often referencing a Runbook.
- **Approval** — required before any non-pre-approved action executes
  (constitution 5).
- **Action** — the executed remediation, always via
  `AI → Serendip Action → Policy → Provider → Execution` (never direct
  infra access).
- **Verification** — confirms the action actually resolved the symptom
  before the Incident is closed.
- **Postmortem** — recorded root cause, timeline, and action taken, once
  resolved.

### Incident shape (illustrative)

```yaml
Incident:
  id: INC-2026-001
  severity: SEV2
  service: payments
  environment: production
  detected_by: [metric, log, trace]
  symptoms:
    - HTTP 500 increased
    - DB latency increased
  ai:
    diagnosis: "Database connection exhaustion"
    confidence: 0.91
  actions:
    proposed: [increase pool, restart affected pods, scale database]
    approved: [restart affected pods]
```

This structure is what makes AI diagnosis operate on something concrete
rather than free text — see `ai-operations.md`.

## Alerts (spec 024)

The first step of the lifecycle is built: alert rules, Alerts and
notification channels. Incidents, correlation and runbooks are not yet.
`serendipd` evaluates every rule itself and sends every notification
through one validating client (ADR 0022). OpenChoreo's alerting path,
Alertmanager and the Prometheus Operator aren't used. Design detail:
`specs/024-alerts/plan.md`.

### Rules

An `AlertRule` is owned by a Component or an Application, with an
`environmentId`. Its spec is a `condition`, a `severity` (`info`,
`warning`, `critical`), a `summary`, its `channelIds` (or the Project's
default channels when empty), an `interval` (1–60 minutes, default 1) and
`enabled`.

| Condition | Data | Fires when |
|---|---|---|
| `metric`: `cpu` or `memory`, as `value` (millicores or MiB), `percentOfRequest` or `percentOfLimit`; `above`; `for` | the metrics adapter | every point in `for` is above |
| `restarts`: `above` within `window` | kube-state-metrics' restart counter through Prometheus (max − min per container, summed) | the count is above |
| `logMatch`: `text`, `above` (at most 999, RP-073) within `window` | the logs adapter's `total` | more lines match |
| `requestErrorRate`: `abovePercent`, `minRequests` within `windowMinutes` (spec 025) | Hubble's request metrics through Prometheus, 5xx over all, server side | the rate is above, with at least `minRequests` |
| `requestLatency`: `percentile` 95 or 99, `aboveMs` within `windowMinutes` (spec 025) | Hubble's latency histogram (interpolated in its buckets, RP-085) | the percentile is above |
| `traceErrors`: `above` within `windowMinutes` (spec 025) | the stored traces, `count(DISTINCT trace_id)` with an error | more traces failed |
| `packageDisk`: `percentOfBudget` | the package's `usage` | built-in only |

The three request and trace conditions need their source: on a CNI
without request metrics, or without the tracing package, the rule reads
`ConditionRefused` with the reason (`networking.md`).

- **Every query is Serendip's,** a fixed template filled from validated
  IDs, scoped by all three OpenChoreo UIDs. There's no query language.
- **The evaluator** (`core/reconcile/alerts.go`) ticks every 15 s, runs
  at most 2 queries at once (20 s each), and caps an install at 200
  enabled rules with a 1-minute floor. Next runs count from the due
  time, not the tick.
- **Status:** `active` with its reason (`PackageNotReady` while the
  observability package isn't Ready), `lastEvaluated` and `lastValue`,
  `silence` (who, why, until, at most 30 days), and an `Evaluating`
  condition. A query error, a refused condition or no data
  (`QueryFailed`, `ConditionRefused`, `NoData`) sets the condition and
  never changes an Alert. Values are held in memory and written on a change, with an
  hourly refresh of `lastEvaluated`; the API answers the live time
  (RP-080).
- **The built-in disk rule** (`observability-disk`, warning at 85% of the
  budget) is created when the package becomes Ready, notifies the
  platform's default channels, and can be silenced, retuned or disabled
  but not deleted (409). It goes with the package, so removal needs
  `cascade=true`.

### Alerts

An `Alert` is written only by `serendipd` and owned by its rule; there's
no generic write or delete route.
- **Folding:** the fingerprint is `rule|component|environment`. While it
  fires, evaluations update the one Alert; a firing within 5 minutes of
  its last resolution reopens the same Alert, so a flapping rule is one
  record.
- **Status:** `state` (`firing` or `resolved`), `startedAt`, `resolvedAt`
  with its reason (`clear`, `ruleDeleted`, `packageRemoved`,
  `componentGone`), `value` and `threshold`, `acknowledged` (who, when),
  and up to 20 `deliveries`.
- **A `logMatch` Alert records the count,** never the log lines.
- **Retention:** resolved Alerts older than 30 days are removed hourly,
  at most 500 a pass, through one policy-decided Approval per rule
  (RP-071).

### Channels and delivery

| Type | Settings | Secrets |
|---|---|---|
| `telegram` | `chatId`; the host is always `api.telegram.org` | `botTokenSecretId` (the whole credential, RP-069) |
| `email` | `smtpServerId`, `to` (at most 20) | the SMTP server's username and password |
| `webhook` | `url`, or `urlSecretId` when the URL holds a token (only its host is stored) | `signingSecretId` (optional) |

- **A channel** is platform-wide or owned by a Project, with a
  `minSeverity` (default `info`) and `default`. An `SMTPServer`
  (platform-only: `host`, `port`, `tls` of `starttls` or `implicit`,
  `from` and its two Secrets) is a relay Projects use without holding
  its credentials. A Gmail app password or Brevo works; a mail server on
  the laptop doesn't.
- **The sender** (`core/notify`) checks the address actually dialed and
  refuses loopback, unspecified, private (RFC 1918, ULA), link-local,
  CGNAT, multicast, broadcast and the cluster's own pod, Service and node
  ranges (RP-076), for IPv4, IPv6 and IPv4-mapped IPv6. It follows no
  redirects, uses no proxy, needs TLS 1.2 or later and pools no
  connections. The check runs at save, at test send and at every
  delivery. Only an Owner can set `allowPrivateDestination` (an in-house
  LAN receiver; the cluster's ranges and loopback stay refused) or
  `allowPlainHTTP`, each with a reason.
- **Delivery** (`core/reconcile/delivery.go`) keeps pending deliveries on
  the Alert, so a restart resumes them. Up to 5 attempts, waiting 10 s,
  30 s, 1 min and 5 min; a 4xx other than 408 or 429 isn't retried. A 429's
  `Retry-After` (Telegram's `retry_after`) is honoured up to 15 minutes.
  At most one send a second per channel, and 20 a minute to a Telegram
  group; when more are queued, Telegram and email get one digest.
- **Recorded on each delivery,** so "why wasn't I told" has an answer:
  `pending`, `sent`, `failed` with its host-only error, `silenced`, or
  `belowMinSeverity`. Silences suppress deliveries, not Alerts.
- **Alert content leaves the machine** (RP-070): the summary, names,
  values and a console link, never log lines. The channel form says so.

### The bodies

**Telegram** (`sendMessage`, HTML). A firing is:
- a bold first line, "🔴 FIRING · critical" (🟠 warning, 🔵 info);
- the Component in bold, then "Environment · Project";
- a detail line: the value against the threshold, and when it started;
- "Summary: <summary>";
- an italic footer, "Rule <name>";
- a "View in console" URL button.

A resolution is one line, "✅ RESOLVED · Component · Environment ·
Project", plus its detail (how long it lasted), sent as a reply to the
firing. A test is "🧪 TEST". A `localhost` console URL can't be a button,
so the link goes in the text. Every value is HTML-escaped.

**Email:** the subject "[CRITICAL] firing: <summary>" or "[RESOLVED]
<summary>", a plain-text and an HTML part with the same fields and the
link, and `Message-ID`/`In-Reply-To` so the resolution threads under the
firing.

**Webhook, `serendip.alert.v1`:** a `POST` with
`Content-Type: application/json`, `X-Serendip-Event` (`firing`,
`resolved` or `test`), `User-Agent: serendip-notify/1`, one body per
Alert (never a digest):

```json
{
  "version": "serendip.alert.v1",
  "event": "firing",
  "alert": {
    "id": "…", "rule": "checkout-memory", "severity": "critical",
    "summary": "checkout memory above 400 MiB", "state": "firing",
    "project": "shop", "application": "storefront",
    "component": "checkout", "environment": "production",
    "startedAt": "2026-11-15T12:25:39Z",
    "value": 412.5, "threshold": 400,
    "consoleUrl": "https://…/alerts/…"
  }
}
```

`project`, `application`, `component`, `environment`, `resolvedAt` and
`consoleUrl` are left out when empty; times are UTC. The shape is fixed
for `v1`: a change to it is a new version, never people's templates.

**The signature,** when the channel has a `signingSecretId`:

```
X-Serendip-Signature: t=<unix seconds>,v1=<hex HMAC-SHA256(secret, t + "." + raw body)>
```

A receiver recomputes the HMAC over the raw bytes it received, compares
in constant time, and refuses a `t` more than a few minutes old (a
replay). `docs/guides/alert-integrations.md` has a working check.

### Heartbeat

One platform `Heartbeat` (`PUT /v1/heartbeat`, Owners) sends `GET` or an
empty `POST` to an outside monitor's URL (a Secret) every 1–10 minutes,
through the same checked client. **It sends only while Serendip can
alert:** the evaluator ticked within 2 minutes, the delivery loop ran and
the store answers. Otherwise it sends nothing, and the monitor's own
timeout raises the alarm; it never sends a "fail". It proves Serendip
can alert, not that a message reached a person (RP-074). The monitor
must not run on the same machine (the guide).

### Where to use it

- **API:** `/v1/alertrules` (CRUD, `…/silence`, `…/unsilence`),
  `/v1/alerts` (list with filters and `limit`, `summary`, get,
  `…/acknowledge`), `/v1/notificationchannels` (CRUD, `…/test`),
  `/v1/smtpservers`, `/v1/heartbeat` (`PUT`, `GET`, `DELETE`, `…/test`),
  and `POST /v1/secrets/{id}/telegramchats` to find a chat ID.
  Authorization: `authorization.md` "Alerts and notifications".
- **CLI and MCP:** `docs/cli-and-mcp.md` "Alerts and notifications".
  Channel, SMTP and heartbeat writes are CLI-only.
- **Console:** Alerts on a Component's Observability tab and on an
  Application, the bell in the top bar and `/alerts`, and Settings ›
  Notifications (channels, SMTP servers, the heartbeat).

## Networking (spec 025)

Design: `networking.md`, ADR 0023. What an operator does with it:

- **Which CNI runs:** `serendip networking show` (or Platform › Network
  layer) gives the plugin, version, policy enforcement and every reason
  something is unavailable, refreshed every 5 minutes.
- **Changing a cluster's CNI is a reinstall.** K3s's CNI is fixed at
  install, and `serendipd` never changes it. To move from Flannel to
  Cilium (or back): back up what the store and the platform hold,
  uninstall K3s (`k3s-uninstall.sh`; what Cilium leaves on a real host
  isn't checked yet, RP-083), bootstrap again with `serendip-agent
  bootstrap --cni cilium|default`, and let `serendipd` re-ensure the
  platform. The new networking provider is detected on its next pass.
- **Drift:** if Cilium's pinned values change (Hubble's metrics or
  export, or Envoy's retry and path settings), the facts say so. On the
  Envoy settings, request visibility is removed until they're back
  (RP-081). Re-check on every Cilium upgrade
  (`providers/networking/cilium/VERSIONS.md`).
- **Flows** (`serendip flows <deployment>`, the Component's Networking
  tab › Connections): drops first, with the policy reason. A dropped
  connection between two Components usually means the platform's
  NetworkPolicy refuses it (a missing dependency). Records are per
  minute, not per packet, kept 3 days.
- **Requests and traffic:** the Observability tab's HTTP requests block and
  the topology diagram's traffic overlay. A Component without request
  metrics says why (TCP-only, a policy shape skipped, an `external`
  CNI).
- **Traces:** install the `tracing` package (Platform › Packages), then
  redeploy a Component to give it the OTLP endpoint (`serendip traces
  list|show`, the Observability tab › Traces). Removing the package deletes
  the stored traces, through an Approval, and rolls each traced
  Component once.
- **Disk:** flows and traces share the logs store's 5 GiB and its 3-day
  retention, and count towards the built-in `observability-disk` rule
  (RP-088, RP-092).

## SLOs and error budgets

SLO/SLI/error-budget/burn-rate are first-class Serendip concepts, not just a
Grafana dashboard convention:

```
Application
├── SLO (e.g. 99.95%)
├── SLI
├── Error Budget
├── Burn Rate
└── SLO Alerts
```

The observability backend (`capability-model.md`) may *visualize* these
(e.g. via Grafana-style dashboards), but Serendip owns the semantics —
computing burn rate and firing SLO alerts is not delegated to the backend
provider.

## Change intelligence

Serendip tracks changes as first-class events and correlates them against
degradation:

```
Change (deployment, config, Terraform/Crossplane, Helm, Kubernetes,
        secret rotation, infra change)
  → Metric degradation
  → Alert
  → Incident
```

This is powered by the resource graph (`resource-model.md`): an Incident on
a Component can be correlated against the most recent Change on that
Component or its dependencies (e.g. "the strongest correlation is a
PostgreSQL connection-saturation event that began 4 minutes after
deployment #184").

## Runbooks

A Runbook is a structured, referenceable remediation procedure:

```yaml
Alert: PostgreSQL connections > 90%
Runbook:
  1. Inspect connection pool
  2. Check recent deployments
  3. Check DB CPU
  4. Check long-running queries
  5. Scale pool if approved
```

AI recommendations reference Runbook steps rather than inventing arbitrary
actions — this keeps recommendations auditable and keeps the
severity-based autonomy matrix (`ai-operations.md`) meaningful.

## Resource / service graph

Incident diagnosis depends on the same resource graph defined in
`resource-model.md` (`depends-on` / `runs-on` edges). Example:

```
Internet → Gateway → Frontend → API → {Redis, PostgreSQL} → Kubernetes → Node
```

When the API degrades, the graph is what lets diagnosis walk from symptom to
probable cause instead of surfacing an undifferentiated pile of alerts.

## Ticketing / ITSM

Incidents (operational, Serendip-native) and Tickets (ITSM, e.g. a customer
support request) overlap but are not the same resource. Native Incidents are
always available; ITSM systems (ServiceNow, Jira, Zammad, GLPI) are optional
providers for the Ticketing capability (`capability-model.md`) that
Serendip correlates against, rather than replaces.

## Open questions

- On-call scheduling/paging is explicitly deferred — the source material
  marks it "integrate/provider initially," not a native capability for v1.
- Cost/FinOps, security posture, vulnerability scanning, status pages, and
  synthetic monitoring are all marked "later" in the source discussion —
  not scoped for v1, not designed yet.
