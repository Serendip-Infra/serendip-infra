# ADR 0023: Networking is observed, and Cilium is edge-1's default CNI

Status: Accepted
Date: 2026-11-23

## Context

Spec 025 fills the gaps spec 023 left in a Component's observability:
request rates, errors and latency, connections with their drop reasons,
and traces. Request data and drops come from the CNI, so the CNI became a
capability for the first time (`capability-model.md` already named Cilium
as the networking default, with no provider behind it).

Three things shaped the decision:
- **The CNI runs before the node is Ready.** A package `serendipd` adds
  later can't install it, and K3s's CNI is fixed at install (spec 005's
  breadcrumb in `bootstrap-agent.md`). Changing it means a new cluster.
- **Constitution 4 (the T470)** decides defaults by measurement. Cilium
  (agent, operator, Envoy) is heavier than K3s's Flannel or kind's
  kindnet, and the cost wasn't published anywhere.
- **Hubble's L7 metrics need Envoy in the path,** through a
  CiliumNetworkPolicy with an HTTP rule. Allows are a union, and Envoy's
  defaults retry 5xx (T001). The proxy must neither widen the
  platform's isolation nor change what the application sees.

## Decision

### 1. Networking is a capability Serendip observes, not one it installs

- `core/reconcile.NetworkingProvider` has one method, `Facts`: the
  plugin and version (as reported data), whether policies are enforced
  (`enforced`, `notEnforced`, `unknown`, with who enforces them),
  whether flows and request metrics are available, the pod ranges, and
  `Unavailable` reasons in Serendip's words.
- `serendipd` detects the CNI from what runs in `kube-system` and
  registers `cilium` or `external`. It reads the facts every re-ensure
  interval (5 minutes), caches them, and serves them at
  `GET /v1/networking`.
- **It never installs, upgrades or reconfigures the CNI.** It reports
  drift from the values Serendip pins. The one thing the `cilium`
  provider writes is request visibility (decision 3) and the node guard
  (decision 4), both its own objects.
- Azure analogue: AKS's network plugin (Azure CNI or kubenet) is chosen
  at cluster creation, shown on the cluster, and not changed in place.

### 2. The install path picks the CNI, and Cilium is the default

- `serendip-agent bootstrap --cni cilium|default`, `cilium` the default.
  With `cilium`, K3s gets `--flannel-backend=none
  --disable-network-policy`, and the agent writes a K3s `HelmChart`
  (`bootstrap: true`) with Cilium 1.20.5 and Serendip's pinned values
  into K3s's auto-deploy directory. K3s's own Helm controller installs
  it; the agent has no Helm (rejected in `build-vs-adopt.md`). A test
  keeps the agent's values identical to the provider's.
- `default` keeps K3s's Flannel and its network policy controller.
- **The pinned values:** IPAM `kubernetes`, kube-proxy kept, one
  operator, Hubble metrics (`httpV2` with `labelsContext`, `drop`,
  `flow`) and the static flow exporter on, relay and UI off, and Envoy
  set not to retry (`http-retry-count: "0"`) or rewrite paths
  (`http-normalize-path: "false"`). If either Envoy setting changes
  back, the provider removes its L7 policies and says why (RP-081).

**The numbers** (T013, simulated T470: 2 cores of quota, kind;
`profiles/edge-1/bench/results/2026-11-23-spec025-simulated-t470/`):

| Phase | kindnet node | Cilium node | Cilium's cost |
|---|---|---|---|
| Platform idle | 1538 MiB, 235 mcores | 1981 MiB, 327 mcores | +443 MiB, +92 mcores |
| Observability idle | 2131 MiB, 270 mcores | 2562 MiB, 347 mcores | +431 MiB, +77 mcores |
| HTTP traffic, 10 req/s | 2413 MiB, 355 mcores | 2874 MiB, 483 mcores | +461 MiB, +128 mcores |
| Traffic, traced | 2558 MiB, 365 mcores | 3054 MiB, 483 mcores | +496 MiB, +118 mcores |

- Cilium's own pods are 236–302 MiB of that; the rest is node overhead
  (BPF maps) and kube-apiserver holding Cilium's CRDs.
