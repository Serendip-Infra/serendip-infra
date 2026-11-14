# A: after removal

**SIMULATION — does not satisfy the reference-hardware gate.** kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t) (cpus 2.0, cpuset 0-3). Not simulated: 16-32 GB RAM (only measured, not limited); SATA/NVMe disk speed.

- Started 2026-11-12T23:57:22Z, 15 samples every 20s
- Node bench-t470-control-plane (4 CPU, 8133872Ki memory), v1.34.0

### Cluster

| Group | Memory mean (MiB) | Memory max | CPU mean (mcores) | CPU max |
|---|---|---|---|---|
| Kubernetes system | 1204.2 | 1506.1 | 169.4 | 205.5 |
| Secrets (OpenBao) | 19.5 | 19.5 | 10.7 | 12.8 |
| Certificates (cert-manager) | 82.5 | 101.6 | 2.7 | 3.4 |
| Application platform: control plane | 66.6 | 66.7 | 7.0 | 9.1 |
| Application platform: data plane | 8.4 | 8.6 | 0.4 | 1.9 |
| Gateway (kgateway) | 63.0 | 70.8 | 5.8 | 7.2 |
| Identity (ThunderID) | 12.2 | 12.3 | 0.3 | 1.3 |
| Workloads | 13.7 | 13.7 | 0.0 | 0.0 |
| Node overhead outside pods (kubelet, container runtime, system) | 1342.0 | 1357.4 | 62.9 | 112.7 |
| **Node total** | 2812.2 | 3156.2 | 259.1 | 302.8 |

Container images on disk: 5084 MiB. (Node filesystem used: 82439 MiB; on kind this is the whole Docker VM disk, not Serendip's.)

Largest pods by memory:

| Pod | Group | Memory mean (MiB) | CPU mean (mcores) |
|---|---|---|---|
| kube-system/kube-apiserver-bench-t470-control-plane | Kubernetes system | 922.9 | 85.1 |
| kube-system/etcd-bench-t470-control-plane | Kubernetes system | 91.3 | 45.0 |
| kube-system/kube-controller-manager-bench-t470-control-plane | Kubernetes system | 87.1 | 24.6 |
| kgateway-system/kgateway-7b8984bc55-9vxwg | Gateway (kgateway) | 42.8 | 1.9 |
| cert-manager/cert-manager-cainjector-784846b495-bsfbd | Certificates (cert-manager) | 40.0 | 1.1 |
| kube-system/kube-scheduler-bench-t470-control-plane | Kubernetes system | 30.7 | 9.2 |
| openchoreo-control-plane/controller-manager-6b99ffdcd-8mmnz | Application platform: control plane | 29.3 | 5.8 |
| cert-manager/cert-manager-bc44b5799-cd9nt | Certificates (cert-manager) | 28.1 | 1.2 |
| kgateway-system/serendip-external-657d66d6c7-b9hlr | Gateway (kgateway) | 20.2 | 3.9 |
| serendip-system/openbao-0 | Secrets (OpenBao) | 19.5 | 10.7 |

Volumes (PersistentVolumeClaims):

| Volume | Group | Used first (MiB) | Used last (MiB) | Capacity (MiB) | Growth over the phase | Per day (extrapolated) |
|---|---|---|---|---|---|---|
| serendip-identity/thunderid-certs-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-identity/thunderid-database-pvc | Identity (ThunderID) | 0.9 | 0.9 | 1024 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-identity/thunderid-secrets-pvc | Identity (ThunderID) | 0.0 | 0.0 | 16 | +0.0 MiB in 4m45s | 0.0 MiB |
| serendip-system/openbao-data | Secrets (OpenBao) | 4.4 | 4.4 | 1024 | +0.0 MiB in 4m45s | 0.0 MiB |
