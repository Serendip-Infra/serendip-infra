> **SIMULATION — does not satisfy the reference-hardware gate.**

# edge-1 footprint — 2026-11-23 15:02 +0530

## Machine

- Host: windows/amd64, AMD Ryzen 7 7840HS with Radeon 780M Graphics
- Node: bench-t470-control-plane (Debian GNU/Linux 12 (bookworm), kernel 6.6.87.2-microsoft-standard-WSL2, containerd://2.1.3, kubelet v1.34.0)
- Node capacity reported by Kubernetes: 4 CPU, 8133876Ki memory
- Simulated: 2.0 CPUs of quota on cpuset 0-3. kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t); CNI kindnet
- Not simulated: 16-32 GB RAM (only measured, not limited)
- Not simulated: SATA/NVMe disk speed
- Settle 180s, then 12 samples every 10s

### Platform at idle (no workloads)

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 782.7 | 787.3 | 148.6 | 180.4 |
| Secrets (OpenBao) | 18.6 | 18.7 | 7.8 | 9.8 |
| Certificates (cert-manager) | 63.7 | 64.6 | 2.7 | 5.0 |
| Application platform: control plane | 54.2 | 55.2 | 6.4 | 7.5 |
| Application platform: data plane | 6.6 | 6.9 | 0.1 | 0.5 |
| Gateway (kgateway) | 55.4 | 55.5 | 6.2 | 8.5 |
| Identity (ThunderID) | 12.2 | 12.3 | 0.3 | 0.7 |
| Node overhead outside pods (kubelet, container runtime, system) | 545.0 | 546.3 | 63.3 | 134.2 |
| **Node total** | 1538.5 | 1543.5 | 235.4 | 307.3 |

