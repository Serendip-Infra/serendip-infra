# Versions: providers/networking/cilium

Upstream version assumptions for the Cilium networking provider (spec 025).
Update this file and the constants named below together. Verified in T001
(2026-11-19/20) on the scratch kind cluster `t025` (`kindest/node:v1.34.0`,
`disableDefaultCNI`) and in a `rancher/k3s:v1.36.4-k3s1` container,
unless marked otherwise. Recorded answers: `testdata/t001-*`.

| Item | Version / value | Defined in |
|---|---|---|
| Cilium chart | `cilium` 1.20.5, `oci://quay.io/cilium/charts/cilium` (also `https://helm.cilium.io`); agent `quay.io/cilium/cilium:v1.20.5`, Envoy `cilium-envoy` v1.39.x (own DaemonSet), Hubble `1.20.5` | T004 (`values.yaml`) |
| Values (T001's final, `testdata/t001-values.yaml`) | IPAM `kubernetes`; `kubeProxyReplacement: false`; `operator.replicas: 1`; Hubble on, relay and UI off; metrics `httpV2:labelsContext=source_namespace,source_pod,destination_namespace,destination_pod`, `drop`, `flow`; the static exporter (below); `extraConfig` `http-retry-count: "0"`, `http-normalize-path: "false"` | T004 |
| Pod ranges | IPAM `kubernetes` keeps them on the Node: `spec.podCIDRs` `10.244.0.0/24` (kind), `10.42.0.0/24` (K3s) | T004, RP-076 |
| `cilium-config` keys read | `ipam`, `hubble-metrics`, `hubble-export-*` (`file-path`, `allowlist`, `denylist`, `fieldmask`, `fieldaggregate`, `aggregation-interval`), `kube-proxy-replacement`, `enable-policy`, `http-retry-count`, `http-normalize-path` | T004 |
| L7 visibility | A `CiliumNetworkPolicy` with `http: [{}]` on the port, `enableDefaultDeny: {ingress: false, egress: false}`, peers copied from OpenChoreo's NetworkPolicy (`testdata/t001-l7-policy.yaml`) | T004 |
| Hubble metrics | `:9965` on the agent, headless Service `hubble-metrics` in `kube-system` | T006 |
| Flow export | file `/var/run/cilium/hubble/events.log` on the node, 10 MB × 5; `fieldAggregate`/`aggregationInterval` were seen at 1.20.5 and not checked on older releases, so the provider refuses an agent older than the pinned minor (1.20) | T004, T006 |

Notes (T001):

- **Mirroring OpenChoreo's NetworkPolicy keeps its isolation.** Its
  policy admits the port from the same namespace and from pods labelled
  `openchoreo.dev/system-component` in any namespace. The mirror:
  - same namespace: `fromEndpoints` `k8s:io.kubernetes.pod.namespace=<ns>`;
  - any namespace: `matchExpressions` `k8s:io.kubernetes.pod.namespace`
    Exists **and** `openchoreo.dev/system-component` Exists (without the
    namespace expression a CNP selector means the policy's own namespace).
  - Probed before and after: a pod in `default` timed out both times
    (and showed as `POLICY_DENIED`), the labelled pod and a same-namespace
    pod answered 200 both times. Flows to the port show
    `trace_observation_point: TO_PROXY`.
- **Envoy retries 5xx by default (`http-retry-count` 3).** With the L7
  policy on, a server answering 10% 500s looked 100% healthy: Envoy
  retried each failure, so the metrics showed only 200s, the load grew,
  and a non-idempotent request could run twice. With `http-retry-count:
  "0"` the 500s came through (5 of 39). `http-normalize-path` (default
  `true`) rewrites request paths; a proxy that's only there to observe
  shouldn't, so it's off too. RP-081.
- **`httpV2` labels:** `sourceContext=pod;destinationContext=pod` gives
  `source="ns/pod"`, `destination="ns/pod"`, which OpenChoreo's adapter
  can't join. `labelsContext=source_namespace,source_pod,destination_namespace,destination_pod`
  gives the four labels its PromQL joins to `kube_pod_labels` on
  (`testdata/t001-hubble-metrics-*.txt`).
- **Export filters:**
  - `source_pod`/`destination_pod` don't take a namespace prefix
    (`["dp-"]` matched nothing). `source_label`/`destination_label`
    `["k8s:openchoreo.dev/component-uid"]` (label exists) selects
    Components' pods by a label the workload can't set on itself.
  - The denyList drops DNS (`destination_port`/`source_port` 53) and
    ICMPv6: before it, DNS was 2,101 of 2,855 records (7 of 9.6 MB, in
    under a minute).
  - The allowList keeps drops, TCP SYNs that aren't replies, and UDP
    that isn't a reply, with either end a Component.
- **Aggregation** (`fieldAggregate` and `aggregationInterval`, new in
  1.19/1.20): one record per distinct key per interval, with
  `aggregate.{ingress,egress}_flow_count`. With `fieldMask` on labels the
  records were about 3 KB (every namespace label is copied in); with
  `source/destination.{namespace,workloads,identity}` instead they're
  about 550 B. At 60 s, three Components talking: 8–10 records a minute,
  about 7.5 MiB a day.
  - A Component is named by its `workloads[0].name`, the Deployment's
    name, which OpenChoreo derives from Serendip's IDs
    (`cmp-<hash>-env-<hash>-<hash>`); a workload can't change it.
  - Each connection between two Cilium endpoints is recorded twice
    (EGRESS at the source, INGRESS at the destination): de-duplicated at
    query time.
  - A bare pod (no controller) has no `workloads`: only its namespace
    and numeric identity are kept.
- **K3s bootstrap:** started with `--flannel-backend=none
  --disable-network-policy`, a `HelmChart` (`helm.cattle.io/v1`,
  `bootstrap: true`, chart, repo, version, `valuesContent`) in a
  subdirectory of `/var/lib/rancher/k3s/server/manifests/` was picked up.
  Its `helm-install-cilium` Job ran on the host network while the node
  was NotReady, Cilium started, the node went Ready and CoreDNS got a
  pod address (10.42.0.181).
  - Harness only: in a plain `docker run` container, `/sys/fs/bpf` must
    be a shared mount (`mount --make-rshared /` before `k3s server`), or
    the agent's `mount-bpf-fs` init container fails. kind's node image
    does this already; a real host doesn't need it.
- **Kernel:** Docker Desktop's WSL2 kernel `6.6.87.2-microsoft-standard-WSL2`,
  cgroup v2: no problems seen.
