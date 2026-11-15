# B: under load, disk growth

**SIMULATION — does not satisfy the reference-hardware gate.** kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t) (cpus 2.0, cpuset 0-3). Not simulated: 16-32 GB RAM (only measured, not limited); SATA/NVMe disk speed.

- Started 2026-11-12T15:51:37Z, 13 samples every 600s
- Node bench-t470-control-plane (4 CPU, 8133872Ki memory), v1.34.0

### Cluster

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 1067.4 | 1120.0 | 155.4 | 161.0 |
| Secrets (OpenBao) | 19.1 | 19.2 | 8.8 | 12.1 |
| Certificates (cert-manager) | 78.0 | 78.4 | 3.6 | 4.4 |
| Application platform: control plane | 62.6 | 63.2 | 7.5 | 9.2 |
| Application platform: data plane | 7.8 | 8.3 | 0.3 | 1.9 |
| Gateway (kgateway) | 57.0 | 57.4 | 5.5 | 6.8 |
| Identity (ThunderID) | 11.8 | 11.9 | 0.2 | 0.6 |
| Workloads | 13.7 | 13.7 | 0.0 | 0.0 |
| Observability | 677.1 | 723.0 | 66.5 | 89.5 |
| Other: serendip-obs-load | 3.6 | 3.6 | 1.0 | 1.1 |
| Node overhead outside pods (kubelet, container runtime, system) | 1040.8 | 1101.6 | 108.3 | 182.8 |
| **Node total** | 3038.9 | 3133.7 | 357.1 | 416.8 |

Container images on disk: 3387 MiB. (Node filesystem used: 80902 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 824.1 | 75.5 |
| openchoreo-observability-plane/openobserve-0 | Observability | 432.3 | 38.2 |
| openchoreo-observability-plane/prometheus-openchoreo-observability-0 | Observability | 151.2 | 11.3 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 78.3 | 43.8 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 73.2 | 20.7 |
| cert-manager/cert-manager-cainjector-784846b495-bsfbd | Certificates (cert-manager) | 39.1 | 1.7 |
| kgateway-system/kgateway-7b8984bc55-9vxwg | Gateway (kgateway) | 37.2 | 1.9 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-8mmnz | Application platform: control plane | 28.0 | 5.7 |
| cert-manager/cert-manager-bc44b5799-cd9nt | Certificates (cert-manager) | 26.4 | 1.5 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 25.8 | 8.1 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| openchoreo-observability-plane/data-openobserve-0 | Observability | 71.1 | 104.0 | 5120 | +32.9 MiB in 2h0m5s | 394.5 MiB |
| openchoreo-observability-plane/observer-alerts-data | Observability | 0.1 | 0.1 | 128 | +0.0 MiB in 2h0m5s | 0.0 MiB |
| openchoreo-observability-plane/prometheus-openchoreo-observability-db-prometheus-openchoreo-observability-0 | Observability | 6.3 | 38.7 | 3072 | +32.4 MiB in 2h0m5s | 389.0 MiB |
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 2h0m5s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 0.9 | 0.9 | 1024 | +0.0 MiB in 2h0m5s | 0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 2h0m5s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 2h0m5s | 0.0 MiB |
