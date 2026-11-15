# Spec 024: Traces, alert rules and notification channels

Status: Reviewed, 2026-11-15. Email and traces stay in scope (the defaults offered at review); cs-tools' alert services adopted as design, not code.

The follow-up spec 023 named (Scope OUT, plan "Explicitly NOT done"). It
builds on the observability package: it adds traces to it, and gives
Serendip its first operational resource kind, the **Alert**, with the rules
that raise alerts and the channels that carry them to people. Epic #65.

## Problem

Spec 023 made a Component's stored logs and its CPU and memory history
readable. Two gaps remain in the console's Observability tab, both shown
today as "Not available yet":

- **Nobody is told when something goes wrong.** A Component can run out of
  memory, restart in a loop or print errors for hours, and Serendip says
  nothing until someone opens the tab. The observability package can't
  even warn about its own disk filling before retention acts (RP-068).
- **A slow request can't be followed across Components.** Logs and
  metrics are per Component. When an Application's front end is slow,
  nothing shows which downstream Component the time went to.

`operations.md` makes alerting Serendip's own: "Serendip owns incident,
alerting, and SLO semantics natively". `resource-model.md` lists the
**Alert** as an operational resource, the input to the Incidents of spec
028. So what fires and to whom is Serendip's model, while the evaluation
can stay in the observability backend if that's cheaper (a plan decision,
as long as `core/` sees only the capability).

Three things this spec has to settle:

- **Destinations are a way out of the cluster, and into it.** A
  notification channel makes Serendip (or the backend) send an HTTP
  request to an address a person typed. Spec 023 had to turn off
  OpenObserve's SSRF guard for its own webhook (RP-062), so no destination
  may reach the backend without Serendip validating it first.
- **Channels hold credentials.** A webhook URL with a token in it, or an
  SMTP password, is a secret, not a field in the store.
- **Traces cost memory and need instrumented code.** A trace store and an
  OpenTelemetry collector are more weight on the reference machine, and
  only Components that emit spans produce anything. The cost has to be
  measured and stated, as spec 023's was.

## Prior art: WSO2's cs-tools alert services

`wso2-open-operations/cs-tools` (Apache-2.0, Go), WSO2's replacement for a
ServiceNow-backed stack, has two services that solve this spec's alert
half for a larger deployment. **Their designs are adopted; their code is
not** (it is built on PostgreSQL, Kafka and Redis, which constitution 4
rules out, and each library would need an ADR):

- **`sre-alert-ingestion-service`** normalises webhooks from ten monitoring
  sources to one alert shape. Its OpenObserve and Prometheus mappings are
  the reference for the shape of a firing that reaches `serendipd` (open
  question 4).
- **`sre-alert-core-service`** folds alerts into one open record by a
  fingerprint (`source|service|metric|environment|identifier`) inside a
  window, turns a resolving alert into a resolution rather than a new
  record, and delivers from a separate loop with retries and backoff.
  That is the model for IN 9 (one open Alert per rule and Component, not
  one per evaluation) and IN 16 (honest delivery).
- **Also borrowed:** idempotent writes, so a replayed firing is a no-op;
  refusing new work under load instead of queueing without bound;
  collapsing repeats into one notification, since chat webhooks allow
  about one message a second.

Its incident grouping is spec 028's, and its entity-service is a candidate
external Ticketing provider for that spec (`docs/build-vs-adopt.md`).

## Scope — IN

### Traces

1. **Measured cost, stated before install.** The package's `Requirements`
   (spec 022/023) are re-measured with traces on: idle, under load (spans
   from a few instrumented Components beside spec 023's log load), and the
   trace store's disk growth per day at that load, with its retention. The
   catalogue states the new numbers and where they came from. If traces
   don't fit comfortably beside edge-1, the catalogue says so (constitution
   4, 6).
2. **Collection.** A Component that sends OpenTelemetry spans to the
   address Serendip documents has them stored, tagged with its Project,
   Component and Environment. Components that send nothing cost nothing
   extra beyond the collector.
3. **Trace search.** For a Component in an Environment, and across an
   Application in an Environment: traces in a time range, newest first,
   filterable by minimum duration and by error, capped and paginated.
   Each row shows its start, duration, root operation, span count and
   whether it errored.
