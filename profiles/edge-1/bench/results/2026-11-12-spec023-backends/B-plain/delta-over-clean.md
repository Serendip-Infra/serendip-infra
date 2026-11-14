### B-plain: plane and plain Prometheus, idle (no logs module) over C: after removal

- **Node memory:** +276 MiB mean, +343 MiB at its max
- **Node CPU:** +28 mcores mean, +59 mcores at its max

| Group | Memory change (MiB, mean) |
|---|---|
| Kubernetes system | -7.8 |
| Certificates (cert-manager) | +6.2 |
| Application platform: control plane | +2.4 |
| Application platform: data plane | +0.7 |
| Gateway (kgateway) | -2.3 |
| Observability | +215.8 |

Largest pod changes:

| Pod | Memory change (MiB, mean) |
|---|---|
| openchoreo-observability-plane/openchoreo-observability-prometheus-84746b7844-td8d4 | +106.7 |
| openchoreo-observability-plane/observer-75b8dbf7cf-pjt2j | +34.8 |
| openchoreo-observability-plane/controller-manager-dfc9d8dcd-kchck | +28.0 |
| openchoreo-observability-plane/cluster-agent-observabilityplane-597bbd6cbf-9nbkt | +19.0 |
| openchoreo-observability-plane/kube-state-metrics-68594d8bb6-gnh6k | +18.5 |
| openchoreo-observability-plane/metrics-adapter-prometheus-5c87b5f765-2xjdh | +8.7 |
| kube-system/kube-apiserver-bench-t470-control-plane | -8.6 |
| cert-manager/cert-manager-bc44b5799-cd9nt | +5.1 |
| kgateway-system/kgateway-7b8984bc55-9vxwg | -2.3 |
| kube-system/kube-controller-manager-bench-t470-control-plane | -1.2 |
