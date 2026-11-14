# B: after removal

**SIMULATION — does not satisfy the reference-hardware gate.** kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t) (cpus 2.0, cpuset 0-3). Not simulated: 16-32 GB RAM (only measured, not limited); SATA/NVMe disk speed.

- Started 2026-11-12T17:53:20Z, 15 samples every 20s
- Node bench-t470-control-plane (4 CPU, 8133872Ki memory), v1.34.0

### Cluster

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 1323.1 | 1662.2 | 159.4 | 207.2 |
| Secrets (OpenBao) | 19.2 | 19.2 | 10.9 | 12.8 |
| Certificates (cert-manager) | 74.2 | 96.1 | 2.5 | 3.1 |
| Application platform: control plane | 63.1 | 63.2 | 6.2 | 8.1 |
| Application platform: data plane | 7.9 | 8.2 | 0.2 | 2.4 |
| Gateway (kgateway) | 60.8 | 64.9 | 5.9 | 7.1 |
| Identity (ThunderID) | 11.9 | 11.9 | 0.2 | 0.6 |
| Workloads | 13.7 | 13.7 | 0.0 | 0.0 |
| Node overhead outside pods (kubelet, container runtime, system) | 1029.7 | 1036.3 | 54.1 | 102.5 |
| **Node total** | 2603.7 | 2971.6 | 239.5 | 277.5 |

Container images on disk: 3386 MiB. (Node filesystem used: 80659 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 1063.0 | 78.0 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 88.5 | 44.5 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 78.1 | 22.3 |
| kgateway-system/kgateway-7b8984bc55-9vxwg | Gateway (kgateway) | 41.1 | 2.0 |
| cert-manager/cert-manager-cainjector-784846b495-bsfbd | Certificates (cert-manager) | 34.9 | 1.1 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-8mmnz | Application platform: control plane | 28.1 | 5.4 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 26.8 | 8.2 |
| cert-manager/cert-manager-bc44b5799-cd9nt | Certificates (cert-manager) | 26.5 | 1.1 |
| kgateway-system/serendip-external-657d66d6c7-b9hlr | Gateway (kgateway) | 19.8 | 3.8 |
| serendip-system/openbao-0 | Secrets (OpenBao) | 19.2 | 10.9 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 0.9 | 0.9 | 1024 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 4m45s | 0.0 MiB |
