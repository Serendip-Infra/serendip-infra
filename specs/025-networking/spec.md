# Spec 025: Networking: Cilium, Hubble and traces

Status: Reviewed, 2026-11-19.

The follow-up that specs 023 and 024 named. Spec 023 found that a
Component's request rate, errors and latency need Cilium's Hubble L7
metrics (its plan, finding 7: the `http` metric returns `{}`), so that
section and the topology diagram's live traffic overlay read "Not
available yet". Spec 024 moved traces here, with trace-based alert
conditions (its plan, "Carried to spec 025"). Epic #66.

## Problem

- **Serendip has no Networking capability.** `capability-model.md` lists
  Networking (CNI) as required with Cilium as the default, but nothing is
  built. Clusters run whatever CNI they came with: K3s's Flannel (spec
  005 left K3s's defaults on) or kind's kindnet in development. Serendip
  doesn't know which, and says nothing about what it can and can't do.
- **NetworkPolicy enforcement is assumed, not known.** Several security
  fixes rest on NetworkPolicies: the observability adapters (RP-054), the
  OpenChoreo API, the build pods (RP-049). Whether they hold depends on
  the CNI, and kindnet's behaviour already differed from what was
  expected (RP-054's residual). Flannel on K3s relies on K3s's embedded
  policy controller.
- **No request telemetry.** Nobody can see how many requests a Component
  serves, how many fail, or how slow they are, nor which Components talk
  to which. A NetworkPolicy that silently drops traffic looks like a
  timeout.
- **No traces.** A slow request across three Components can't be
  followed.
- **Alerts can't fire on requests.** Spec 024's rules cover CPU, memory,
  restarts and logs only.
- **The cluster's pod ranges are partly guessed** (RP-076): with Cilium's
  own address management, nodes don't carry `podCIDRs`, so the notifier's
  "cluster's own ranges" would miss them.

Changing the CNI of a running cluster isn't a supported path (K3s picks
it at install; `bootstrap-agent.md`). This spec has to say plainly what
happens to existing clusters.

## Scope

### In

1. **A Networking capability** in `core/` with Cilium as its first
   provider. The capability reports:
   - which CNI the cluster runs, and its version;
   - whether NetworkPolicies are enforced;
   - whether flow visibility and request metrics are available;
   - the cluster's pod address ranges (RP-076).
2. **An `external` networking provider** (the escape hatch, constitution
   1): an existing cluster on Flannel, kindnet or any other CNI is
   detected and reported honestly, with what it lacks (no flows, no
   request metrics). Nothing is installed on it.
