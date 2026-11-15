# C: under load, disk growth

**SIMULATION — does not satisfy the reference-hardware gate.** kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t) (cpus 2.0, cpuset 0-3). Not simulated: 16-32 GB RAM (only measured, not limited); SATA/NVMe disk speed.

- Started 2026-11-13T18:40:56Z, 7 samples every 600s
- Node bench-t470-control-plane (4 CPU, 8133872Ki memory), v1.34.0

### Cluster

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 954.9 | 982.8 | 139.2 | 147.2 |
| Secrets (OpenBao) | 21.6 | 21.8 | 6.8 | 9.5 |
| Certificates (cert-manager) | 70.8 | 71.2 | 2.4 | 3.1 |
| Application platform: control plane | 71.4 | 72.2 | 6.8 | 8.7 |
| Application platform: data plane | 8.6 | 9.1 | 0.0 | 0.1 |
| Gateway (kgateway) | 62.1 | 62.5 | 4.8 | 5.4 |
| Identity (ThunderID) | 13.3 | 13.8 | 0.2 | 0.3 |
| Workloads | 13.7 | 13.7 | 0.0 | 0.0 |
| Observability | 2069.4 | 2112.6 | 377.2 | 425.3 |
| Other: serendip-obs-load | 4.3 | 4.3 | 1.0 | 1.3 |
| Node overhead outside pods (kubelet, container runtime, system) | 1575.5 | 1640.0 | 79.4 | 181.7 |
| **Node total** | 4865.5 | 4904.1 | 617.9 | 778.4 |

Container images on disk: 7654 MiB. (Node filesystem used: 85640 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| signoz/chi-signoz-clickhouse-cluster-0-0-0 | Observability | 1042.5 | 308.4 |
| signoz/signoz-zookeeper-0 | Observability | 760.8 | 7.0 |
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 697.4 | 66.6 |
| signoz/signoz-otel-collector-5fc589ffcf-876bd | Observability | 85.0 | 20.5 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 80.9 | 21.2 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 77.3 | 38.5 |
| signoz/k8s-infra-otel-agent-99gp7 | Observability | 62.1 | 34.2 |
| signoz/signoz-0 | Observability | 48.4 | 2.0 |
| kgateway-system/kgateway-7b8984bc55-9vxwg | Gateway (kgateway) | 40.5 | 1.7 |
| signoz/k8s-infra-otel-deployment-7d94574947-rs8db | Observability | 39.0 | 1.4 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 1h0m11s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 0.9 | 0.9 | 1024 | +0.0 MiB in 1h0m11s | 0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 1h0m11s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 1h0m11s | 0.0 MiB |
| signoz/data-signoz-zookeeper-0 | Observability | 0.8 | 0.8 | 8192 | +0.0 MiB in 1h0m11s | 0.0 MiB |
| signoz/data-volumeclaim-template-chi-signoz-clickhouse-cluster-0-0-0 | Observability | 604.3 | 566.6 | 5120 | -37.8 MiB in 1h0m11s | -904.2 MiB |
| signoz/signoz-db-signoz-0 | Observability | 4.6 | 4.6 | 1024 | +0.0 MiB in 1h0m11s | 0.0 MiB |
