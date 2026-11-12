# Spec 023: Observability — the observability-plane package

Status: Reviewed, 2026-11-09. Treated as a POC. At review: traces, alerts
and the traffic overlay are deferred, and SigNoz is measured too.

Carved out of spec 020 (plan decision 10, agreed 2026-10-20), after spec
022 delivered the builds package. 020 built the package framework and the
greyed-out Observability tab; this spec delivers the observability package
itself, its first features, and the Observability capability's first real
provider.

## Problem

The console's Observability tab reads "unavailable", and the Observability
capability has no interface in `core/` and no provider. Today a person can
see a Component's **live** pod logs and events (spec 020), but only while
the pods exist and only the recent lines:

- Logs from a pod that crashed, was rescheduled or was replaced by a new
  release are gone.
- Logs can't be searched by text or time, and can't be read across an
  Application's Components together.
- There is no record of a Component's CPU and memory over time, so "is it
  slow because it's starved?" has no answer in Serendip. The only numbers
  are the benchmark harness's point-in-time reads.

OpenChoreo has an **observability plane**: a query service (the Observer)
over pluggable log, metric and trace backends, with collectors in the data
and workflow planes. Spec 020 sketched installing it with OpenChoreo's
OpenSearch and Prometheus modules.

Three things make this package harder than builds:

- **Weight.** Upstream doesn't document what the backends cost. OpenSearch
  is a JVM service; on a 2-core, 16 GB laptop beside edge-1 it may not fit
  comfortably. Spec 022 made measured cost a precondition of install, so
  this package can't ship until it's measured.
- **The backend default is unresolved.** `capability-model.md` and
  `overview.md` say not to treat OpenObserve, SigNoz or OpenSearch as the
  default until a T470 benchmark is recorded. OpenChoreo now ships an
  OpenObserve community module beside the OpenSearch one, so the
  package's backend can be chosen by measurement rather than taken as-is.
- **The query service has its own authentication.** The Observer checks
  OpenChoreo identity tokens, while Serendip runs OpenChoreo's control
  plane with security off and reaches the platform through the Kubernetes
  API (ADR 0019). How Serendip reaches the Observer without opening a path
  around its own RBAC is a plan decision.

## Scope — IN

### The package's cost, stated before install

1. **Measured cost, from one declaration.** The observability provider
   declares its measured `Requirements` (spec 022), and the package can't
   be installed without them. The numbers state:
   - idle memory and CPU once installed, including cost that shows up
     outside the package's own pods (for example the API server serving
     its CRDs, as RP-046 found for builds);
   - the cost **under load**: an edge-1 with a few light Components
     logging steadily, and a log search running;
   - the stored data's disk growth per day at that load, and the default
     retention.
2. **Honest fit.** If the measured cost doesn't fit comfortably on the
   reference machine beside edge-1, the catalogue says so with the
   numbers, and an Owner may still install it (constitution 4, 6).

### Installing and removing it

3. **Install and readiness.** An Owner installs the package through the
   API, console, CLI or MCP, the same way as builds. It installs the
   observability plane, a log store, a metric store and the collectors on
   the existing data plane (and the workflow plane, if builds is
   installed), registers it with the platform, and reports not installed
   → installing → ready, or failed with a plain message. It runs as a
   background package job (spec 022).
4. **Cheap resync.** Re-reconciling with nothing changed doesn't re-run
   the install, and a component deleted by hand is repaired, as for
   builds.
5. **Retention is bounded.** Stored logs and metrics have a default
   retention that keeps disk use within a stated budget on the reference
   machine. The package states its retention and current disk use.
6. **Removal** goes through an Approval (spec 019).
   - **Before the delete, the console says what is lost:** all stored
     logs and metrics history. Live pod logs and events keep working.
   - After removal the Observability sections grey out again, the
     collectors stop on every plane, and the cluster holds none of the
     package's workloads (checked live).
   - **Installing builds after observability** (or the other way round)
     leaves the workflow plane's build logs collected or not, consistently;
     removing one package doesn't break the other.

### Features

