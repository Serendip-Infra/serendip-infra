# Tasks: Spec 023 — Observability, the observability-plane package

Ordered. `[P]` = parallel-safe within its block, `[A]` = subagent-eligible
(tests or docs). Run `contract-guardian` before committing anything under
`core/` or `providers/`, and `go-reviewer` after each non-trivial Go task.

Dependency sketch:
- T001 → T002 → T003 → (user decision if SigNoz wins) → T004 → T005 →
  T006 → T007 → T008 → (T009 ∥ T010 ∥ T011) → T012 → T013 → T014 → T015.
- T004's core types can start alongside T002 if T001 confirms the
  Observer path.

**Memory:** the machine was short on memory on 2026-11-11 (a background
`serendipd` was reaped). Spike and benchmark one backend at a time, and
delete each before the next. Run the keep-awake beside long runs.

## T001 — Spike: the Observer path on serendip-t018 ✅ 2026-11-12

> **Done as:** config B installed by hand on `serendip-t018`; plan.md
> "Spike findings" 1–17; `VERSIONS.md`.
> - **Main changes:** query the adapters, not the Observer; scope by
>   UIDs; use the Service proxy; build the node rule from live
>   addresses; uninstall deletes the namespace and the Prometheus
>   Operator CRDs.
> - **Not done here:** build-log scope (builds isn't installed), moved
>   to T013.
> - **Clean-up:** `openchoreo-observability-plane` and the 11 CRDs are
>   deleted, as are the test pods.

- **Do,** on `serendip-t018`, removing the scratch objects afterwards:
  1. **Confirm the pinned artifacts:** `helm show chart` for the plane
     chart 1.3.1 (matching the control plane), logs-openobserve 0.8.0,
     logs-opensearch 0.8.1 and metrics-prometheus 0.9.3. Record the versions in `VERSIONS.md`.
  2. **Install config B** (the plane chart, logs-openobserve and
     metrics-prometheus with Alertmanager off) with `security.enabled=false`
     and `gateway.enabled=false`. Check:
     - whether the chart creates the plane object (namespaced `ObservabilityPlane/default` for our `DataPlane`) or
       Serendip must;
     - that the data plane picks it up with its spec unedited.
  3. **Queries through the Observer** for an existing Component:
     - `logs/query` by component scope, with a search phrase;
     - `logs/query` by workflow-run scope, for a spec 022 build;
     - `metrics/query` with `resource`, then `http`. Where do HTTP
       metrics come from?
     - **Does component scope resolve with security off?** (The UID
       resolver.)
  4. **The path from `serendipd`:** port-forward against the Service
     proxy, measuring the latency of 20 short queries each. With
     deny-all-but-collectors policies, check which still works.
  5. **No side door:** from a workload pod, try the Observer, the
     adapters, OpenObserve and Prometheus, before and after the policies.
     Record what `hostNetwork` Fluent Bit forces open.
  6. **Retention settings:** that the logs module's retention (3 days)
     and Prometheus' `retention`/`retentionSize` are accepted and visible
     in the running config.
  7. **Removal:** uninstall in decision 13's order while a Component runs
     and a build is installed. The Component keeps serving and builds
     still works. List the CRDs left behind.
- **Files:** `plan.md` ("Spike findings", amending decisions where the
  facts differ), `providers/observability/openchoreo/VERSIONS.md`.
- **Verify:** each item has observed evidence in `plan.md`; `kubectl get
  ns,crd,clusterobservabilityplane` shows only the recorded leftovers.

## T002 [P] — Bench: the load generator and disk use ✅ 2026-11-12

> **Done as:**
> - **The load:** `observability-load/` (`loggers.yaml`, `driver.sh`,
>   `load.sh`). On `serendip-t018`: 100 lines per logger per 10 s, 117
>   bytes a line. The driver runs and builds its query window correctly;
>   with no backend installed, its queries failed as expected.
> - **Disk in `bench-edge1`:** the kubelet's volume stats, plus a `du`
>   probe pod for host-path (local-path) volumes, which the kubelet
>   doesn't report (plan "Finding from T002"). Each volume's first and
>   last use and growth per day, over the real span. `compare` gives
>   growth between runs.
> - **Tests:** unit tests, and `TestLiveVolumes` (opt-in through
>   `BENCH_KUBECONFIG`). It measured all four local-path PVCs on
>   `serendip-t018` and left no probe pod.
> - **Review:** the `go-reviewer`'s findings were fixed, except
>   multi-node probe placement (the harness measures one node) and
>   fake-client tests of the probe's pod lifecycle (the live test
>   covers it).

