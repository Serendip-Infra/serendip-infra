# Versions: providers/observability/openchoreo

Upstream version assumptions for the observability package (spec 023).
Update this file and the constants named below together. The "Defined in"
column names the files that pin each item. Verified in T001
(2026-11-12, `serendip-t018`) unless marked otherwise.

| Item | Version / value | Defined in |
|---|---|---|
| OpenChoreo observability-plane chart | 1.3.1, `oci://ghcr.io/openchoreo/helm-charts/openchoreo-observability-plane` (digest `sha256:0a459a90d51c28781bfe0189a1fb3757fe19b0901fd7bcabbe9dcd8ddc84858f`); matches the control plane (1.3.3 exists) | `installer.go` |
| Logs module (config B) | `observability-logs-openobserve` 0.8.0, `oci://cr.openchoreo.dev/openchoreo/helm-charts/` (digest `sha256:4f99feb62be138db3558e6690ad708d41f23b2fd42728bf31824a028daa02a24`); OpenObserve `v0.92.2`, Fluent Bit `5.1.5` | `values.go` |
| Logs module (config A, T003 only; rejected, ADR 0021) | `observability-logs-opensearch` 0.8.1 (digest `sha256:e85b36e9d0be995977afcc16af9af210f3a8a902cf9355efb06399e6b27c27e0`) | n/a |
| Metrics module | `observability-metrics-prometheus` 0.9.3 (digest `sha256:ed4245d732518d283f30fb1f7879f0e512e31b497e0cabfbac13c123090d11fd`); installed for its **adapter only**, `kube-prometheus-stack.enabled=false` (ADR 0021; as shipped it brings kube-prometheus-stack with 10 `monitoring.coreos.com` CRDs) | `values.go` |
| Metrics store | `prometheus-community/prometheus` 29.47.2, `oci://ghcr.io/prometheus-community/charts/prometheus`; images pinned to the module's: Prometheus `v3.15.1-distroless`, config reloader `v0.94.2`, kube-state-metrics `v2.20.0` (subchart 9.0.1). Values: `prometheus-values.yaml` here, embedded, which a test keeps identical to the bench's `profiles/edge-1/bench/observability-load/prometheus-plain-values.yaml` (T003) | `values.go`, `prometheus-values.yaml` |
| Query API | the adapters' `POST /api/v1/logs/query` (`logs-adapter:9098`) and `POST /api/v1/metrics/query` (`metrics-adapter:9099`), from `openapi/observability-{logs,metrics}-adapter*.yaml` at v1.3.1 | `observer.go` |
| Query shapes (T006, from the OpenAPI and community-modules `main`; **checked live** on `serendip-t018`, the end bound found exclusive) | logs: `limit` 1–1000, `sortOrder` `desc`, `searchPhrase` (a substring `LIKE`); scope `{namespace, projectUid, componentUid, environmentUid}` or `{namespace, workflowRunName}` (namespace without `workflows-`); answer `logs[]` of `{timestamp, log, level, metadata{componentUid, environmentUid, podName, containerName, ...}}`, build-run entries only `{timestamp, log}`, microseconds. Metrics: `resource` answers `{timestamp, value}` series, CPU in cores, memory in bytes; `step` like `5m` | `observer.go`, `provider.go` |
| Disk use (T006, **checked live**) | OpenObserve: `GET /api/default/streams?type=logs` with the admin Secret's Basic credentials, over a port-forward (the Service proxy strips `Authorization`), `compressed_size` + `index_size` in **MiB**; its `/metrics` has no stored-size gauge at v0.92.2. Prometheus: `openchoreo-observability-prometheus:9091/metrics` `prometheus_tsdb_storage_blocks_bytes` + `prometheus_tsdb_wal_storage_size_bytes`, through the proxy | `usage.go` |
| Alert condition queries (spec 024 T001, **checked live** on `serendip-t018`; recorded in `testdata/condition-*.json`) | metric: the same `resource` query over `[now − for, now]` at `step` `1m`, which answers for + 1 points (both ends, aligned to the end). logMatch: the logs query with `limit` 1; the answer's `total` is the exact match count past the limit (v1.3.1). Restarts: Prometheus HTTP API **v1** instant query, `GET /api/v1/query?query=…&time=<unix s>` on `openchoreo-observability-prometheus:9091` through the Service proxy; answer `{status, data{resultType: vector, result[{metric, value: [ts, "v"]}]}}`, errors `{status: error, errorType, error}` (the error echoes the query, so it's never passed on). The PromQL joins `kube_pod_container_status_restarts_total` (added to `metricAllowlist` for this; kube-state-metrics v2.20.0) to `kube_pod_labels`' `label_openchoreo_dev_{component,environment,project}_uid` | `conditions.go`, `prometheus.go`, `prometheus-values.yaml` |
| Object names (T006) | the application provider's `cmp-`/`env-` + 10 hex of SHA-256 of the ID, and the build provider's `build-` + 16 hex; repeated in `scope.go` | `scope.go` |
| Plane object | `ObservabilityPlane/default` in `openchoreo-control-plane` (namespaced; a namespaced `DataPlane` falls back to it by name) | `installer.go` |
| Workload selectors the checks use | the charts' own pod selectors at these versions (rendered with `helm template`, T005): `app.kubernetes.io/component` `observer`, `cluster-agent`, `controller-manager` (plane); `app.kubernetes.io/name=openobserve-standalone`, `app=logs-adapter-openobserve`, DaemonSet `fluent-bit` (logs); `app.kubernetes.io/name` `prometheus` + `component=server`, `kube-state-metrics` (Prometheus); `app=metrics-adapter-prometheus` (metrics); the chart's policy `observability-plane-ingress` | `installer.go`, `policies.go` |
| Verification cluster | `kindest/node:v1.34.0` (kindnet enforces NetworkPolicy) | n/a |

