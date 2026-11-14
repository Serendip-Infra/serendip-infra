# B-plain: plane and plain Prometheus, idle (no logs module)

**SIMULATION — does not satisfy the reference-hardware gate.** kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t) (cpus 2.0, cpuset 0-3). Not simulated: 16-32 GB RAM (only measured, not limited); SATA/NVMe disk speed.

- Started 2026-11-14T00:12:17Z, 15 samples every 20s
- Node bench-t470-control-plane (4 CPU, 8133872Ki memory), v1.34.0

### Cluster

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 998.7 | 1057.0 | 145.1 | 159.1 |
| Secrets (OpenBao) | 21.8 | 21.8 | 9.8 | 11.6 |
| Certificates (cert-manager) | 77.7 | 77.8 | 2.5 | 3.0 |
| Application platform: control plane | 74.5 | 74.7 | 6.6 | 9.3 |
| Application platform: data plane | 9.5 | 10.0 | 0.3 | 2.1 |
| Gateway (kgateway) | 61.9 | 62.0 | 5.1 | 6.0 |
| Identity (ThunderID) | 14.2 | 14.2 | 0.3 | 1.2 |
| Workloads | 13.7 | 13.7 | 0.0 | 0.0 |
| Observability | 215.8 | 220.9 | 7.0 | 9.2 |
| Node overhead outside pods (kubelet, container runtime, system) | 1651.1 | 1662.0 | 69.5 | 94.5 |
| **Node total** | 3138.9 | 3205.6 | 246.2 | 277.4 |

Container images on disk: 7652 MiB. (Node filesystem used: 85097 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 730.6 | 71.0 |
| openchoreo-observability-plane/openchoreo-observability-prometheus-84746b7844-td8d4 | Observability | 106.7 | 5.2 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 83.9 | 19.8 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 82.6 | 41.0 |
| kgateway-system/kgateway-7b8984bc55-9vxwg | Gateway (kgateway) | 40.3 | 1.7 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-8mmnz | Application platform: control plane | 35.3 | 5.3 |
| openchoreo-observability-plane/observer-75b8dbf7cf-pjt2j | Observability | 34.8 | 0.3 |
| cert-manager/cert-manager-cainjector-784846b495-bsfbd | Certificates (cert-manager) | 32.2 | 0.9 |
| cert-manager/cert-manager-bc44b5799-cd9nt | Certificates (cert-manager) | 30.3 | 1.1 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 28.7 | 7.8 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| openchoreo-observability-plane/observer-alerts-data | Observability | 0.1 | 0.1 | 128 | +0.0 MiB in 4m45s | 0.0 MiB |
| openchoreo-observability-plane/openchoreo-observability-prometheus | Observability | 0.7 | 0.9 | 3072 | +0.2 MiB in 4m45s | 60.4 MiB |
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 0.9 | 0.9 | 1024 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 4m45s | 0.0 MiB |