7. **Log search.** Stored log lines for a Component in an Environment, and
   across an Application's Components in an Environment, filtered by time
   range and text, newest first, capped and paginated. Each line carries
   its time, Component, pod and container. Lines from pods that no longer
   exist are included.
8. **Metrics.** Per Component in an Environment: CPU and memory over a
   chosen time range, against its requests and limits where set. Request
   rate, errors and latency are shown only if the plan finds them
   available without extra components; otherwise they are listed as not
   available, not faked.
9. **Build logs, when builds is installed.** A finished build's logs stay
   readable after its pods are gone, through the same Build logs route
   (spec 022). Without observability it behaves as today.
10. **Without the package,** the sections show as unavailable, naming the
    package; installed but not ready reads "installing" or "not ready".
    Pod logs and events (spec 020) keep working either way.
11. **Assistant use.** The MCP tools return capped, summarised results an
    assistant can use for diagnosis (constitution 5: diagnose freely),
    under the same permissions as a person.

### Across all features

12. **Authorization** (spec 017, the actions named in spec 020):
    - stored logs need `serendip/components/logs/read` (Log Reader), the
      same as pod logs;
    - metrics need `serendip/observability/read`;
    - results are scoped like everything else: a person sees only the
      Components and Environments their roles reach;
    - install and removal stay platform-level, at `/`;
    - every new route has a rule.
13. **No way around Serendip.** Serendip doesn't expose the log store, the
    metric store or the Observer to people or agents directly, and no new
    listener on the node accepts unauthenticated queries. Any such path
    that has to exist is in the risk register with its mitigation.
14. **Audit** (constitution 8): installs and removals, with actor,
    interface and decision. Reads aren't audited (as for pod logs today).
15. **Interface parity** (constitution 7): every console view is an API
    call shown in "Same request through the API", a CLI command and an MCP
    tool; `api/catalog` covers them.

## Scope — OUT (explicitly)

- **Traces, alert rules and notification channels, and the live traffic
  overlay on the topology diagram.** Spec 020 listed these for 023; they
  move to a follow-up spec so this one can settle weight, backend and the
  query path first. The follow-up builds on this package.
- **The AI root-cause agent** OpenChoreo's chart offers (it costs about
  1 GiB on its own and is a separate AI-operations decision).
- **Building a metrics TSDB or log store** (constitution 10).
- **Turning observability on by default in edge-1.** The three-way
  measurement answers `capability-model.md`'s backend question, but the
  package stays optional and off by default.
- **Changing edge-1's default component list.**
- **Bring-your-own observability** (an existing Grafana, Loki or
  OpenSearch). The `external` escape hatch stays planned.
- **Dashboards, Grafana, custom queries** in a query language.
- **Serendip's own logs and metrics** (`serendipd`, the agent): only
  Components and builds in this spec.
- **Multi-cluster or a remote observability plane.**
- **Long-term storage, archiving, or log export.**

## Affected resources and capabilities

- **The Observability capability** (`core/`, new interface): query a
  Component's or Application's stored logs, query a Component's metrics,
  report retention and disk use. Capability-shaped; no product names in
  `core/` (constitution 1).
- **Its provider** (`providers/`): OpenChoreo's observability plane with
  the chosen backends, as the `observability` package, with its installer
  and measured `Requirements`.
- **The package catalogue and `ProviderInstance`:** the second real
  installer; what-is-lost-on-removal; the interaction with the builds
  package.
- **Builds (spec 022):** the logs route reads stored logs when a run's
  pods are gone and observability is installed.
- **The application platform provider:** the data plane (and workflow
  plane) reference the observability plane; their install must keep
  working without it.
- **Authorization:** routes for log search and metrics.
- **The console:** the Observability tab (log search, metrics charts),
  the Packages page's cost, retention and removal wording.
- **CLI and MCP:** new commands and tools.
- **Benchmarks:** idle, under-load and disk cost on the simulated T470,
  for each backend the plan compares.
- **Docs:** `capability-model.md` and `overview.md`'s open question
  (updated with the measurement), an ADR for the backend choice,
  `build-vs-adopt.md`, the risk register.

## Acceptance criteria

