# B: under load (5 × 10 lines/s, a search every 10 s, metrics every 30 s)

**SIMULATION — does not satisfy the reference-hardware gate.** kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t) (cpus 2.0, cpuset 0-3). Not simulated: 16-32 GB RAM (only measured, not limited); SATA/NVMe disk speed.

- Started 2026-11-12T15:41:44Z, 30 samples every 20s
- Node bench-t470-control-plane (4 CPU, 8133872Ki memory), v1.34.0

### Cluster

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 1086.4 | 1122.8 | 162.2 | 216.1 |
| Secrets (OpenBao) | 19.0 | 19.3 | 7.8 | 9.7 |
| Certificates (cert-manager) | 77.4 | 77.7 | 2.6 | 4.6 |
| Application platform: control plane | 62.3 | 62.5 | 6.4 | 9.2 |
| Application platform: data plane | 7.9 | 8.2 | 0.4 | 2.5 |
| Gateway (kgateway) | 56.6 | 56.6 | 5.5 | 7.0 |
| Identity (ThunderID) | 11.7 | 11.8 | 0.2 | 0.6 |
| Workloads | 13.7 | 13.7 | 0.0 | 0.0 |
| Observability | 617.1 | 693.4 | 64.1 | 90.8 |
| Other: serendip-obs-load | 3.6 | 3.6 | 1.0 | 1.2 |
| Node overhead outside pods (kubelet, container runtime, system) | 1001.1 | 1065.0 | 105.3 | 206.8 |
| **Node total** | 2956.8 | 3042.8 | 355.5 | 444.9 |

Container images on disk: 3387 MiB. (Node filesystem used: 80689 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 827.0 | 81.3 |
| openchoreo-observability-plane/openobserve-0 | Observability | 389.5 | 35.9 |
| openchoreo-observability-plane/prometheus-openchoreo-observability-0 | Observability | 136.4 | 10.7 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 95.9 | 45.2 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 72.8 | 20.8 |
| cert-manager/cert-manager-cainjector-784846b495-bsfbd | Certificates (cert-manager) | 38.8 | 1.1 |
| kgateway-system/kgateway-7b8984bc55-9vxwg | Gateway (kgateway) | 36.9 | 1.9 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-8mmnz | Application platform: control plane | 27.8 | 5.1 |
| cert-manager/cert-manager-bc44b5799-cd9nt | Certificates (cert-manager) | 26.2 | 1.1 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 25.0 | 8.0 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| openchoreo-observability-plane/data-openobserve-0 | Observability | 66.9 | 71.0 | 5120 | +4.1 MiB in 9m48s | 598.6 MiB |
| openchoreo-observability-plane/observer-alerts-data | Observability | 0.1 | 0.1 | 128 | +0.0 MiB in 9m48s | 0.0 MiB |
| openchoreo-observability-plane/prometheus-openchoreo-observability-db-prometheus-openchoreo-observability-0 | Observability | 3.8 | 6.3 | 3072 | +2.5 MiB in 9m48s | 361.3 MiB |
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 9m48s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 0.9 | 0.9 | 1024 | +0.0 MiB in 9m48s | 0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 9m48s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 9m48s | 0.0 MiB |
