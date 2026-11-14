# C: baseline (before install)

**SIMULATION — does not satisfy the reference-hardware gate.** kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t) (cpus 2.0, cpuset 0-3). Not simulated: 16-32 GB RAM (only measured, not limited); SATA/NVMe disk speed.

- Started 2026-11-13T18:07:28Z, 15 samples every 20s
- Node bench-t470-control-plane (4 CPU, 8133872Ki memory), v1.34.0

### Cluster

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 1026.5 | 1058.6 | 152.2 | 217.5 |
| Secrets (OpenBao) | 22.2 | 22.4 | 10.3 | 12.9 |
| Certificates (cert-manager) | 71.0 | 71.1 | 2.5 | 2.9 |
| Application platform: control plane | 67.1 | 67.4 | 6.3 | 8.0 |
| Application platform: data plane | 8.3 | 8.5 | 0.2 | 1.1 |
| Gateway (kgateway) | 61.5 | 61.5 | 5.3 | 6.0 |
| Identity (ThunderID) | 12.3 | 12.4 | 0.3 | 0.6 |
| Workloads | 13.7 | 13.7 | 0.0 | 0.0 |
| Node overhead outside pods (kubelet, container runtime, system) | 1448.6 | 1450.0 | 56.7 | 103.5 |
| **Node total** | 2731.2 | 2762.6 | 233.7 | 262.7 |

Container images on disk: 7652 MiB. (Node filesystem used: 85016 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 759.7 | 75.7 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 87.0 | 20.5 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 81.6 | 42.9 |
| kgateway-system/kgateway-7b8984bc55-9vxwg | Gateway (kgateway) | 39.9 | 1.8 |
| cert-manager/cert-manager-cainjector-784846b495-bsfbd | Certificates (cert-manager) | 31.6 | 1.0 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-8mmnz | Application platform: control plane | 29.9 | 5.2 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 27.8 | 8.0 |
| cert-manager/cert-manager-bc44b5799-cd9nt | Certificates (cert-manager) | 25.7 | 1.0 |
| serendip-system/openbao-0 | Secrets (OpenBao) | 22.2 | 10.3 |
| kgateway-system/serendip-external-657d66d6c7-b9hlr | Gateway (kgateway) | 21.7 | 3.4 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 0.9 | 0.9 | 1024 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 4m45s | 0.0 MiB |
