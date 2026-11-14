# A: under load, disk growth

**SIMULATION — does not satisfy the reference-hardware gate.** kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t) (cpus 2.0, cpuset 0-3). Not simulated: 16-32 GB RAM (only measured, not limited); SATA/NVMe disk speed.

- Started 2026-11-12T22:55:37Z, 7 samples every 600s
- Node bench-t470-control-plane (4 CPU, 8133872Ki memory), v1.34.0

### Cluster

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 1037.1 | 1106.3 | 168.8 | 178.1 |
| Secrets (OpenBao) | 19.5 | 19.5 | 8.9 | 11.3 |
| Certificates (cert-manager) | 84.9 | 85.6 | 2.3 | 2.7 |
| Application platform: control plane | 66.1 | 66.6 | 6.5 | 7.5 |
| Application platform: data plane | 8.3 | 8.4 | 0.3 | 1.8 |
| Gateway (kgateway) | 61.8 | 61.9 | 5.7 | 6.0 |
| Identity (ThunderID) | 12.2 | 12.2 | 0.3 | 0.5 |
| Workloads | 13.7 | 13.7 | 0.0 | 0.0 |
| Observability | 1429.8 | 1470.2 | 122.5 | 141.6 |
| Other: serendip-obs-load | 3.5 | 3.5 | 1.1 | 1.3 |
| Node overhead outside pods (kubelet, container runtime, system) | 1344.8 | 1366.2 | 113.8 | 148.8 |
| **Node total** | 4081.6 | 4127.0 | 430.3 | 484.0 |

Container images on disk: 5087 MiB. (Node filesystem used: 82856 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| openchoreo-observability-plane/opensearch-master-0 | Observability | 1054.6 | 91.3 |
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 765.9 | 81.1 |
| openchoreo-observability-plane/prometheus-openchoreo-observability-0 | Observability | 213.0 | 12.3 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 85.3 | 23.3 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 84.2 | 47.8 |
| openchoreo-observability-plane/kube-state-metrics-7d75d8ff7c-wkp5t | Observability | 52.6 | 0.5 |
| cert-manager/cert-manager-cainjector-784846b495-bsfbd | Certificates (cert-manager) | 43.2 | 1.0 |
| kgateway-system/kgateway-7b8984bc55-9vxwg | Gateway (kgateway) | 41.6 | 1.8 |
| openchoreo-observability-plane/prometheus-operator-698cd6c5b-jbqdz | Observability | 38.6 | 3.7 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 30.8 | 9.6 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| openchoreo-observability-plane/observer-alerts-data | Observability | 0.1 | 0.1 | 128 | +0.0 MiB in 1h0m5s | 0.0 MiB |
| openchoreo-observability-plane/opensearch-master-opensearch-master-0 | Observability | 65.4 | 340.8 | 5120 | +275.4 MiB in 1h0m5s | 6600.6 MiB |
| openchoreo-observability-plane/prometheus-openchoreo-observability-db-prometheus-openchoreo-observability-0 | Observability | 6.3 | 21.1 | 3072 | +14.8 MiB in 1h0m5s | 354.0 MiB |
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 1h0m5s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 0.9 | 0.9 | 1024 | +0.0 MiB in 1h0m5s | 0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 1h0m5s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 1h0m5s | 0.0 MiB |
