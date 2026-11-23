# Plan: Spec 025, Networking: Cilium, Hubble and traces

Status: Approved, 2026-11-19. Amended 2026-11-20 with T001's findings,
and 2026-11-23 with T006's and T007's.

Spec: `spec.md` (approved 2026-11-19). Epic #66.

## Checked live in T001 (2026-11-20)

T001 ran on a scratch kind cluster `t025` with Cilium 1.20.5 and a
`rancher/k3s` container. The detail is in
`providers/networking/cilium/VERSIONS.md` and
`providers/observability/openchoreo/VERSIONS.md` (spec 025 section). The
decisions below are already updated. What changed from the research:

- **Envoy retries every 5xx three times** by default once L7 visibility
  is on, which hid a 10% error rate completely. Retries and path
  normalization are turned off (decision 4).
- **Hubble's HTTP labels** must come from `labelsContext`, not
  `sourceContext`/`destinationContext`, for OpenChoreo's `metric=http`
  join (decision 4). That query has no p95 (decision 7).
- **The flow export** needs filtering by the `component-uid` label, a
  DNS deny, a field mask of workloads and identities, and 60 s
  aggregation. Without them it wrote 10 MB a minute; with them, about
  7.5 MiB a day (decision 9).
- **The tracing adapter can't scope** by UIDs or names, so traces are
  read from OpenObserve with SQL, and the adapter isn't installed
  (decisions 10 and 12).
- **Confirmed as planned:** the mirrored policy keeps OpenChoreo's
  isolation; the K3s `HelmChart` bootstrap; IPAM `kubernetes` pod
  ranges; identity attestation through Serendip's collector config.

## Research (2026-11-19, oss-researcher; checked live in T001, above)

- **Cilium 1.20.5** (17 Nov 2026), Apache-2.0, chart `cilium/cilium` at
  `https://helm.cilium.io`. Kernel 5.10+ (Ubuntu 22.04/24.04 fine).
  - K3s needs `--flannel-backend=none --disable-network-policy`; the node
    is NotReady until Cilium runs. kind needs
    `networking.disableDefaultCNI: true`.
  - `ipam.mode=kubernetes` uses the Node's `spec.podCIDRs` (K3s and kind
    both allocate them); cluster-pool keeps them in `CiliumNode` only.
  - kube-proxy replacement needs `k8sServiceHost/Port`, cgroup v2 and is
    only safe at cluster creation.
  - Envoy runs as its own DaemonSet (`cilium-envoy`) and is used only
    for L7. Hubble relay (gRPC :4245) and UI are optional.
  - No published idle footprint: measure.
- **Hubble metrics:** `httpV2` (`hubble_http_requests_total`,
  `hubble_http_request_duration_seconds`), `drop`, `flow`, on :9965 with
  a headless `hubble-metrics` Service carrying `prometheus.io/*`
  annotations. **L7 HTTP metrics are only emitted for endpoints a
  CiliumNetworkPolicy with an L7 `http` rule selects**; the old
  visibility annotation is gone. A policy selecting an endpoint makes it
  default-deny unless `enableDefaultDeny: {ingress: false}`.
- **Hubble flow export** to a file on the node
  (`hubble.export.static`: path, size, backups, `allowList`/`denyList`
  as FlowFilters, `fieldMask`), one JSON object per line, no relay
  needed.
- **OpenChoreo's `metric=http`** (metrics module 0.9.x, which Serendip
  already installs for its adapter) reads exactly `hubble_http_*`,
  joined to `kube_pod_labels`, `reporter="server"`, with P50/P90/P99 and
  request, success and failure counts. So Hubble's metrics fill spec
  023's empty section without new query code.
