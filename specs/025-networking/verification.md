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
  package was installed through `POST /v1/provider-instances`. For
  criteria 7, 8, 9 and 11 (after T013) it was rebuilt at 3dc9709, with
  the tracing package's measured requirements, and again with fix F6.
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
7. **Traces from a standard SDK. PASS** (17:00 UTC, after T013; evidence
   `c789-api.txt`, `c7-cli-mcp.txt`, `ui-c7-traces.txt`,
   `ui-c7-traces-on.png`).
   - **Install:** `serendipd` rebuilt with T013's `TracingRequirements`.
     The package installed through `POST /v1/provider-instances` and was
     Ready in 46 s.
   - **The workloads:** in `t012-a`, three Components on `t012-otel:2`
     (otelhttp, OTLP/HTTP, configured only by `OTEL_*`; its source is
     now `profiles/edge-1/bench/traffic-app`):
     - `otel-srv`: exposed, 10% 500s, 15 ms delay;
     - `otel-caller`: calls it twice a second, a dependency;
     - `spoofer`: see criterion 8.
     Each got the trace endpoint in its first release; nothing in the app
     names Serendip.
   - **API:** each Deployment lists its traces. Opening one of
     `otel-srv`'s shows three spans in start order: `call` and
     `HTTP GET` from `otel-caller`, then `GET` from `otel-srv`. Each
     span names its Component and carries its HTTP attributes.
   - **CLI:** `serendip traces list` and `traces show` (exit 0). A Reader
     is refused with exit 3, "missing
     serendip/components/traces/read".
   - **MCP:** `deployments_traces_list` and `deployments_traces_get`, as
     the signed-in admin.
   - **Console:** `otel-caller`'s Observability tab lists its traces
     (newest first, OK/Error), opens one as a waterfall (`otel-caller`
     twice, then `otel-srv`) and shows a span's attributes.
8. **Trace identity. PASS after fix F6.**
   - **The spoofer:** `spoofer` calls only itself, with
     `OTEL_SERVICE_NAME=otel-srv`. Its `OTEL_RESOURCE_ATTRIBUTES` claim
     `otel-srv`'s pod name and namespace, and its OpenChoreo component,
     environment and project UIDs, all copied from the live pod's labels.
   - **Its spans are attributed to `spoofer`,** from the sending pod. None
     of the 20 traces listed under `otel-srv` is the spoofer's. Its
     spans kept none of the claimed `service.*`, `k8s.*` or `openchoreo`
     attributes.
   - **Finding F6:** an Owner opening the spoofer's trace ID through
     `otel-srv`'s route got 200, showing only `spoofer`'s spans. After the
     fix that answers 404 ("not found for Deployment"), as a trace in which
     `otel-srv` has no span should.
9. **Trace scoping. PASS.**
   - **`reader-c`:** Reader on Application `shop` (Reader leaves out trace
     read), plus a custom role `t012-trace-reader` with only
     `serendip/components/traces/read`, at `otel-caller`'s Deployment.
   - **`otel-srv`'s traces:** 403 for the list, and 403 for one of its
     trace IDs opened under `otel-srv` ("missing
     serendip/components/traces/read at Deployment/…").
   - **`otel-caller`'s traces:** listed. The caller→server trace opens
     with only `otel-caller`'s spans and `partial: true`, so
     `otel-srv`'s span isn't shown or counted.
   - **A guessed ID:** the spoofer's trace ID, guessed under
     `otel-caller`, is 404.
   - **`reader-a`** (Reader at Project `t012-a`): 403 on any trace read.
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
11. **Removing tracing. PASS** (`c11-api.txt`).
    - **Before:** a `traceErrors` rule on `otel-srv` (above 999 traces
      with errors in 5 min) evaluated to 66.
    - **The removal:** `DELETE /v1/provider-instances/{tracing}` at
      16:59:05 answered 204 with Approval `01a20958…` (Owner, decided by
      policy). It was Executed at once and CleanedUp at 17:00:56, after
      the store's stream deletion.
    - **After:**
      - the collector pod is gone, and trace reads answer 503 ("traces
        need the tracing package … it isn't installed");
      - the rule is kept, `Evaluating=False/ConditionRefused` with "trace
        conditions need the tracing package installed";
      - stored logs and metrics (CPU, memory and the request series)
        still answer;
      - the three traced Components restarted once (17:00:55), without
        the trace variables.
    - **Stored traces deleted:** tracing was reinstalled (Ready in 23 s).
      The window from 16:40 to the removal then lists 0 traces for all
      three Components.
12. **Control-plane footprint. PASS** (T013,
    `profiles/edge-1/bench/results/2026-11-23-spec025-simulated-t470/`):
    `serendipd` at idle read 33.2 MiB (kindnet) and 82.1 MiB (Cilium),
    against spec 024's 79.0 MiB. The Cilium figure is the Windows working
    set; a restart on the same stack read 28.6 MiB. Every group is
    within tolerance of spec 024's run.
13. **Node cost and the edge-1 default. PASS** (T013):
    - Cilium costs +443 MiB and +92 mcores at idle, and +461 MiB and
      +128 mcores under 10 requests a second (RP-089).
    - Tracing costs +145 to +180 MiB and ~10 mcores, and ~730 MiB a day
      of the logs store (RP-088).
    - The edge-1 default stays Cilium. ADR 0023 (T015) gives the reason.
14. **Audit. PASS** (`c14-audit.txt`; the tracing rows in
    `c14-tracing-audit.txt`).
    - **The tracing package:** the install (`verifier`, "allow: Owner at
      /"), the removal ("approved: 01a20958… by policy"), its Approval's
      rows and the reinstall are audited, with the reconciler's status
      writes. The removal needed an Approval (constitution 5).
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
- **F6 (fixed): a trace opened under a Deployment that has no span in it.**
  - `GET /v1/deployments/{id}/traces/{traceId}` answered 404 only when
    none of the caller's readable Deployments in the Environment had a
    span in the trace. So an Owner could open any trace of the
    Environment through any Deployment's route. That isn't a leak (they
    may read every span shown), but the trace was "shown under" a
    Component it has no part in.
  - **Fix:** a whole trace with no span from the route's own Deployment
    is 404 too. A truncated trace is exempt, since its own spans may be
    past the cap. The catalogue summary and `docs/cli-and-mcp.md` say "one
    trace this Deployment is part of".
  - **Test:** `TestSpec025Criterion9_Traces` (a trace of web's alone,
    opened through api's Deployment, by a Log Reader and by the Owner).
  - Confirmed live after a rebuild: the spoofer's trace under `otel-srv`
    is 404.
- **F7 (fixed, test tooling):** `t012-otel` used `TARGET` as a URL, but
  OpenChoreo gives a dependency on a port other than 80 as `host:port`.
  So the caller's requests failed before reaching the network (found
  in T013's first run). It now adds `http://` when there's no scheme
  (`t012-otel:2`).

## Carried

- Nothing: criteria 7, 8, 9, 11 and the tracing part of 14 ran after
  T013, through `serendipd` on this cluster.

## Also seen

- **RP-082 in practice:** an internal-only HTTP port is TCP to the
  platform, so `srv` had no request metrics until its port was exposed.
  An east-west HTTP service needs `expose` today to be measured.
