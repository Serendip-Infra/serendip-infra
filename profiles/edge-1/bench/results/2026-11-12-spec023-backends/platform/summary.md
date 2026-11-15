> **SIMULATION — does not satisfy the reference-hardware gate.**

# edge-1 footprint — 2026-11-12 19:53 +0530

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
| Kubernetes system | 760.5 | 793.9 | 162.3 | 193.0 |
| Secrets (OpenBao) | 18.5 | 18.7 | 8.1 | 9.9 |
| Certificates (cert-manager) | 64.0 | 65.3 | 2.6 | 4.1 |
| Application platform: control plane | 56.1 | 58.4 | 5.9 | 6.6 |
| Application platform: data plane | 6.7 | 6.9 | 0.0 | 0.2 |
| Gateway (kgateway) | 53.0 | 55.3 | 5.7 | 6.8 |
| Identity (ThunderID) | 12.1 | 12.2 | 0.3 | 0.6 |
| Node overhead outside pods (kubelet, container runtime, system) | 485.6 | 487.8 | 59.5 | 95.8 |
| **Node total** | 1456.5 | 1489.3 | 244.4 | 308.4 |

Container images on disk: 2204 MiB. (Node filesystem used: 79395 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 521.3 | 83.0 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 92.2 | 44.0 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 64.9 | 20.9 |
| kgateway-system/kgateway-7b8984bc55-9vxwg | Gateway (kgateway) | 34.9 | 1.9 |
| cert-manager/cert-manager-bc44b5799-cd9nt | Certificates (cert-manager) | 26.6 | 1.1 |
| cert-manager/cert-manager-cainjector-784846b495-bsfbd | Certificates (cert-manager) | 24.6 | 1.0 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-8mmnz | Application platform: control plane | 23.2 | 5.1 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 21.1 | 8.0 |
| serendip-system/openbao-0 | Secrets (OpenBao) | 18.5 | 8.1 |
| kgateway-system/serendip-external-657d66d6c7-b9hlr | Gateway (kgateway) | 18.2 | 3.8 |

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
| RSS (MiB) | 29.6 | 29.9 |
| CPU (millicores) | 0.0 | 0.0 |

### Constitution rule 4 (control plane < 256 MiB RSS, < 5% CPU at idle)

- RSS 29.6 MiB: **PASS**
- CPU 0.00% of 2.0 cores: **PASS**
- covers serendipd only: serendip-agent is not a long-running daemon yet and is not measured

### With 3 light Components deployed

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 802.6 | 802.9 | 167.3 | 190.4 |
| Secrets (OpenBao) | 18.6 | 18.6 | 8.2 | 11.4 |
| Certificates (cert-manager) | 62.9 | 62.9 | 2.5 | 3.2 |
| Application platform: control plane | 66.2 | 66.6 | 7.5 | 11.0 |
| Application platform: data plane | 8.2 | 8.7 | 0.0 | 0.2 |
| Gateway (kgateway) | 54.3 | 54.7 | 5.7 | 6.3 |
| Identity (ThunderID) | 12.0 | 12.1 | 0.3 | 0.8 |
| Workloads | 13.7 | 13.7 | 0.0 | 0.0 |
| Node overhead outside pods (kubelet, container runtime, system) | 516.1 | 517.3 | 72.2 | 121.2 |
| **Node total** | 1554.5 | 1556.8 | 263.7 | 324.0 |

Container images on disk: 2254 MiB. (Node filesystem used: 79448 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 558.6 | 85.4 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 95.0 | 46.1 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 64.7 | 21.2 |
| kgateway-system/kgateway-7b8984bc55-9vxwg | Gateway (kgateway) | 35.9 | 1.9 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-8mmnz | Application platform: control plane | 31.9 | 6.5 |
| cert-manager/cert-manager-bc44b5799-cd9nt | Certificates (cert-manager) | 26.3 | 1.0 |
| cert-manager/cert-manager-cainjector-784846b495-bsfbd | Certificates (cert-manager) | 24.4 | 1.0 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 22.5 | 8.0 |
| serendip-system/openbao-0 | Secrets (OpenBao) | 18.6 | 8.2 |
| kgateway-system/serendip-external-657d66d6c7-b9hlr | Gateway (kgateway) | 18.4 | 3.8 |

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
| RSS mean (MiB) | 31.5 | 31.7 |
| CPU mean (millicores) | 1.6 | 2.2 |

- CPU 0.11% of 2.0 cores (budget < 5%, no request errors): **PASS**
- RSS 31.7 MiB (budget < 256 MiB): **PASS**


### 16 GB headroom

Budget = 16 GB − 2 GB host OS = 14336 MiB. Projected = platform 1486 MiB + 10 × 8.9 MiB per Component = 1575 MiB (11% of budget).

**Verdict: fits 16 GB comfortably.**

### Timings

- Cold start (fresh cluster, includes image pulls): 328s
- Warm restart (everything installed): 31s
- Component create → Serving: 42s, 42s, 42s (includes the image pull; status is re-checked about 2, 6, 14, 22 and 30s after create)
