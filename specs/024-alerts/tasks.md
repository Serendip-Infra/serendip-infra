# Tasks: Spec 024 — Alert rules and notification channels

Ordered. `[P]` = parallel-safe within its block, `[A]` = subagent-eligible
(tests or docs). Run `contract-guardian` before committing anything under
`core/` or `providers/`, and `go-reviewer` after each non-trivial Go task.

Dependency sketch:
- T001 → T002 → (T003 ∥ T004) → T005 → T006 → T007 → (T008 ∥ T009) →
  T010 → T011 → T012 → T013.
- T002's core types and T004's sender don't need the cluster, so they can
  start while T001's spike runs.

**Memory:** spike and benchmark one thing at a time on `serendip-t018` and
`bench-t470`, and run the keep-awake beside long runs.

## T001 — Spike: the condition queries on serendip-t018

- **Do,** with the observability package installed:
  1. **Metric condition:** the metrics adapter's points over a `for`
     window, for one Deployment, with all three UIDs.
  2. **Log condition:** a `logMatch` search with `limit = above + 1`.
  3. **Restarts:** kube-state-metrics' restart counter for one
     Deployment's pods, through the Service proxy to Prometheus. Find
     out:
     - whether the pods can be matched by label, or only by name from
       the Deployment's ReplicaSets;
     - whether a crash-looping pod's count rises within a minute.
  4. **Each query's latency** at the current store size.
  5. **Real services:** send one message each to a scratch Slack
     channel, Discord webhook, Teams Workflow, `ntfy.sh` topic and
     Telegram bot. Record:
     - the accepted body;
     - the answer to a malformed one;
     - each service's rate-limit headers.

     Delete the test channels afterwards.
- **Files:** `plan.md` ("Spike findings", amending decisions where the
  facts differ), and recorded answers in
  `providers/observability/openchoreo/testdata/` and
  `core/notify/testdata/`.
- **Verify:**
  - every item has a finding in `plan.md`, with the command or output it
    came from;
  - the recorded answers parse in a scratch test;
  - no scratch pods, webhooks or topics are left.

## T002 — Core types: rules, Alerts, channels, SMTP servers [P]

- **Do:**
  - the four kinds in `core/resource/alerting.go` (plan decisions 1, 3
    and 6), with validation, the caps (200 rules, 1–60 minutes,
    `above` ≤ 999, 20 recipients) and graph edges;
  - remove `ops.go`'s placeholder `AlertSpec`;
  - store wiring: the kinds and `resourcePaths` (not `Alert`), and deletes
    refused with 409 while something uses them.
- **Files:** `core/resource/alerting.go`, `alerting_test.go`, `ops.go`,
  and the store and kind registration.
- **Verify:**
  - `go test ./core/...` passes, with table tests for each condition's
    and channel type's validation, the caps, the edges and the 409;
  - `bash .claude/scripts/check-imports.sh` is clean.

## T003 — Authorization: actions, roles and routes [P]

- **Do:**
  - the actions of plan decision 9, with `allowPrivateDestination` in
    Contributor's `NotActions`;
  - the rule that writing a rule needs read on each channel it names;
  - a route-table row for every route of decision 12;
  - deletes of the three kinds through Approvals (decision 10).
- **Files:** `core/policy/builtins.go`, `api/authz*.go`, the route-table
  test, and `core/reconcile/approval.go` (targets).
- **Verify:**
  - `go test ./core/policy/... ./api/...` passes;
  - the route-table test has a row for each new route;
  - table tests show a Contributor denied `allowPrivateDestination`, a
    Contributor in Project A denied a rule naming Project B's channel,
    and a Reader denied writing a rule.

## T004 — The sender: address check and channel types [P]

- **Do:** `core/notify` (plan decisions 5 and 6), standard library only:
  - the checked dialer, with every range in decision 5, IPv6 and
    IPv4-mapped;
  - no redirects, no proxy, TLS 1.2 or later;
  - the four webhook formats, SMTP, ntfy and Telegram, with escaping and
    no mentions;
  - the signed `serendip.alert.v1` body;
  - `Retry-After`;
  - errors cleaned down to the host.
