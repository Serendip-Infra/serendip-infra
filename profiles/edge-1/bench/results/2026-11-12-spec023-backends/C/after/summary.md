# C: after removal

**SIMULATION — does not satisfy the reference-hardware gate.** kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t) (cpus 2.0, cpuset 0-3). Not simulated: 16-32 GB RAM (only measured, not limited); SATA/NVMe disk speed.

- Started 2026-11-13T19:43:41Z, 15 samples every 20s
- Node bench-t470-control-plane (4 CPU, 8133872Ki memory), v1.34.0

### Cluster

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 1006.5 | 1066.5 | 149.6 | 199.3 |
| Secrets (OpenBao) | 21.5 | 21.5 | 7.8 | 8.7 |
| Certificates (cert-manager) | 71.5 | 71.5 | 2.4 | 3.0 |
| Application platform: control plane | 72.1 | 72.3 | 6.1 | 8.8 |
| Application platform: data plane | 8.8 | 9.2 | 0.3 | 1.7 |
| Gateway (kgateway) | 64.2 | 69.0 | 5.3 | 6.7 |
| Identity (ThunderID) | 13.8 | 13.8 | 0.3 | 0.5 |
| Workloads | 13.7 | 13.7 | 0.0 | 0.0 |
| Node overhead outside pods (kubelet, container runtime, system) | 1590.4 | 1601.0 | 46.3 | 62.9 |
| **Node total** | 2862.5 | 2929.2 | 218.0 | 230.1 |

Container images on disk: 7652 MiB. (Node filesystem used: 85020 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 739.2 | 75.3 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 85.1 | 20.2 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 81.6 | 40.9 |
| kgateway-system/kgateway-7b8984bc55-9vxwg | Gateway (kgateway) | 42.6 | 1.9 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-8mmnz | Application platform: control plane | 34.4 | 5.1 |
| cert-manager/cert-manager-cainjector-784846b495-bsfbd | Certificates (cert-manager) | 32.3 | 0.9 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 28.2 | 8.0 |
| cert-manager/cert-manager-bc44b5799-cd9nt | Certificates (cert-manager) | 25.3 | 1.0 |
| kgateway-system/serendip-external-657d66d6c7-b9hlr | Gateway (kgateway) | 21.6 | 3.4 |
| serendip-system/openbao-0 | Secrets (OpenBao) | 21.5 | 7.8 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 0.9 | 0.9 | 1024 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 4m45s | 0.0 MiB |
