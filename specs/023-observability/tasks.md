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

## T003 — Measure the three backends, and ADR 0021

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

## T004 — Core types

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

## T005 — The installer

- **Do:** `providers/observability/openchoreo`:
  - Install, in decision 1's order: the values per the chosen backend,
    generated admin credentials kept in the cluster only, the CR and CA
    trust, and the policies;
  - the fingerprint and resync checks (decision 12), including that each
    policy's selector matches a pod;
  - Uninstall (decision 13), every step idempotent;
  - recovery of a locked Helm release, as for builds.
- **Files:** `installer.go`, `values.go`, `policies.go`, `doc.go`, with
  tests on a fake client and a fake Helm runner.
- **Verify:** `go test ./providers/observability/...` passes, including:
  an unchanged resync makes no Helm call and at most 8 GETs; a deleted
  policy is restored; Uninstall twice is clean. Then `go-reviewer`.

## T006 — The Observer client and the provider

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

## T007 — Wiring in serendipd

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

## T008 — The API

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

## T009 [P][A] — Tests: edge cases and fakes

- **Do:** with the `test-writer`, table tests for:
  - cursors across time ranges;
  - an empty, ready package;
  - the provider becoming not ready mid-request;
  - concurrent query caps per caller.
- **Files:** `*_test.go` under `api/`, `core/reconcile/` and
  `providers/observability/`.
- **Verify:** `go test ./...` passes; `go vet ./...` is clean.

## T010 [P][A] — CLI and MCP

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
