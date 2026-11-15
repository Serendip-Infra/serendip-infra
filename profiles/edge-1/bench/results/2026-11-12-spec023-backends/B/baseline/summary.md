# B: baseline (before install)

**SIMULATION — does not satisfy the reference-hardware gate.** kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t) (cpus 2.0, cpuset 0-3). Not simulated: 16-32 GB RAM (only measured, not limited); SATA/NVMe disk speed.

- Started 2026-11-12T15:10:21Z, 15 samples every 20s
- Node bench-t470-control-plane (4 CPU, 8133872Ki memory), v1.34.0

### Cluster

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 919.6 | 931.2 | 157.1 | 224.1 |
| Secrets (OpenBao) | 18.9 | 19.1 | 7.7 | 10.4 |
| Certificates (cert-manager) | 62.9 | 62.9 | 2.5 | 3.0 |
| Application platform: control plane | 61.8 | 62.2 | 6.5 | 11.0 |
| Application platform: data plane | 7.8 | 8.3 | 0.4 | 3.1 |
| Gateway (kgateway) | 57.4 | 59.5 | 5.7 | 6.7 |
| Identity (ThunderID) | 12.2 | 12.2 | 0.3 | 0.5 |
| Workloads | 13.7 | 13.7 | 0.0 | 0.0 |
| Node overhead outside pods (kubelet, container runtime, system) | 774.4 | 788.6 | 54.7 | 96.3 |
| **Node total** | 1928.6 | 1941.0 | 234.8 | 283.4 |

Container images on disk: 2844 MiB. (Node filesystem used: 80043 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 673.8 | 75.5 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 82.4 | 20.5 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 74.2 | 46.8 |
| kgateway-system/kgateway-7b8984bc55-9vxwg | Gateway (kgateway) | 38.3 | 2.0 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-8mmnz | Application platform: control plane | 27.6 | 5.4 |
| cert-manager/cert-manager-bc44b5799-cd9nt | Certificates (cert-manager) | 26.3 | 1.0 |
| cert-manager/cert-manager-cainjector-784846b495-bsfbd | Certificates (cert-manager) | 24.4 | 1.0 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 23.9 | 7.9 |
| kgateway-system/serendip-external-657d66d6c7-b9hlr | Gateway (kgateway) | 19.1 | 3.7 |
| serendip-system/openbao-0 | Secrets (OpenBao) | 18.9 | 7.7 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 1.5 | 1.5 | 1024 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 4m45s | 0.0 MiB |
