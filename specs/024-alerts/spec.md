# Spec 024: Alert rules and notification channels

Status: Reviewed, 2026-11-15; amended the same day with the plan.
- **Email stays in scope.**
- **Traces moved to spec 025** (beside Hubble's request metrics), with
  trace-based alert conditions.
- **cs-tools' alert services** are adopted as design, not code.

The follow-up spec 023 named (Scope OUT, plan "Explicitly NOT done"). It
builds on the observability package and gives Serendip its first
operational resource kind, the **Alert**, with the rules that raise alerts
and the channels that carry them to people. Epic #65.

## Problem

Spec 023 made a Component's stored logs and its CPU and memory history
readable. Nobody is told when something goes wrong: a Component can run out
of memory, restart in a loop or print errors for hours, and Serendip says
nothing until someone opens the Observability tab (whose alerts section
reads "Not available yet"). The observability package can't even warn
about its own disk filling before retention acts (RP-068).

`operations.md` makes alerting Serendip's own: "Serendip owns incident,
alerting, and SLO semantics natively". `resource-model.md` lists the
**Alert** as an operational resource, the input to the Incidents of spec
028. So what fires and to whom is Serendip's model. Where the evaluation
runs is a plan decision, as long as `core/` sees only the capability.

Two things this spec has to settle:

- **Destinations are a way out of the cluster, and into it.** A
  notification channel makes Serendip (or the backend) send a request to an
  address a person typed. Spec 023 had to turn off OpenObserve's SSRF guard
  for its own webhook (RP-062), so no destination may be used without
  Serendip validating it first.
- **Channels hold credentials.** A webhook URL with a token in it, or an
  SMTP password, is a secret, not a field in the store.

## Prior art: WSO2's cs-tools alert services

`wso2-open-operations/cs-tools` (Apache-2.0, Go) is WSO2's replacement for
a ServiceNow-backed stack. Two of its services solve this spec's problem
for a larger deployment. **Their designs are adopted, but not their
code.** The code is built on PostgreSQL, Kafka and Redis, which
constitution 4 rules out, and each library would need an ADR.

- **`sre-alert-ingestion-service`** normalises webhooks from ten monitoring
  sources to one alert shape. Its OpenObserve and Prometheus mappings are
  the reference if a backend's firing ever has to reach `serendipd`.
- **`sre-alert-core-service`** does four things this spec follows:
  - it folds alerts into one open record by a fingerprint
    (`source|service|metric|environment|identifier`) inside a window;
  - it turns a resolving alert into a resolution, not a new record;
  - it delivers from a separate loop, with retries and backoff.

  The folding is the model for IN 4 (one open Alert per rule and
  Component, not one per evaluation). The delivery loop is the model for
  IN 11 (honest delivery).
- **Also borrowed:**
  - idempotent writes, so a replayed firing is a no-op;
  - refusing new work under load instead of queueing without bound;
  - collapsing repeats into one notification, since chat webhooks allow
    about one message a second.

Its incident grouping is spec 028's. Its entity-service is a candidate
external Ticketing provider for that spec (`docs/build-vs-adopt.md`).

## Scope — IN

### Alert rules

1. **An `AlertRule` resource,** owned by a Component (or by an
   Application, applying to each of its Components), in an Environment.
   Its condition uses data the package already has:
   - **metric threshold:** CPU or memory, held for a duration, as one of:
     - an absolute value;
     - a share of the Component's request;
     - a share of its limit;
   - **restarts:** container restarts above a count within a window;
   - **log match:** lines containing a text, above a count within a window.

   Each rule also has:
   - a severity (`critical`, `warning` or `info`);
   - a summary, which its alerts carry;
   - the channels it notifies.

   A condition that can't be evaluated is refused at create, with the
   reason.
2. **Built-in rules for the package itself:** stored-data disk use near
   the package's budget (RP-068). They exist while the package is
   installed, can be silenced, and notify the platform's default channels.
3. **Declarative and reconciled** (constitution 3).
   - A rule's status says whether it is being evaluated, and if not, why:
     the package isn't ready, the Component is gone, or the condition was
     refused.
   - A resync with nothing changed costs near zero.
   - Evaluation's own cost is measured.

### Alerts

4. **An `Alert` resource** (`resource-model.md`'s operational kind): one
   firing of one rule for one Component in one Environment. It records:
   - when it started and resolved;
   - its severity and its rule;
   - the value that crossed the threshold;
   - which channels were notified, with what result.

   A rule that keeps firing updates one open Alert; it doesn't create one
   per evaluation.
5. **Lifecycle:** `firing` → `resolved` when the condition clears.
   - A person can **acknowledge** an Alert, which records who and when.
   - A person can **silence** a rule for a time.
   - Alerts are kept for a stated retention.

   Incidents, correlation and diagnosis are specs 028 and 029.
6. **Listing:** the Alerts for a Component, an Application, an
   Environment, or everything the caller can see.
   - They can be filtered by state and severity, newest first.
   - The console shows a firing count where it's useful: the Component's
     tab, the Application, the top bar.

### Notification channels

7. **A `NotificationChannel` resource,** at the platform or a Project.
   Every type is free to use, sends outbound only, and needs no public URL
   on the laptop (plan "Channel research"). The types:
   - **webhook:** an HTTPS POST to a URL, in one of these formats:
     - Serendip's documented JSON body, with an optional shared-secret
       signature header;
     - Slack-compatible (Slack, Mattermost, Rocket.Chat, Google Chat);
     - Discord;
     - Microsoft Teams Workflows;
   - **email** through an SMTP server the Owner configures (host, port,
     sender, credentials);
   - **ntfy** push to a phone, through `ntfy.sh` or a self-hosted server,
     with critical alerts at ntfy's urgent priority;
   - **Telegram,** a bot's message to a chat.

   A rule names its channels; a Project can set default channels for
   rules that name none.
8. **Credentials are secrets** (spec 009).
   - A webhook URL's token, a signing secret and SMTP credentials live in
     the Secrets capability.
   - They never appear in the store, the API's responses, audit diffs or
     logs.
   - A channel shows whether its secret is set, not its value.
9. **Destinations are validated by Serendip** before anything is sent
   (RP-062):
   - HTTPS only for webhooks, unless an Owner allows plain HTTP for one
     channel, with the reason recorded.
   - The host is resolved, and refused if any of its addresses is:
     - loopback or link-local;
     - private;
     - in the cluster's pod or Service ranges;
     - one of the node's own addresses.

     An Owner can allow one channel explicitly, for an in-house receiver.
   - The check runs again at send time, so a name that later resolves
     somewhere refused is still refused.
   - Redirects aren't followed.
10. **Test send.** A person who can edit a channel can send a test
    notification and see the result: delivered, or the error.
11. **Delivery is honest.**
    - Each notification's result is recorded on the Alert.
    - A failed send is retried a few times with backoff, then reported as
      failed, not dropped.
    - Both the firing and its resolution are sent.

### Removing the package

12. **Removal states what's lost** (spec 023's removal, extended):
    - rules stop being evaluated and show as inactive;
    - open Alerts are resolved as "package removed";
    - channels stay.

    If this spec creates the backend's alert-rule objects, the plan
    settles RP-066 (their CRD outliving removal).

### Across all features

13. **Authorization** (spec 017).
    - New actions for alert rules, for Alerts (read, acknowledge,
      silence) and for channels (read, write, test), scoped like
      everything else.
    - A rule's channels must be ones its author can use.
    - Channel and SMTP settings at `/` stay with Owners.
    - Every new route has a rule.
14. **Deletes go through an Approval** (spec 019), like every other delete.
15. **No way around Serendip** (spec 023's criterion 13). The backend's
    alerting API doesn't answer people or workloads directly, and this
    spec opens no new path from workloads.
16. **Audit** (constitution 8).
    - Every create, update and delete of rules and channels, every
      acknowledgement, silence and test send, with actor, interface and
      decision.
    - The notifications sent are recorded on the Alert.
17. **Interface parity** (constitution 7): every console view is an API
    call shown in "Same request through the API", a CLI command and an
    MCP tool, and `api/catalog` covers them.
18. **Assistant use.**
    - MCP tools read Alerts as capped, summarised results (diagnose
      freely, constitution 5).
    - An agent may acknowledge an Alert if its roles allow it.
    - Creating rules and channels follows the same roles as for a person.

## Scope — OUT (explicitly)

- **Traces, and trace-based alert conditions** (errors, latency
  percentiles). They move to spec 025 with Hubble's request metrics and
  the traffic overlay. The findings from this spec's research carry over
  (plan.md "Carried to spec 025").
- **Incidents, alert correlation and grouping, AI diagnosis** (specs 028,
  029).
- **SLOs, SLIs, error budgets and burn-rate alerts.** `operations.md`
  makes them Serendip's own; they need request metrics (spec 025) and a
  spec of their own.
- **On-call schedules, escalation and paging** (`operations.md`:
  integrate later).
- **Paid or account-heavy channels:**
  - SMS (Twilio, an Android phone as a gateway, Sri Lankan gateways);
  - voice calls (Twilio Voice, ntfy Pro's calls);
  - the WhatsApp Cloud API (paid templates, Meta Business verification);
  - Pushover.

  They can be added later as opt-in channel types. Unofficial WhatsApp
  clients are rejected outright (they break WhatsApp's terms, and accounts
  get banned).
- **OTP codes for sign-in or approvals.** That is identity's job
  (ThunderID), not alerting's: the plan records what was found for a later
  spec.
- **Notifying people about pending Approvals.** Channels are built so that
  can use them later (spec 019 left notifications out).
- **Alerts on Serendip's own health** (`serendipd`, the agent, nodes),
  beyond the package's built-in disk rule.
- **Custom query languages** (PromQL, SQL) in rules.
- **Bring-your-own alerting** (an existing Alertmanager). The `external`
  escape hatch stays planned.
- **Turning observability on by default in edge-1.**

## Affected resources and capabilities

- **The Observability capability** (`core/`): evaluating alert
  conditions. No product names in `core/` (constitution 1).
- **New resource kinds:** `AlertRule`, `Alert` (operational) and
  `NotificationChannel`, with status conditions and graph edges:
  - rule → Component or Application;
  - rule → channel;
  - alert → rule.
- **A notification sender:** delivers webhooks, email, ntfy and Telegram,
  with destination validation. Whether it lives in `core/` or as a provider of
  a Notification capability is a plan decision.
- **The observability provider:** the condition queries, and the
  built-in rule.
- **Secrets (spec 009):** channel credentials.
- **Authorization, approvals, audit:** new actions, routes and deletes.
- **The console:** alert rules and alerts, channels, firing counts.
- **CLI and MCP:** new commands and tools.
- **Benchmarks:** the cost of rule evaluation on the simulated T470.
- **Docs:**
  - `operations.md`, `resource-model.md`, `capability-model.md`,
    `application-platform.md`, `authorization.md`;
  - ADRs as needed;
  - `build-vs-adopt.md`;
  - the risk register (RP-062, RP-066, RP-068).

## Acceptance criteria

1. **A metric rule fires and resolves.** A memory rule on a Component:
   - fires an Alert when a load crosses it, and the channel receives the
     firing;
   - resolves when the load stops, and the Alert and the channel both
     receive the resolution;
   - updates one Alert over repeated evaluations.
2. **Restart and log rules** each fire on a Component made to crash-loop
   or to print a known error line.
3. **Built-in disk rule.** With the threshold lowered for the test, the
   package's disk rule fires and notifies the platform's default channel.
4. **Delivery on every channel type.**
   - A webhook receiver gets the documented body with a valid signature.
   - Each webhook format is accepted by a real Slack, Discord and Teams
     channel (or a recorded capture of their documented schema).
   - An SMTP test server gets the email.
   - An ntfy topic and a Telegram chat each get the firing and the
     resolution, with a critical alert at ntfy's urgent priority.
   - A 429 from a service is honoured (its `Retry-After`).
   - A failing receiver shows retries and then "failed" on the Alert.
5. **Destination validation.** Creating, updating or test-sending a
   channel to any of these is refused with a plain reason:
   - `127.0.0.1`;
   - a private address;
   - the cluster's Service range;
   - a name that resolves to one of those;
   - a plain-HTTP URL;
   - a redirect.

   With an Owner's explicit allowance, an in-cluster receiver works.
   Checked on `serendip-t018`, not only in unit tests.
6. **Secrets stay secret.** No channel token, signing secret or SMTP
   password appears in any of these (checked by searching for a known
   value):
   - API responses;
   - the store's rows;
   - audit diffs;
   - events;
   - Alerts;
   - `serendipd`'s logs.
7. **Reconciled.**
   - A rule's status says whether it's being evaluated, and why not.
   - A resync with nothing changed adds near-zero CPU (measured).
   - The cost of evaluation at the rule cap is measured.
8. **Acknowledge and silence.**
   - An acknowledged Alert shows who acknowledged it and when.
   - A silenced rule sends nothing until the silence ends, and its Alerts
     are still recorded.
9. **Authorization.** Every new route has a rule in the route-table test,
   and:
   - a Reader sees Alerts but can't create rules;
   - a Contributor scoped to one Project can't read another Project's
     rules or Alerts, or use its channels;
   - platform channels and SMTP need an Owner;
   - an agent uses its own roles.
10. **No side door.** From a pod in a workload namespace, nothing new
    answers. Checked on the real cluster.
11. **Removal** needs an Approval (spec 023) and the console states what's
    lost. Afterwards:
    - rules read inactive;
    - open Alerts are resolved;
    - channels remain.
12. **Footprint.** edge-1 with the package installed and the rule cap
    configured: the idle and evaluation cost is recorded, with no
    regression against spec 023's run otherwise. Findings go in
    `docs/risk-register.md`.
13. **Nothing else breaks.** Existing tests, the bench harness and a
    real-cluster run on `serendip-t018` pass.

## Open questions (answered in plan.md)

1. **Where rules are evaluated:**
   - in the backend: OpenChoreo's `ObservabilityAlertRule`, Prometheus
     rules or OpenObserve alerts;
   - or in `serendipd`, by querying the adapters on a schedule.
2. **Where the sender lives:** in `core/` (a small stdlib HTTP and SMTP
   client), or a Notification capability with a provider.
3. **How a firing reaches `serendipd`** without an unauthenticated
   listener, if the backend evaluates.
4. **Alert retention,** and how much the store grows from Alerts and
   delivery records on SQLite.
5. **RP-066:** whether this spec creates `observabilityalertrules`
   objects.
6. **Which channel types v1 ships:** researched; the answer is in IN 7.
