# ADR 0022: Serendip evaluates alert rules and sends notifications itself

Status: Accepted
Date: 2026-11-16

## Context

Spec 024 adds alert rules and notification channels on top of the
observability package (spec 023, ADR 0021). The spec first leaned towards
letting the backend evaluate rules, with Serendip only sending the
notifications. OpenChoreo has its own alerting path for that:
`ObservabilityAlertRule` → the Observer → the logs or metrics adapter →
an OpenObserve alert or a `PrometheusRule`. A firing comes back through
the adapter's webhook, and the Observer stores it and sends the
notification itself, from `ObservabilityAlertsNotificationChannel` CRs.

On this install that path doesn't fit (plan 024, "The decision that
shapes everything"):
- **Metric rules need the Prometheus Operator and Alertmanager.** The
  metrics adapter only writes `PrometheusRule` CRs. ADR 0021 removed the
  Operator and its 10 CRDs, about 450 MiB at idle plus about 390 MiB
  kube-apiserver kept after removal.
- **The Observer would send to person-typed URLs** from a pod inside the
  cluster, with no validation of Serendip's (RP-062), and keep SMTP and
  webhook credentials as Kubernetes `secretKeyRef`s, outside the Secrets
  capability.
- **It would be a second store of Alerts** beside Serendip's resource
  model, and OpenObserve sends no "resolved" event, so Serendip would
  work out resolution anyway.

## Decision

### 1. `serendipd` evaluates every rule

- **The evaluator** (`core/reconcile/alerts.go`) is one goroutine beside
  the engine, an ops loop like package jobs. Every 15 s it takes the
  rules that are due and runs at most 2 queries at once, 20 s each.
- **Conditions are fixed queries Serendip builds** (`metric`,
  `restarts`, `logMatch`, and the built-in `packageDisk`), never
  person-written PromQL or SQL. `core/` passes a condition in its own
  terms through `ObservabilityProvider.EvaluateCondition`, and the
  provider (`providers/observability/openchoreo/conditions.go`) builds
  the backend query, with all three OpenChoreo UIDs in scope.
- **Caps:** 200 enabled rules per install and a 1-minute minimum
  interval.
- **Alert state is Serendip's** (`Alert`, written only by `serendipd`),
  folded by fingerprint with a 5-minute reopen window. Resolution is the
  same evaluation coming back clear. An evaluation error never changes
  an Alert's state.
- Nothing is installed for alerting: no Operator, no Alertmanager, no
  backend alert objects, and no inbound listener.

### 2. One validating sender, `core/notify`, standard library only

- **Telegram's Bot API, SMTP and Serendip's signed webhook
  (`serendip.alert.v1`)** are sent by `core/notify`, through one
  `http.Client` whose dialer checks the address actually dialed
  (loopback, private, link-local, CGNAT, multicast and the cluster's own
  ranges refused, IPv4 and IPv6), with no redirects, no proxy, TLS 1.2
  or later and no connection pooling.
- **Every credential is a Secret reference** (spec 009), read at send
  time; errors are cleaned down to the host.
- **Deliveries run in their own loop** (`core/reconcile/delivery.go`),
  kept on the Alert so a restart resumes them, with retries, per-channel
  rates and digests for chat.

### 3. `core/notify` lives in `core/`, without a Notification capability

This is a reading of constitution 1, recorded here as a deviation:
- **The reading:** Telegram's Bot API, SMTP and the webhook are
  protocols and public HTTPS APIs, not infrastructure products Serendip
  installs. `core/notify` names the formats it speaks, as `core/jsonschema`
  names JSON Schema. A Notification capability with a provider registry
  would add an interface and a registry entry for a handful of HTTPS
  POSTs.
- **The limit (contract-guardian, T004 and T012):** the reading holds
  only while no Notification capability exists. When a second chat
  protocol, or a paid channel with its own account (SMS, voice,
  WhatsApp Cloud), arrives as a pluggable provider, Telegram becomes a
  provider detail then, and this ADR is superseded.

## Consequences

- **Query load is Serendip's to bound.** T011 (simulated T470, 200
  one-minute rules): `serendipd` +4.2 mcores and +5.5 MiB, the node +149
  to +234 mcores, mostly OpenObserve (RP-080). Latency plateaued over
  2.5 h (p95 0.8–1.2 s).
- **Polling** means a condition shorter than the interval can be missed,
  and a firing is seen up to one interval late (RP-072).
- **Only `serendipd` ever contacts a person-typed address**, through one
  checked client (spec IN 9). RP-062's mitigation becomes this sender.
- **OpenChoreo's alert CRD** (`observabilityalertrules`) stays unused;
  RP-066 is unchanged.
- **Formats Serendip doesn't speak** (Slack, Discord, Teams, ntfy, SMS,
  calls) go through the webhook to a relay such as n8n or Node-RED
  (`docs/guides/alert-integrations.md`).
- **Liveness of the alerting itself** is checked from outside, by the
  heartbeat to an outside monitor (plan decision 15, RP-074).

## Alternatives rejected

- **OpenChoreo's alerting path** (`ObservabilityAlertRule`, the Observer's
  channels and sender): the reasons in Context.
- **Alertmanager** beside plain Prometheus: it reverses ADR 0021's
  measured saving, covers metrics only (log and restart conditions would
  still be Serendip's), and keeps its receivers' credentials in its own
  config.
- **A Notification capability with providers now:** an interface and a
  registry for three formats, before a second implementation of any of
  them exists (constitution 10). Revisited under Decision 3's limit.
- **WSO2 cs-tools' alert services' code:** built on PostgreSQL, Kafka and
  Redis (constitution 4). Its design (fingerprint folding, a separate
  retrying delivery loop, digests) is adopted.