1. **Cost before install.** With the package not installed,
   `GET /v1/packages`, `serendip packages list`, the MCP tool and the
   Packages page show its measured idle and under-load cost, disk growth
   and default retention, with where the numbers came from, and match a
   recorded run in `profiles/edge-1/bench/results/`.
2. **Backend chosen by measurement.** The plan's spike measures
   OpenSearch, OpenObserve and SigNoz on the simulated T470 (idle, load,
   disk), and an ADR records the choice with the numbers.
   `capability-model.md` and `overview.md`'s open question is answered
   with the same numbers.
3. **Install.** An Owner installs the package; the Observability tab goes
   from "unavailable" to "installing" to available, and the install is
   audited. A Contributor's install is refused (403).
4. **Cheap resync.** With the package installed and nothing changed, a
   resync doesn't re-run the install (measured near-zero added CPU); a
   component deleted by hand is repaired on the next resync.
5. **Log search.** A Component logs a known line; its pod is deleted and
   replaced; the line is still found by text and time, through the
   console, CLI and MCP. A search across the Application finds lines from
   two Components. Results are capped and paginated.
6. **Metrics.** A Component's CPU and memory over the last hour are shown,
   and a load placed on it shows in the chart. Metrics the backend can't
   provide read "not available".
7. **Build logs.** With builds installed, a build's logs are still
   readable after its pods are deleted.
8. **Authorization.** Every new route has a rule in the route-table test.
   A Reader sees metrics but not logs; a Log Reader scoped to one Project
   can't read another Project's logs; an agent's search uses its own
   roles.
9. **No side door.** From a pod in a workload namespace, the log store,
   metric store and Observer don't answer unauthenticated queries (or any
   remaining path is in the risk register with its mitigation), checked
   on the real cluster.
10. **Retention.** Stored data stays within its stated disk budget over a
    measured period, and the package reports its disk use.
11. **Removal.** Removing the package needs an Approval; the console
    states what's lost; afterwards the sections grey out, collectors are
    gone from every plane, and builds still works if installed.
12. **Footprint.**
    - edge-1 without the package: no regression against spec 022's
      simulated-T470 run.
    - With it: idle, under-load and disk cost recorded, and the stated
      cost matches.
    - Findings go in `docs/risk-register.md`.
13. **Nothing else breaks.** Existing tests, the bench harness, and a
    real-cluster run on `serendip-t018` pass.

## Open questions (for plan.md)

1. **Which backends.** OpenChoreo's default modules (OpenSearch for logs,
   Prometheus for metrics) or its OpenObserve community module for logs,
   with Prometheus for metrics either way. The spike installs both on the
   simulated T470, measures idle, load and disk, and checks that the
   Observer answers the queries we need with each. **SigNoz is measured
   too** (decided at review, 2026-11-09): it isn't an OpenChoreo module
   and wouldn't plug into the Observer, but measuring all three answers
   `capability-model.md`'s open question in one go. If SigNoz wins
   clearly, the plan says what using it would take (bypassing the
   Observer) before choosing.
2. **How Serendip queries.** Through the Observer (which needs an OpenChoreo
   identity token: a ThunderID client, `client_credentials`), or straight
   to the backends' APIs inside the cluster, or both. Which one keeps the
   query path closed to everyone but `serendipd`, and at what cost.
3. **Turning the data plane's collectors on.** What changes in the
   existing data-plane and workflow-plane installs (the
   `observabilityPlaneRef`, the collectors), and how removing the package
   reverses it without disturbing running Components.
4. **Retention and disk.** The default retention, how it's enforced per
   backend, and the disk budget on edge-1's local-path storage.
5. **Metric source.** Whether CPU and memory come from Prometheus scraping
   the kubelet, or from something already present, and whether request
   rate, errors and latency need Cilium's Hubble (and what that costs).
6. **Scoping results.** How a query is narrowed to the caller's Projects
   and Environments before it reaches the backend, not filtered after.
7. **The package's CRD cost.** Which upstream CRDs are needed, and whether
   a minimal set is possible (as spec 022 did for Argo, RP-046).
