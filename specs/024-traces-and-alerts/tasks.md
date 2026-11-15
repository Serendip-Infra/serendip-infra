# Tasks: Spec 024 — Traces, alert rules and notification channels

Ordered. `[P]` = parallel-safe within its block, `[A]` = subagent-eligible
(tests or docs). Run `contract-guardian` before committing anything under
`core/` or `providers/`, and `go-reviewer` after each non-trivial Go task.

Dependency sketch:
- T001 → T002 → (T003 ∥ T004) → T005 → T006 → T007 → T008 →
  (T009 ∥ T010) → T011 → T012 → T013 → T014 → T015.
- T002's core types and T004's sender don't need the cluster, so they can
  start while T001's spike runs.

**Memory:** spike and benchmark one thing at a time on `serendip-t018` and
`bench-t470`, and run the keep-awake beside long runs.

## T001 — Spike: tracing module and the alert queries on serendip-t018

- **Do,** with the observability package installed, removing scratch
  objects afterwards:
  1. **Pin the tracing module.** `helm show chart` and `helm show values`
     for `observability-tracing-openobserve`: the newest release that
     supports plane 1.3.1. Record it in `VERSIONS.md`.
  2. **Install it by hand on the existing OpenObserve,** with its bundled
     store off and tail sampling off. Check:
     - whether `observer.tracingAdapter.enabled` is needed when the
       Observer isn't queried;
     - whether the logs module's `openobserve-admin-credentials` serves
       it, or what Secret it needs without ESO;
     - the collector's ports, and its idle memory.
  3. **Spans and identity.** Deploy two OTLP-instrumented sample
     Components, one calling the other. Then:
     - check the spans' resource attributes carry the three
       `openchoreo.dev/*-uid` labels;
     - from one workload, send spans claiming the other Component's UIDs,
       and see whether `k8sattributes` overrides them (decision 2;
       RP-070 or a config fix).
  4. **Trace queries** through the Service proxy (`traces/query`,
     `spans/query`):
     - scope by names, then a wrong name, a missing field and another
       Environment's names. Does any of them widen?
     - the answer's shape against the OpenAPI at the pinned version.
  5. **Condition queries:**
     - a `logMatch` search with `limit = above + 1`;
     - the metrics adapter's points for a `for` window;
     - kube-state-metrics' restart counter for one Deployment's pods: can
       the pods be matched by label, or only by name?
     - each query's latency at the current store size.
  6. **Side door:** from a workload pod, 4317 and 4318 answer; the
     collector's other ports, the tracing adapter and OpenObserve's trace
     API don't, before and after a draft policy.
  7. **Removing the module** while traces exist: the stored traces stay,
     and reappear on re-install.
- **Files:** `plan.md` ("Spike findings", amending decisions where the
  facts differ), `providers/observability/openchoreo/VERSIONS.md`.
- **Verify:** every item above has a finding in `plan.md` with the
  command or output it came from; `kubectl get all -A` shows no scratch
  objects left.

## T002 — Core types: rules, Alerts, channels, SMTP servers [P]

- **Do:**
  - `AlertRule`, `Alert`, `NotificationChannel` and `SMTPServer` in
    `core/resource/alerting.go`, with validation (plan decisions 5 and
    7), the caps (200 rules, 1–60 min, `above` ≤ 999) and graph edges.
    Replace `ops.go`'s placeholder `AlertSpec`.
  - `ProviderInstance.spec.options` (`traces`).
  - `provider.Requirements.Options`.
  - Store wiring: kinds, `resourcePaths` (not `Alert`), references refused
    on delete while in use (409).
- **Files:** `core/resource/alerting.go`, `alerting_test.go`, `ops.go`,
  `capability.go`; `core/provider/requirements.go`; store/kind
  registration.
- **Verify:** `go test ./core/...` passes, with table tests for each
  condition type's validation, the caps, the edges and the 409; and
  `bash .claude/scripts/check-imports.sh` is clean.

## T003 — Authorization: actions, roles and routes [P]

- **Do:** the actions of plan decision 12. Traces in the `NotActions` of
  Reader, User Access Administrator and RBAC Administrator;
  `allowPrivateDestination` in Contributor's. The rule that a rule's
  write reads each named channel. Route-table rows for every route of
  decision 15. Deletes of the three kinds through Approvals (decision 13).
- **Files:** `core/policy/builtins.go`, `api/authz*.go`, route-table test,
  `core/reconcile/approval.go` (targets).
- **Verify:** `go test ./core/policy/... ./api/...` passes. The route-table
  test has a row for each new route. Table tests show a Reader denied
  traces, a Contributor denied `allowPrivateDestination`, and a
  Contributor in Project A denied a rule naming Project B's channel.

