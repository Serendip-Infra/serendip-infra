# B: plane and logs-openobserve, idle (no metrics module)

**SIMULATION — does not satisfy the reference-hardware gate.** kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t) (cpus 2.0, cpuset 0-3). Not simulated: 16-32 GB RAM (only measured, not limited); SATA/NVMe disk speed.

- Started 2026-11-12T15:18:18Z, 15 samples every 20s
- Node bench-t470-control-plane (4 CPU, 8133872Ki memory), v1.34.0

### Cluster

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 890.2 | 915.0 | 160.0 | 185.7 |
| Secrets (OpenBao) | 18.9 | 18.9 | 8.1 | 9.7 |
| Certificates (cert-manager) | 63.2 | 63.3 | 2.5 | 4.0 |
| Application platform: control plane | 62.1 | 62.2 | 6.5 | 9.8 |
| Application platform: data plane | 8.0 | 8.4 | 0.4 | 4.3 |
| Gateway (kgateway) | 56.5 | 61.9 | 5.6 | 6.2 |
| Identity (ThunderID) | 12.2 | 12.2 | 0.3 | 0.6 |
| Workloads | 13.7 | 13.7 | 0.0 | 0.0 |
| Observability | 170.8 | 177.3 | 14.8 | 18.7 |
| Node overhead outside pods (kubelet, container runtime, system) | 817.7 | 821.2 | 66.6 | 81.6 |
| **Node total** | 2113.5 | 2140.2 | 264.8 | 304.6 |

Container images on disk: 2853 MiB. (Node filesystem used: 80103 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 655.2 | 80.4 |
| openchoreo-observability-plane/openobserve-0 | Observability | 127.1 | 10.0 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 73.3 | 21.4 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 73.2 | 44.2 |
| kgateway-system/kgateway-7b8984bc55-9vxwg | Gateway (kgateway) | 37.4 | 1.9 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-8mmnz | Application platform: control plane | 27.6 | 5.3 |
| cert-manager/cert-manager-bc44b5799-cd9nt | Certificates (cert-manager) | 26.4 | 1.1 |
| cert-manager/cert-manager-cainjector-784846b495-bsfbd | Certificates (cert-manager) | 24.4 | 1.0 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 24.0 | 7.9 |
| kgateway-system/serendip-external-657d66d6c7-b9hlr | Gateway (kgateway) | 19.1 | 3.8 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| openchoreo-observability-plane/data-openobserve-0 | Observability | 19.1 | 46.7 | 5120 | +27.7 MiB in 4m45s | 8376.1 MiB |
| openchoreo-observability-plane/observer-alerts-data | Observability | 0.1 | 0.1 | 128 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 1.5 | 1.5 | 1024 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 4m45s | 0.0 MiB |