3. **New clusters with Cilium from the first boot.** The install path
   (the agent's K3s bootstrap, and the development kind clusters) can
   bring up a node whose CNI is Cilium, chosen at install in the setup
   choice, with "recommended" labeling as the PRD's setup wizard requires.
   Switching an existing cluster is documented as a reinstall; no live
   migration.
4. **Hubble flow visibility,** read through Serendip's API and scoped
   like every other read:
   - a Component's recent flows (to and from it), including flows a
     policy **dropped**, with the reason;
   - a caller sees a flow only if they can read at least one end; the
     other end is named only if they can read it too.
5. **Request metrics for Components:** request rate, error rate and
   latency (p50, p95, p99) for HTTP traffic, filling spec 023's
   `metric=http` section. Non-HTTP traffic gets connection counts only.
6. **The live traffic overlay** on the topology diagram: per-edge request
   rate and error rate between Components the caller can read, over a
   chosen window.
7. **Traces:** Components can send OpenTelemetry traces to the platform
   (a standard OTLP endpoint they're given), and a caller can list a
   Component's traces in a window and open one trace's spans. Traces live
   in the observability package's existing store, with retention like
   logs.
8. **Trace and request alert conditions** joining spec 024's rule model:
   error count or rate, and p95/p99 latency, over a window, on a
   Component or an Application.
9. **Interface parity** (constitution 7): every read above, and the new
   conditions, through the API, CLI, MCP and UI, under the same
   authorization (Azure analogue: Network Watcher flow logs and
   Application Insights' request and dependency telemetry, read under
   the monitored resource's Reader role).
10. **Honest reporting** (constitution 6): the platform's status says
    which CNI runs, whether policies are enforced, and when request
    metrics, flows or traces aren't available and why.
11. **Measured cost** on the simulated T470: Cilium and Hubble against
    the cluster's current CNI, and the tracing pipeline, recorded in the
    risk register, with the edge-1 default decided by the numbers
    (constitution 4).

### Out

- **A Network resource** (VNets, subnets, security groups,
  user-authored network policies). `resource-model.md` says it needs its
  own spec; this one only reports and reads.
- **Cilium's Gateway API, service mesh, ClusterMesh, encryption
  (WireGuard/IPsec), BGP and egress gateways.** kgateway stays the
  gateway (ADR 0011); a service mesh is out by constitution 10.
- **Hubble UI or any upstream console.** Serendip shows the data in its
  own UI.
- **Live migration** of an existing cluster from Flannel or kindnet.
- **Policies on the build pods** (RP-049): this spec makes them
  enforceable; writing them stays in the builds-hardening follow-up.
- **Metrics and logs from Components' own instrumentation** (custom
  OTLP metrics and logs): only traces this time.
- **Composite alert conditions** (still the later follow-up spec 024
  named).

## Affected resources and capabilities

- **New capability:** Networking (CNI), providers `cilium` and
  `external`.
- **Observability capability:** gains request metrics, flows and traces
  reads; the observability package gains the tracing pipeline.
- **Platform status** (the package and provider instance reports):
  networking facts above.
- **AlertRule** (spec 024): new condition types.
- **Install path:** the agent's K3s bootstrap and the development
  cluster scripts.
- **UI:** the Component Observability tab (requests, flows, traces), the
  topology diagram's overlay, the alert rule form.
- **Notifier:** the cluster's own ranges include the CNI's pools
  (RP-076).

## Acceptance criteria

1. On a new cluster installed with Cilium, the platform status names
   Cilium and its version, says policies are enforced, and lists the pod
   pool ranges; `core/notify` refuses an address in those ranges.
2. On an existing Flannel or kindnet cluster, the status names the CNI
   (or "unknown"), and the requests, flows and traffic overlay sections
   say "not available: the cluster's CNI doesn't provide them", in the
   API, CLI, MCP and UI. Nothing is installed or changed on it.
3. A NetworkPolicy Serendip relies on (RP-054's observability policies,
   the OpenChoreo API's) is checked live on the Cilium cluster: a
   blocked connection is dropped, and the drop shows in the target's
   flows with the policy reason.
4. A Component serving HTTP traffic shows request rate, error rate and
   p50/p95/p99 latency that agree with a load generator's own counts
   (within a tolerance the plan sets) over a 10-minute run.
5. The topology overlay shows the request and error rate on each edge
   between two Components that talk, and nothing on an edge the caller
   can't read both ends of.
6. A caller who can read Project A but not Project B sees A's flows to B
   with B's end unnamed, and sees no flow between two B Components.
7. A Component instrumented with a standard OpenTelemetry SDK sends
   traces with no Serendip-specific code, and its traces and spans can be
   listed and opened through the API, CLI, MCP and UI.
8. A trace shown under a Component contains only spans that came from
   that Component's pods: a workload claiming another Component's
   identity in its span attributes doesn't land under that Component.
9. Trace reads are scoped before they reach the backend, as logs are
   (spec 023): a caller without read on a Component gets no traces of it,
   even by guessing a trace ID.
10. An alert rule on p95 latency and one on error rate fire within the
    rule's window plus one evaluation interval when a test Component is
    made slow or failing, and resolve when it recovers.
11. Removing the tracing part of the observability package stops trace
    collection and deletes stored traces; rules with trace conditions
    report "not available" (they aren't deleted), and logs and metrics
    keep working.
12. Control-plane idle footprint (constitution 4: `serendipd` + agent)
    stays within rule 4 and within 10 MiB of spec 024's measurement.
13. Cilium and Hubble's node cost, and the tracing pipeline's, are
    measured on the simulated T470 against the current CNI, recorded as
    risk register entries, and the edge-1 default (Cilium or the existing
    CNI) is set from those numbers, with the reason in an ADR.
14. Every new mutation (installing Cilium, the tracing pipeline, new
    rule types) is audited (constitution 8). Removing the tracing part,
    which deletes stored traces, needs an approval (constitution 5).

## Open questions for the plan

- How Cilium is installed before a node can run pods (K3s with no CNI
  isn't Ready), and which component owns that: the agent or `serendipd`.
- Whether Cilium replaces kube-proxy, and what that costs or saves on
  the T470.
- Where request metrics come from: Hubble's metrics scraped by the
  existing Prometheus, or the gateway's (kgateway/Envoy) for north-south
  traffic, or both.
- Whether L7 visibility needs per-Component policies that turn on
  Cilium's proxy, and its cost per request.
- The OpenChoreo tracing module's fit (spec 024's carried research:
  name-based scoping, span identity, `observer.tracingAdapter.enabled`),
  against a direct OpenObserve read.
- The retention and disk budget for traces and flows on edge-1.
