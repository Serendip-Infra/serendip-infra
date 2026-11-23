| Group | Memory A | Memory B | Memory | CPU A | CPU B | CPU |
|---|---|---|---|---|---|---|
| Kubernetes system | 757.5 | 782.7 | PASS | 171.6 | 148.6 | PASS |
| Secrets (OpenBao) | 18.7 | 18.6 | PASS | 7.9 | 7.8 | PASS |
| Certificates (cert-manager) | 62.3 | 63.7 | PASS | 2.8 | 2.7 | PASS |
| Application platform: control plane | 55.2 | 54.2 | PASS | 7.9 | 6.4 | PASS |
| Application platform: data plane | 6.7 | 6.6 | PASS | 0.0 | 0.1 | PASS |
| Gateway (kgateway) | 53.5 | 55.4 | PASS | 6.1 | 6.2 | PASS |
| Identity (ThunderID) | 12.1 | 12.2 | PASS | 0.3 | 0.3 | PASS |

Tolerance: memory ±10% for groups ≥ 50 MiB, else ±10 MiB; CPU ±25 millicores. "new" groups exist only in run B and are not part of the verdict. Overall: **PASS**

## Package installed, idle

| Group | Memory A | Memory B | Memory | CPU A | CPU B | CPU |
|---|---|---|---|---|---|---|
| Kubernetes system | 804.5 | 818.6 | PASS | 172.8 | 152.1 | PASS |
| Secrets (OpenBao) | 19.0 | 18.9 | PASS | 9.0 | 7.8 | PASS |
| Certificates (cert-manager) | 62.0 | 63.2 | PASS | 3.0 | 2.5 | PASS |
| Application platform: control plane | 55.6 | 55.9 | PASS | 7.7 | 6.8 | PASS |
| Application platform: data plane | 6.5 | 6.4 | PASS | 0.0 | 0.0 | PASS |
| Gateway (kgateway) | 54.6 | 55.8 | PASS | 5.9 | 5.9 | PASS |
| Identity (ThunderID) | 12.0 | 12.2 | PASS | 0.3 | 0.3 | PASS |
| Observability | 276.6 | 274.3 | PASS | 19.7 | 17.4 | PASS |

Tolerance: memory ±10% for groups ≥ 50 MiB, else ±10 MiB; CPU ±25 millicores. "new" groups exist only in run B and are not part of the verdict. Overall: **PASS**

## Package under load

| Group | Memory A | Memory B | Memory | CPU A | CPU B | CPU |
|---|---|---|---|---|---|---|
| Kubernetes system | 789.9 | 803.5 | PASS | 171.6 | 151.8 | PASS |
| Secrets (OpenBao) | 19.4 | 19.0 | PASS | 9.4 | 8.9 | PASS |
| Certificates (cert-manager) | 61.8 | 63.3 | PASS | 2.9 | 2.5 | PASS |
| Application platform: control plane | 55.8 | 56.1 | PASS | 7.6 | 6.7 | PASS |
| Application platform: data plane | 6.5 | 6.4 | PASS | 0.0 | 0.0 | PASS |
| Gateway (kgateway) | 54.8 | 56.2 | PASS | 5.9 | 6.0 | PASS |
| Identity (ThunderID) | 12.1 | 12.2 | PASS | 0.3 | 0.3 | PASS |
| Observability | 381.1 | 382.9 | PASS | 69.2 | 54.2 | PASS |
| Other: serendip-obs-load | 3.1 | 3.2 | PASS | 1.2 | 1.6 | PASS |

Tolerance: memory ±10% for groups ≥ 50 MiB, else ±10 MiB; CPU ±25 millicores. "new" groups exist only in run B and are not part of the verdict. Overall: **PASS**

serendipd, package idle: 82.1 → 35.7 MiB RSS, 1.13 → 1.02 mcores.


Volumes, run A → run B:

| Volume | Used A (MiB) | Used B (MiB) | Between starts | Per day |
|---|---|---|---|---|
| openchoreo-observability-plane/data-openobserve-0 | 6.8 | 6.8 | 171.9 h | +0.0 MiB |
| openchoreo-observability-plane/observer-alerts-data | 0.1 | 0.1 | 171.9 h | +0.0 MiB |
| openchoreo-observability-plane/openchoreo-observability-prometheus | 1.1 | 1.0 | 171.9 h | -0.0 MiB |
| serendip-identity/thunderid-certs-pvc | 0.0 | 0.0 | 171.9 h | +0.0 MiB |
| serendip-identity/thunderid-database-pvc | 1.3 | 1.3 | 171.9 h | +0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | 0.0 | 0.0 | 171.9 h | +0.0 MiB |
| serendip-system/openbao-data | 4.4 | 4.4 | 171.9 h | +0.0 MiB |
