# Networking

The Networking capability: which CNI a cluster runs, what Serendip
learns from it, and the request metrics, connection records and traces
built on top (spec 025, ADR 0023). Design detail:
`specs/025-networking/plan.md`. Upstream versions and what to re-check on
an upgrade: `providers/networking/cilium/VERSIONS.md`,
`providers/observability/openchoreo/VERSIONS.md`.

## Observed, not installed

The CNI runs before the node is Ready, so it's chosen by the install
path, never by `serendipd`:

| Install | CNI | Chosen by |
|---|---|---|
| K3s, `serendip-agent bootstrap --cni cilium` (default) | Cilium 1.20.5, Serendip's values | a K3s `HelmChart` the agent writes (`bootstrap-agent.md`) |
| K3s, `--cni default` | Flannel, K3s's policy controller | K3s |
| kind (dev, bench), `CNI=cilium kind-t470-setup.sh up` | Cilium, the same values | Helm, from the script |
| kind, default | kindnet | kind |

`serendipd` detects what runs (`kube-system` DaemonSets) and registers
one networking provider: `cilium` (`providers/networking/cilium`) or
`external` (`providers/networking/external`, for anything else). It never
upgrades or reconfigures the CNI. Changing a cluster's CNI is a reinstall
(`operations.md`, "Networking").

### Facts

`core/reconcile.NetworkingProvider.Facts` returns `NetworkFacts`, read
every re-ensure interval (5 minutes) and served at `GET /v1/networking`
(`serendip/platform/read`), with when they were read; 503 until the
first read.

| Field | `cilium` | `external` |
|---|---|---|
| `plugin`, `version` | `cilium`, the DaemonSet's image tag | `kindnet` (and tag), `flannel`, or `unknown` |
| `policyEnforcement` | `enforced` (Cilium) | kindnet `enforced` from its 2024-08 image; Flannel `unknown` (K3s's controller, unless turned off at install); `unknown` otherwise |
| `flows`, `requestMetrics` | true unless Hubble's settings drifted | always false |
| `podRanges` | the nodes' `podCIDRs` (and `CiliumNode` pools if IPAM isn't `kubernetes`) | the nodes' `podCIDRs` |
| `unavailable` | drift, agents not ready, Components skipped by request visibility | "flows: the cluster's CNI doesn't record connections", "requests: ... doesn't provide request metrics" |

The pod ranges join the nodes' and ServiceCIDRs' ranges that alert
delivery refuses (RP-076). The console shows the facts on the
**Platform** page, "Network layer".

### What the `cilium` provider writes

Only its own objects, re-ensured on the interval:
- **Request visibility:** a `CiliumNetworkPolicy`
  `serendip-l7-<networkpolicy>` beside each NetworkPolicy OpenChoreo
  gives a Component with an HTTP endpoint. It copies the policy's
  ingress peers, keeps its HTTP ports only, adds `http: [{}]` and turns
  default-deny off, so Envoy sees the requests without admitting anyone
  new. Owned by the NetworkPolicy. A shape it can't copy is skipped with
  a reason (RP-082). If Envoy's pinned `http-retry-count: "0"` or
  `http-normalize-path: "false"` drifts, every such policy is removed
  (RP-081). A write is audited as an external change (RP-090).
- **The node guard:** `serendip-node-metrics-guard`, a deny-only
  `CiliumClusterwideNetworkPolicy` closing the node's metrics ports
  (9962 agent, 9963 operator, 9964 Envoy, 9965 Hubble) to every pod
  outside `kube-system` and `openchoreo-observability-plane`. Without
  it, any workload could read every Component's request metrics (RP-091).

## Request metrics and the traffic overlay

- **Source:** Hubble's `httpV2` metrics (labels from `labelsContext`:
  source and destination namespace and pod), scraped by the
  observability package's Prometheus through a `hubble` job on the
  headless `hubble-metrics` Service, keeping only the request, duration,
  drop and flow series.
- **Reads** are Serendip's own PromQL, joined to `kube_pod_labels` by
  the three OpenChoreo UIDs, `reporter="server"`:
  `GET /v1/deployments/{id}/metrics` gains `requests` (rate, error rate
  as 5xx, p50/p95/p99), and `GET /v1/applications/{id}/traffic` gives
  the edges between an Application's Components in one Environment.
  Edges whose two ends the caller can't both read are dropped.
