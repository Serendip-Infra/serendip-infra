# Spec 025 verification (T012, real cluster)

Run 2026-11-23 on `serendip-t025`, a kind cluster (`kindest/node:v1.34.0`,
2 CPUs of quota on 4 threads), created with
`CNI=cilium profiles/edge-1/bench/kind-t470-setup.sh up` and Cilium 1.20.5
from the provider's values. Criterion 2 ran on `serendip-t018`, kindnet.
Times are UTC unless marked local (UTC+05:30). Tokens and passwords were
read from files and never printed.

## Setup

- **`serendip-t025` was recreated** (its old store and OpenBao file were
  moved aside, not deleted). This checks ca783b5's fix: with the gateway on
  host port 30083 in both the NodePort and the kind mapping, console
  sign-in through ThunderID worked on the first try.
- **`serendipd`:** built from `spec-025` at 38965e4 (T011), with the
  console embedded, on `127.0.0.1:8089`. It was rebuilt twice as fixes
  F1 and F2 landed, and restarted to add API keys. It set up OpenBao,
  ThunderID, kgateway and OpenChoreo by itself. The observability
  package was installed through `POST /v1/provider-instances`.
- **API keys:**
  - `verifier`: Owner at `/`.
  - `reader-a`: Reader at Project `t012-a` only.
  - `reader-c`: Reader at Application `shop`, Reader at `caller`'s
    Deployment, and a custom role with only `serendip/environments/read`
    at the Environment. It can read `caller`'s traffic, not `srv`'s.
  - New keys are migrated to Owner at `/` on first start (spec 017's
    rule). Each was then cut back to its role.
  - **The person:** `admin` (Owner) signed in to the console through
    headless Chrome. `serendip mcp` acted as `agent:serendip-mcp/<admin>`.
- **Workloads:**
  - Project `t012-a`, Environment `dev`, Application `shop`:
    - `srv`: the test image `t025-http:1`, 10% 500s, 20 ms delay. Its
      port is exposed, as RP-082 needs for request metrics.
    - `caller`: calls `srv` 5 times a second, a dependency.
    - `probe-obs` and `probe-api`: call the observability logs adapter
      and the OpenChoreo API every 5 s.
    - `a-to-b`: calls Project B's `b-srv` every 3 s.
  - Project `t012-b`, Application `bank`: `b-srv` (exposed) and
    `b-caller`.
- **Images:** loaded with `docker save` and `ctr import`.
- **Tooling:** outside the repo, in `~/.serendip-t025-dev/t012/`:
  - API, CLI and MCP helpers;
  - the console scripts, using the CDP driver from spec 023;
  - `loadgen` (stdlib Go: a fixed rate with its own counts);
  - `t012-otel`, a plain OpenTelemetry Go SDK app (otelhttp, OTLP/HTTP,
    configured only by `OTEL_*`), built for criterion 7;
  - the evidence under `evidence/`.

## Evidence, by acceptance criterion

