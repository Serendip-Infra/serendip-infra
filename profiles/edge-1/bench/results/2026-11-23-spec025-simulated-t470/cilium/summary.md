> **SIMULATION — does not satisfy the reference-hardware gate.**

# edge-1 footprint — 2026-11-23 19:18 +0530

## Machine

- Host: windows/amd64, AMD Ryzen 7 7840HS with Radeon 780M Graphics
- Node: bench-t470-control-plane (Debian GNU/Linux 12 (bookworm), kernel 6.6.87.2-microsoft-standard-WSL2, containerd://2.1.3, kubelet v1.34.0)
- Node capacity reported by Kubernetes: 4 CPU, 8133876Ki memory
- Simulated: 2.0 CPUs of quota on cpuset 0-3. kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t); CNI cilium
- Not simulated: 16-32 GB RAM (only measured, not limited)
- Not simulated: SATA/NVMe disk speed
- Settle 180s, then 12 samples every 10s

### Platform at idle (no workloads)

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 846.5 | 863.1 | 163.6 | 237.3 |
| Secrets (OpenBao) | 18.5 | 18.5 | 8.1 | 9.6 |
| Certificates (cert-manager) | 65.6 | 67.2 | 2.7 | 4.0 |
| Application platform: control plane | 55.3 | 56.6 | 6.0 | 8.0 |
| Application platform: data plane | 6.4 | 6.7 | 0.0 | 0.2 |
| Gateway (kgateway) | 56.6 | 56.7 | 6.4 | 7.8 |
| Identity (ThunderID) | 12.1 | 12.3 | 0.3 | 0.6 |
| Networking (Cilium) | 236.0 | 239.5 | 57.7 | 90.2 |
| Node overhead outside pods (kubelet, container runtime, system) | 684.0 | 691.8 | 82.1 | 151.0 |
| **Node total** | 1981.1 | 1998.2 | 326.9 | 493.1 |