## T004 — The sender: address check, webhook, SMTP [P]

- **Do:** `core/notify` (plan decision 9), standard library only:
  - the checked dialer: every range in decision 9, IPv6 and IPv4-mapped;
  - no redirects, no proxy, TLS 1.2+;
  - the signed `serendip.alert.v1` body;
  - SMTP with STARTTLS or implicit TLS, auth only after TLS;
  - error messages cleaned to the host.
- **Files:** `core/notify/*.go` and tests.
- **Verify:** `go test ./core/notify/...` passes, against `httptest` and a
  stdlib fake SMTP server. It covers:
  - each refused range, and a name that resolves to one;
  - a DNS answer that changes between check and dial (the `Control` hook
    refuses);
  - a 302;
  - `HTTPS_PROXY` set;
  - the allowance skipping private ranges only;
  - the signature verified by a reference implementation in the test;
  - SMTP refusing plain auth;
  - no URL path or query in any returned error.

## T005 — The evaluator and delivery loop

- **Do:** `core/reconcile/alerts.go` and `delivery.go`:
  - due rules each tick, 2 at a time, with timeouts;
  - folding by fingerprint with the 5-minute reopen window;
  - in-memory `lastValue`, flushed once a minute;
  - errors setting `Evaluating=False` without changing Alerts;
  - resume after a restart;
  - deliveries with backoff, the per-channel limit and digests, silences;
  - retention (30 days, 500 a pass, one audit row);
  - `ObservabilityProvider.EvaluateCondition` in the interface.
  - Wire into `cmd/serendipd`.
- **Files:** `core/reconcile/alerts.go`, `delivery.go`,
  `observability.go`, tests; `cmd/serendipd/*.go`.
- **Verify:** `go test ./core/reconcile/...` passes, with a fake provider
  and a fake clock:
  - fire, update, resolve, then reopen within the window and a new Alert
    after it;
  - an error leaves a firing Alert firing;
  - restart resumes a pending delivery without re-sending a sent one;
  - retries stop at 5 and then `failed`;
  - a 400 isn't retried;
  - 10 queued for one channel become one digest;
  - a silence records `silenced`;
  - 200 rules at 1 minute make at most 2 concurrent queries and no store
    write per evaluation (counted).

## T006 — The provider: conditions and the built-in disk rule

- **Do:** `conditions.go` (plan decision 7: the fixed queries over the
  metrics, logs and tracing adapters and Prometheus, from validated IDs).
  The built-in disk rule's lifecycle (decision 8) in
  `providerinstance.go`.
- **Files:** `providers/observability/openchoreo/conditions.go`, tests;
  `core/reconcile/providerinstance.go`.
- **Verify:** `go test ./providers/observability/... ./core/reconcile/...`
  passes, with recorded adapter answers from T001. Each condition's
  request is checked (scope always carries all three UIDs, and the PromQL
  holds only IDs). The built-in rule is created on Ready, can't be
  deleted (409), and goes with the package.

## T007 — The provider: traces

- **Do:**
  - `tracing.go`: the module's install and removal as a package-job step
    when `options.traces` changes, the values (bundled store off, tail
    sampling off, collector memory limit), and the option in the applied
    fingerprint;
  - `traces.go`: the adapter client and the UID check against span
    attributes;
  - the ingest-only NetworkPolicy in `policies.go`;
  - `ObservabilityProvider.SearchTraces` and `TraceSpans`.
- **Files:** `providers/observability/openchoreo/tracing.go`, `traces.go`,
  `policies.go`, `installer.go`, tests; `core/reconcile/observability.go`.
- **Verify:** `go test ./providers/observability/...` passes. Tests cover:
  - a trace with no span from the target dropped;
  - a span from another Component hidden;
  - `minDurationMs` and `errorsOnly` applied, with `truncated` set;
  - toggling the option runs the job once, and a resync without a change
    runs nothing.

## T008 — The API

- **Do:** the routes of plan decision 15:
  - `api/traces_handler.go`: hidden spans decided per span with the
    caller's roles, and one trace refused with 404 unless it belongs to
    the Deployment;
  - `api/alerts_handler.go` and `api/notifications_handler.go`: secrets
    shown as `secretSet`, platform channels' URLs hidden from non-`/`
    readers, test send;
  - `api/catalog` entries; audit for every mutation, acknowledgement,
    silence and test send.
- **Files:** `api/*_handler.go`, `api/catalog/*.go`, `api/server.go`,
  tests.
- **Verify:** `go test ./api/...` passes, including the catalog's route
  parity test. Handler tests cover:
  - a trace ID from another Deployment answers 404;
  - one hidden span among visible ones;
  - a channel response with no secret value;
  - a private URL refused at create and at test with the reason;
  - an Owner's allowance accepted;
  - an acknowledgement audited with actor and interface.

