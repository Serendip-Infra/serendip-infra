# Versions: providers/observability/openchoreo

Upstream version assumptions for the observability package (spec 023).
Update this file and the constants named below together. The "Defined in"
column names files T005 and T006 create. Verified in T001
(2026-11-12, `serendip-t018`) unless marked otherwise.

| Item | Version / value | Defined in |
|---|---|---|
| OpenChoreo observability-plane chart | 1.3.1, `oci://ghcr.io/openchoreo/helm-charts/openchoreo-observability-plane` (digest `sha256:0a459a90d51c28781bfe0189a1fb3757fe19b0901fd7bcabbe9dcd8ddc84858f`); matches the control plane (1.3.3 exists) | `installer.go` |
| Logs module (config B) | `observability-logs-openobserve` 0.8.0, `oci://cr.openchoreo.dev/openchoreo/helm-charts/` (digest `sha256:4f99feb62be138db3558e6690ad708d41f23b2fd42728bf31824a028daa02a24`); OpenObserve `v0.92.2`, Fluent Bit `5.1.5` | `values.go` |
| Logs module (config A, T003 only) | `observability-logs-opensearch` 0.8.1 (not installed in T001) | n/a |
| Metrics module | `observability-metrics-prometheus` 0.9.3 (digest `sha256:ed4245d732518d283f30fb1f7879f0e512e31b497e0cabfbac13c123090d11fd`); kube-prometheus-stack with 10 `monitoring.coreos.com` CRDs (4.7 MB). T003 may replace it (plan decision 3) | `values.go` |
| Query API | the adapters' `POST /api/v1/logs/query` (`logs-adapter:9098`) and `POST /api/v1/metrics/query` (`metrics-adapter:9099`), from `openapi/observability-{logs,metrics}-adapter*.yaml` at v1.3.1 | `observer.go` |
| Plane object | `ObservabilityPlane/default` in `openchoreo-control-plane` (namespaced; a namespaced `DataPlane` falls back to it by name) | `installer.go` |
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
- **Scopes are UIDs** (`projectUid`, `componentUid`, `environmentUid`).
  The adapters ignore names, and the query widens to the whole namespace.
- **The chart's own NetworkPolicy blocks `hostNetwork` Fluent Bit.**
  Serendip adds a rule for the nodes' current `InternalIP`s, on
  OpenObserve's port 5080 only.
- **Uninstall leaves** the PVCs, the Secrets, the CA ConfigMap, a setup-job
  pod, and the CRDs (Prometheus Operator's 10, and `observabilityalertrules`).
- **Re-check on every upgrade:**
  - the policy selectors (the labels `app.kubernetes.io/name=openobserve-standalone`
    and others);
  - the adapter request schema;
  - the hardcoded `Use_Kubelet true`.
