# Spec 023 research notes (oss-researcher, 2026-11-09)

Distilled upstream findings for plan.md. "Inferred" items are unconfirmed
upstream; T001's spike checks them. Latest stable OpenChoreo is **v1.3.3**
(2026-10-29). Confirm the chart tag with
`helm show chart oci://ghcr.io/openchoreo/helm-charts/openchoreo-observability-plane --version 1.3.3`.

## The observability-plane chart

- **Installs:** the Observer (1 replica, 100m/128Mi requests, 200m/200Mi
  limits); a cluster agent (`planeType=observabilityplane`, `planeID=default`,
  wss to `cluster-gateway.openchoreo-control-plane...:8443/ws`, 50m/128Mi,
  limit 256Mi, cert-manager certs); a controller manager (100m/256Mi, limit
  512Mi). RCA and FinOps agents are off by default (~1 GiB each).
- **No backends.** The Observer calls adapters: logs `logs-adapter:9098`,
  tracing `:9100`, finops `:9101` (30 s timeouts). The modules supply
  backends, adapters and collectors.
- **CRDs:** only `observabilityalertrules`. `ClusterObservabilityPlane`'s
  CRD is in the control-plane chart. Whether the plane chart creates the CR
  is unconfirmed.
- **Security:** `security.enabled=true` by default; OIDC
  (`issuer`, `jwksUrl`, `tokenUrl`, `jwt.audience`) and the client
  `openchoreo-observer-resource-reader-client` (client_credentials).
  Disabling turns auth off for all components; that's only sane if the
  Observer is unreachable (inferred).
- **Gateway:** `gateway.enabled=true` (kgateway, 80/443) by default. Set it
  to false when a gateway already exists.
- **Network policy:** `networkPolicy.enabled=true`, with ingress only from
  `gateway-default` and `opentelemetry-collector`.
- **Alert store:** SQLite, 128Mi PVC (default).
- **Plane-chart footprint:** about 0.3 CPU requested and about 0.5 GiB in
  limits.

## Observer API (`openapi/observer-api.yaml`, openchoreo/openchoreo)

- `POST /api/v1/logs/query`: `startTime`, `endTime`, `limit`, `sortOrder`,
  `searchPhrase`, `logLevels`, `searchScope`, which is either
  - `{namespace, project, component, environment}`, or
  - `{namespace, workflowRunName, taskName}`, for build logs.
- `POST /api/v1/metrics/query`: `metric` (`resource` or `http`),
  `startTime`, `endTime`, `step`, `searchScope`.
- Also: events, platform-logs, traces, runtime-topology, alerts and
  incidents (v1alpha1). `/health` has no auth.

## Wiring

- The data-plane chart 1.3.3 has **no collector keys**. Collectors come
  from the modules and install into `openchoreo-observability-plane`; Fluent
  Bit runs as a DaemonSet.
- `ClusterDataPlane.spec.observabilityPlaneRef` is optional. If it's
  omitted, the data plane uses a `ClusterObservabilityPlane` named `default`
  if one exists; otherwise observability is off.

## Community modules

- **logs-opensearch 0.8.1:**
  - **Deploys:** OpenSearch 3.9.0 (`singleNode: true`), Fluent Bit 5.1.5
    (chart 0.58.3), a setup job (index templates and retention) and a logs
    adapter.
  - **OpenSearch sizing:** the module sets none, so it gets the upstream
    defaults: `-Xmx512M`, a 1000m CPU request, no limits and an 8Gi PVC.
    Override `openSearch.opensearchJavaOpts`, `resources` and
    `persistence.size`.
  - **Fluent Bit:** requests 25m/125Mi, limits 100m/150Mi.
  - **Adapter:** requests 50m/64Mi, limit 100Mi.
  - **Retention:** `openSearchSetup.dataRetentionTime` defaults to 30 days
    (365 for audit).
  - **Required settings:** `fluentBitCustomizations.clusterInstance=singleCluster`,
    the `opensearch-admin-credentials` secret, and an override of the
    hardcoded `OPENSEARCH_INITIAL_ADMIN_PASSWORD`.
  - **Node setting:** needs `vm.max_map_count=262144` on the host (inferred).
  - `kube-system` logs are excluded.
- **logs-openobserve 0.7.x/0.8.x** (OpenChoreo 1.3.0+):
  - **Deploys:** `openobserve-standalone` 0.92.2 (single node, 10Gi PVC),
    Fluent Bit (`hostNetwork`), a logs adapter (it calls
    `observer-internal:8081`) and a setup job.
  - **OpenObserve sizing:** requests 20m/500Mi, limits 500m/1000Mi.
  - **Adapter:** requests 50m/128Mi, limits 200m/256Mi.
  - **Retention:** no general log retention in the values; probably
    `ZO_COMPACT_DATA_RETENTION_DAYS` (inferred).
  - **Credentials:** the `openobserve-admin-credentials` secret.
  - **Tracing:** tracing-openobserve 0.2.1 can reuse the same instance.
- **metrics-prometheus 0.9.2** (`oci://cr.openchoreo.dev/openchoreo/helm-charts/observability-metrics-prometheus`):
  - **Deploys:** kube-prometheus-stack 92.16.0 with the Prometheus Operator,
    Prometheus (:9091), Alertmanager (1Gi), kube-state-metrics (pods only)
    and a metrics adapter. Grafana and node-exporter are off.
  - **CRDs:** `crds.enabled: true`, with no CRD-light option. Prometheus
    Operator's CRDs are large, the same risk as RP-046.
  - **Retention:** 10 days or 8 GiB, on a 10Gi PVC.
  - **Resources:** Prometheus has none set.
  - **Scraping:** the kubelet ServiceMonitor is on, and cAdvisor is likely
    scraped through it (inferred).
  - **HTTP metrics:** the source is unknown (Hubble isn't mentioned); check
    the adapter.

## SigNoz

- **Version:** chart `signoz-0.149.0` (2026-11-04).
- **Components:** ClickHouse 25.12.x and ZooKeeper, SigNoz core (SQLite,
  1Gi), the OTel collector and a migrator.
- **Sizing:** default requests are about 100m and 200–256Mi each, with no
  limits; ClickHouse has a 20Gi PVC. There is no official minimum; local
  docs suggest 8 GB and 4 CPU.
- **Kubernetes collection:** `k8s-infra` is a separate chart, and its log
  and kubelet-metric collection is unconfirmed.
- **Query API:** `/api/v5/query_range` with a `SIGNOZ-API-KEY` header
  (service account).
- **Fit:** not an OpenChoreo module, so there is no Observer adapter.

## Memory

No independent small-hardware measurements exist. The figures below are
estimates and claims, which is why the spike measures all three:
- **OpenSearch with `-Xmx512M`:** about 1 GB RSS (an estimate);
- **OpenObserve:** about 512 MB idle (a claim);
- **SigNoz:** 1.5–2 GB idle (a competitor's claim).

## Sources

openchoreo/openchoreo v1.3.3 (`install/helm/openchoreo-observability-plane`,
`openapi/observer-api.yaml`); github.com/openchoreo/community-modules;
openobserve.ai/blog/openchoreo-and-openobserve; signoz.io capacity docs;
github.com/SigNoz/charts.