Notes (T001):

- **Required values:**
  - `security.enabled=false`;
  - `gateway.enabled=false`;
  - `observer.controlPlaneApiUrl`;
  - `observer.extraEnvs` with a non-`.invalid` `OBSERVER_BASE_URL`;
  - `observer.secretName`, a Secret with `UID_RESOLVER_OAUTH_CLIENT_SECRET`
    (a placeholder is enough);
  - `fluentBitCustomizations.clusterInstance`;
  - `openobserve-admin-credentials`, generated;
  - `openobserve-standalone.config.ZO_COMPACT_DATA_RETENTION_DAYS`
    (the default is 3650).
  - `openobserve-standalone.config.ZO_TELEMETRY="false"`. The default
    is `"true"`: a heartbeat to `e1.zinclabs.dev` every 30 minutes.
  - `openobserve-standalone.config.ZO_MMDB_DISABLE_DOWNLOAD="true"` and
    `openobserve-standalone.extraEnv[0]` `ZO_MODEL_PRICING_ENABLED=false`
    (T006, seen live): otherwise GeoLite databases are fetched from
    `geoip.zinclabs.dev` and LLM pricing from GitHub at start. The chart
    renders only the `config` keys it knows, so the pricing switch goes
    through `extraEnv`.
  - `openobserve-standalone.extraEnv[1]` `ZO_SSRF_ALLOW_LOOPBACK=true`
    (T012, seen live on a fresh `bench-t470`): OpenObserve's SSRF guard
    refuses alert destinations at private addresses, so the chart's
    setup Job can't create its `http://logs-adapter:9098/...` webhook,
    fails with `BackoffLimitExceeded`, and the install never finishes.
    With the flag, v0.92.2 skips the create-time check entirely
    (`ssrf_guard.rs`); `ZO_SKIP_SSRF_CHECKS` is the documented switch
    for private addresses. The module's `main` sets the same flag.
    `serendip-t018` hid this: its destination already existed. RP-062.
- **Scopes are UIDs** (`projectUid`, `componentUid`, `environmentUid`).
  The adapters ignore names, and the query widens to the whole namespace.
- **Fluent Bit runs off the host network** (T014, RP-064):
  `fluent-bit.hostNetwork=false`, `dnsPolicy=ClusterFirst`, and
  `existingConfigMap=serendip-fluent-bit`. That ConfigMap is the chart's
  render of `templates/fluent-bit/config.yaml`, embedded verbatim in
  `fluent-bit/`, with one edit (`fluentbit.go`): the kubernetes filter's
  `tls.verify Off` / `Use_Kubelet true` become `On` / `false`. The
  chart's default `Kubelet_Host` is 127.0.0.1, the pod's own loopback
  off the host network, so pod metadata now comes from the API server
  (the chart's ClusterRole allows `pods`/`namespaces` get, list and
  watch). A hash of it goes in a pod annotation, so a change rolls the
  DaemonSet. The plane chart's policy then covers `:2020`, and Fluent
  Bit's flushes come from a pod in the namespace, so the T001 node rule
  is gone.
- **Uninstall leaves** the PVCs, the Secrets, the CA ConfigMap, a setup-job
  pod, and the CRDs (Prometheus Operator's 10, and `observabilityalertrules`).
- **Plain Prometheus must look like kube-prometheus-stack to the adapter**
  (T003, RP-056):
  - the Service `openchoreo-observability-prometheus`, port 9091 (the
    adapter's `PROMETHEUS_ADDRESS`, fixed in its ConfigMap template);
  - the scrape jobs `kubelet` (cAdvisor, `/metrics/cadvisor`) and
    `kube-state-metrics`;
  - kube-state-metrics' `metricAllowlist` and `metricLabelsAllowlist`,
    copied from the module's values.
- **Re-check on every upgrade:**
  - the policy selectors (the labels `app.kubernetes.io/name=openobserve-standalone`
    and others);
  - the adapter request schema;
  - the metrics module's kube-prometheus-stack values (scrape jobs,
    kube-state-metrics allowlists) and the adapter's Prometheus address,
    against the plain Prometheus values above;
  - the Fluent Bit config: re-render it into `fluent-bit/` (the command
    is in `fluentbit.go`); `TestFluentBitConfig_*` fail if the edited
    lines moved;
  - OpenObserve's start-up log for any outside URL, and the `config`
    keys the chart renders (a new key may need `extraEnv`);
  - the streams API's stats fields and unit (`usage.go`).