1. **Cilium cluster status. PASS.**
   - **API, CLI and MCP:** `GET /v1/networking`, `serendip networking
     show` and `networking_get` give `cilium 1.20.5`, policies
     `enforced` ("Cilium's own policy enforcement"), flows and request
     metrics true, pod range `10.244.0.0/24`.
   - **Console:** the Platform page's Network layer section shows the
     same facts (`ui-c1-platform.png`).
   - **`core/notify`:** a webhook channel to `http://10.244.0.77:8080/`
     was refused at create with 400 ("10.244.0.77 is in the cluster's
     own ranges"). It was refused even with `allowPrivateDestination`
     and `allowPlainHTTP` set.
2. **kindnet cluster. PASS** (on `serendip-t018`, with the same build).
   - **API, CLI and MCP:**
     - `networking show` reports `kindnet v20250512-df8de77b`, policies
       enforced (kindnet's own controller), flows and request metrics
       false, with `unavailable` naming why.
     - `flows` and `traffic` answer **409** "connection records aren't
       available: the cluster's CNI doesn't record connections" and
       "request metrics aren't available: the cluster's CNI doesn't
       provide request metrics". The CLI exits 6; MCP returns
       `isError`.
     - `metrics` has no `requests` and lists "requests: the cluster's
       CNI doesn't provide request metrics".
   - **Console** (`ui-c2-*.png`): the Platform page shows both as "Not
     available" with the reason. So do the Component's HTTP requests
     and Connections, and the topology's Live traffic.
   - **Nothing networking-related was installed or changed.** A snapshot
     of namespaces, workloads, NetworkPolicies, ConfigMaps and the CRD
     count was taken before start and after a full 5-minute interval:
     - no Cilium objects and no CiliumNetworkPolicy CRD;
     - Fluent Bit got no flow input.
     - What did change: the observability package was re-applied at its
       spec-025 version on start. That is the package upgrade any build
       change brings: OpenObserve's StatefulSet generation 2 → 4, and
       Prometheus's config gained the `hubble` job, whose lookup fails
       on kindnet (RP-085).
     - The platform's periodic ensure rewrote `serendip-deny-openchoreo-api`
       (spec 020).
3. **A policy Serendip relies on, checked live. PASS.**
   - `probe-obs`'s calls to `logs-adapter:9098` (RP-054's
     `observability-plane-ingress`) and `probe-api`'s to
     `openchoreo-api:8080` (`serendip-deny-openchoreo-api`) are dropped.
   - The drops show in each Deployment's flows (CLI, MCP, console) as
     `dropped`, "refused by a network policy", with the destination
     workload and port.
   - `hubble observe` on the node shows the same SYNs as `Policy denied
     DROPPED`. Finding F3 is about their count.
4. **Request metrics against a load generator. PASS** (tolerance below).
   - **Load:** `loadgen` sent 10 requests a second to `srv` through the
     gateway for 600 s (06:54:12–07:04:12): 5999 requests, 622 5xx
     (10.37%). Client-side p50/p95/p99 were 22.8/26.0/35.9 ms, gateway
     hop included. `caller` was paused for the run.
   - **Serendip** (`serendip metrics`, steady minutes 06:57–07:04):

     | Measure | Serendip | Generator | Difference |
     |---|---|---|---|
     | Rate | 10.000/s (9.991–10.010) | 10.000/s | 0.00% |
     | 5xx rate | 1.071/s | 1.079/s | −0.7% |
     | p50 | 17.6 ms | 22.8 ms | same bucket |
     | p95 | 24.4 ms | 26.0 ms | same bucket |
     | p99 | 25.0–36.3 ms | 35.9 ms | same bucket |

   - **Tolerance (set here; the plan didn't, though the spec said it
     would):**
     - rate and 5xx rate within 2% over the steady minutes;
     - each percentile in the same Hubble bucket as the generator's
       value. Hubble's buckets are fixed (5, 10, 25, 50, 100 ms, ...),
       and `histogram_quantile` interpolates inside one. RP-085 records
       this precision.
     - The server's bucket counts confirm the placement: 531 of 5326 at
       ≤5 ms (the 500s, answered before the delay) and 4732 in
       (10, 25] ms.
   - **Console:** the HTTP requests block shows the same series
     (`ui-c4-srv-observability.png`).
5. **Traffic overlay. PASS.**
   - **As `verifier`:** `traffic` gives one edge `caller → srv` with its
     rate and 5xx rate (CLI, MCP). The console's overlay labels the edge
     "0.9/s, 10% failed" with a Failing status, and its table matches
     (`ui-c5-topology.png`).
   - **As `reader-c`,** who can read `caller`'s Deployment but not
     `srv`'s: no edges, `omitted: 4`.
6. **Flows across Projects. PASS.**
   - **As `reader-a`** (Project A only): `a-to-b`'s flows to B show B's
     end as `{"outside": true}`.
   - `b-caller`'s and `b-srv`'s flows answer 403 "missing
     serendip/observability/read". So does `bank`'s traffic, so no flow
     between two B Components is visible.
   - The calls are dropped by B's OpenChoreo policy (same-namespace
     only), so every row is a refusal.
   - Finding F4: an Owner sees B's end as outside too.
7. **Traces from a standard SDK. PENDING (after T013).**
   - `t012-otel` (otelhttp, OTLP/HTTP, nothing Serendip-specific) is
     built and loaded.
   - The tracing package can't be installed through `serendipd` until
     T013 sets its requirements: `packages list` says "not installable
     until its resource cost is measured".
   - `traces list` answers 503 through the CLI and MCP.
8. **Trace identity. PENDING through `serendipd`** (after T013). It held
   at the provider level in T007 (the spoofing Component, tasks.md T007).
9. **Trace scoping. PENDING** (after T013).
10. **Latency and error-rate rules. PASS after fix F1 and F2.**
    - **The rules:** `t012-srv-p95` (p95 above 200 ms over 5 min,
      warning) and `t012-srv-errors` (5xx above 30% over 5 min, at
      least 10 requests, critical), on `srv`, each evaluated every
      minute.
    - **First, both were refused:** "request metrics aren't available on
      this server" (F1). After the fix they evaluated at 24.5 ms and
      10.2%.
    - **Run 1** (before F2) fired at 07:17:16, 1 m 46 s after the slow
      pod started. It resolved at 07:20:16, only 2 m 14 s after the
      recovery: the 5-minute window wasn't honoured (F2).
    - **Run 2,** with `srv` changed to 400 ms delay and 60% 500s:
      - the slow pod started at 07:23:44, so the bound was 07:29:44;
      - p95 fired at 07:25:59 (451 ms), errors at 07:26:59 (32.6%);
      - recovered (pod started 07:27:42): errors resolved at 07:30:59
        (3 m 17 s), p95 at 07:31:59 (4 m 17 s);
      - p95 comes back under the threshold once the slow pod's samples
        leave the window, less up to one 1-minute scrape.
    - **Console:** the Alerts page lists all four with values and
      thresholds (`ui-c10-alerts.png`).