- **Tracing module** `observability-tracing-openobserve` 0.3.2 (OpenChoreo
  1.3.3 current; Serendip runs the 1.3.1 plane chart):
  - an OTel Collector Deployment (contrib 0.163.2, OTLP 4317/4318),
    `k8sattributes` → `tail_sampling` → OpenObserve; an adapter on
    :9100; the plane needs `observer.tracingAdapter.enabled=true`;
  - reuses an existing OpenObserve (`openobserve-standalone.enabled=false`,
    `common.openObserveHost`), Secret `openobserve-admin-credentials`
    (which the logs module already has);
  - tail sampling is on, rate-limited to **10 spans a second**;
  - **identity is spoofable as shipped:** `k8sattributes` has no
    `pod_association`, and the default trusts a span's own `k8s.pod.ip`
    before the connection's address;
  - OpenChoreo doesn't give workloads an OTLP endpoint;
  - query: `traces/query` (window, limit, sort; no duration or error
    filter, no cursor; each trace has `durationNs`, `hasErrors`,
    `spanCount`, root span) and `traces/{id}/spans/query` (spans with
    `status`, attributes on request). A span-detail call takes no scope.
  - Collector 100Mi/200Mi, adapter 128Mi/256Mi (chart requests/limits).

## Decisions

1. **Networking is a capability Serendip observes, not one it
   installs.** The CNI has to run before the node is Ready, so it
   belongs to the install path (decision 5), not to a package
   `serendipd` adds later. `serendipd` detects what runs and registers
   the matching provider: `cilium` or `external`. It never upgrades or
   reconfigures the CNI; it reports drift from the values it expects
   (Hubble metrics and export off, for example) as a reason on the
   networking status. Upgrading Cilium is out of scope (a later spec).
   Azure analogue: the VNet's network plugin (Azure CNI vs kubenet) is
   chosen at AKS cluster creation and shown on the cluster, not changed
   in place.
2. **`core/reconcile/networking.go`:**

   ```go
   type PolicyEnforcement string // "enforced", "notEnforced", "unknown"

   type NetworkFacts struct {
       Plugin, Version   string            // reported names are data
       PolicyEnforcement PolicyEnforcement
       PolicyReason      string            // e.g. "the CNI's own controller"
       Flows             bool
       RequestMetrics    bool
       PodRanges         []netip.Prefix
       Unavailable       []string          // reasons, Serendip's words
   }

   type NetworkingProvider interface {
       Facts(ctx context.Context) (NetworkFacts, error)
   }

   // FlowSource is the plain value one provider hands another
   // (provider-model.md's cooperation pattern): where the CNI writes its
   // flow records on each node, so the observability package can collect
   // them. Zero when the CNI keeps none.
   type FlowSource struct{ HostPath, Format string }
   ```

   Product names appear only as reported data (`Plugin`), as
   `ProviderID` already does. `serendipd` reads `Facts` every 5 minutes
   (with the cluster ranges), caches it, and serves it.
