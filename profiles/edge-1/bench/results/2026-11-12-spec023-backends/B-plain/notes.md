# B-plain — 2026-11-14

Plan decision 3's fallback: the plane chart, the metrics module with kube-prometheus-stack off (only its adapter), and prometheus-community/prometheus 29.47.2 with ../../../observability-load/prometheus-plain-values.yaml. No logs module.

- the adapter's resource query for obs-load-1 (by component UID) returned cpuUsage, cpuRequests, cpuLimits, memoryUsage, memoryRequests and memoryLimits; both scrape jobs (kubelet, kube-state-metrics) up
- idle delta over C/after (the cluster after config C's removal, the cleanest state measured): delta-over-clean.md
- no monitoring.coreos.com CRDs installed; kube-apiserver unchanged
