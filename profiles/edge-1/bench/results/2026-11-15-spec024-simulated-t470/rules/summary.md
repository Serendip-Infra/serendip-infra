> **SIMULATION — does not satisfy the reference-hardware gate.**

# edge-1 footprint — 2026-11-16 11:09 +0530

## Machine

- Host: windows/amd64, AMD Ryzen 7 7840HS with Radeon 780M Graphics
- Node: bench-t470-control-plane (Debian GNU/Linux 12 (bookworm), kernel 6.6.87.2-microsoft-standard-WSL2, containerd://2.1.3, kubelet v1.34.0)
- Node capacity reported by Kubernetes: 4 CPU, 8133872Ki memory
- Simulated: 2.0 CPUs of quota on cpuset 0-3. kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t)
- Not simulated: 16-32 GB RAM (only measured, not limited)
- Not simulated: SATA/NVMe disk speed
- Settle 180s, then 12 samples every 10s

### Platform at idle (no workloads)

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 757.5 | 802.8 | 171.6 | 193.6 |
| Secrets (OpenBao) | 18.7 | 19.0 | 7.9 | 10.6 |
| Certificates (cert-manager) | 62.3 | 62.6 | 2.8 | 4.4 |
| Application platform: control plane | 55.2 | 56.1 | 7.9 | 9.9 |
| Application platform: data plane | 6.7 | 6.9 | 0.0 | 0.2 |
| Gateway (kgateway) | 53.5 | 53.6 | 6.1 | 7.4 |
| Identity (ThunderID) | 12.1 | 12.2 | 0.3 | 0.8 |
| Node overhead outside pods (kubelet, container runtime, system) | 455.9 | 461.8 | 71.3 | 137.5 |
| **Node total** | 1422.0 | 1466.0 | 268.0 | 329.1 |