Container images on disk: 2234 MiB. (Node filesystem used: 102243 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 538.6 | 74.4 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 96.2 | 40.2 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 64.5 | 20.3 |
| kgateway-system/kgateway-7b8984bc55-spmkl | Gateway (kgateway) | 36.2 | 2.1 |
| cert-manager/cert-manager-bc44b5799-ghhtg | Certificates (cert-manager) | 26.0 | 1.1 |
| cert-manager/cert-manager-cainjector-784846b495-4pd42 | Certificates (cert-manager) | 25.1 | 1.1 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 21.9 | 7.4 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-n6v5l | Application platform: control plane | 21.9 | 5.5 |
| kgateway-system/serendip-external-657d66d6c7-kwmk7 | Gateway (kgateway) | 19.2 | 4.0 |
| serendip-system/openbao-0 | Secrets (OpenBao) | 18.6 | 7.8 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 1m58s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 1.1 | 1.1 | 1024 | +0.0 MiB in 1m58s | 0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 1m58s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 1m58s | 0.0 MiB |

### serendipd (host process)

| | Mean | Max |
|---|---|---|
| RSS (MiB) | 33.2 | 33.6 |
| CPU (millicores) | 0.6 | 6.1 |

### Constitution rule 4 (control plane < 256 MiB RSS, < 5% CPU at idle)

- RSS 33.2 MiB: **PASS**
- CPU 0.03% of 2.0 cores: **PASS**
- covers serendipd only: serendip-agent is not a long-running daemon yet and is not measured


### 16 GB headroom

**Verdict: not computed: no light-workload data.**

### Timings

- Cold start (fresh cluster, includes image pulls): 253s
- Warm restart (everything installed): 101s
- Component create → Serving:  (includes the image pull; status is re-checked about 2, 6, 14, 22 and 30s after create)

## Package: openchoreo-observability-plane

Install (create → Ready, with image pulls): 4m38s

### Installed, idle (60 samples)

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 818.6 | 875.5 | 152.1 | 198.5 |
| Secrets (OpenBao) | 18.9 | 19.2 | 7.8 | 9.9 |
| Certificates (cert-manager) | 63.2 | 63.6 | 2.5 | 3.9 |
| Application platform: control plane | 55.9 | 56.4 | 6.8 | 7.9 |
| Application platform: data plane | 6.4 | 6.4 | 0.0 | 0.3 |
| Gateway (kgateway) | 55.8 | 55.9 | 5.9 | 7.0 |
| Identity (ThunderID) | 12.2 | 12.2 | 0.3 | 0.8 |
| Observability | 274.3 | 303.3 | 17.4 | 23.3 |
| Node overhead outside pods (kubelet, container runtime, system) | 825.2 | 908.4 | 77.7 | 148.5 |
| **Node total** | 2130.6 | 2196.8 | 270.4 | 334.8 |

Container images on disk: 3244 MiB. (Node filesystem used: 103270 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 561.3 | 76.9 |
| openchoreo-observability-plane/openobserve-0 | Observability | 129.9 | 6.5 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 98.9 | 41.1 |
| openchoreo-observability-plane/openchoreo-observability-prometheus-84746b7844-gpfnl | Observability | 80.0 | 5.5 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 68.8 | 20.3 |
| kgateway-system/kgateway-7b8984bc55-spmkl | Gateway (kgateway) | 35.9 | 1.8 |
| cert-manager/cert-manager-bc44b5799-ghhtg | Certificates (cert-manager) | 25.8 | 1.0 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 25.6 | 7.5 |
| cert-manager/cert-manager-cainjector-784846b495-4pd42 | Certificates (cert-manager) | 24.9 | 1.0 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-n6v5l | Application platform: control plane | 22.6 | 5.8 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| openchoreo-observability-plane/data-openobserve-0 | Observability | 6.9 | 6.8 | 5120 | -0.0 MiB in 10m18s | -3.8 MiB |
| openchoreo-observability-plane/observer-alerts-data | Observability | 0.1 | 0.1 | 128 | +0.0 MiB in 10m18s | 0.0 MiB |
| openchoreo-observability-plane/openchoreo-observability-prometheus | Observability | 0.6 | 1.0 | 3072 | +0.4 MiB in 10m18s | 59.5 MiB |
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 10m18s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 1.2 | 1.3 | 1024 | +0.1 MiB in 10m18s | 16.4 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 10m18s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 10m18s | 0.0 MiB |

serendipd with the package idle: 35.7 MiB RSS, 1.02 mcores mean (without the package, same run: 33.2 MiB, 0.55 mcores). Resync on the fingerprint path: **+0.47 mcores**.

### Under load (60 samples over 10 min)

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 803.5 | 825.3 | 151.8 | 221.3 |
| Secrets (OpenBao) | 19.0 | 19.2 | 8.9 | 13.0 |
| Certificates (cert-manager) | 63.3 | 63.4 | 2.5 | 3.6 |
| Application platform: control plane | 56.1 | 56.2 | 6.7 | 7.8 |
| Application platform: data plane | 6.4 | 6.4 | 0.0 | 0.3 |
| Gateway (kgateway) | 56.2 | 56.6 | 6.0 | 8.3 |
| Identity (ThunderID) | 12.2 | 12.3 | 0.3 | 0.7 |
| Observability | 382.9 | 465.5 | 54.2 | 72.2 |
| Other: serendip-obs-load | 3.2 | 3.3 | 1.6 | 30.4 |
| Node overhead outside pods (kubelet, container runtime, system) | 866.6 | 964.4 | 96.7 | 476.4 |
| **Node total** | 2269.5 | 2367.6 | 328.7 | 751.9 |

Container images on disk: 3266 MiB. (Node filesystem used: 103314 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 563.1 | 75.5 |
| openchoreo-observability-plane/openobserve-0 | Observability | 224.6 | 33.8 |
| openchoreo-observability-plane/openchoreo-observability-prometheus-84746b7844-gpfnl | Observability | 85.7 | 6.4 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 78.0 | 41.5 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 70.5 | 20.6 |
| kgateway-system/kgateway-7b8984bc55-spmkl | Gateway (kgateway) | 36.3 | 1.8 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 27.2 | 7.5 |
| cert-manager/cert-manager-bc44b5799-ghhtg | Certificates (cert-manager) | 25.9 | 1.0 |
| cert-manager/cert-manager-cainjector-784846b495-4pd42 | Certificates (cert-manager) | 24.9 | 1.0 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-n6v5l | Application platform: control plane | 22.7 | 5.7 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| openchoreo-observability-plane/data-openobserve-0 | Observability | 7.1 | 15.8 | 5120 | +8.7 MiB in 10m18s | 1215.8 MiB |
| openchoreo-observability-plane/observer-alerts-data | Observability | 0.1 | 0.1 | 128 | +0.0 MiB in 10m18s | 0.0 MiB |
| openchoreo-observability-plane/openchoreo-observability-prometheus | Observability | 1.1 | 1.7 | 3072 | +0.7 MiB in 10m18s | 93.3 MiB |
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 10m18s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 1.3 | 1.5 | 1024 | +0.1 MiB in 10m18s | 15.3 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 10m18s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 10m18s | 0.0 MiB |

serendipd: 35.8 MiB RSS, 1.25 mcores mean.

## HTTP traffic and tracing

A caller calling a server every 100 ms (t012-otel:2), under the log load.

| Phase | Node mean / max (MiB) | Node CPU mean / max (mcores) | Networking (Cilium) MiB / mcores | Observability MiB / mcores | Tracing (collector) MiB / mcores | Workloads MiB / mcores | serendipd RSS / CPU |
|---|---|---|---|---|---|---|---|
| HTTP traffic | 2413 / 2630 | 355 / 443 | – | 426 / 68 | – | 28 / 15 | 36.8 MiB / 1.52 mcores |
| HTTP traffic, traced | 2558 / 2723 | 365 / 550 | – | 443 / 73 | 49 / 3 | 19 / 15 | 37.1 MiB / 2.57 mcores |

- Tracing installed in 1m24s; traced − untraced, node: **+145 MiB mean (+93 at max), +10 mcores mean (+107 at max)**
- Observability volumes, untraced: +0.0 MiB over 20m43s, about 0 MiB a day
- Observability volumes, traced: +22.7 MiB over 20m44s, about 1579 MiB a day
- The server's Deployment listed 50 traces from the last 5 minutes at the end

The load driver's queries:

```
metrics: 126 queries, 0 not 2xx, mean 0.014 s, max 0.083 s
logs: 377 queries, 0 not 2xx, mean 0.488 s, max 1.136 s
```

### Stated cost (Requirements)

The node's use beyond the platform phase above (1538 MiB, 235 mcores mean), never below the package's own pods.

| | Idle memory | Idle CPU | Peak memory | Peak CPU |
|---|---|---|---|---|
| **Stated (node)** | 593 MiB | 36 mcores | 830 MiB | 517 mcores |
| Package's own pods | 275 MiB | 18 mcores | 466 MiB | 73 mcores |