- **Files:** `core/notify/*.go` and tests, and `testdata/` from T001.
- **Verify:** `go test ./core/notify/...` passes, against `httptest` and a
  stdlib fake SMTP server. The tests cover:
  - each refused range, and a name that resolves to one;
  - a DNS answer that changes between the check and the dial (the
    `Control` hook refuses it);
  - a 302;
  - `HTTPS_PROXY` set;
  - the allowance skipping private ranges only;
  - the signature, checked by a reference implementation in the test;
  - each format's body matching T001's recording;
  - `@everyone` and `<!channel>` neutralised;
  - SMTP refusing plain auth;
  - no token or URL path in any returned error.

## T005 — The evaluator and delivery loop

- **Do:** `core/reconcile/alerts.go` and `delivery.go` (plan decisions 2,
  7 and 11):
  - due rules on each tick, 2 queries at a time, with timeouts;
  - folding, with the 5-minute reopen window;
  - `lastValue` held in memory and flushed once a minute;
  - errors setting `Evaluating=False` without changing Alerts;
  - resuming after a restart;
  - deliveries with backoff, the per-channel rate and digests, and
    silences;
  - retention: 30 days, 500 rows a pass, one audit row;
  - `ObservabilityProvider.EvaluateCondition` in the interface;
  - wiring into `cmd/serendipd`.
- **Files:** `core/reconcile/alerts.go`, `delivery.go`,
  `observability.go`, their tests, and `cmd/serendipd/*.go`.
- **Verify:** `go test ./core/reconcile/...` passes, with a fake provider
  and a fake clock. The tests cover:
  - fire, update and resolve, then a reopen inside the window and a new
    Alert after it;
  - an error leaving a firing Alert firing;
  - a restart resuming a pending delivery without re-sending a sent one;
  - retries stopping at 5, then `failed`;
  - a 400 not retried;
  - 10 queued for one channel becoming one digest;
  - a silence recording `silenced`;
  - 200 rules at 1 minute making at most 2 concurrent queries, with no
    store write per evaluation (counted).

## T006 — The provider: conditions and the built-in disk rule

- **Do:**
  - `conditions.go` (plan decision 3): the fixed queries over the metrics
    and logs adapters and Prometheus, built from validated IDs;
  - the Prometheus query client;
  - the built-in disk rule's lifecycle (decision 4) in
    `providerinstance.go`;
  - pin the queries' API versions in `VERSIONS.md`.
- **Files:** `providers/observability/openchoreo/conditions.go`,
  `prometheus.go`, tests, `VERSIONS.md`, and
  `core/reconcile/providerinstance.go`.
- **Verify:** `go test ./providers/observability/... ./core/reconcile/...`
  passes, using T001's recorded answers. The tests check:
  - each condition's request: the scope carries all three UIDs, and the
    PromQL holds only IDs;
  - the built-in rule is created on Ready, can't be deleted (409), and
    goes with the package.

## T007 — The API

- **Do:**
  - the routes of plan decision 12;
  - secrets shown as `secretSet`, and platform channels' settings hidden
    from readers below `/`;
  - test send;
  - `api/catalog` entries;
  - an audit row for every mutation, acknowledgement, silence and test
    send.
- **Files:** `api/alerts_handler.go`, `api/notifications_handler.go`,
  `api/catalog/*.go`, `api/server.go`, and tests.
- **Verify:** `go test ./api/...` passes, including the catalogue's
  route-parity test. Handler tests cover:
  - a channel response holding no secret value;
  - a private URL refused, at create and at test, with the reason;
  - an Owner's allowance accepted;
  - Alerts scoped to the caller's Projects;
  - an acknowledgement audited with its actor and interface.

## T008 — CLI and MCP [P] [A]

- **Do:** the commands and tools of plan decision 13, built from the
  catalogue, with the MCP results capped.
- **Files:** `cmd/serendip/*.go`, `mcp/*.go`, and tests.
- **Verify:** `go test ./cmd/serendip/... ./mcp/...` passes. It covers
  each command's request and usage errors, the MCP caps, and a test that
  a channel's token never appears in CLI or MCP output.

