# C: under load (5 × 10 lines/s, a search every 10 s, metrics every 30 s)

**SIMULATION — does not satisfy the reference-hardware gate.** kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t) (cpus 2.0, cpuset 0-3). Not simulated: 16-32 GB RAM (only measured, not limited); SATA/NVMe disk speed.

- Started 2026-11-13T18:30:53Z, 30 samples every 20s
- Node bench-t470-control-plane (4 CPU, 8133872Ki memory), v1.34.0

### Cluster

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 998.5 | 1030.2 | 149.0 | 206.1 |
| Secrets (OpenBao) | 21.8 | 22.1 | 8.5 | 10.6 |
| Certificates (cert-manager) | 70.6 | 71.0 | 2.3 | 3.4 |
| Application platform: control plane | 68.0 | 68.6 | 6.5 | 10.1 |
| Application platform: data plane | 8.3 | 8.8 | 0.4 | 1.6 |
| Gateway (kgateway) | 62.0 | 62.1 | 5.2 | 7.0 |
| Identity (ThunderID) | 13.0 | 13.3 | 0.3 | 1.5 |
| Workloads | 13.7 | 13.7 | 0.0 | 0.0 |
| Observability | 1924.8 | 2039.2 | 366.2 | 483.5 |
| Other: serendip-obs-load | 4.2 | 4.3 | 1.0 | 1.4 |
| Node overhead outside pods (kubelet, container runtime, system) | 1614.3 | 1705.1 | 124.1 | 1112.3 |
| **Node total** | 4799.2 | 4934.8 | 663.5 | 1755.2 |

Container images on disk: 7654 MiB. (Node filesystem used: 85647 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| signoz/chi-signoz-clickhouse-cluster-0-0-0 | Observability | 898.0 | 294.5 |
| signoz/signoz-zookeeper-0 | Observability | 760.7 | 7.2 |
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 733.0 | 76.1 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 85.1 | 40.1 |
| signoz/signoz-otel-collector-5fc589ffcf-876bd | Observability | 82.4 | 23.5 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 81.1 | 19.7 |
| signoz/k8s-infra-otel-agent-99gp7 | Observability | 61.6 | 34.9 |
| signoz/signoz-0 | Observability | 50.2 | 1.0 |
| kgateway-system/kgateway-7b8984bc55-9vxwg | Gateway (kgateway) | 40.3 | 1.8 |
| signoz/k8s-infra-otel-deployment-7d94574947-rs8db | Observability | 39.6 | 1.2 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 9m55s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 0.9 | 0.9 | 1024 | +0.0 MiB in 9m55s | 0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 9m55s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 9m55s | 0.0 MiB |
| signoz/data-signoz-zookeeper-0 | Observability | 0.8 | 0.8 | 8192 | +0.0 MiB in 9m55s | 0.0 MiB |
| signoz/data-volumeclaim-template-chi-signoz-clickhouse-cluster-0-0-0 | Observability | 307.8 | 606.5 | 5120 | +298.8 MiB in 9m55s | 43397.2 MiB |
| signoz/signoz-db-signoz-0 | Observability | 4.6 | 4.6 | 1024 | +0.0 MiB in 9m55s | 0.0 MiB |
