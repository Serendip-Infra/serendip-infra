> **SIMULATION — does not satisfy the reference-hardware gate.**

# edge-1 footprint — 2026-11-14 11:54 +0530

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
| Kubernetes system | 787.7 | 792.2 | 150.0 | 177.0 |
| Secrets (OpenBao) | 18.2 | 18.3 | 7.3 | 8.7 |
| Certificates (cert-manager) | 64.8 | 73.0 | 2.6 | 3.3 |
| Application platform: control plane | 54.6 | 54.9 | 5.3 | 6.8 |
| Application platform: data plane | 6.4 | 6.4 | 0.0 | 0.2 |
| Gateway (kgateway) | 53.2 | 53.5 | 5.5 | 6.2 |
| Identity (ThunderID) | 12.1 | 12.1 | 0.3 | 0.5 |
| Node overhead outside pods (kubelet, container runtime, system) | 584.8 | 591.1 | 56.5 | 106.9 |
| **Node total** | 1581.9 | 1588.2 | 227.5 | 302.0 |

Container images on disk: 2204 MiB. (Node filesystem used: 82196 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 540.6 | 76.8 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 96.4 | 41.3 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 66.5 | 19.0 |
| kgateway-system/kgateway-7b8984bc55-wx8bb | Gateway (kgateway) | 35.3 | 2.0 |
| cert-manager/cert-manager-bc44b5799-98zfv | Certificates (cert-manager) | 26.8 | 0.9 |
| cert-manager/cert-manager-cainjector-784846b495-c5jp5 | Certificates (cert-manager) | 25.1 | 1.2 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 22.7 | 7.8 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-gbs59 | Application platform: control plane | 21.8 | 4.6 |
| serendip-system/openbao-0 | Secrets (OpenBao) | 18.2 | 7.3 |
| kgateway-system/serendip-external-657d66d6c7-xwnps | Gateway (kgateway) | 17.9 | 3.4 |

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
| RSS (MiB) | 29.5 | 29.7 |
| CPU (millicores) | 0.4 | 4.5 |

### Constitution rule 4 (control plane < 256 MiB RSS, < 5% CPU at idle)

- RSS 29.5 MiB: **PASS**
- CPU 0.02% of 2.0 cores: **PASS**
- covers serendipd only: serendip-agent is not a long-running daemon yet and is not measured

### With 3 light Components deployed

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 808.8 | 811.3 | 160.2 | 207.2 |
| Secrets (OpenBao) | 18.4 | 18.4 | 6.7 | 9.0 |
| Certificates (cert-manager) | 63.0 | 63.1 | 2.9 | 4.4 |
| Application platform: control plane | 65.2 | 66.2 | 6.5 | 8.8 |
| Application platform: data plane | 7.3 | 7.4 | 0.1 | 0.3 |
| Gateway (kgateway) | 54.0 | 54.0 | 5.8 | 7.4 |
| Identity (ThunderID) | 12.0 | 12.0 | 0.3 | 0.7 |
| Workloads | 13.6 | 13.6 | 0.0 | 0.0 |
| Node overhead outside pods (kubelet, container runtime, system) | 633.0 | 634.7 | 72.7 | 188.5 |
| **Node total** | 1675.3 | 1679.6 | 255.3 | 363.0 |

Container images on disk: 2254 MiB. (Node filesystem used: 82248 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 559.2 | 84.2 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 99.6 | 42.9 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 64.6 | 19.2 |
| kgateway-system/kgateway-7b8984bc55-wx8bb | Gateway (kgateway) | 35.8 | 2.0 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-gbs59 | Application platform: control plane | 30.9 | 5.5 |
| cert-manager/cert-manager-bc44b5799-98zfv | Certificates (cert-manager) | 26.2 | 1.1 |
| cert-manager/cert-manager-cainjector-784846b495-c5jp5 | Certificates (cert-manager) | 24.6 | 1.2 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 23.2 | 8.0 |
| serendip-system/openbao-0 | Secrets (OpenBao) | 18.4 | 6.7 |
| kgateway-system/serendip-external-657d66d6c7-xwnps | Gateway (kgateway) | 18.2 | 3.8 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 1m58s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 1.2 | 1.2 | 1024 | +0.0 MiB in 1m58s | 0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 1m58s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 1m58s | 0.0 MiB |

### serendipd with 3 web-console tabs polling

159 requests (1.30/s), 0 errors.

| | No tabs | 3 tabs |
|---|---|---|
| RSS mean (MiB) | 31.4 | 31.4 |
| CPU mean (millicores) | 0.8 | 2.6 |

- CPU 0.13% of 2.0 cores (budget < 5%, no request errors): **PASS**
- RSS 31.4 MiB (budget < 256 MiB): **PASS**


### 16 GB headroom

Budget = 16 GB − 2 GB host OS = 14336 MiB. Projected = platform 1611 MiB + 10 × 8.7 MiB per Component = 1698 MiB (12% of budget).

**Verdict: fits 16 GB comfortably.**

### Timings

- Cold start (fresh cluster, includes image pulls): 316s
- Warm restart (everything installed): 92s
- Component create → Serving: 18s, 34s, 34s (includes the image pull; status is re-checked about 2, 6, 14, 22 and 30s after create)