3. **`external` provider** (`providers/networking/external`): reads
   kube-system DaemonSets and node annotations. kindnet → enforced
   (kindnet's controller); Flannel on K3s → `unknown` ("K3s's own policy
   controller, unless it was turned off at install; Serendip can't
   see"); anything else → plugin "unknown", enforcement `unknown`. Flows
   and request metrics are always false, with the reason spec criterion
   2 gives.
4. **`cilium` provider** (`providers/networking/cilium`):
   - **Facts** from the `cilium` DaemonSet's image tag, `cilium-config`
     (Hubble metrics list, export settings, IPAM mode) and the nodes'
     `podCIDRs` (plus `CiliumNode` pools if IPAM isn't `kubernetes`).
   - **Request visibility** (the only thing it writes): for each
     Component with an HTTP endpoint, a `CiliumNetworkPolicy`
     `serendip-l7-<networkpolicy>` beside the NetworkPolicy OpenChoreo
     gives that Component. It **copies that policy's ingress peers and
     ports** and adds `http: [{}]` on the HTTP ports only, with
     `enableDefaultDeny: {ingress: false, egress: false}`. Allows are a
     union, so an L7 rule from "anywhere" would widen OpenChoreo's
     isolation; mirroring the peers can't. Owner reference: the
     NetworkPolicy, so Kubernetes deletes it with the Component. Ensured
     on an interval like the gateway (spec 014), re-derived when the
     source policy changes. T001 checks live that a peer refused before
     is still refused, and that traffic on the port goes through the
     proxy.
   - **Pinned values** (`values.yaml`, embedded; T001's final is
     `testdata/t001-values.yaml`): IPAM `kubernetes`, kube-proxy kept
     (decision 6), `operator.replicas=1`, Hubble on with metrics
     `httpV2:labelsContext=source_namespace,source_pod,destination_namespace,destination_pod`
     (the labels OpenChoreo's adapter joins on, checked in T001), `drop`,
     `flow`, relay and UI off, and the static exporter (decision 9).
   - **The L7 proxy must not change behaviour** (T001): `extraConfig`
     sets `http-retry-count: "0"` (Cilium's default 3 retried every 500,
     so a 10% error rate showed as none, and the load grew) and
     `http-normalize-path: "false"` (no path rewriting). The provider
     reports drift if either is changed back.
5. **Install path, Cilium from the first boot:**
   - **K3s** (`agent/bootstrap`): `serendip-agent bootstrap --cni
     cilium|default`, `cilium` recommended. With `cilium`, K3s gets
     `--flannel-backend=none --disable-network-policy`, and the agent
     writes a K3s `HelmChart` manifest (`bootstrap: true`, so its job
     runs on the host network before any CNI) into K3s's auto-deploy
     directory, with the pinned chart and values. K3s's own Helm
     controller installs it; the agent waits for the node to be Ready.
     No Helm in the agent. The values are a copy, which a test keeps
     identical to the provider's (spec 023's pattern for Prometheus).
     `--dry-run` prints both.
   - **kind** (development and bench): `kind-t470-setup.sh` gains
     `CNI=cilium`, which uses `disableDefaultCNI` and installs the same
     chart and values with Helm. A new dev cluster `serendip-t025`
     (Cilium); `serendip-t018` stays kindnet, the `external` test case.
   - Changing an existing cluster's CNI is documented as a reinstall
     (`operations.md`), nothing more.
6. **kube-proxy stays.** Replacing it needs the API server's address at
   install, cgroup v2 and can't be undone in place; the saving on one
   node is small. Recorded in build-vs-adopt with a revisit at the
   bench (if Cilium plus kube-proxy is over budget, kube-proxy-free is
   the first thing to measure).
7. **Request metrics** come from Hubble through the existing Prometheus:
   - the plain Prometheus gets a `hubble` scrape job: `dns_sd_configs`
     on the headless `hubble-metrics` Service (one target per node), and
     `metric_relabel_configs` keeping only `hubble_http_requests_total`,
     `hubble_http_request_duration_seconds_{bucket,sum,count}`,
     `hubble_drop_total` and `hubble_flows_processed_total` (T001);
   - `Metrics` gains a `Requests` block (rate, error rate, p50/p95/p99),
     **all from Serendip's own PromQL** (amended in T006): the adapter's
     `metric=http` has no p95 (T001) and its "unsuccessful" isn't
     defined as 5xx, which the alert conditions are, so using it for four
     series would let the chart and the alert disagree. The queries join
     `hubble_http_*{reporter="server"}` to `kube_pod_labels` by the three
     UIDs, as the restarts condition does. P90 isn't shown;
   - `Unavailable` says "requests: the cluster's CNI doesn't provide
     request metrics" on an `external` cluster, and "requests: no HTTP
     endpoint" for TCP-only Components;
   - non-HTTP traffic: connection counts from the flow records
     (decision 9), not a metric.
8. **Traffic overlay:** one PromQL per Application and Environment,
   `sum by (source, destination component uid)` of
   `rate(hubble_http_requests_total[w])` joined to `kube_pod_labels`,
   through the Service proxy as the restarts condition does. Edges whose
   two ends the caller can't both read are dropped before answering
   (spec criterion 5). `ObservabilityProvider` gains
   `Traffic(ctx, env, targets []LogTarget, q MetricsQuery) ([]TrafficEdge, error)`.
9. **Flows:** Hubble's static exporter writes **drops and new
   connections, aggregated per minute** (T001 settled every setting):
   - `allowList`: verdict `DROPPED`, or `FORWARDED` with a TCP SYN and
     not a reply, or UDP and not a reply, each with either end carrying
     `k8s:openchoreo.dev/component-uid` (`source_label`/`destination_label`;
     a namespace prefix doesn't work in `source_pod`);
   - `denyList`: DNS (port 53 either way) and ICMPv6, which were three
     quarters of the volume;
   - `fieldMask` and `fieldAggregate`: time, verdict, drop reason,
     direction, both ends' namespace, `workloads` and numeric identity,
     and the destination port; `aggregationInterval: 60s`. Each record
     carries `aggregate.{ingress,egress}_flow_count`;
   - a Component is named by its workload (the Deployment's name, which
     OpenChoreo derives from Serendip's IDs and the workload can't
     change), not by labels: labels made records about 3 KB, workloads
     about 550 B;
   - measured: 8–10 records a minute for three Components talking,
     about 7.5 MiB a day. Without the filters it was 10 MB a minute;
   - both ends of a connection between Cilium endpoints write a record
     (EGRESS, INGRESS): reads de-duplicate them;
   - a bare pod (no controller) is shown by namespace only.
   Not every packet or HTTP request: request detail is the metrics' job.
   - The observability package's Fluent Bit (Serendip already ships its
     config, RP-064) gets a tail input on that path, from the
     `FlowSource` value `cmd/serendipd` passes, through the subchart's
     `extraVolumes`/`extraVolumeMounts` (a read-only hostPath; T001
     found the keys, T006 checks the tail live), into an OpenObserve
     stream `serendip_flows`, with the logs' retention.
   - Reads go to OpenObserve's search API with the admin Secret, over
     the port-forward `usage.go` already uses: SQL built by Serendip
     from the target's namespace and Deployment name (`scope.go`
     already derives them from the IDs).
   - **Scoping:** a flow is returned if either end is a target the
     caller can read; the other end's names are kept only if the caller
     can read it too, else "outside your access" (spec criterion 6).
     **Amended in T009:** the provider names only its targets, so the
     API passes the other Deployments of the Environment the caller may
     read as `FlowQuery.Peers`: named, never searched (at most
     `MaxLogTargets`; past that an end shows as outside). The API also
     clears any named end that isn't the target or a peer. A network
     layer without flows or request metrics answers 409 with the facts'
     reason, never an empty list; 503 until the facts are first read.
     `ObservabilityProvider.Flows(ctx, targets, q FlowQuery) (FlowPage, error)`,
     cursor like logs.
   - Hubble relay (gRPC) is rejected: a gRPC client is a dependency
     needing an ADR, and relay costs memory for what the file gives.
10. **Traces are a separate package**, `openchoreo-tracing` (capability
    `tracing`), so removing it leaves logs and metrics (spec criterion
    11). It requires the observability package Ready, and refuses to
    install otherwise.
    - Installs module 0.3.2 with the bundled OpenObserve off, the
      existing one and its Secret reused, and **its adapter off**
      (`adapter.enabled=false`): it can't scope by UIDs or names (T001),
      so Serendip reads OpenObserve itself (decision 12), and 128 MiB is
      saved. The plane chart needs no change at 1.3.1 (T001: there is no
      `tracingAdapter.enabled`).
    - **Serendip's own collector config** (as spec 023 did for Fluent
      Bit), through `opentelemetry-collector.configMap.existingName`: a
      `transform` processor keeps only an allow-list of resource
      attributes (`service.*`, `telemetry.*`, `process.*`,
      `deployment.*`, lowercase) and deletes span attributes named
      `service.*` in any spelling; then `k8s_attributes` with
      `pod_association` from the **connection only**; then a `filter`
      drops spans with no component UID. A workload can't claim
      another's identity (spec criterion 8; checked in T001: a
      Component claiming another's pod IP, UID and pod name had every
      span stored under its own). **Amended in T007:** T001's
      `delete_matching_keys` on `k8s.*|openchoreo.dev/*|container.*` let
      other spellings through: the store lowercases keys and turns other
      characters into `_`, so `OpenChoreo.dev/component-uid` (resource)
      or `service.openchoreo.dev/component-uid` (span) from an
      unlabelled pod landed on the attested column (RP-086,
      `testdata/t007-spoof-check.json`).
    - The installer waits for the collector's readiness itself: T001's
      first install was marked `failed` when the collector crash-looped
      past Helm's wait.
    - **Tail sampling off** (10 spans a second drops nearly everything
      on a real load; tail sampling also holds traces in memory). A
      `memory_limiter` processor and the retention bound it instead;
      measured in T013.
    - No NetworkPolicy of Serendip's own on the collector: the plane
      chart's `observability-plane-unrestricted-ingress` already admits
      any pod to it, and identity doesn't depend on who connects (T001).
    - Removal deletes the `traces` stream (stated in `Removal`), behind
      the existing package-removal approval (spec criterion 14).