## T009 — CLI and MCP [P] [A]

- **Do:** the commands and tools of plan decision 16, from the catalogue.
  MCP results capped (200 spans, 2 KiB of attributes a span).
- **Files:** `cmd/serendip/*.go`, `mcp/*.go`, tests.
- **Verify:** `go test ./cmd/serendip/... ./mcp/...` passes, covering each
  command's request and usage errors and the MCP caps, with a test that a
  token in a channel never appears in CLI or MCP output.

## T010 — The console [P]

- **Do:** plan decision 17: traces and alerts in the Observability tab,
  the Application view, the bell and the Alerts page, Settings ›
  Notifications, and the Packages traces switch. `TraceTimeline` follows
  the dataviz skill. "Same request through the API" on each view.
- **Files:** `ui/src/screens/Observability.tsx`, `Alerts.tsx`,
  `Notifications.tsx`, `components/TraceTimeline.tsx`, the top bar,
  Packages, tests.
- **Verify:** `npm test` and `npm run build` in `ui/` pass, with tests for
  the hidden-span bar, the bell's counts and the private-allowance control
  shown to Owners only. The timeline passes `check-contrast.mjs`, and it
  renders in headless Edge in both themes at 360 and 560 px. The
  gzipped build's growth is stated.

## T011 — Live check on serendip-t018

- **Do:** spec acceptance criteria 2–13 on the real cluster, through the
  console (headless Chrome), CLI and MCP:
  - the two-Component trace, including a caller who reads only one;
  - metric, restart and log rules firing and resolving;
  - the disk rule with a lowered threshold;
  - webhook delivery to an in-cluster receiver (with the allowance) and
    an outside one;
  - email to a test SMTP server;
  - a failing receiver;
  - each refused destination;
  - acknowledge and silence;
  - the side door from a workload pod;
  - traces off and on;
  - package removal.
- **Files:** `specs/024-traces-and-alerts/verification.md`; tooling kept
  outside the repo, as spec 023's was.
- **Verify:** `verification.md` has evidence for each of criteria 2–13,
  and findings are fixed or carried as sub-tasks.

## T012 — Secrets check [P]

- **Do:** spec AC 8. After T011's run, search for the known webhook token,
  signing secret and SMTP password in the SQLite store, audit rows,
  events, Alerts, `serendipd`'s log, CLI and MCP output.
- **Files:** `verification.md` (criterion 8).
- **Verify:** the search finds each value only in the Secrets capability's
  provider, with the commands recorded.

## T013 — Benchmarks and measured cost

- **Do,** on the simulated T470 (`bench-t470`), with the harness extended:
  - a trace load phase (the two instrumented Components at a steady
    request rate);
  - a rule-evaluation phase (200 rules at 1 minute);
  - idle, peak and disk-a-day with traces on;
  - evaluation latency at 3 days of data;
  - resync CPU;
  - edge-1 with the package and traces off, against spec 023's run.

  Then set `Requirements.Options["traces"]` from the run.
- **Files:** `profiles/edge-1/bench/`, `cmd/bench-edge1/`,
  `profiles/edge-1/bench/results/2026-11-*-spec024-simulated-t470/`,
  `providers/observability/openchoreo/requirements.go`.
- **Verify:** the results README has each number. `GET /v1/packages`
  shows the traces option's cost matching it (criterion 1). There is no
  regression with traces off (criterion 14). The harness's own tests
  pass.

## T014 — Reviews

- **Do:** `go-reviewer` over the Go changes, `contract-guardian` over
  `core/` and `providers/`, and `security-review` over the branch, with
  the sender, the address check and the trace scoping called out. Fix
  findings (fix, don't defer).
- **Files:** as findings require.
- **Verify:** each review's findings are fixed or recorded as by design,
  with the reason; `go test ./...`, `go vet ./...` and the UI tests pass.

## T015 — Docs and close-out [A]

- **Do:**
  - `operations.md` (rules, Alerts, channels, the webhook body and
    signature);
  - `resource-model.md`, `capability-model.md`, `application-platform.md`
    ("Traces and alerts (spec 024)"), `authorization.md`, `approvals.md`;
  - ADR 0022;
  - `build-vs-adopt.md` (the tracing module adopted; OpenChoreo's
    alerting path, Alertmanager and tail sampling rejected);
  - the risk register (RP-069 to RP-073, RP-062, RP-066 and RP-068
    updated);
  - `docs-keeper`, then `docs/PROGRESS.md`.
- **Files:** as listed.
- **Verify:** `docs-keeper` reports no drift; every RP the plan names is
  indexed in the risk register; `PROGRESS.md` names spec 024 done.