11. **Removing tracing. PENDING** (after T013, with criterion 7).
12. **Control-plane footprint:** T013.
13. **Node cost and the edge-1 default:** T013.
14. **Audit. PASS for what ran; the tracing install and removal are
    PENDING** (after T013).
    - **Audited, from the live store:**
      - the observability install (`verifier`, `http`, allow: Owner at
        `/`);
      - the visibility pass's `NetworkVisibility/request-visibility` row
        (`system:reconciler`), listing the two CiliumNetworkPolicies it
        applied;
      - the two request rules' creation (`cli (declared)`), and the
        evaluator's status writes;
      - the custom role and the role assignment changes. A deleted
        assignment is an Approval.
    - **Installing Cilium** is done by `serendip-agent bootstrap` or the
      kind script, before any control plane exists. So it has no audit
      row: the known gap `docs/architecture/bootstrap-agent.md` records.
      Serendip's own writes to the network layer (the L7 visibility
      policies) are audited.

## Findings

- **F1 (fixed): request and trace rules were refused in `serendipd`.**
  - `wireAlerting` never passed `RequestMetrics` or `Tracing` to the
    evaluator. Every `requestLatency`, `requestErrorRate` and
    `traceErrors` rule read "request metrics aren't available on this
    server" (or "need the tracing package") on any cluster.
  - Unit tests built the evaluator with the options set, so only the
    wiring was missing.
  - **Fix:** `wireAlerting` takes the network layer's `RequestMetrics`
    and an `alertTracing` source. That source refuses trace conditions
    when no tracing ProviderInstance exists (the reason is
    `reconcile.WhyTracingNotInstalled`), waits while the package
    installs, and reads once it's Ready.
  - **Test:** `TestAlertTracing`.
- **F2 (fixed): request and restart conditions dropped replaced pods.**
  - The condition queries joined Hubble's and the restart counters to
    `kube_pod_labels` at the evaluation instant. A pod gone by then (a
    rollout, an eviction) left the window early, so a rule resolved as
    soon as the bad pod was replaced.
  - The restarts condition (spec 023) had the same join.
  - **Fix:** join with `max_over_time(kube_pod_labels{...}[window])`, as
    the traffic overlay already did. The pods are those seen in the
    window. The selectors are unchanged (the same three UIDs), so
    nothing outside the target is read.
  - **Tests:** the query assertions in `conditions_test.go` and
    `networking_test.go`. The behaviour is shown by run 2 above.
- **F3 (fixed, wording): a refused flow's count isn't attempts.**
  - Cilium's export aggregates flow events: `aggregate.ingress_flow_count`.
    A refused connection's SYN retries each count, and the node logs a
    policy-verdict event beside each drop.
  - Measured: `probe-obs` (about 12 attempts a minute, a 5 s timeout)
    showed 216 a minute.
  - Allowed rows do count connections: `caller`'s 300 a minute is one
    new connection per request.
  - **Fix:** the catalogue summary, `docs/cli-and-mcp.md` and the
    console's Connections hint say what a refused count is. The hint
    also said "one row per connection", which was wrong: it's one row
    per minute per pair of ends.
- **F4 (fixed, wording): an outside end isn't always one you can't read.**
  - RP-087 accepts that flows name only the target's Environment's
    readable Deployments, up to 20. An end in another Environment is
    outside even for an Owner.
  - The console said "Not visible to you", and the summary said "named
    only if you may read it".
  - **Fix:** the console now says "Not shown", and the summary, docs and
    hint name the three causes.
- **F5 (fixed, nit):** the latency chart's zero tick read "0 µs"; it now
  reads "0 ms".

## Carried

- **Criteria 7, 8, 9, 11 and the tracing part of 14:** the tracing
  package is installable only once T013 has measured it. They are run
  through `serendipd` (API, CLI, MCP, console) after T013, on this
  cluster, with `t012-otel`.

## Also seen

- **RP-082 in practice:** an internal-only HTTP port is TCP to the
  platform, so `srv` had no request metrics until its port was exposed.
  An east-west HTTP service needs `expose` today to be measured.