11. **Workloads are given the endpoint:** while `tracing` is Ready, the
    application provider adds `OTEL_EXPORTER_OTLP_ENDPOINT` and
    `OTEL_SERVICE_NAME` (the Component's name) to each Component's
    environment at its **next deploy**, from a value `cmd/serendipd`
    passes (the cooperation pattern; neither provider names the other).
    No forced rollout of every Component at install; the console says
    "redeploy to send traces". A Component that sets either variable
    itself keeps its own. **Amended in T007:** an unpinned Deployment's
    release is recomputed on every pass, so "next deploy" is the
    Component's **next new release** (a change to it): a release that
    already exists without the variables is kept. Removing tracing
    does roll each traced Component once (RP-086). So `cmd/serendipd`
    (T008) passes the endpoint while the `tracing` ProviderInstance
    exists, read from the store before the first pass, not while it
    reads Ready: a collector restarting or settling must not roll every
    traced Component twice.
12. **`TracingProvider`** (`core/reconcile/tracing.go`):

    ```go
    type TracingProvider interface {
        SearchTraces(ctx context.Context, targets []LogTarget, q TraceQuery) (TracePage, error)
        Trace(ctx context.Context, t LogTarget, traceID string) (Trace, error)
        EvaluateCondition(ctx context.Context, t LogTarget, c resource.AlertCondition, window time.Duration) (ConditionValue, error)
    }
    ```

    - **Reads are SQL on OpenObserve's `default` traces stream** (T001),
      over the port-forward with the admin Secret, as flows are:
      `WHERE service_openchoreo_dev_component_uid = … AND
      service_openchoreo_dev_environment_uid = …`, fields the collector
      attests. Values are Serendip's own IDs, never caller text in the
      SQL.
    - `TraceQuery`: window, limit (≤ 200), `ErrorsOnly`, `MinDuration`,
      all in the SQL (the adapter had none of them).
    - `Trace` reads the trace's spans **within the target's UIDs only**;
      none is not-found, so a guessed trace ID of another Component
      answers 404 (criterion 9). Spans of other Components in the same
      trace are counted, not shown, unless the caller can read them too.
      **Decided 2026-11-23 (after T007):** the API merges the spans of
      every Deployment in the trace's Environment the caller holds
      `…/traces/read` on (not-found unless one of them has a span), and
      answers `partial: true` when spans remain that it can't show.
      **Amended in T009:** one `Trace` call with all of them
      (`Trace(ctx, targets []LogTarget, traceID)`, at most
      `MaxLogTargets`), not one each: each call is two queries over the
      whole retention, so twenty readable Deployments would have been 40
      queries to open one trace on a T470. One call is two queries, and
      `Hidden` is then exactly the spans of no readable Deployment. The count isn't returned: a span's parent ID
      already shows another service took part, but the count would add
      the other Components' fan-out (as flows show "Outside", not how
      many). Azure: Application Insights' transaction view shows the
      components you can read and marks the rest inaccessible. The
      provider's `Hidden` stays internal.