Container images on disk: 2204 MiB. (Node filesystem used: 84988 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 517.3 | 84.6 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 93.4 | 46.5 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 62.3 | 25.5 |
| kgateway-system/kgateway-7b8984bc55-mxcgv | Gateway (kgateway) | 35.7 | 2.2 |
| cert-manager/cert-manager-bc44b5799-gh9js | Certificates (cert-manager) | 26.3 | 1.2 |
| cert-manager/cert-manager-cainjector-784846b495-5grp7 | Certificates (cert-manager) | 23.8 | 1.1 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 23.1 | 9.1 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-bdqnb | Application platform: control plane | 22.6 | 7.0 |
| serendip-system/openbao-0 | Secrets (OpenBao) | 18.7 | 7.9 |
| kgateway-system/serendip-external-657d66d6c7-mxcwc | Gateway (kgateway) | 17.9 | 3.8 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 1m59s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 1.1 | 1.1 | 1024 | +0.0 MiB in 1m59s | 0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 1m59s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 1m59s | 0.0 MiB |

### serendipd (host process)

| | Mean | Max |
|---|---|---|
| RSS (MiB) | 79.0 | 79.0 |
| CPU (millicores) | 0.1 | 1.4 |

### Constitution rule 4 (control plane < 256 MiB RSS, < 5% CPU at idle)

- RSS 79.0 MiB: **PASS**
- CPU 0.01% of 2.0 cores: **PASS**
- covers serendipd only: serendip-agent is not a long-running daemon yet and is not measured


### 16 GB headroom

**Verdict: not computed: no light-workload data.**

### Timings

- Cold start (fresh cluster, includes image pulls): 372s
- Warm restart (everything installed): 35s
- Component create → Serving:  (includes the image pull; status is re-checked about 2, 6, 14, 22 and 30s after create)

## Package: openchoreo-observability-plane

Install (create → Ready, with image pulls): 4m38s

### Installed, idle (60 samples)

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 804.5 | 833.6 | 172.8 | 214.0 |
| Secrets (OpenBao) | 19.0 | 19.2 | 9.0 | 11.3 |
| Certificates (cert-manager) | 62.0 | 62.1 | 3.0 | 7.3 |
| Application platform: control plane | 55.6 | 55.9 | 7.7 | 8.9 |
| Application platform: data plane | 6.5 | 6.5 | 0.0 | 0.3 |
| Gateway (kgateway) | 54.6 | 54.6 | 5.9 | 7.6 |
| Identity (ThunderID) | 12.0 | 12.1 | 0.3 | 1.1 |
| Observability | 276.6 | 298.4 | 19.7 | 30.5 |
| Node overhead outside pods (kubelet, container runtime, system) | 769.0 | 813.1 | 91.0 | 194.2 |
| **Node total** | 2059.8 | 2121.8 | 309.5 | 436.6 |

Container images on disk: 3209 MiB. (Node filesystem used: 86010 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 546.6 | 87.3 |
| openchoreo-observability-plane/openobserve-0 | Observability | 124.8 | 7.5 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 101.2 | 45.9 |
| openchoreo-observability-plane/openchoreo-observability-prometheus-84746b7844-v9zmk | Observability | 87.1 | 6.6 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 67.3 | 24.7 |
| kgateway-system/kgateway-7b8984bc55-mxcgv | Gateway (kgateway) | 35.9 | 2.0 |
| cert-manager/cert-manager-bc44b5799-gh9js | Certificates (cert-manager) | 26.0 | 1.2 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 25.2 | 9.0 |
| cert-manager/cert-manager-cainjector-784846b495-5grp7 | Certificates (cert-manager) | 23.7 | 1.2 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-bdqnb | Application platform: control plane | 22.4 | 6.6 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| openchoreo-observability-plane/data-openobserve-0 | Observability | 6.8 | 6.8 | 5120 | -0.0 MiB in 10m23s | -2.7 MiB |
| openchoreo-observability-plane/observer-alerts-data | Observability | 0.1 | 0.1 | 128 | +0.0 MiB in 10m23s | 0.0 MiB |
| openchoreo-observability-plane/openchoreo-observability-prometheus | Observability | 0.6 | 1.1 | 3072 | +0.5 MiB in 10m23s | 70.5 MiB |
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 10m23s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 1.2 | 1.3 | 1024 | +0.1 MiB in 10m23s | 16.3 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 10m23s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 10m23s | 0.0 MiB |

serendipd with the package idle: 82.1 MiB RSS, 1.13 mcores mean (without the package, same run: 79.0 MiB, 0.13 mcores). Resync on the fingerprint path: **+1.00 mcores**.

### Under load (180 samples over 31 min)

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 789.9 | 824.4 | 171.6 | 266.2 |
| Secrets (OpenBao) | 19.4 | 19.6 | 9.4 | 14.1 |
| Certificates (cert-manager) | 61.8 | 62.0 | 2.9 | 5.0 |
| Application platform: control plane | 55.8 | 55.9 | 7.6 | 10.8 |
| Application platform: data plane | 6.5 | 6.6 | 0.0 | 0.3 |
| Gateway (kgateway) | 54.8 | 55.1 | 5.9 | 8.0 |
| Identity (ThunderID) | 12.1 | 12.2 | 0.3 | 1.1 |
| Observability | 381.1 | 500.6 | 69.2 | 131.7 |
| Other: serendip-obs-load | 3.1 | 3.1 | 1.2 | 38.5 |
| Node overhead outside pods (kubelet, container runtime, system) | 821.2 | 927.4 | 107.2 | 513.5 |
| **Node total** | 2205.6 | 2351.3 | 375.4 | 743.6 |

Container images on disk: 3231 MiB. (Node filesystem used: 86078 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 553.7 | 86.6 |
| openchoreo-observability-plane/openobserve-0 | Observability | 219.5 | 45.9 |
| openchoreo-observability-plane/openchoreo-observability-prometheus-84746b7844-v9zmk | Observability | 87.8 | 7.6 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 75.2 | 45.5 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 69.2 | 24.1 |
| kgateway-system/kgateway-7b8984bc55-mxcgv | Gateway (kgateway) | 36.2 | 2.0 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 26.8 | 9.0 |
| cert-manager/cert-manager-bc44b5799-gh9js | Certificates (cert-manager) | 25.9 | 1.2 |
| cert-manager/cert-manager-cainjector-784846b495-5grp7 | Certificates (cert-manager) | 23.7 | 1.2 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-bdqnb | Application platform: control plane | 22.5 | 6.5 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| openchoreo-observability-plane/data-openobserve-0 | Observability | 6.9 | 20.3 | 5120 | +13.4 MiB in 31m25s | 615.7 MiB |
| openchoreo-observability-plane/observer-alerts-data | Observability | 0.1 | 0.1 | 128 | +0.0 MiB in 31m25s | 0.0 MiB |
| openchoreo-observability-plane/openchoreo-observability-prometheus | Observability | 1.1 | 2.6 | 3072 | +1.5 MiB in 31m25s | 68.2 MiB |
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 31m25s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 1.3 | 1.7 | 1024 | +0.3 MiB in 31m25s | 15.9 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 31m25s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 31m25s | 0.0 MiB |

serendipd: 82.1 MiB RSS, 1.11 mcores mean.

### Disk phase, still under load (121 samples over 121 min)

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 842.4 | 879.6 | 180.6 | 274.6 |
| Secrets (OpenBao) | 19.7 | 20.0 | 7.3 | 11.6 |
| Certificates (cert-manager) | 62.7 | 63.0 | 2.5 | 3.8 |
| Application platform: control plane | 68.6 | 69.2 | 8.1 | 11.8 |
| Application platform: data plane | 8.1 | 8.6 | 0.2 | 1.9 |
| Gateway (kgateway) | 57.3 | 57.6 | 5.4 | 7.6 |
| Identity (ThunderID) | 12.2 | 12.4 | 0.3 | 1.3 |
| Workloads | 14.1 | 14.1 | 0.0 | 0.0 |
| Observability | 549.1 | 819.5 | 225.7 | 596.7 |
| Other: serendip-obs-load | 3.1 | 3.1 | 1.0 | 1.2 |
| Node overhead outside pods (kubelet, container runtime, system) | 941.1 | 1121.8 | 114.6 | 615.5 |
| **Node total** | 2578.5 | 2926.9 | 545.6 | 1111.2 |

Container images on disk: 3282 MiB. (Node filesystem used: 86394 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 597.1 | 91.7 |
| openchoreo-observability-plane/openobserve-0 | Observability | 375.4 | 178.7 |
| openchoreo-observability-plane/openchoreo-observability-prometheus-84746b7844-v9zmk | Observability | 91.8 | 23.1 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 74.0 | 50.5 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 71.5 | 21.1 |
| kgateway-system/kgateway-7b8984bc55-mxcgv | Gateway (kgateway) | 38.5 | 1.9 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-bdqnb | Application platform: control plane | 33.8 | 6.9 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 28.7 | 7.8 |
| cert-manager/cert-manager-bc44b5799-gh9js | Certificates (cert-manager) | 26.4 | 1.0 |
| cert-manager/cert-manager-cainjector-784846b495-5grp7 | Certificates (cert-manager) | 23.8 | 1.0 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| openchoreo-observability-plane/data-openobserve-0 | Observability | 48.6 | 165.6 | 5120 | +117.0 MiB in 2h0m58s | 1393.0 MiB |
| openchoreo-observability-plane/observer-alerts-data | Observability | 0.1 | 0.1 | 128 | +0.0 MiB in 2h0m58s | 0.0 MiB |
| openchoreo-observability-plane/openchoreo-observability-prometheus | Observability | 5.4 | 15.6 | 3072 | +10.1 MiB in 2h0m58s | 120.7 MiB |
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 2h0m58s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 1.6 | 1.8 | 1024 | +0.2 MiB in 2h0m58s | 2.5 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 2h0m58s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 2h0m58s | 0.0 MiB |

serendipd: 68.6 MiB RSS, 6.78 mcores mean.

## Alert rules

199 rules at 1 minute over 3 Components (logMatch-15m 33, logMatch-5m 33, metric-cpu 34, metric-memory 33, restarts 66), under the log load.

| Phase | Node mean / max (MiB) | Node CPU mean / max (mcores) | serendipd RSS mean / max (MiB) | serendipd CPU mean / max (mcores) | Resource writes / min | Audit rows / min |
|---|---|---|---|---|---|---|
| Baseline (no rules) | 2322 / 2450 | 397 / 512 | 82.8 / 83.5 | 2.57 / 14.72 | 0.2 | 0.2 |
| Rules disabled | 2340 / 2539 | 399 / 574 | 84.8 / 85.6 | 2.95 / 13.29 | 0.1 | 0.1 |
| Rules evaluating | 2499 / 2852 | 631 / 1201 | 88.4 / 90.3 | 6.79 / 40.16 | 25.9 | 25.9 |

- Resync (rules disabled − baseline): **+0.38 mcores**
- Evaluation (evaluating − baseline): **+4.22 mcores, +5.5 MiB RSS**
- Resource writes while evaluating, by kind: AlertRule 814
- Rules' Evaluating condition at the end: False/NoData 27, True/Evaluated 173; Alerts firing: 0

### Evaluator timings (one line per window, from serendipd's log)

| # | Window | Queries | Failed | Mean | p50 | p95 | Max | Slowest tick | Latest pick-up |
|---|---|---|---|---|---|---|---|---|---|
| 1 | 10m19s | 804 | 39 | 264 ms | 20 ms | 959 ms | 2241 ms | 33.9 s | 689.9 s |
| 2 | 10m1s | 1800 | 141 | 249 ms | 18 ms | 1081 ms | 3277 ms | 37.9 s | 15.0 s |
| 3 | 10m14s | 1800 | 109 | 258 ms | 19 ms | 1141 ms | 2690 ms | 37.7 s | 17.9 s |
| 4 | 10m32s | 1800 | 81 | 255 ms | 18 ms | 1076 ms | 2372 ms | 38.2 s | 20.3 s |
| 5 | 10m17s | 1800 | 118 | 242 ms | 18 ms | 1089 ms | 1865 ms | 38.1 s | 19.0 s |
| 6 | 10m46s | 1800 | 93 | 276 ms | 18 ms | 1155 ms | 4879 ms | 47.8 s | 35.6 s |
| 7 | 10m37s | 1800 | 119 | 237 ms | 18 ms | 1059 ms | 2774 ms | 36.8 s | 20.0 s |
| 8 | 10m15s | 1800 | 76 | 236 ms | 17 ms | 1010 ms | 2185 ms | 36.5 s | 15.0 s |
| 9 | 10m15s | 1800 | 92 | 224 ms | 15 ms | 973 ms | 1964 ms | 31.8 s | 15.0 s |
| 10 | 10m15s | 1800 | 150 | 198 ms | 14 ms | 861 ms | 1880 ms | 32.2 s | 15.0 s |
| 11 | 10m15s | 1600 | 90 | 198 ms | 14 ms | 787 ms | 1780 ms | 29.8 s | 15.0 s |
| 12 | 10m10s | 2000 | 145 | 195 ms | 14 ms | 783 ms | 4077 ms | 36.7 s | 15.0 s |
| 13 | 10m5s | 1600 | 67 | 204 ms | 14 ms | 788 ms | 3581 ms | 32.0 s | 15.0 s |
| 14 | 10m0s | 1800 | 56 | 211 ms | 14 ms | 809 ms | 2171 ms | 29.4 s | 15.0 s |
| 15 | 10m15s | 1800 | 114 | 206 ms | 15 ms | 858 ms | 2073 ms | 29.8 s | 15.0 s |

The load driver's queries:

```
metrics: 383 queries, 0 not 2xx, mean 0.018 s, max 0.120 s
logs: 1149 queries, 1 not 2xx, mean 0.903 s, max 5.122 s
```

### Stated cost (Requirements)

The node's use beyond the platform phase above (1422 MiB, 268 mcores mean), never below the package's own pods.

| | Idle memory | Idle CPU | Peak memory | Peak CPU |
|---|---|---|---|---|
| **Stated (node)** | 638 MiB | 42 mcores | 1505 MiB | 844 mcores |
| Package's own pods | 277 MiB | 20 mcores | 820 MiB | 597 mcores |

Disk: the package's 3 volumes grew 127.2 MiB over 121 min of the disk phase, **1514 MiB a day** extrapolated, of 8320 MiB capacity.