- The heaviest sample of the whole stack, observability and tracing
  included, was 3244 MiB: 23% of a 16 GB machine less 2 GB for the OS,
  inside the 50% "comfortable" line.
- `serendipd` itself is unchanged (criterion 12: 33.2 MiB idle on
  kindnet; 28.6 MiB restarted on the Cilium stack).

So **Cilium stays edge-1's default.** It costs about half a GiB and a
tenth of a core, and buys request metrics, connection records with drop
reasons, and policies the CNI enforces itself. `--cni default` stays for
machines where half a GiB matters (an 8 GB one). kube-proxy-free isn't
needed to fit; it's the first thing to measure if the node is ever
tight (RP-089).

### 3. Request visibility mirrors the platform's NetworkPolicy

- For each Component with an HTTP endpoint, the `cilium` provider keeps a
  `CiliumNetworkPolicy` beside the NetworkPolicy OpenChoreo gives it. It
  copies that policy's ingress peers and keeps only its HTTP ports, adds
  `http: [{}]`, and turns default-deny off. Owned by the NetworkPolicy,
  so Kubernetes deletes it with the Component.
- A shape it can't copy faithfully (address ranges, namespace label
  selectors, named ports, unknown fields) is skipped with a reason:
  that Component has no request metrics, never a wider policy (RP-082).

### 4. The node's metrics ports are closed to workloads

- Cilium's agent, operator, Envoy and Hubble serve metrics on the
  node's host network (9962–9965), unauthenticated. Hubble's carry every
  Component's source and destination pods.
- The provider keeps `serendip-node-metrics-guard`, a deny-only
  `CiliumClusterwideNetworkPolicy`: every pod outside `kube-system` and
  the observability plane is denied egress to those ports on the nodes.
  It adds no allow-list (default-deny off). Found in T014's security
  review; RP-091.

### 5. Traces are their own package, read by Serendip

- `openchoreo-tracing` (capability `tracing`) installs OpenChoreo's
  tracing module's OTel Collector only, writing to the logs package's
  OpenObserve. Its adapter, bundled store and tail sampling are off.
- **The collector's config is Serendip's:** resource attributes are an
  allow-list, `service.*` span attributes are dropped, the pod is
  identified by its connection only, and spans with no Component are
  dropped. A workload can't claim another's identity (RP-086).
- Reads are Serendip's SQL on the stored spans, scoped by the attested
  Component, Environment and project UIDs, behind a new data action
  `serendip/components/traces/read` (left out of Reader, like logs).
- While the package exists, Components get `OTEL_EXPORTER_OTLP_ENDPOINT`
  and `OTEL_SERVICE_NAME` on their next new release.

## Alternatives considered

- **Install Cilium from `serendipd` as a package.** The node isn't Ready
  without a CNI, and swapping a running cluster's CNI isn't safe. Rejected.
- **kindnet or Flannel as edge-1's default, Cilium from `small-3`.** Plan
  decision 16's fallback if Cilium didn't fit. It fits, and without it
  edge-1 has no request metrics or drop reasons. Kept as `--cni default`.
- **kube-proxy replacement.** Needs the API server's address at install
  and cgroup v2, can't be undone in place, and saves little on one node.
  Not now (`build-vs-adopt.md`).
- **Hubble relay (gRPC) for flows.** A gRPC client is a dependency, and
  relay costs memory for what the node's export file gives.
- **The tracing module's adapter for reads.** It can't scope by UIDs or
  names (T001). Off, and 128 MiB saved.

## Consequences

- A fresh edge-1 node costs about 450–500 MiB more than before spec 025,
  measured on a simulation. Re-measure on the L470 (2 cores, 8 to
  16 GB); if half a GiB is too much at 8 GB, recommend `--cni default`
  there (RP-089).
- First boot with Cilium needs the internet for the chart and images
  (RP-083).
- Changing a cluster's CNI is a reinstall (`operations.md`, "Networking").
- On an `external` CNI, requests and flows say why they're unavailable
  (409 with the facts' reason), and the request and trace alert
  conditions read `ConditionRefused`.
- Every Cilium upgrade re-checks the pinned Envoy settings, the
  visibility mirror and the guard's ports
  (`providers/networking/cilium/VERSIONS.md`).