- Permission: `serendip/observability/read`, as CPU and memory.
- Percentiles are interpolated in Hubble's buckets (RP-085). A TCP-only
  Component shows "requests: no HTTP endpoint".

## Connection records (flows)

- Hubble's static exporter writes, on each node,
  `/var/run/cilium/hubble/events.log` (10 MB, 5 backups): drops, and new
  TCP and UDP connections with an end at a Component, DNS and ICMPv6
  denied, masked to verdict, drop reason, direction, both ends'
  namespace, workload and identity, and the port, aggregated per minute.
  About 7.5 MiB a day for three Components talking (T001).
- The provider hands this `FlowSource` to the observability package
  (the cooperation pattern, `provider-model.md`); its Fluent Bit tails
  the file read-only into OpenObserve's `serendip_flows` stream, with the
  logs' 3-day retention (RP-084).
- `GET /v1/deployments/{id}/flows` (`from`, `to`, `verdict`
  `all`/`dropped`, `limit`, `before`): Serendip's SQL with the target's
  namespace and Deployment name. A record is returned if either end is
  the target; the other end is named only if the caller can read it too
  (`peers`: the caller's other readable Deployments in the Environment,
  at most 20, RP-087), else "outside your access".
- A network layer without flows or request metrics answers **409** with
  the facts' reason, never an empty list.

## Traces

- **The `tracing` package** (`openchoreo-tracing`, optional, needs the
  observability package Ready): OpenChoreo's tracing module 0.3.2, its
  OTel Collector only, writing to the logs package's OpenObserve. Its
  adapter, bundled store and tail sampling are off; a memory limiter and
  the retention bound it (RP-092). Requirements (T013): idle 180 MiB /
  10 mcores, peak 370 MiB / 200 mcores, about 730 MiB of disk a day at
  10 traced requests a second, kept 3 days in the logs store's 5 GiB
  (RP-088).
- **Identity is attested, not claimed.** Serendip's collector config
  keeps an allow-list of resource attributes, drops `service.*` span
  attributes in any spelling, identifies the pod by its connection only
  (`k8s_attributes` `pod_association`), and drops spans with no
  Component UID (RP-086).
- **Workloads are given the endpoint:** while the package exists,
  Components get `OTEL_EXPORTER_OTLP_ENDPOINT` and `OTEL_SERVICE_NAME` on
  their next new release (one they set themselves is kept). Removing the
  package rolls each traced Component once, and deletes the stored
  traces behind the package-removal approval.
- **Reads** (`core/reconcile.TracingProvider`, `tracing_read.go`):
  Serendip's SQL on the traces stream, scoped by the attested Component,
  Environment and project UIDs.
  - `GET /v1/deployments/{id}/traces` (`from`, `to`, `errorsOnly`,
    `minDuration`, `limit` ≤ 200);
  - `GET /v1/deployments/{id}/traces/{traceId}`: the spans of every
    Deployment in the Environment the caller may read, `partial: true`
    when spans remain it can't show, 404 when none of them has a span.
  - Permission: the data action `serendip/components/traces/read`, left
    out of Reader like `.../logs/read` (span attributes carry URLs and
    user data). Azure analogue: Application Insights' transaction view,
    which marks components you can't read as inaccessible.

## Alert conditions

Three conditions join spec 024's (`operations.md`, "Rules"):
`requestErrorRate`, `requestLatency` (from Hubble's metrics) and
`traceErrors` (from the stored traces). Without their source (an
`external` CNI, the tracing package not installed) a rule reads
`ConditionRefused` with the reason; it isn't deleted.

## Surfaces

- **Console:** the Component's Observability tab (HTTP requests,
  Connections, Traces with a span waterfall), the topology diagram's
  traffic overlay (off until turned on), "Network layer" on Platform,
  and the three conditions in the alert rule form.
- **CLI and MCP** from the catalogue: `serendip networking show`,
  `flows`, `traffic`, `traces list|show` (`docs/cli-and-mcp.md`).

## Not built

Installing or upgrading Cilium from `serendipd`, live CNI migration,
kube-proxy replacement, WireGuard/IPsec, BGP, egress gateways,
ClusterMesh, Hubble relay and UI, user-authored network policies, L7
visibility for gRPC/Kafka/DNS, per-request flow records, policies on
build pods (RP-049), and trace sampling controls.