- **Do:**
  - **The load manifest and script** (plan decision 9): five pinned
    `busybox` loggers, a search loop and a metrics loop, with the search
    and metrics loops aimed at the Observer directly for T003 (no
    Serendip needed).
  - **`bench-edge1` reports disk:** PVC used bytes per namespace from the
    volume stats it already reads, and growth between two runs (`compare`).
- **Files:** `profiles/edge-1/bench/observability-load/`,
  `cmd/bench-edge1/` with tests, and the bench README.
- **Verify:** `go test ./cmd/bench-edge1/...` passes; a run on kind shows
  per-namespace PVC use; the load manifest applies and its pods log at
  about 10 lines a second (`kubectl logs --since=10s | wc -l`).

## T003 — Measure the three backends, and ADR 0021 ✅ 2026-11-14

> **Done as:** configs A, B and C, and B-plain (plain Prometheus), on
> `bench-t470`. Results are in
> `profiles/edge-1/bench/results/2026-11-12-spec023-backends/`.
> ADR 0021 chooses OpenObserve for logs and plain Prometheus for metrics,
> with the module's adapter; decision 3's fallback applies. SigNoz
> didn't win, so no user decision was needed.
>
> - **Different from the task:** a 10-minute load phase, then load
>   continued through a disk phase of 2 h (B) or 1 h (A, C), instead of
>   30 minutes of load and 2 hours of disk for each (plan "Findings from
>   T003" 7).
> - **New accepted risks:** RP-055 and RP-056.
> - **Bench additions:** `backends.sh`, `measure.sh`,
>   `signoz-query.env`, `prometheus-plain-values.yaml`, and
>   `registry-cache.sh` (local image caches; not verified on a new
>   cluster yet).

- **Do,** on a simulated-T470 kind cluster with the edge-1 platform, one
  backend at a time:
  - **Configs A, B and C** (plan decision 2). For each: idle, 30 minutes
    under load, disk growth over 2 hours, the kube-apiserver delta, and
    whether the needed queries work.
  - **The Prometheus Operator CRDs' cost:** if over about 150 MiB, try
    plain Prometheus and amend decision 3.
  - **ADR 0021:** the choice and the table. Update `capability-model.md`
    and `overview.md`'s open question with the numbers.
  - **If SigNoz wins clearly, stop and ask the user** (plan decision 2)
    before T004.
- **Files:** `profiles/edge-1/bench/results/2026-11-*-spec023-backends/`,
  `docs/decisions/0021-*.md`, `docs/architecture/capability-model.md`,
  `overview.md`, `plan.md` (amendments), `build-vs-adopt.md`.
- **Verify:** each config's results folder exists with idle, load and
  disk numbers; ADR 0021 cites them; `kubectl get ns` shows the scratch
  installs removed.

## T004 — Core types ✅ 2026-11-14

> **Done as:**
> - **`core/provider`:** `Requirements.Disk *DiskRequirements`
>   (`GrowthMiBPerDay`, `RetentionDays`, `BudgetMiB`). If it's set, all
>   three must be positive; table cases cover each part missing.
> - **`core/reconcile/observability.go`:** decision 5's types and
>   `ObservabilityProvider`, plus the caps as constants (500 lines a page,
>   20 targets, 16 KiB a line, 300 points). `LogCursor` holds a time and
>   how many lines at that time were already seen. It's encoded as
>   versioned base64url, and an empty cursor means the first page. `Seen`
>   has no upper bound, because many lines can share one timestamp across
>   pages.
> - **`providerinstance.go`:** the optional `PackageUsage` interface and
>   `DiskUsage`.
> - **`core/policy`:** `ActionObservabilityRead`. The role test checks
>   that Reader and Log Reader get it through `*/read` and Secrets User
>   doesn't.
> - **The Verify grep** finds only two older comments that use
>   "observer" as an ordinary English word (`volume.go`, "a passive
>   observer"). No product names.

- **Do:**
  - `provider.Requirements.Disk` and its validation;
  - `ObservabilityProvider` and its types (plan decision 5);
  - `PackageUsage` and `DiskUsage`;
  - `policy.ActionObservabilityRead`;
  - the cursor encoding (decision 5), with a table test.
- **Files:** `core/provider/requirements.go`,
  `core/reconcile/observability.go`, `providerinstance.go`,
  `core/policy/builtins.go`, with tests.
- **Verify:** `go test ./core/...` passes; `bash
  .claude/scripts/check-imports.sh` passes; `grep -rniE
  "openobserve|opensearch|prometheus|observer" core/` finds nothing; a table
  test shows `Disk` refused when half-stated.

## T005 — The installer ✅ 2026-11-14

> **Done as:** `providers/observability/openchoreo/` (`installer.go`,
> `values.go`, `policies.go`, `doc.go`, embedded `prometheus-values.yaml`)
> and `kubernetes.Client.PostRaw`. See plan.md "Findings from T005".
> - **Order:**
>   1. the namespace, the CA copy, the placeholder and admin Secrets;
>   2. the plane chart;
>   3. the node rule;
>   4. the logs module, then plain Prometheus, then the adapter;
>   5. `ObservabilityPlane/default`.
>
>   A test asserts it.
> - **An unchanged resync:** 6 GETs and one metrics query, with no Helm
>   run and no applies.
> - **Repairs** run Helm only for the release at fault. A changed node
>   address, a deleted rule or a deleted plane object is re-applied with
>   no Helm run. A query that returns no data is reported, not repaired
>   (RP-056).
> - **Uninstall** waits for the plane object, removes the releases in
>   reverse order, then the namespace. Running it twice is clean, and it
>   resumes after a failed step.
> - **Reviews:** `go-reviewer`'s "should" findings are fixed: errors no
>   longer swallowed, IPv6 `/128`, one settle window after a restart,
>   waiting for the plane object, admin Secret keys checked.
>   `contract-guardian` passed.
> - **Not done here:** the Observer client and `Usage` (T006);
>   `Requirements` (T012). A live install is T012's run.

- **Do:** `providers/observability/openchoreo`:
  - Install, in decision 1's order as amended by T003 (ADR 0021):
    - the plane chart;
    - the node rule, then logs-openobserve with `ZO_TELEMETRY=false` and
      3-day retention;
    - plain Prometheus, then the metrics module with
      `kube-prometheus-stack.enabled=false`;
    - generated admin credentials (a symbol, a digit and both cases),
      kept in the cluster only;
    - the CR and CA trust, and the policies;
  - a readiness check that runs a real `resource` query for a known pod
    and fails on empty series (RP-056);
  - the fingerprint and resync checks (decision 12), including that each
    policy's selector matches a pod;
  - Uninstall (decision 13), every step idempotent;
  - recovery of a locked Helm release, as for builds.
- **Files:** `installer.go`, `values.go`, `policies.go`, `doc.go`, with
  tests on a fake client and a fake Helm runner.
- **Verify:** `go test ./providers/observability/...` passes, including:
  an unchanged resync makes no Helm call and at most 8 GETs; a deleted
  policy is restored; Uninstall twice is clean. Then `go-reviewer`.

## T006 — The Observer client and the provider ✅ 2026-11-14

> **Done as:** `observer.go` (the adapters' client), `scope.go` (UIDs
> from the live objects), `provider.go`, `usage.go`, with
> `provider_test.go` against an `httptest` API server. See plan.md
> "Findings from T006".
> - **Verify items:** two targets merged newest first across pages with
>   ties (5 tables, page sizes 1 to 100, end bound inclusive and
>   exclusive), no duplicates; queries only for the given targets, with
>   foreign lines dropped; a long line cut on a rune boundary; request
>   metrics reported unavailable and never queried.
> - **Also:** paging edges (tie groups over one answer, filtered full
>   answers, sub-microsecond times, an oversized answer, a failing
>   target, cancellation), metrics units and steps, disk use.
> - **Changes outside the package:** `kubernetes.ObjectMeta.UID`,
>   `kubernetes.ErrBodyTooLarge`, and a comment on `reconcile.LogPage`.
> - **Checked live on `serendip-t018`** (`live_test.go`; plan "Findings
>   from T006" 6): install, uninstall and re-install through the real
>   installer; scopes from real IDs; the answer shapes; the end bound;
>   paging; metrics units; build-run scope; disk use and its unit.
>   - **What the live check changed:** disk use goes over a port-forward
>     to the streams API (the proxy strips the credential, and `/metrics`
>     has no size gauge); OpenObserve's GeoIP and pricing downloads are
>     turned off; clearer messages for an empty store and before the
>     first scrape.
> - **New accepted risks:** RP-058 (tied timestamps in paging), RP-059
>   (a re-created platform object hides earlier logs), RP-060 (Fluent
>   Bit's position file stays on the node) and RP-061 (disk use lags).

- **Do:**
  - the client over decision 4's path (from T001);
  - `SearchLogs` (one query per target, merged by time, cursor, caps);
  - `BuildLogs` (workflow-run scope);
  - `Metrics` (resource series, requests and limits, `Unavailable`,
    at most 300 points);
  - `usage.go` (volume stats).
- **Files:** `observer.go`, `provider.go`, `usage.go`, with tests against
  an `httptest` Observer.
- **Verify:** `go test ./providers/observability/...` passes, including:
  two targets merged newest first across pages with no duplicates in a
  table of tied timestamps; a target never queried outside the list it
  was given; an over-16-KiB line cut; HTTP metrics reported unavailable.
  Then `go-reviewer`.

## T007 — Wiring in serendipd ✅ 2026-11-14

> **Done as:** `cmd/serendipd/packages.go`: the
> `openchoreo-observability-plane` entry carries its installer and
> decision 13's removal wording, with `Requirements` nil until T012, so
> it's offered but `Installable()` and `Validate` refuse it. A generic
> `packageHandle[T]` now backs both `buildHandle` and the new
> `observabilityHandle`: it sets the lazy handle when the package becomes
> Ready, clears it when it stops, and registers the provider each time it
> becomes Ready. `lazy.go` adds `lazyObservability`. In `wiring.go`,
> `obsplane.NewInstaller` fails startup without `helm`, like builds', and
> `kubeWiring.observability` is the handle the API reads in T008. Disk
> use needs no wiring: the catalogue's `Installer` is the provider's
> `*Installer`, which implements `reconcile.PackageUsage`, and T008 reads
> it by type assertion.
> - **Verify:** `go build ./...` and `go test ./cmd/serendipd/...` pass
>   (also `-race -count=5`). `TestObservabilityInstallReachesHandle` runs
>   the real engine against a catalogue with requirements: before
>   install, `NotInstalled` and `ErrProviderNotReady`; after creating the
>   ProviderInstance, Ready, the provider registered, and a read through
>   the handle reaches it.

- **Do:**
  - the catalogue entry, with the installer, `Removal` and
    `Requirements` (still nil until T012, so uninstallable outside
    tests, as for builds before its T013);
  - the lazy `observability` handle;
  - registration on readiness.
- **Files:** `cmd/serendipd/packages.go` and its wiring, with tests.
- **Verify:** `go build ./...` and `go test ./cmd/serendipd/...` pass; a
  test catalogue with requirements installs and reports readiness through
  the handle.

## T008 — The API ✅ 2026-11-14

> **Done as:** `api/observability_handler.go`, with the three routes and
> their rules in `authz.go`, wired by `WithObservability`.
> - **Order of checks:** each route checks readiness first (503 naming
>   the package, saying not installed, installing or not ready, with
>   `Retry-After`), then the stores, then takes one of 3 slots per
>   caller (20 in all), then runs the query under a 20 s deadline.
> - **An Application's search** needs `?environment`. That Environment
>   gets the same 404 whether it's missing or unreadable. Each Deployment
>   is checked for `serendip/components/logs/read`, and only allowed
>   ones become targets. `omitted` counts the rest. It's 403 if none is
>   allowed, and 422 over 20 targets.
> - **Search defaults:** the last hour, 100 lines (at most 500), and
>   `q` up to 256 bytes. Ranges stay within the stated retention.
> - **Metrics:** at least 15 minutes. Non-finite samples are dropped, and
>   each series gets a `summary` (min, avg, max, latest).
>   `?summary=true` keeps at most 60 points.
> - **Build-logs fallback:** `runOrGone` and `storedLogs`. Once a run is
>   gone and the package is ready, the lines come from the store, oldest
>   first, as `{"lines", "truncated", "stored": true}`. With `follow`,
>   the same lines arrive as NDJSON, then `{"end":"ended"}`. The read is
>   capped by `tailLines`/5000 lines, 4 MiB and 20 pages. Otherwise the
>   answer is 410, as before.
> - **Packages:** `GET /v1/packages` adds `usage` (or a generic
>   `usageError`) for an installed package whose installer has
>   `PackageUsage`. `serendip packages list` prints the disk cost and
>   the current use.
> - **Catalogue:** `deployments_logs_search` (`serendip logs search`),
>   `applications_logs_search` (`serendip logs search-application`) and
>   `deployments_metrics` (`serendip metrics`).
>   - A new `Operation.MCPQuery` makes the MCP metrics tool always send
>     `summary=true`. Its `summary` parameter is CLI-only.
>   - `docs/cli-and-mcp.md` gained a section on stored logs and metrics.
> - **Reviews:** go-reviewer found no authorization leak. Fixed:
>   - a nil error passed on when the capability was Ready but not wired;
>   - readiness checked after a 200, 403 or 422 could already answer;
>   - client cancellation logged as a 502;
>   - a deadline in the build fallback answered 502, not 504;
>   - NaN or Inf breaking the JSON;
>   - no byte budget on stored build logs;
>   - a zero creation time scanning without bound;
>   - `timeRange` naming the wrong parameter.
>
>   Regression tests are in `observability_internal_test.go`.
> - **Verify:** `go test ./...` and `go vet ./...` pass.
>   `spec023_acceptance_test.go` runs criteria 3, 5–8 and 11 on spec
>   022's world with a fake provider. The route-table test, a Reader
>   getting metrics but a 403 on logs, the scoped searches asserted on
>   the fake's input (a person and an agent with logs on one Deployment
>   each), `omitted`, and the catalog parity test all pass.

- **Do:**
  - the three routes (decision 11) with decision 6's scoping, caps, 503
    when not ready, and `omitted`;
  - the build-logs fallback (decision 10);
  - package usage in `GET /v1/packages`;
  - the authz rules and the catalogue entries;
  - `api/spec023_acceptance_test.go` on a fake provider (criteria 3, 5–8,
    11).
- **Files:** `api/observability_handler.go`, `build_handler.go`,
  `package_handler.go`, `authz.go`, `catalog/`, with tests.
- **Verify:** `go test ./api/...` passes, including:
  - the route-table test;
  - a Reader gets metrics but a 403 on logs;
  - a Log Reader scoped to one Project gets only that Project's
    Deployments as targets (asserted on the fake provider's input);
  - the application search reports `omitted`;
  - the catalog parity test.

## T009 [P][A] — Tests: edge cases and fakes ✅ 2026-11-14

> **Done as** (by the `test-writer`):
> - **Files:** `api/observability_handler_test.go` (a fault-injecting
>   `ctl023` in front of `obs023`, through `newWorld023Wrapped`),
>   `api/observability_caps_internal_test.go`,
>   `providers/observability/openchoreo/edges_test.go`, and two tests in
>   `core/reconcile/observability_test.go`.
> - **Cursors across ranges:** a narrowed `from` and `to`, cursors at
>   and beyond the edges, ties across pages, and the encoding's limits.
> - **An empty, ready package:** `"lines": []` (never null) and no
>   `next`; `omitted: 0`, without a provider call when the Environment
>   is empty; and empty metrics with an empty summary.
> - **Not ready mid-request:** 503 with `Retry-After` (never 502) on all
>   three routes. For build logs, it's 410, followed or not.
> - **Caps:** 3 per caller and 20 in all; 429 with `Retry-After`; slots
>   released after success, errors, cancellation and validation
>   failures. The tests use channels, not sleeps, and check for leaked
>   goroutines.
> - **No bug found.** Its one observation, that `begin`'s `done` would
>   release twice if called twice, is fixed with a `sync.Once`.
> - **Verify:** `go test ./...` and `go vet ./...` are clean, and
>   `go test -count=3` passes on the touched packages (the
>   `test-writer` also ran `-race -count=3`).


- **Do:** with the `test-writer`, table tests for:
  - cursors across time ranges;
  - an empty, ready package;
  - the provider becoming not ready mid-request;
  - concurrent query caps per caller.
- **Files:** `*_test.go` under `api/`, `core/reconcile/` and
  `providers/observability/`.
- **Verify:** `go test ./...` passes; `go vet ./...` is clean.

## T010 [P][A] — CLI and MCP ✅ 2026-11-14

> **Done as:** the commands, the tools, `Operation.MCPQuery` and the
> docs section came with T008. T010 added the tests, in
> `cmd/serendip/observability_test.go`, on a world with the capability
> ready (`newWorldWith`, a provider registry and server options) and a
> fake provider at the real one's resolution:
> - **The CLI:** the three commands' requests (dry run), the usage
>   errors (no `--environment`, `--limit` over 500, a non-boolean
>   `--summary`), and real calls: a search finds its line, an
>   Application's search reports `omitted: 0`, and an hour's metrics are
>   60 points with a summary.
> - **MCP:** the three tools are listed, and `deployments_metrics`
>   doesn't offer `summary`. A 24-hour call gets at most 60 points a
>   series, with the summary over all 288 points (min and max true).
>   An assistant passing `summary: false` is refused (unknown
>   parameter).
> - **The test can fail:** with `MCPQuery` removed, the MCP test fails
>   on 288 points.
> - **Verify:** `go test ./cmd/serendip/... ./api/...` and `go vet`
>   pass.

- **Do:**
  - generated commands (`serendip logs search`, `serendip metrics`);
  - the MCP tools, with the metrics summary (decision 11);
  - `docs/cli-and-mcp.md`.
- **Files:** `cmd/serendip/`, `api/mcp_handler.go` as needed, docs, with
  tests.
- **Verify:** `go test ./cmd/serendip/... ./api/...` passes; the MCP
  metrics tool's output for a 24-hour range is at most 60 points plus a
  summary.

## T011 [P] — The console

- **Do:**
  - **The Observability tab:**
    - log search (text, time presets, newest first, "older");
    - CPU and memory charts against requests and limits, inline SVG,
      following the `dataviz` skill;
    - "not available" items.
  - **An Application-level log search,** with the `omitted` notice.
  - **The Packages page:** the disk line and current use.
  - **"Same request through the API"** on each view.
- **Files:** `ui/src/screens/Runtime.tsx` (or a new `Observability.tsx`),
  the Application screen, `Packages`, with tests.
- **Verify:** `npm test` and `npm run build` in `ui/` pass; the charts
  read in light and dark (a screenshot in the T013 verification).

## T012 — The package's footprint, and its Requirements

- **Do:**
  - on the simulated T470, install through Serendip's own installer;
  - measure idle, under load (T002) and disk over 2 hours, plus the
    resync cost;
  - fill `requirements.go`, including `Disk`;
  - confirm an edge-1 run without the package shows no regression
    against spec 022's run.
- **Files:** `providers/observability/openchoreo/requirements.go`,
  `profiles/edge-1/bench/results/2026-11-*-spec023-simulated-t470/`.
- **Verify:** `GET /v1/packages` shows the numbers and they match the
  results folder (criterion 1); resync adds near-zero CPU over a
  10-minute idle (criterion 4).

## T013 — Real-cluster verification on serendip-t018

- **Do:** `verification.md`, covering criteria 1, 3–11 and 13, through
  the console, CLI and MCP:
  - search a deleted pod's line;
  - an Application search;
  - metrics under load;
  - a build's logs after its pods are gone;
  - the side-door check from a workload pod;
  - retention and disk use;
  - removal through an Approval with builds still working.
- **Files:** `specs/023-observability/verification.md`, and fixes found.
- **Verify:** every criterion has evidence in `verification.md`; `go
  test ./...` and the UI tests pass after fixes.

## T014 — Reviews and fixes

- **Do:** `contract-guardian` on `core/` and `providers/`; `go-reviewer`;
  a security review of the query path, the policies and the generated
  credentials. Fix, or record in the risk register.
- **Files:** as the reviews find.
- **Verify:** each review's findings are fixed or recorded with an RP
  number; `go test ./...` passes.

## T015 [A] — Docs and close-out

- **Do:**
  - `application-platform.md` ("Observability (spec 023)"),
    `provider-model.md` (`Disk`) and `authorization.md`;
  - the risk register (new RPs: the Observer's auth off, `hostNetwork`
    Fluent Bit, stored secrets in logs, disk, CRDs left behind, time
    cursors);
  - `build-vs-adopt.md`;
  - `PROGRESS.md`.
- **Files:** those docs.
- **Verify:** the `docs-keeper` reports no drift between the docs and the
  code; every RP the plan names exists in the register's index.