13. **Alert conditions** (spec 024's model, `core/resource/alerting.go`):
    - `requestErrorRate` (percent of 5xx over the window) and
      `requestLatency` (`percentile` 95 or 99, milliseconds), from
      Hubble's metrics through `ObservabilityProvider.EvaluateCondition`;
    - `traceErrors` (count of traces with errors), through
      `TracingProvider.EvaluateCondition`: one `count(DISTINCT
      trace_id)` query;
    - on a Component or an Application, like `metric`;
    - without the source (an `external` CNI, tracing not installed) the
      rule reads `ConditionRefused` with the reason, as spec 024's
      refused conditions do; it isn't deleted.
14. **Authorization** (`core/policy/builtins.go`), Azure-style:
    - request metrics and the traffic overlay: the existing
      `serendip/observability/read` on the Deployment, as CPU and memory;
    - flows: the same action, since they're connection metadata (Azure:
      Network Watcher flow logs read with Reader on the resource);
    - **traces: a new data action `serendip/components/traces/read`,**
      left out of Reader like `…/logs/read`, because span attributes
      carry URLs, queries and user data (Azure: Application Insights'
      data is read separately from the resource, like Log Analytics'
      table-level read);
    - `GET /v1/networking`: `serendip/platform/read`.
15. **The notifier's own ranges** (RP-076) add `NetworkFacts.PodRanges`
    to the nodes' and ServiceCIDRs' ranges. With IPAM `kubernetes` they
    match the nodes' anyway; the merge covers cluster-pool installs.
