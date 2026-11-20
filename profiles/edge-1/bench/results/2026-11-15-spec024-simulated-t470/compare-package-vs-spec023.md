# Package without rules: spec 024 vs spec 023 (criterion 12)

Run A: `2026-11-14-spec023-simulated-t470/package/result.json`. Run B: `rules/result.json` (this run; its platform, package idle and load phases come before any rule exists).

```
bench-edge1 compare profiles/edge-1/bench/results/2026-11-14-spec023-simulated-t470/package/result.json profiles/edge-1/bench/results/2026-11-15-spec024-simulated-t470/rules/result.json
```

## Platform at idle

| Group | Memory A | Memory B | Memory | CPU A | CPU B | CPU |
|---|---|---|---|---|---|---|
| Kubernetes system | 765.0 | 757.5 | PASS | 168.7 | 171.6 | PASS |
| Secrets (OpenBao) | 17.6 | 18.7 | PASS | 8.9 | 7.9 | PASS |
| Certificates (cert-manager) | 65.1 | 62.3 | PASS | 3.7 | 2.8 | PASS |
| Application platform: control plane | 58.8 | 55.2 | PASS | 8.0 | 7.9 | PASS |
| Application platform: data plane | 6.5 | 6.7 | PASS | 0.1 | 0.0 | PASS |
| Gateway (kgateway) | 53.7 | 53.5 | PASS | 6.1 | 6.1 | PASS |
| Identity (ThunderID) | 12.0 | 12.1 | PASS | 0.3 | 0.3 | PASS |

Tolerance: memory ±10% for groups ≥ 50 MiB, else ±10 MiB; CPU ±25 millicores. "new" groups exist only in run B and are not part of the verdict. Overall: **PASS**

## Package installed, idle

| Group | Memory A | Memory B | Memory | CPU A | CPU B | CPU |
|---|---|---|---|---|---|---|
| Kubernetes system | 818.8 | 804.5 | PASS | 155.4 | 172.8 | PASS |
| Secrets (OpenBao) | 17.9 | 19.0 | PASS | 9.6 | 9.0 | PASS |
| Certificates (cert-manager) | 62.4 | 62.0 | PASS | 2.6 | 3.0 | PASS |
| Application platform: control plane | 57.9 | 55.6 | PASS | 7.8 | 7.7 | PASS |
| Application platform: data plane | 6.3 | 6.5 | PASS | 0.0 | 0.0 | PASS |
| Gateway (kgateway) | 54.9 | 54.6 | PASS | 5.5 | 5.9 | PASS |
| Identity (ThunderID) | 11.9 | 12.0 | PASS | 0.3 | 0.3 | PASS |
| Observability | 273.1 | 276.6 | PASS | 17.1 | 19.7 | PASS |

Tolerance: memory ±10% for groups ≥ 50 MiB, else ±10 MiB; CPU ±25 millicores. "new" groups exist only in run B and are not part of the verdict. Overall: **PASS**

## Package under load

| Group | Memory A | Memory B | Memory | CPU A | CPU B | CPU |
|---|---|---|---|---|---|---|
| Kubernetes system | 781.0 | 789.9 | PASS | 147.2 | 171.6 | PASS |
| Secrets (OpenBao) | 18.3 | 19.4 | PASS | 9.6 | 9.4 | PASS |
| Certificates (cert-manager) | 62.3 | 61.8 | PASS | 2.4 | 2.9 | PASS |
| Application platform: control plane | 58.2 | 55.8 | PASS | 7.3 | 7.6 | PASS |
| Application platform: data plane | 6.3 | 6.5 | PASS | 0.0 | 0.0 | PASS |
| Gateway (kgateway) | 55.9 | 54.8 | PASS | 5.2 | 5.9 | PASS |
| Identity (ThunderID) | 12.0 | 12.1 | PASS | 0.3 | 0.3 | PASS |
| Observability | 403.6 | 381.1 | PASS | 58.6 | 69.2 | PASS |
| Other: serendip-obs-load | 3.6 | 3.1 | PASS | 1.2 | 1.2 | PASS |

Tolerance: memory ±10% for groups ≥ 50 MiB, else ±10 MiB; CPU ±25 millicores. "new" groups exist only in run B and are not part of the verdict. Overall: **PASS**

serendipd, package idle: 30.5 → 82.1 MiB RSS, 0.87 → 1.13 mcores.


Volumes, run A → run B:

| Volume | Used A (MiB) | Used B (MiB) | Between starts | Per day |
|---|---|---|---|---|
| openchoreo-observability-plane/data-openobserve-0 | 6.5 | 6.8 | 46.3 h | +0.1 MiB |
| openchoreo-observability-plane/observer-alerts-data | 0.1 | 0.1 | 46.3 h | +0.0 MiB |
| openchoreo-observability-plane/openchoreo-observability-prometheus | 1.0 | 1.1 | 46.3 h | +0.1 MiB |
| serendip-identity/thunderid-certs-pvc | 0.0 | 0.0 | 46.3 h | +0.0 MiB |
| serendip-identity/thunderid-database-pvc | 1.3 | 1.3 | 46.3 h | +0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | 0.0 | 0.0 | 46.3 h | +0.0 MiB |
| serendip-system/openbao-data | 4.4 | 4.4 | 46.3 h | +0.0 MiB |