4. **One trace.** A trace's spans as a timeline: operation, Component,
   start, duration, status and attributes. Spans from Components the
   caller can't read are shown as hidden (count and duration only), not
   omitted silently and not revealed.
5. **Retention is bounded,** like logs and metrics, inside the package's
   stated disk budget.

### Alert rules

6. **An `AlertRule` resource,** owned by a Component (or an Application,
   applying to each of its Components), in an Environment. Kinds of
   condition, from data the package already has:
   - **metric threshold:** CPU or memory, as an absolute value or as a
     share of the Component's request or limit, held for a duration;
   - **restarts:** container restarts above a count within a window;
   - **log match:** lines containing a text, above a count within a window;
   - **trace errors or latency:** error count, or a latency percentile,
     above a value within a window (only once traces are installed).

   Each rule has a severity (`critical`, `warning`, `info`), a summary the
   alert carries, and the channels it notifies. Conditions the backend
   can't evaluate are refused at create, with the reason.
7. **Built-in rules for the package itself:** stored-data disk use near
   the package's budget (RP-068). They exist while the package is
   installed, can be silenced, and notify the platform's default channels.
8. **Declarative and reconciled** (constitution 3): a rule's desired state
   is reconciled to the backend; the rule's status says whether it is
   active, and why not (package not ready, Component gone, condition
   refused). A rule edited or deleted in the backend by hand is repaired
   on the next resync, and a resync with nothing changed costs near zero.

### Alerts

