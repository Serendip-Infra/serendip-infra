# A: under load (5 × 10 lines/s, a search every 10 s, metrics every 30 s)

**SIMULATION — does not satisfy the reference-hardware gate.** kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t) (cpus 2.0, cpuset 0-3). Not simulated: 16-32 GB RAM (only measured, not limited); SATA/NVMe disk speed.

- Started 2026-11-12T22:45:43Z, 30 samples every 20s
- Node bench-t470-control-plane (4 CPU, 8133872Ki memory), v1.34.0

### Cluster

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 1026.1 | 1106.4 | 184.2 | 298.1 |
| Secrets (OpenBao) | 19.5 | 19.8 | 9.3 | 13.5 |
| Certificates (cert-manager) | 84.3 | 84.3 | 2.7 | 3.6 |
| Application platform: control plane | 65.5 | 65.7 | 7.3 | 10.5 |
| Application platform: data plane | 8.4 | 8.8 | 0.2 | 1.6 |
| Gateway (kgateway) | 61.6 | 61.6 | 6.0 | 7.8 |
| Identity (ThunderID) | 12.2 | 12.2 | 0.2 | 0.6 |
| Workloads | 13.7 | 13.7 | 0.0 | 0.0 |
| Observability | 1366.2 | 1384.2 | 154.5 | 224.3 |
| Other: serendip-obs-load | 3.5 | 3.5 | 1.1 | 1.3 |
| Node overhead outside pods (kubelet, container runtime, system) | 1361.6 | 1373.4 | 108.1 | 176.4 |
| **Node total** | 4022.3 | 4119.3 | 473.7 | 554.3 |

Container images on disk: 5086 MiB. (Node filesystem used: 82473 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| openchoreo-observability-plane/opensearch-master-0 | Observability | 1005.3 | 122.9 |
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 737.7 | 95.0 |
| openchoreo-observability-plane/prometheus-openchoreo-observability-0 | Observability | 197.7 | 12.8 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 101.3 | 48.4 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 86.0 | 24.2 |
| openchoreo-observability-plane/kube-state-metrics-7d75d8ff7c-wkp5t | Observability | 54.9 | 0.5 |
| cert-manager/cert-manager-cainjector-784846b495-bsfbd | Certificates (cert-manager) | 43.0 | 1.1 |
| kgateway-system/kgateway-7b8984bc55-9vxwg | Gateway (kgateway) | 41.4 | 2.0 |
| openchoreo-observability-plane/prometheus-operator-698cd6c5b-jbqdz | Observability | 38.1 | 3.9 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 30.9 | 9.3 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| openchoreo-observability-plane/observer-alerts-data | Observability | 0.1 | 0.1 | 128 | +0.0 MiB in 9m48s | 0.0 MiB |
| openchoreo-observability-plane/opensearch-master-opensearch-master-0 | Observability | 20.1 | 65.5 | 5120 | +45.4 MiB in 9m48s | 6661.1 MiB |
| openchoreo-observability-plane/prometheus-openchoreo-observability-db-prometheus-openchoreo-observability-0 | Observability | 3.8 | 6.3 | 3072 | +2.5 MiB in 9m48s | 372.9 MiB |
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 9m48s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 0.9 | 0.9 | 1024 | +0.0 MiB in 9m48s | 0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 9m48s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 9m48s | 0.0 MiB |
