# C: installed, idle

**SIMULATION — does not satisfy the reference-hardware gate.** kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t) (cpus 2.0, cpuset 0-3). Not simulated: 16-32 GB RAM (only measured, not limited); SATA/NVMe disk speed.

- Started 2026-11-13T18:18:52Z, 30 samples every 20s
- Node bench-t470-control-plane (4 CPU, 8133872Ki memory), v1.34.0

### Cluster

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 1014.9 | 1060.6 | 145.7 | 186.1 |
| Secrets (OpenBao) | 22.2 | 22.2 | 8.5 | 11.3 |
| Certificates (cert-manager) | 71.0 | 71.2 | 2.4 | 3.1 |
| Application platform: control plane | 67.6 | 67.9 | 6.2 | 9.1 |
| Application platform: data plane | 8.2 | 8.7 | 0.3 | 1.9 |
| Gateway (kgateway) | 62.2 | 69.5 | 5.1 | 6.3 |
| Identity (ThunderID) | 12.6 | 12.8 | 0.2 | 0.5 |
| Workloads | 13.7 | 13.7 | 0.0 | 0.0 |
| Observability | 1662.1 | 1704.0 | 209.0 | 264.1 |
| Node overhead outside pods (kubelet, container runtime, system) | 1598.1 | 1654.2 | 76.1 | 118.2 |
| **Node total** | 4532.6 | 4606.0 | 453.4 | 539.6 |

Container images on disk: 7653 MiB. (Node filesystem used: 85279 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| signoz/signoz-zookeeper-0 | Observability | 760.2 | 7.4 |
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 750.3 | 73.8 |
| signoz/chi-signoz-clickhouse-cluster-0-0-0 | Observability | 641.1 | 161.9 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 82.6 | 39.4 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 82.4 | 19.6 |
| signoz/signoz-otel-collector-5fc589ffcf-876bd | Observability | 77.1 | 9.0 |
| signoz/k8s-infra-otel-agent-99gp7 | Observability | 58.9 | 28.2 |
| signoz/signoz-0 | Observability | 53.5 | 1.0 |
| kgateway-system/kgateway-7b8984bc55-9vxwg | Gateway (kgateway) | 40.6 | 1.8 |
| signoz/k8s-infra-otel-deployment-7d94574947-rs8db | Observability | 40.3 | 1.2 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 9m48s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 0.9 | 0.9 | 1024 | +0.0 MiB in 9m48s | 0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 9m48s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 9m48s | 0.0 MiB |
| signoz/data-signoz-zookeeper-0 | Observability | 0.8 | 0.8 | 8192 | +0.0 MiB in 9m48s | 0.0 MiB |
| signoz/data-volumeclaim-template-chi-signoz-clickhouse-cluster-0-0-0 | Observability | 152.6 | 253.9 | 5120 | +101.3 MiB in 9m48s | 14865.8 MiB |
| signoz/signoz-db-signoz-0 | Observability | 4.6 | 4.6 | 1024 | +0.0 MiB in 9m48s | 0.0 MiB |