9. **An `Alert` resource** (`resource-model.md`'s operational kind): one
   firing of one rule for one Component in one Environment. It records
   when it started and resolved, its severity, the rule, the value that
   crossed the threshold, and which channels were notified with what
   result. A rule that keeps firing updates one open Alert; it doesn't
   create one per evaluation.
10. **Lifecycle:** `firing` → `resolved` when the condition clears.
    A person can **acknowledge** an Alert (recording who and when) and
    **silence** a rule for a time. Alerts are kept for a stated retention.
    Incidents, correlation and diagnosis are spec 028 and 029.
11. **Listing:** Alerts for a Component, an Application, an Environment,
    and all the caller can see, filterable by state and severity, newest
    first. The console shows a firing count where it's useful (the
    Component's tab, the Application, the top bar).

### Notification channels

12. **A `NotificationChannel` resource,** at the platform or a Project.
    Types:
    - **webhook:** an HTTPS POST of a documented JSON body, with an
      optional shared-secret signature header;
    - **email** through an SMTP server the Owner configures (host, port,
      sender, credentials).

    A rule names its channels; a Project can set default channels for
    rules that name none.
13. **Credentials are secrets** (spec 009): a webhook URL's token, a
    signing secret and SMTP credentials live in the Secrets capability,
    never in the store, the API's responses, audit diffs or logs. A
    channel shows whether its secret is set, not its value.
14. **Destinations are validated by Serendip before anything is sent or
    stored in the backend** (RP-062):
    - HTTPS only for webhooks, unless an Owner allows plain HTTP for one
      channel, with the reason recorded;
    - the host is resolved and refused if any address is loopback,
      link-local, private, the cluster's pod or Service ranges, or the
      node's own addresses, unless an Owner allows that channel
      explicitly (an in-house receiver);
    - checked again at send time, so a name that later resolves
      somewhere refused is still refused;
    - redirects aren't followed.
15. **Test send.** A person who can edit a channel can send a test
    notification and see the result (delivered, or the error).
16. **Delivery is honest:** each notification's result is recorded on the
    Alert; failed sends are retried a few times with backoff and then
    reported as failed, not dropped. A firing and its resolution are
    both sent.

### Installing and removing

17. **Traces are part of the observability package,** not a new package:
    an Owner turns them on or off on the installed package (or the plan
    shows a reason for a separate package). Turning them off says what is
    lost (stored traces) before it happens.
18. **Removal states what's lost** (spec 023's removal, extended): stored
    traces; rules stop being evaluated and show as inactive; open Alerts
    are resolved as "package removed"; channels stay. The plan settles
    RP-066 (the backend's alert-rule CRD outliving removal) if this spec
    creates objects of that kind.

### Across all features

19. **Authorization** (spec 017): new actions for traces, alert rules,
    alerts (read, acknowledge, silence) and channels (read, write, test),
    scoped like everything else. A rule's channels must be ones its author
    can use. Channel and SMTP settings at `/` stay with Owners. Every new
    route has a rule.
20. **Deletes go through an Approval** (spec 019) like every other delete.
21. **No way around Serendip** (spec 023's criterion 13): the trace store,
    the collector's query side and the backend's alerting API don't answer
    people or workloads directly. The collector's ingest port is the one
    new path from workloads, and it can't read anything back.
22. **Audit** (constitution 8): every create, update and delete of rules
    and channels, acknowledgements, silences and test sends, with actor,
    interface and decision. Notifications sent are recorded on the Alert.
23. **Interface parity** (constitution 7): every console view is an API
    call shown in "Same request through the API", a CLI command and an
    MCP tool; `api/catalog` covers them.
24. **Assistant use:** MCP tools read traces and Alerts as capped,
    summarised results (diagnose freely, constitution 5). An agent may
    acknowledge an Alert if its roles allow; creating rules and channels
    follows the same roles as for a person.

## Scope — OUT (explicitly)

- **Incidents, alert correlation and grouping, AI diagnosis** (specs 028,
  029).
- **SLOs, SLIs, error budgets and burn-rate alerts.** `operations.md`
  makes them Serendip's; they need request metrics (spec 025, Hubble) and
  a spec of their own.
- **Request rate, errors and latency metrics and the traffic overlay**
  (spec 025).
- **On-call schedules, escalation and paging** (`operations.md`: integrate
  later).
- **Chat-specific channels** (Slack, Teams, Discord) beyond what a generic
  webhook reaches; per-product formatting is a later spec.
- **Notifying people about pending Approvals,** though channels are built
  so that can use them later (spec 019 left notifications out).
- **Auto-instrumenting Components** (injecting OpenTelemetry agents).
  Components emit spans themselves.
- **Alerts on Serendip's own health** (`serendipd`, the agent, nodes)
  beyond the package's built-in disk rule.
- **Custom query languages** (PromQL, SQL) in rules or searches.
- **Bring-your-own tracing or alerting** (an existing Jaeger, Alertmanager);
  the `external` escape hatch stays planned.
- **Turning observability on by default in edge-1.**

## Affected resources and capabilities

- **The Observability capability** (`core/`): trace search and trace
  read; reconciling alert rules and reading their state. No product names
  in `core/` (constitution 1).
- **New resource kinds:** `AlertRule`, `Alert` (operational) and
  `NotificationChannel`, with status conditions and graph edges (rule →
  Component/Application, rule → channel, alert → rule).
- **A notification sender:** delivering webhooks and email, with
  destination validation. Whether it lives in `core/` or as a provider of
  a Notification capability is a plan decision.
- **The observability provider:** traces in the package, rule
  reconciliation, new `Requirements`.
- **Secrets (spec 009):** channel credentials.
- **Authorization, approvals, audit:** new actions, routes, deletes.
- **The console:** traces in the Observability tab, alert rules and
  alerts, channels, firing counts.
- **CLI and MCP:** new commands and tools.
- **Benchmarks:** idle, under-load and disk cost with traces on, on the
  simulated T470; the cost of rule evaluation.
- **Docs:** `operations.md`, `resource-model.md`, `capability-model.md`,
  `application-platform.md`, `authorization.md`, ADRs as needed,
  `build-vs-adopt.md`, the risk register (RP-062, RP-066, RP-068).

## Acceptance criteria

1. **Cost before install.** With traces off, `GET /v1/packages`, the CLI,
   MCP and the Packages page show the measured cost of turning them on
   (idle, under load, disk a day, retention), matching a recorded run in
   `profiles/edge-1/bench/results/`.
2. **Traces.** A sample Application of two instrumented Components calls
   one from the other; the trace is found by Component and by Application,
   by time and minimum duration, and its spans show both Components on one
   timeline, through the console, CLI and MCP. A caller who can read only
   one of the two Components sees the other's spans as hidden.
3. **A metric rule fires and resolves.** A memory rule on a Component
   fires an Alert when a load crosses it, the channel receives the firing,
   and the Alert and the channel receive the resolution when the load
   stops. Repeated evaluations update one Alert.
4. **Restart and log rules** each fire on a Component made to crash-loop
   or to print a known error line.
5. **Built-in disk rule.** With the threshold lowered for the test, the
   package's disk rule fires and notifies the platform's default channel.
6. **Webhook and email delivery.** A webhook receiver gets the documented
   body with a valid signature; an SMTP test server gets the email. A
   failing receiver shows retries and then "failed" on the Alert.
7. **Destination validation.** Creating, updating or test-sending a
   channel to `127.0.0.1`, a private address, the cluster's Service range,
   a name resolving to one of those, a plain-HTTP URL or a redirect is
   refused with a plain reason. With an Owner's explicit allowance, an
   in-cluster receiver works. Checked against the real backend on
   `serendip-t018`, not only in unit tests.
8. **Secrets stay secret.** No channel token, signing secret or SMTP
   password appears in API responses, the store's rows, audit diffs,
   events, Alerts or `serendipd`'s logs (checked by searching for a known
   value).
9. **Reconciled.** A rule deleted or edited in the backend by hand is
   repaired on the next resync; a resync with nothing changed adds
   near-zero CPU (measured).
10. **Acknowledge and silence.** An acknowledged Alert shows who and when;
    a silenced rule sends nothing until the silence ends, and its Alerts
    still record.
11. **Authorization.** Every new route has a rule in the route-table test.
    A Reader sees Alerts but can't create rules; a Contributor scoped to
    one Project can't read another Project's traces, rules or Alerts, or
    use its channels; platform channels and SMTP need an Owner; an agent
    uses its own roles.
12. **No side door.** From a pod in a workload namespace, the trace store,
    the collector's query side and the backend's alerting API don't
    answer; the ingest port accepts spans but returns nothing stored.
    Checked on the real cluster.
13. **Removal and turning traces off** need an Approval where they delete
    data; the console states what's lost; afterwards rules read inactive,
    open Alerts are resolved, channels remain, and the cluster holds none
    of the traces' workloads.
14. **Footprint.** edge-1 with the package and traces off: no regression
    against spec 023's run. With traces on: idle, load and disk recorded,
    and the stated cost matches. Findings go in `docs/risk-register.md`.
15. **Nothing else breaks.** Existing tests, the bench harness and a
    real-cluster run on `serendip-t018` pass.

## Open questions (for plan.md)

1. **Where rules are evaluated.** In the backend (OpenChoreo's
   `ObservabilityAlertRule` through the logs/metrics adapters, Prometheus
   rules, OpenObserve alerts) or in `serendipd` by querying the adapters
   on a schedule. Trade-offs: memory and CPU on the T470, how a firing
   reaches Serendip (a webhook into `serendipd`, or polling), whether the
   backend's own sender is used at all (RP-062), and keeping `core/`
   product-free. The leaning is: the backend evaluates, Serendip receives
   the firing and sends notifications itself, so only Serendip ever
   contacts a person-typed address.
2. **Which trace store and collector.** OpenChoreo's tracing module with
   OpenObserve (the same instance as logs, spec 023's reason for choosing
   it) or another. Its cost, its query path (an adapter like the logs
   one?), and how spans get the Project, Component and Environment IDs
   (spec 023 found an unknown scope field widens a query silently).
3. **Where the sender lives.** In `core/` (a small stdlib HTTP and SMTP
   client) or a Notification capability with a provider. Whether email
   is in this spec or webhook-only first.
4. **How a firing reaches `serendipd`** without opening an unauthenticated
   listener (spec 023 criterion 13): the API server's Service proxy, a
   token the backend presents, or polling.
5. **Alert retention** and the store's growth from Alerts and delivery
   records on SQLite.
6. **Traces as a package option** (spec IN 17) versus a second package,
   and how `Requirements` express an optional part.
7. **RP-066:** whether this spec creates `observabilityalertrules`
   objects, and so whether removal must delete them or say they remain.