16. **The edge-1 default is decided by the bench** (T013, constitution
    4): Cilium (agent, operator, Envoy) plus Hubble metrics and export,
    against kindnet on the simulated T470, idle and under the
    observability load. ADR 0023 records the numbers and the default;
    if Cilium doesn't fit, `default` stays recommended for edge-1 and
    Cilium for `small-3`, and the wizard labels say so.

## API, CLI, MCP

| Route | Action | Notes |
|---|---|---|
| `GET /v1/networking` | `platform/read` | `NetworkFacts`, plus when they were read |
| `GET /v1/deployments/{id}/metrics` | `observability/read` | gains `requests` |
| `GET /v1/deployments/{id}/flows` | `observability/read` | `from`, `to`, `verdict` (`all`, `dropped`), `limit`, `before` |
| `GET /v1/applications/{id}/traffic` | `observability/read` on each end | `environment`, `from`, `to` |
| `GET /v1/deployments/{id}/traces` | `components/traces/read` | `from`, `to`, `errorsOnly`, `minDuration`, `limit` |
| `GET /v1/deployments/{id}/traces/{traceId}` | `components/traces/read` | spans, attributes |

CLI (`serendip networking show`, `serendip flows`, `serendip traces
list|show`, `serendip traffic`) and MCP tools come from the catalogue
(spec 021's parity test). Traces are off MCP by default like logs
(spec 021's rule for data actions), unless that rule says otherwise
at T010.

## UI

- **Component Observability tab:** a Requests section (rate, error rate,
  latency) in place of "Not available yet"; Flows (dropped first, with
  the policy reason); Traces (list, then a span waterfall).
- **Topology diagram:** an overlay toggle, edge labels req/s and error %,
  colored by error rate.
- **Settings → Platform:** networking facts, and "not available" reasons.
- **Alert rule form:** the three new condition types.

## Explicitly NOT done

- Installing, upgrading or reconfiguring Cilium from `serendipd`
  (decision 1); live CNI migration.
- kube-proxy replacement (decision 6); WireGuard/IPsec, BGP, egress
  gateways, ClusterMesh, Cilium Gateway API, Hubble relay and UI.
- User-authored network policies or a Network resource (spec Out).
- L7 visibility for gRPC, Kafka or DNS; per-HTTP-request flow records.
- Policies on the build pods (RP-049).
- Components' own OTLP metrics and logs (the collector drops them).
- Trace sampling controls in the API (tail sampling is off; a later
  spec if needed).
- Composite conditions.

## Risks (to the risk register as found)

- **The L7 proxy on every HTTP Component:** Envoy's memory and
  per-request latency (T013 measures; a per-Environment off switch is
  the fallback, not built now). It also changes behaviour unless set
  not to (T001: retries and path normalization, both off now); other
  Envoy defaults (timeouts: `http-request-timeout` 3600 s,
  `http-stream-idle-timeout` 300 s) apply to proxied traffic only.
- **A Cilium upgrade changing a default** the observe-only proxy relies
  on: the provider reports drift on the two settings above.
- **Mirroring OpenChoreo's NetworkPolicy:** if the upstream policy
  changes shape (peers Serendip can't translate), the provider skips
  that Component with a reason instead of guessing (no L7 metrics there).
- **Flow volume:** one record per connection, not per packet; a chatty
  workload still fills the stream. Bounded by retention and the disk
  rule (spec 024's `packageDisk`).
- **Traces without sampling** under load: memory limiter drops, and
  disk. Measured.
- **Hubble metric cardinality** with pod contexts: fine at edge-1's
  scale, recorded with a revisit for `small-3`.
- **The agent's values copy** drifting from the provider's: a test.
- **K3s's `HelmChart` `bootstrap`:** checked in T001 (node Ready,
  CoreDNS running). The chart is fetched from `helm.cilium.io` at first
  boot, so an offline install needs the chart in K3s's static charts
  directory instead (T005 decides).
- **Admin credentials for flow reads** (as `usage.go`): RP-054's
  existing entry extended.

## Files touched (outline)

- `core/reconcile`: `networking.go` (new), `tracing.go` (new),
  `observability.go` (`Requests`, `Traffic`, `Flows`), `alerts.go`
  (routing the new conditions).
- `core/resource/alerting.go`: three condition types and validation.
- `core/policy/builtins.go`: the traces action.
- `providers/networking/cilium/` and `providers/networking/external/`
  (new), `VERSIONS.md`.
- `providers/observability/openchoreo/`: Hubble scrape and allowlist
  (`prometheus-values.yaml`), `conditions.go`, `observer.go`, flows
  (Fluent Bit input, OpenObserve search), the tracing installer and its
  collector config, `VERSIONS.md`.
- `providers/application/openchoreo`: the OTLP environment variables.
- `providers/kubernetes/ranges.go`: merge.
- `agent/bootstrap`: `--cni`, K3s flags, the HelmChart manifest.
- `cmd/serendipd`: detection, registry, cooperation values, catalogue.
- `api/`: `networking_handler.go`, `flows`, `traffic`, `traces`
  handlers; catalogue.
- `cmd/serendip`, MCP: from the catalogue.
- `ui/src/screens`: Components (Observability tab), `TopologyDiagram`,
  Settings, Alerts form; `lib/`.
- `profiles/edge-1/bench/`: `kind-t470-setup.sh` `CNI=cilium`, the
  values, results.
- Docs: `docs/architecture/networking.md` (new), `capability-model.md`,
  `operations.md`, `bootstrap-agent.md`, ADR 0023, risk register,
  build-vs-adopt.
