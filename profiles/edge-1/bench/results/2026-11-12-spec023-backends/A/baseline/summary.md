# A: baseline (before install)

**SIMULATION — does not satisfy the reference-hardware gate.** kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t) (cpus 2.0, cpuset 0-3). Not simulated: 16-32 GB RAM (only measured, not limited); SATA/NVMe disk speed.

- Started 2026-11-12T22:23:59Z, 15 samples every 20s
- Node bench-t470-control-plane (4 CPU, 8133872Ki memory), v1.34.0

### Cluster

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 820.2 | 845.8 | 176.5 | 207.7 |
| Secrets (OpenBao) | 19.3 | 19.3 | 9.1 | 12.3 |
| Certificates (cert-manager) | 71.0 | 71.1 | 2.9 | 4.0 |
| Application platform: control plane | 65.4 | 65.9 | 7.4 | 11.0 |
| Application platform: data plane | 8.3 | 8.5 | 0.4 | 1.7 |
| Gateway (kgateway) | 61.5 | 64.5 | 6.1 | 7.5 |
| Identity (ThunderID) | 12.1 | 12.1 | 0.3 | 1.2 |
| Workloads | 13.7 | 13.7 | 0.0 | 0.0 |
| Node overhead outside pods (kubelet, container runtime, system) | 1292.0 | 1295.5 | 61.8 | 104.4 |
| **Node total** | 2363.6 | 2379.5 | 264.7 | 308.7 |

Container images on disk: 5084 MiB. (Node filesystem used: 82371 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 551.5 | 86.9 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 87.4 | 25.5 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 82.3 | 47.6 |
| kgateway-system/kgateway-7b8984bc55-9vxwg | Gateway (kgateway) | 41.5 | 2.1 |
| cert-manager/cert-manager-cainjector-784846b495-bsfbd | Certificates (cert-manager) | 31.4 | 1.3 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 30.8 | 9.6 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-8mmnz | Application platform: control plane | 29.2 | 6.1 |
| cert-manager/cert-manager-bc44b5799-cd9nt | Certificates (cert-manager) | 26.6 | 1.2 |
| kgateway-system/serendip-external-657d66d6c7-b9hlr | Gateway (kgateway) | 20.0 | 4.0 |
| serendip-system/openbao-0 | Secrets (OpenBao) | 19.3 | 9.1 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 4m46s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 0.9 | 0.9 | 1024 | +0.0 MiB in 4m46s | 0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 4m46s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 4m46s | 0.0 MiB |
