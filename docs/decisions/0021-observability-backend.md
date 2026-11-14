# ADR 0021: Observability backend: OpenObserve for logs, plain Prometheus for metrics

Status: Accepted
Date: 2026-11-14

## Context

The observability package (`specs/023-observability`) needs a log store
and a metrics store. `capability-model.md` and `overview.md` left the
default open until OpenObserve, SigNoz and OpenSearch were measured on
reference (T470) hardware.

Constraints:
- **The machine:** a laptop with 2 cores and 16 GB, running the edge-1
  platform already.
- **Serendip queries through OpenChoreo's adapters** (plan 023 decision 4,
  amended), so a backend with an OpenChoreo module costs least to build.
  SigNoz has none: using it would mean a SigNoz adapter in Serendip.
- **Nothing leaves the machine unless asked** (plan 023, "Finding during
  T003"): edge-1 must work offline.

## Measurements

Spec 023 T003, 2026-11-12 to 2026-11-14, on `bench-t470`. That is a kind
node limited to 2 CPUs on 4 threads to approximate an i5-6300U. Memory
isn't limited, and disk speed isn't simulated, so this is a simulation,
not the reference-hardware gate.

Method:
- **Each config** was measured on its own, against a baseline taken just
  before its install (`bench-edge1 sample` and `delta`, node level).
- **The load:** five loggers writing 10 lines a second each, a log search
  every 10 s, and a 1-hour metrics query every 30 s (plan decision 9).
- **The configs:**
  - **A:** the OpenChoreo plane chart 1.3.1, logs-opensearch 0.8.1 and
    metrics-prometheus 0.9.3.
  - **B:** the plane chart, logs-openobserve 0.8.0 and
    metrics-prometheus 0.9.3.
  - **C:** SigNoz 0.149.0 with k8s-infra (logs and kubelet metrics).
  - **B-plain:** the plane chart and plain Prometheus, without the logs
    module.
- **Results:** `profiles/edge-1/bench/results/2026-11-12-spec023-backends/`.

| | A: OpenSearch | B: OpenObserve | C: SigNoz |
|---|---|---|---|
| Node memory, idle | +1,609 MiB | +832 MiB | +1,801 MiB |
| Node memory, under load | +1,659 MiB | +1,028 MiB | +2,068 MiB |
| Node CPU, under load (mean) | +209 mcores | +121 mcores | +430 mcores (peaks of 1,521) |
| The log store's own memory, under load | OpenSearch 1,005 MiB | OpenObserve 390 MiB | ClickHouse 898 MiB, ZooKeeper 761 MiB |
| Log volume growth, extrapolated per day | about 6.6 GB | about 0.4 GB | not measurable in 1 h* |
| Log search, mean (worst) | 0.13 s (0.75 s) | 0.53 s (3.1 s) | 0.05 s (0.12 s) |
| Metrics query, mean | 0.019 s | 0.014 s | 0.023 s |
| Queries that failed | 0 of 568 | 0 of 1,000 | 0 of 551 |
| Install time | 169 s | 65 s, plus 268 s for metrics | 271 s |
| Images | about 1.4 GB (OpenSearch is 1.16 GB) | about 0.5 GB | about 0.9 GB |
| Left behind after removal | kube-apiserver +371 MiB until restarted | kube-apiserver +390 MiB until restarted | nothing (+131 MiB node, no pod above 5 MiB) |

\* ClickHouse started at about 600 MiB of its own system tables, then
shrank 38 MiB as it merged.

Notes:
- **B's disk phase ran 2 hours, and A's and C's 1 hour.** A's per-day
  growth includes index creation, so it may be overstated. It is still
  an order of magnitude above B's.
- **OpenObserve's search slowed as data grew:** a mean of 0.24 s in the
  10-minute load phase, and 0.53 s over the whole run (RP-055).

**The metrics module's cost.** B's metrics module (kube-prometheus-stack
with the Prometheus Operator) costs about 647 MiB at idle:
- **about 180 MiB is kube-apiserver and etcd** holding its 10
  `monitoring.coreos.com` CRDs (RP-046's pattern);
- **after removal, kube-apiserver keeps about 390 MiB** until it
  restarts.

**B-plain** is the plane chart and plain Prometheus, with the module's
adapter pointed at it:
- **The node costs +276 MiB at idle,** of which the metrics part is
  134 MiB:
  - Prometheus 107 MiB;
  - kube-state-metrics 19 MiB;
  - the adapter 9 MiB.
- **kube-apiserver doesn't change.**
- **The adapter's queries work.** Its `resource` query, scoped by
  component UID, returned CPU and memory usage, requests and limits.

## Decision

### 1. Logs: OpenObserve, through OpenChoreo's logs-openobserve module

- **Why:** it is the lightest store by a wide margin, and its disk grows
  about 0.4 GB a day at the test load. Under the 3-day retention, that
  levels off near 1.2 GB.
- **Its search is the slowest of the three,** at about half a second.
  That's acceptable for a log tab.
- **The installer sets** `ZO_TELEMETRY=false`, strong generated
  credentials, and a 3-day retention.
- **The node rule goes in before the install** (T003): it admits the
  nodes to OpenObserve's ingest port.
  - **Superseded in T014 (RP-064):** Fluent Bit runs off the host
    network, on Serendip's copy of the chart's config with pod metadata
    from the API server, so it needs no node rule and the plane's
    policy covers its `:2020`.

### 2. Metrics: plain Prometheus, with the module's adapter only

This amends plan decision 3, whose fallback threshold was 150 MiB of CRD
cost.

- **The package installs:**
  - `prometheus-community/prometheus` 29.47.2, with no operator and no
    CRDs;
  - metrics-prometheus 0.9.3, with `kube-prometheus-stack.enabled=false`,
    for its adapter alone.
- **Prometheus stands in for kube-prometheus-stack.** Its values
  (`profiles/edge-1/bench/observability-load/prometheus-plain-values.yaml`,
  the starting point for T005) give it:
  - the Service name and port the adapter's address fixes,
    `openchoreo-observability-prometheus:9091`;
  - the same two scrape sources with the same job names: the kubelet's
    cAdvisor endpoint, and kube-state-metrics limited to the module's pod
    metrics and `openchoreo.dev` labels.
- **Retention:** 3 days, capped at 2 GB.
- **Savings:** about 450 MiB at idle, at node level (the module's
  647 MiB against about 194 MiB), and nothing is left in kube-apiserver
  after removal.

### 3. SigNoz is not the edge-1 default

- **It costs the most:** about 2 GB at idle and under load, and about
  3.5× B's CPU.
  - ZooKeeper alone holds 760 MiB to coordinate a ClickHouse cluster of
    one.
- **It has no OpenChoreo module,** so Serendip would need its own SigNoz
  adapter.
- **It needs workarounds an installer would carry:**
  - **A root user,** or no organization exists and its collector
    ingests nothing.
  - **Its usage reporting turned off.** It reports every 6 hours by
    default, user emails included.
  - **Its ClickHouse object deleted** before its operator, or removal
    hangs.
  - **A ClickHouse password that's fixed in its chart** and the same on
    every install.
- **Its log search was the fastest:** about 0.05 s. That, and tracing
  from the same store, make it worth looking at again for a multi-node
  install with memory to spare (`build-vs-adopt.md`).

### 4. Scope: the lowest-resource install

- **These choices are edge-1's,** sized for the 16 GB T470. Its trade-offs
  are accepted because memory is the constraint (RP-055, RP-056).
- **A larger profile may choose differently:**
  - **The metrics module as shipped** (the operator, and later its
    alerts): no change to Serendip's query path, since it sits behind
    the same adapter.
  - **SigNoz:** it would need a SigNoz adapter in Serendip (decision 3).
- **The full test pass comes after v1** (all features in), on real
  hardware, including upgrades and long runs. Per-spec checks until then.

## Consequences

- **The package's observability footprint,** at node level and idle,
  is an expected 350–400 MiB, against 832 MiB for the modules as
  shipped. T013 measures the real figure. It comes from:
  - about 185 MiB for the plane and logs (B's logs-idle phase);
  - about 194 MiB for plain metrics: B-plain's 276 MiB, less the
    plane's pods (about 82 MiB). Its pods alone are 134 MiB; the rest
    is node overhead.
- **Serendip owns a Prometheus configuration.** It must keep matching
  what the module's adapter queries (RP-056). Upstream changes to the
  module's kube-prometheus-stack values don't reach it, so every module
  upgrade re-checks the scrape jobs, the kube-state-metrics allowlist and
  the Service address (`VERSIONS.md`).
- **The module's alert path is unavailable.** Its PrometheusRule CRs and
  Alertmanager need the operator. Alerts are deferred in spec 023
  anyway; a later alerts spec decides between plain Prometheus rules and
  the operator.
- **OpenSearch stays rejected for edge-1** (`build-vs-adopt.md`). It
  costs twice B's memory, grows its disk about 16 times faster, and its
  1.16 GB image is the largest download of the three.

## Alternatives rejected

- **The metrics module as shipped (kube-prometheus-stack):** about
  450 MiB more at idle, 10 CRDs, and about 390 MiB left in
  kube-apiserver after removal.
- **OpenSearch for logs:** twice the memory, about 6.6 GB of disk a day
  at the test load, and a vm.max_map_count change on the node.
- **SigNoz for both:** the heaviest config measured, no OpenChoreo
  module, and the setup workarounds above.
- **Prometheus written by hand, without a chart:** the chart is pinned
  and small. Writing it ourselves would add manifests to keep current,
  for nothing in return.
