# B: installed, idle

**SIMULATION — does not satisfy the reference-hardware gate.** kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t) (cpus 2.0, cpuset 0-3). Not simulated: 16-32 GB RAM (only measured, not limited); SATA/NVMe disk speed.

- Started 2026-11-12T15:29:38Z, 30 samples every 20s
- Node bench-t470-control-plane (4 CPU, 8133872Ki memory), v1.34.0

### Cluster

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 1092.9 | 1185.4 | 161.0 | 210.5 |
| Secrets (OpenBao) | 19.0 | 19.2 | 8.1 | 9.8 |
| Certificates (cert-manager) | 77.4 | 77.5 | 2.7 | 5.2 |
| Application platform: control plane | 62.4 | 62.8 | 7.1 | 11.2 |
| Application platform: data plane | 7.8 | 7.9 | 0.3 | 2.4 |
| Gateway (kgateway) | 56.7 | 57.4 | 5.6 | 6.9 |
| Identity (ThunderID) | 11.7 | 11.7 | 0.3 | 0.6 |
| Workloads | 13.7 | 13.7 | 0.0 | 0.0 |
| Observability | 480.8 | 493.6 | 24.7 | 32.8 |
| Node overhead outside pods (kubelet, container runtime, system) | 938.5 | 961.0 | 93.6 | 189.1 |
| **Node total** | 2760.9 | 2842.8 | 303.4 | 394.2 |

Container images on disk: 3365 MiB. (Node filesystem used: 80644 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 835.3 | 79.3 |
| openchoreo-observability-plane/openobserve-0 | Observability | 293.0 | 6.5 |
| openchoreo-observability-plane/prometheus-openchoreo-observability-0 | Observability | 104.9 | 9.9 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 95.8 | 45.7 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 72.8 | 21.3 |
| cert-manager/cert-manager-cainjector-784846b495-bsfbd | Certificates (cert-manager) | 38.8 | 1.2 |
| kgateway-system/kgateway-7b8984bc55-9vxwg | Gateway (kgateway) | 37.0 | 1.9 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-8mmnz | Application platform: control plane | 27.9 | 5.8 |
| cert-manager/cert-manager-bc44b5799-cd9nt | Certificates (cert-manager) | 26.2 | 1.0 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 24.3 | 8.2 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| openchoreo-observability-plane/data-openobserve-0 | Observability | 63.4 | 63.8 | 5120 | +0.4 MiB in 9m46s | 55.9 MiB |
| openchoreo-observability-plane/observer-alerts-data | Observability | 0.1 | 0.1 | 128 | +0.0 MiB in 9m46s | 0.0 MiB |
| openchoreo-observability-plane/prometheus-openchoreo-observability-db-prometheus-openchoreo-observability-0 | Observability | 1.2 | 3.2 | 3072 | +2.1 MiB in 9m46s | 302.3 MiB |
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 9m46s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 0.9 | 0.9 | 1024 | +0.0 MiB in 9m46s | 0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 9m46s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 9m46s | 0.0 MiB |
