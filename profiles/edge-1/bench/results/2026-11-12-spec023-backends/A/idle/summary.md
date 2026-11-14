# A: installed, idle

**SIMULATION — does not satisfy the reference-hardware gate.** kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t) (cpus 2.0, cpuset 0-3). Not simulated: 16-32 GB RAM (only measured, not limited); SATA/NVMe disk speed.

- Started 2026-11-12T22:33:41Z, 30 samples every 20s
- Node bench-t470-control-plane (4 CPU, 8133872Ki memory), v1.34.0

### Cluster

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 1051.1 | 1107.9 | 181.1 | 242.5 |
| Secrets (OpenBao) | 19.5 | 19.5 | 8.6 | 11.0 |
| Certificates (cert-manager) | 84.3 | 85.2 | 2.8 | 3.8 |
| Application platform: control plane | 65.6 | 65.8 | 7.4 | 10.7 |
| Application platform: data plane | 8.3 | 8.5 | 0.4 | 2.8 |
| Gateway (kgateway) | 61.5 | 69.6 | 6.1 | 9.3 |
| Identity (ThunderID) | 12.2 | 12.2 | 0.2 | 0.5 |
| Workloads | 13.7 | 13.7 | 0.0 | 0.0 |
| Observability | 1320.2 | 1341.3 | 53.0 | 128.1 |
| Node overhead outside pods (kubelet, container runtime, system) | 1336.3 | 1346.2 | 106.2 | 240.5 |
| **Node total** | 3972.7 | 4019.3 | 365.9 | 501.7 |

Container images on disk: 5086 MiB. (Node filesystem used: 82406 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| openchoreo-observability-plane/opensearch-master-0 | Observability | 983.9 | 32.3 |
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 760.0 | 91.4 |
| openchoreo-observability-plane/prometheus-openchoreo-observability-0 | Observability | 178.9 | 11.3 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 103.2 | 48.8 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 87.2 | 24.5 |
| openchoreo-observability-plane/kube-state-metrics-7d75d8ff7c-wkp5t | Observability | 55.8 | 0.5 |
| cert-manager/cert-manager-cainjector-784846b495-bsfbd | Certificates (cert-manager) | 43.1 | 1.2 |
| kgateway-system/kgateway-7b8984bc55-9vxwg | Gateway (kgateway) | 41.4 | 2.0 |
| openchoreo-observability-plane/prometheus-operator-698cd6c5b-jbqdz | Observability | 38.4 | 4.0 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 30.9 | 9.4 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| openchoreo-observability-plane/observer-alerts-data | Observability | 0.1 | 0.1 | 128 | +0.0 MiB in 9m48s | 0.0 MiB |
| openchoreo-observability-plane/opensearch-master-opensearch-master-0 | Observability | 6.8 | 9.3 | 5120 | +2.6 MiB in 9m48s | 377.1 MiB |
| openchoreo-observability-plane/prometheus-openchoreo-observability-db-prometheus-openchoreo-observability-0 | Observability | 1.1 | 3.2 | 3072 | +2.1 MiB in 9m48s | 308.8 MiB |
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 9m48s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 0.8 | 0.9 | 1024 | +0.0 MiB in 9m48s | 4.6 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 9m48s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 9m48s | 0.0 MiB |