## T009 — The console [P]

- **Do:** plan decision 14: alerts in the Observability tab, the
  Application view, the bell and the Alerts page, and Settings ›
  Notifications, with each channel type's form and the ntfy topic
  warning (RP-069). Each view gets its "Same request through the API".
- **Files:** `ui/src/screens/Observability.tsx`, `Alerts.tsx`,
  `Notifications.tsx`, the top bar, and tests.
- **Verify:**
  - `npm test` and `npm run build` in `ui/` pass;
  - tests cover the bell's counts, the allowance controls shown to
    Owners only, and a secret field that never shows its value;
  - the gzipped build's growth is stated.

## T010 — Live check on serendip-t018

- **Do:** spec acceptance criteria 1–11 on the real cluster, through the
  console (headless Chrome), the CLI and MCP:
  - metric, restart and log rules firing and resolving;
  - the disk rule, with its threshold lowered;
  - every channel type delivering:
    - a webhook to an in-cluster receiver (with the allowance) and to an
      outside one;
    - real Slack, Discord, Teams, ntfy and Telegram targets;
    - email to a test SMTP server;
  - a failing receiver, and a 429;
  - each refused destination;
  - acknowledge and silence;
  - the side door, from a workload pod;
  - package removal.

  Then the secrets check (criterion 6): search for each known token in
  the store, audit rows, events, Alerts, `serendipd`'s log, and the CLI
  and MCP output.
- **Files:** `specs/024-alerts/verification.md`. The tooling is kept
  outside the repo, as spec 023's was.
- **Verify:**
  - `verification.md` has evidence for each of criteria 1–11;
  - each known secret is found only in the Secrets provider;
  - findings are fixed, or carried as sub-tasks.

## T011 — Benchmarks and measured cost

- **Do,** on the simulated T470 (`bench-t470`), with the harness
  extended:
  - a rule-evaluation phase: 200 rules at 1 minute, beside spec 023's
    log load;
  - evaluation latency, and `serendipd`'s CPU and memory;
  - store writes a minute;
  - resync CPU;
  - edge-1 with the package and no rules, against spec 023's run.
- **Files:** `profiles/edge-1/bench/`, `cmd/bench-edge1/`, and
  `profiles/edge-1/bench/results/2026-11-*-spec024-simulated-t470/`.
- **Verify:**
  - the results README has each number;
  - `serendipd` stays within constitution 4's control-plane budget;
  - there's no regression without rules (criterion 12);
  - the harness's own tests pass.

## T012 — Reviews

- **Do:** fix the findings (fix, don't defer) of:
  - `go-reviewer` over the Go changes;
  - `contract-guardian` over `core/` and `providers/`, including the
    `core/notify` reading of constitution 1 (plan decision 5);
  - `security-review` over the branch, with the sender, the address
    check, escaping and secrets called out.
- **Files:** as the findings require.
- **Verify:**
  - each review's findings are fixed, or recorded as by design with the
    reason;
  - `go test ./...`, `go vet ./...` and the UI tests pass.

## T013 — Docs and close-out [A]

- **Do:**
  - `operations.md`: rules, Alerts, channels, the bodies and the
    signature;
  - `resource-model.md`, `capability-model.md`,
    `application-platform.md` ("Alerts (spec 024)"), `authorization.md`
    and `approvals.md`;
  - ADR 0022;
  - `build-vs-adopt.md`:
    - adopted: the four webhook formats, ntfy and Telegram, as protocols;
    - rejected: OpenChoreo's alerting path, Alertmanager, unofficial
      WhatsApp clients and email-to-SMS;
    - open: SMS, voice, WhatsApp Cloud and OTP;
  - the risk register: RP-069 to RP-073 added, and RP-062, RP-066 and
    RP-068 updated;
  - `docs-keeper`, then `docs/PROGRESS.md`.
- **Files:** as listed.
- **Verify:**
  - `docs-keeper` reports no drift;
  - every RP the plan names is indexed in the risk register;
  - `PROGRESS.md` names spec 024 done.