Container images on disk: 3185 MiB. (Node filesystem used: 103213 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 591.9 | 84.7 |
| kube-system/cilium-cjjq2 | Networking (Cilium) | 184.3 | 46.0 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 111.3 | 45.5 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 69.4 | 20.6 |
| kgateway-system/kgateway-7b8984bc55-ftwc2 | Gateway (kgateway) | 38.5 | 1.9 |
| kube-system/cilium-operator-5646c8f9fc-jl7fx | Networking (Cilium) | 38.3 | 7.6 |
| cert-manager/cert-manager-cainjector-784846b495-d7z54 | Certificates (cert-manager) | 27.0 | 1.0 |
| cert-manager/cert-manager-bc44b5799-ss9td | Certificates (cert-manager) | 26.3 | 1.2 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-px7tw | Application platform: control plane | 22.5 | 5.1 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 22.5 | 7.3 |

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
| RSS (MiB) | 82.1 | 82.4 |
| CPU (millicores) | 0.7 | 3.0 |

### Constitution rule 4 (control plane < 256 MiB RSS, < 5% CPU at idle)

- RSS 82.1 MiB: **PASS**
- CPU 0.03% of 2.0 cores: **PASS**
- covers serendipd only: serendip-agent is not a long-running daemon yet and is not measured


### 16 GB headroom

**Verdict: not computed: no light-workload data.**

### Timings

- Cold start (fresh cluster, includes image pulls): 358s
- Warm restart (everything installed): 42s
- Component create → Serving:  (includes the image pull; status is re-checked about 2, 6, 14, 22 and 30s after create)

## Package: openchoreo-observability-plane

Install (create → Ready, with image pulls): 8m22s

### Installed, idle (60 samples)

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 837.9 | 874.6 | 158.8 | 201.6 |
| Secrets (OpenBao) | 18.8 | 19.1 | 7.3 | 9.0 |
| Certificates (cert-manager) | 65.0 | 65.2 | 2.5 | 3.3 |
| Application platform: control plane | 56.2 | 56.3 | 6.1 | 8.5 |
| Application platform: data plane | 6.2 | 6.2 | 0.0 | 0.2 |
| Gateway (kgateway) | 57.4 | 58.6 | 6.0 | 7.4 |
| Identity (ThunderID) | 12.2 | 12.3 | 0.3 | 0.7 |
| Observability | 279.5 | 312.9 | 17.9 | 24.7 |
| Networking (Cilium) | 257.9 | 262.8 | 62.5 | 95.5 |
| Node overhead outside pods (kubelet, container runtime, system) | 970.9 | 996.9 | 85.2 | 181.4 |
| **Node total** | 2562.0 | 2610.7 | 346.6 | 471.3 |

Container images on disk: 4195 MiB. (Node filesystem used: 104242 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 603.6 | 82.3 |
| kube-system/cilium-cjjq2 | Networking (Cilium) | 207.4 | 51.5 |
| openchoreo-observability-plane/openobserve-0 | Observability | 123.8 | 6.6 |
| openchoreo-observability-plane/openchoreo-observability-prometheus-84746b7844-cqds9 | Observability | 89.4 | 5.7 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 82.7 | 44.0 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 72.6 | 19.5 |
| kgateway-system/kgateway-7b8984bc55-ftwc2 | Gateway (kgateway) | 38.5 | 1.9 |
| kube-system/cilium-operator-5646c8f9fc-jl7fx | Networking (Cilium) | 37.1 | 7.1 |
| cert-manager/cert-manager-cainjector-784846b495-d7z54 | Certificates (cert-manager) | 26.8 | 1.0 |
| cert-manager/cert-manager-bc44b5799-ss9td | Certificates (cert-manager) | 25.8 | 1.0 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| openchoreo-observability-plane/data-openobserve-0 | Observability | 7.7 | 7.4 | 5120 | -0.2 MiB in 10m18s | -32.2 MiB |
| openchoreo-observability-plane/observer-alerts-data | Observability | 0.1 | 0.1 | 128 | +0.0 MiB in 10m18s | 0.0 MiB |
| openchoreo-observability-plane/openchoreo-observability-prometheus | Observability | 0.8 | 1.4 | 3072 | +0.6 MiB in 10m18s | 80.3 MiB |
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 10m18s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 1.3 | 1.4 | 1024 | +0.1 MiB in 10m18s | 16.4 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 10m18s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 10m18s | 0.0 MiB |

serendipd with the package idle: 83.4 MiB RSS, 1.30 mcores mean (without the package, same run: 82.1 MiB, 0.68 mcores). Resync on the fingerprint path: **+0.62 mcores**.

### Under load (60 samples over 10 min)

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 840.3 | 858.2 | 163.6 | 201.6 |
| Secrets (OpenBao) | 18.9 | 18.9 | 7.8 | 12.3 |
| Certificates (cert-manager) | 65.3 | 65.4 | 2.7 | 3.7 |
| Application platform: control plane | 56.3 | 56.4 | 6.3 | 7.8 |
| Application platform: data plane | 6.2 | 6.2 | 0.0 | 0.2 |
| Gateway (kgateway) | 57.5 | 57.6 | 6.2 | 9.4 |
| Identity (ThunderID) | 12.3 | 12.3 | 0.3 | 0.8 |
| Observability | 397.0 | 474.2 | 59.7 | 92.2 |
| Networking (Cilium) | 270.3 | 273.1 | 66.1 | 122.3 |
| Other: serendip-obs-load | 3.3 | 3.3 | 1.6 | 30.4 |
| Node overhead outside pods (kubelet, container runtime, system) | 1050.0 | 1154.1 | 108.0 | 282.5 |
| **Node total** | 2777.5 | 2914.2 | 422.2 | 658.6 |

Container images on disk: 4217 MiB. (Node filesystem used: 104288 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 608.7 | 83.4 |
| openchoreo-observability-plane/openobserve-0 | Observability | 227.1 | 38.4 |
| kube-system/cilium-cjjq2 | Networking (Cilium) | 218.5 | 54.7 |
| openchoreo-observability-plane/openchoreo-observability-prometheus-84746b7844-cqds9 | Observability | 95.8 | 6.4 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 78.3 | 46.1 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 72.8 | 20.5 |
| kgateway-system/kgateway-7b8984bc55-ftwc2 | Gateway (kgateway) | 38.6 | 1.9 |
| kube-system/cilium-operator-5646c8f9fc-jl7fx | Networking (Cilium) | 38.5 | 7.3 |
| cert-manager/cert-manager-cainjector-784846b495-d7z54 | Certificates (cert-manager) | 26.8 | 1.1 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 26.3 | 7.6 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| openchoreo-observability-plane/data-openobserve-0 | Observability | 7.7 | 18.4 | 5120 | +10.7 MiB in 10m20s | 1489.3 MiB |
| openchoreo-observability-plane/observer-alerts-data | Observability | 0.1 | 0.1 | 128 | +0.0 MiB in 10m20s | 0.0 MiB |
| openchoreo-observability-plane/openchoreo-observability-prometheus | Observability | 1.4 | 2.2 | 3072 | +0.8 MiB in 10m20s | 113.3 MiB |
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 10m20s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 1.4 | 1.5 | 1024 | +0.1 MiB in 10m20s | 16.3 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 10m20s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 10m20s | 0.0 MiB |

serendipd: 83.3 MiB RSS, 1.17 mcores mean.

### Disk phase, still under load (61 samples over 61 min)

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 884.9 | 920.2 | 174.0 | 233.1 |
| Secrets (OpenBao) | 20.6 | 20.8 | 7.7 | 12.2 |
| Certificates (cert-manager) | 62.7 | 66.3 | 2.6 | 3.9 |
| Application platform: control plane | 67.3 | 67.8 | 7.4 | 12.6 |
| Application platform: data plane | 7.9 | 8.5 | 0.3 | 2.7 |
| Gateway (kgateway) | 61.0 | 61.5 | 6.3 | 8.2 |
| Identity (ThunderID) | 12.3 | 12.4 | 0.3 | 1.3 |
| Workloads | 18.9 | 19.5 | 15.8 | 19.4 |
| Observability | 475.9 | 660.8 | 79.3 | 122.8 |
| Networking (Cilium) | 302.1 | 303.3 | 94.8 | 145.9 |
| Other: serendip-obs-load | 3.4 | 3.4 | 1.0 | 1.2 |
| Tracing (collector) | 49.2 | 49.4 | 3.3 | 5.3 |
| Node overhead outside pods (kubelet, container runtime, system) | 1132.9 | 1279.5 | 103.2 | 210.6 |
| **Node total** | 3099.0 | 3243.9 | 496.0 | 597.8 |

Container images on disk: 4617 MiB. (Node filesystem used: 104858 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 650.4 | 89.3 |
| openchoreo-observability-plane/openobserve-0 | Observability | 289.2 | 55.5 |
| kube-system/cilium-cjjq2 | Networking (Cilium) | 240.4 | 72.6 |
| openchoreo-observability-plane/openchoreo-observability-prometheus-84746b7844-cqds9 | Observability | 109.1 | 8.1 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 75.4 | 49.0 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 73.8 | 21.5 |
| openchoreo-observability-plane/opentelemetry-collector-57986dc9cb-cgjdj | Tracing (collector) | 49.2 | 3.3 |
| kube-system/cilium-operator-5646c8f9fc-jl7fx | Networking (Cilium) | 45.2 | 7.5 |
| kgateway-system/kgateway-7b8984bc55-ftwc2 | Gateway (kgateway) | 41.3 | 2.0 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-px7tw | Application platform: control plane | 32.0 | 6.2 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| openchoreo-observability-plane/data-openobserve-0 | Observability | 43.9 | 90.8 | 5120 | +46.9 MiB in 1h0m37s | 1113.5 MiB |
| openchoreo-observability-plane/observer-alerts-data | Observability | 0.1 | 0.1 | 128 | +0.0 MiB in 1h0m37s | 0.0 MiB |
| openchoreo-observability-plane/openchoreo-observability-prometheus | Observability | 6.0 | 10.7 | 3072 | +4.7 MiB in 1h0m37s | 112.4 MiB |
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 1h0m37s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 1.3 | 1.5 | 1024 | +0.2 MiB in 1h0m37s | 3.6 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 1h0m37s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 1h0m37s | 0.0 MiB |

serendipd: 50.6 MiB RSS, 3.62 mcores mean.

## HTTP traffic and tracing

A caller calling a server every 100 ms (t012-otel:2), under the log load.

| Phase | Node mean / max (MiB) | Node CPU mean / max (mcores) | Networking (Cilium) MiB / mcores | Observability MiB / mcores | Tracing (collector) MiB / mcores | Workloads MiB / mcores | serendipd RSS / CPU |
|---|---|---|---|---|---|---|---|
| HTTP traffic | 2874 / 3057 | 483 / 911 | 301 / 89 | 429 / 73 | – | 16 / 16 | 81.4 MiB / 2.12 mcores |
| HTTP traffic, traced | 3054 / 3240 | 483 / 582 | 302 / 87 | 460 / 77 | 49 / 3 | 18 / 15 | 59.2 MiB / 3.55 mcores |

- Tracing installed in 1m50s; traced − untraced, node: **+179 MiB mean (+183 at max), -0 mcores mean (-329 at max)**
- Observability volumes, untraced: +7.8 MiB over 20m47s, about 543 MiB a day
- Observability volumes, traced: +11.2 MiB over 20m46s, about 780 MiB a day
- The server's Deployment listed 50 traces from the last 5 minutes at the end

The load driver's queries:

```
metrics: 249 queries, 0 not 2xx, mean 0.019 s, max 0.107 s
logs: 747 queries, 0 not 2xx, mean 0.629 s, max 1.878 s
```

### Stated cost (Requirements)

The node's use beyond the platform phase above (1981 MiB, 327 mcores mean), never below the package's own pods.

| | Idle memory | Idle CPU | Peak memory | Peak CPU |
|---|---|---|---|---|
| **Stated (node)** | 581 MiB | 20 mcores | 1263 MiB | 332 mcores |
| Package's own pods | 280 MiB | 18 mcores | 661 MiB | 123 mcores |

Disk: the package's 3 volumes grew 51.6 MiB over 61 min of the disk phase, **1226 MiB a day** extrapolated, of 8320 MiB capacity.
