| Group | Memory A | Memory B | Memory | CPU A | CPU B | CPU |
|---|---|---|---|---|---|---|
| Kubernetes system | 814.9 | 787.7 | PASS | 164.9 | 150.0 | PASS |
| Secrets (OpenBao) | 18.5 | 18.2 | PASS | 11.7 | 7.3 | PASS |
| Certificates (cert-manager) | 63.5 | 64.8 | PASS | 2.8 | 2.6 | PASS |
| Application platform: control plane | 55.8 | 54.6 | PASS | 7.6 | 5.3 | PASS |
| Application platform: data plane | 6.6 | 6.4 | PASS | 0.0 | 0.0 | PASS |
| Gateway (kgateway) | 52.6 | 53.2 | PASS | 5.9 | 5.5 | PASS |
| Identity (ThunderID) | 12.6 | 12.1 | PASS | 0.4 | 0.3 | PASS |

Tolerance: memory ±10% for groups ≥ 50 MiB, else ±10 MiB; CPU ±25 millicores. "new" groups exist only in run B and are not part of the verdict. Overall: **PASS**

Volumes, run A → run B:

| Volume | Used A (MiB) | Used B (MiB) | Between starts | Per day |
|---|---|---|---|---|
| serendip-identity/thunderid-certs-pvc | — | 0.0 | 190.7 h | new |
| serendip-identity/thunderid-database-pvc | — | 1.2 | 190.7 h | new |
| serendip-identity/thunderid-secrets-pvc | — | 0.0 | 190.7 h | new |
| serendip-system/openbao-data | — | 4.4 | 190.7 h | new |
