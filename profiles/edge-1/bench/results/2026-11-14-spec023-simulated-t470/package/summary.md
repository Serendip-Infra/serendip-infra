> **SIMULATION — does not satisfy the reference-hardware gate.**

# edge-1 footprint — 2026-11-14 12:52 +0530

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
| Kubernetes system | 765.0 | 791.7 | 168.7 | 199.5 |
| Secrets (OpenBao) | 17.6 | 17.6 | 8.9 | 11.7 |
| Certificates (cert-manager) | 65.1 | 68.4 | 3.7 | 10.1 |
| Application platform: control plane | 58.8 | 61.5 | 8.0 | 9.1 |
| Application platform: data plane | 6.5 | 6.6 | 0.1 | 0.2 |
| Gateway (kgateway) | 53.7 | 53.7 | 6.1 | 7.1 |
| Identity (ThunderID) | 12.0 | 12.0 | 0.3 | 0.6 |
| Node overhead outside pods (kubelet, container runtime, system) | 487.3 | 489.4 | 61.9 | 87.7 |
| **Node total** | 1466.0 | 1499.9 | 257.6 | 310.3 |

Container images on disk: 2204 MiB. (Node filesystem used: 82189 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 523.2 | 86.8 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 94.7 | 44.6 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 64.1 | 23.0 |
| kgateway-system/kgateway-7b8984bc55-259cf | Gateway (kgateway) | 35.8 | 2.3 |
| cert-manager/cert-manager-bc44b5799-qs45p | Certificates (cert-manager) | 27.8 | 2.1 |
| cert-manager/cert-manager-cainjector-784846b495-vfczc | Certificates (cert-manager) | 24.4 | 1.0 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-7kvp7 | Application platform: control plane | 23.6 | 6.9 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 21.7 | 8.0 |
| kgateway-system/serendip-external-657d66d6c7-4tdtm | Gateway (kgateway) | 17.9 | 3.8 |
| serendip-system/openbao-0 | Secrets (OpenBao) | 17.6 | 8.9 |

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
| RSS (MiB) | 29.2 | 29.5 |
| CPU (millicores) | 0.0 | 0.0 |

### Constitution rule 4 (control plane < 256 MiB RSS, < 5% CPU at idle)

- RSS 29.2 MiB: **PASS**
- CPU 0.00% of 2.0 cores: **PASS**
- covers serendipd only: serendip-agent is not a long-running daemon yet and is not measured


### 16 GB headroom

**Verdict: not computed: no light-workload data.**

### Timings

- Cold start (fresh cluster, includes image pulls): 288s
- Warm restart (everything installed): 32s
- Component create → Serving:  (includes the image pull; status is re-checked about 2, 6, 14, 22 and 30s after create)

## Package: openchoreo-observability-plane

Install (create → Ready, with image pulls): 3m34s

### Installed, idle (60 samples)

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 818.8 | 848.1 | 155.4 | 206.3 |
| Secrets (OpenBao) | 17.9 | 18.2 | 9.6 | 12.0 |
| Certificates (cert-manager) | 62.4 | 62.5 | 2.6 | 3.8 |
| Application platform: control plane | 57.9 | 58.9 | 7.8 | 9.3 |
| Application platform: data plane | 6.3 | 6.3 | 0.0 | 0.3 |
| Gateway (kgateway) | 54.9 | 55.2 | 5.5 | 7.0 |
| Identity (ThunderID) | 11.9 | 12.0 | 0.3 | 0.9 |
| Observability | 273.1 | 303.0 | 17.1 | 24.2 |
| Node overhead outside pods (kubelet, container runtime, system) | 770.6 | 809.6 | 79.6 | 188.6 |
| **Node total** | 2073.8 | 2129.3 | 278.0 | 396.5 |

Container images on disk: 3209 MiB. (Node filesystem used: 83210 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 565.4 | 77.4 |
| openchoreo-observability-plane/openobserve-0 | Observability | 123.8 | 6.1 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 99.7 | 43.5 |
| openchoreo-observability-plane/openchoreo-observability-prometheus-84746b7844-tbsch | Observability | 83.3 | 5.7 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 67.3 | 21.2 |
| kgateway-system/kgateway-7b8984bc55-259cf | Gateway (kgateway) | 36.3 | 1.9 |
| cert-manager/cert-manager-bc44b5799-qs45p | Certificates (cert-manager) | 25.8 | 1.1 |
| cert-manager/cert-manager-cainjector-784846b495-vfczc | Certificates (cert-manager) | 24.2 | 1.1 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-7kvp7 | Application platform: control plane | 23.5 | 6.8 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 23.0 | 7.6 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| openchoreo-observability-plane/data-openobserve-0 | Observability | 6.6 | 6.5 | 5120 | -0.0 MiB in 10m17s | -4.9 MiB |
| openchoreo-observability-plane/observer-alerts-data | Observability | 0.1 | 0.1 | 128 | +0.0 MiB in 10m17s | 0.0 MiB |
| openchoreo-observability-plane/openchoreo-observability-prometheus | Observability | 0.6 | 1.0 | 3072 | +0.4 MiB in 10m17s | 53.6 MiB |
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 10m17s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 1.2 | 1.3 | 1024 | +0.1 MiB in 10m17s | 16.4 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 10m17s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 10m17s | 0.0 MiB |

serendipd with the package idle: 30.5 MiB RSS, 0.87 mcores mean (without the package, same run: 29.2 MiB, 0.00 mcores). Resync on the fingerprint path: **+0.87 mcores**.

### Under load (180 samples over 31 min)

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 781.0 | 818.8 | 147.2 | 194.1 |
| Secrets (OpenBao) | 18.3 | 18.6 | 9.6 | 13.6 |
| Certificates (cert-manager) | 62.3 | 62.5 | 2.4 | 3.7 |
| Application platform: control plane | 58.2 | 58.4 | 7.3 | 9.7 |
| Application platform: data plane | 6.3 | 6.3 | 0.0 | 0.2 |
| Gateway (kgateway) | 55.9 | 56.2 | 5.2 | 6.8 |
| Identity (ThunderID) | 12.0 | 12.1 | 0.3 | 1.0 |
| Observability | 403.6 | 577.4 | 58.6 | 130.3 |
| Other: serendip-obs-load | 3.6 | 3.6 | 1.2 | 34.7 |
| Node overhead outside pods (kubelet, container runtime, system) | 824.7 | 942.7 | 93.4 | 422.5 |
| **Node total** | 2226.0 | 2413.0 | 325.3 | 628.8 |

Container images on disk: 3231 MiB. (Node filesystem used: 83278 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 552.5 | 73.2 |
| openchoreo-observability-plane/openobserve-0 | Observability | 246.5 | 38.4 |
| openchoreo-observability-plane/openchoreo-observability-prometheus-84746b7844-tbsch | Observability | 83.6 | 6.5 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 72.4 | 41.1 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 67.3 | 19.5 |
| kgateway-system/kgateway-7b8984bc55-259cf | Gateway (kgateway) | 37.2 | 1.8 |
| cert-manager/cert-manager-bc44b5799-qs45p | Certificates (cert-manager) | 25.7 | 1.0 |
| cert-manager/cert-manager-cainjector-784846b495-vfczc | Certificates (cert-manager) | 24.2 | 1.0 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 24.0 | 7.6 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-7kvp7 | Application platform: control plane | 23.7 | 6.4 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| openchoreo-observability-plane/data-openobserve-0 | Observability | 6.7 | 20.8 | 5120 | +14.1 MiB in 31m3s | 654.2 MiB |
| openchoreo-observability-plane/observer-alerts-data | Observability | 0.1 | 0.1 | 128 | +0.0 MiB in 31m3s | 0.0 MiB |
| openchoreo-observability-plane/openchoreo-observability-prometheus | Observability | 1.0 | 2.5 | 3072 | +1.6 MiB in 31m3s | 72.5 MiB |
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 31m3s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 1.3 | 1.7 | 1024 | +0.3 MiB in 31m3s | 16.1 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 31m3s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 31m3s | 0.0 MiB |

serendipd: 29.6 MiB RSS, 0.93 mcores mean.

### Disk phase, still under load (121 samples over 121 min)

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 774.1 | 799.7 | 146.2 | 191.4 |
| Secrets (OpenBao) | 18.5 | 18.8 | 9.2 | 12.7 |
| Certificates (cert-manager) | 62.4 | 62.7 | 2.4 | 3.5 |
| Application platform: control plane | 58.8 | 59.2 | 7.3 | 8.7 |
| Application platform: data plane | 6.3 | 6.4 | 0.0 | 0.2 |
| Gateway (kgateway) | 56.7 | 57.0 | 5.0 | 6.2 |
| Identity (ThunderID) | 12.1 | 12.2 | 0.3 | 1.2 |
| Observability | 428.8 | 572.6 | 60.0 | 180.1 |
| Other: serendip-obs-load | 3.6 | 3.6 | 1.0 | 1.1 |
| Node overhead outside pods (kubelet, container runtime, system) | 829.7 | 970.6 | 85.7 | 146.3 |
| **Node total** | 2251.0 | 2432.5 | 317.1 | 411.3 |

Container images on disk: 3231 MiB. (Node filesystem used: 83392 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 548.7 | 72.6 |
| openchoreo-observability-plane/openobserve-0 | Observability | 263.6 | 39.8 |
| openchoreo-observability-plane/openchoreo-observability-prometheus-84746b7844-tbsch | Observability | 90.1 | 6.7 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 67.5 | 40.3 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 67.2 | 19.5 |
| kgateway-system/kgateway-7b8984bc55-259cf | Gateway (kgateway) | 38.0 | 1.7 |
| cert-manager/cert-manager-bc44b5799-qs45p | Certificates (cert-manager) | 25.7 | 1.0 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 24.7 | 7.6 |
| cert-manager/cert-manager-cainjector-784846b495-vfczc | Certificates (cert-manager) | 24.2 | 1.0 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-7kvp7 | Application platform: control plane | 24.0 | 6.3 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| openchoreo-observability-plane/data-openobserve-0 | Observability | 20.9 | 57.0 | 5120 | +36.2 MiB in 2h0m50s | 430.8 MiB |
| openchoreo-observability-plane/observer-alerts-data | Observability | 0.1 | 0.1 | 128 | +0.0 MiB in 2h0m50s | 0.0 MiB |
| openchoreo-observability-plane/openchoreo-observability-prometheus | Observability | 2.5 | 8.0 | 3072 | +5.5 MiB in 2h0m50s | 65.1 MiB |
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 2h0m50s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 1.7 | 1.7 | 1024 | -0.0 MiB in 2h0m50s | -0.5 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 2h0m50s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 2h0m50s | 0.0 MiB |

serendipd: 29.5 MiB RSS, 0.81 mcores mean.

The load driver's queries:

```
metrics: 290 queries, 0 not 2xx, mean 0.012 s, max 0.094 s
logs: 868 queries, 0 not 2xx, mean 0.470 s, max 1.501 s
```

### Stated cost (Requirements)

The node's use beyond the platform phase above (1466 MiB, 258 mcores mean), never below the package's own pods.

| | Idle memory | Idle CPU | Peak memory | Peak CPU |
|---|---|---|---|---|
| **Stated (node)** | 608 MiB | 21 mcores | 967 MiB | 372 mcores |
| Package's own pods | 274 MiB | 18 mcores | 578 MiB | 181 mcores |

Disk: the package's 3 volumes grew 41.6 MiB over 121 min of the disk phase, **496 MiB a day** extrapolated, of 8320 MiB capacity.
