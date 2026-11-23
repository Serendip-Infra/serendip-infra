# Versions: providers/networking/external

Upstream assumptions for the `external` networking provider (spec 025
T003): a CNI Serendip didn't install. It only reads kube-system's
DaemonSets and the nodes; it writes nothing.

| Item | Assumption | Checked |
|---|---|---|
| kindnet | DaemonSet `kindnet` in `kube-system`, image `kindest/kindnetd:vYYYYMMDD-<hash>`; enforces NetworkPolicies from `v20240813` (kind v0.24.0, kube-network-policies), with no flag to turn them off | live on `serendip-t018` (kindnetd `v20250512-df8de77b`, 2026-11-20): reported `enforced`, and a deny-all ingress policy made a probe that had answered time out |
| Flannel on K3s | embedded, no DaemonSet; nodes carry `flannel.alpha.coreos.com/*` and `k3s.io/*` annotations; `k3s.io/node-args` (server nodes only) is a JSON array of the server's arguments, so `--disable-network-policy` there means K3s's policy controller is off. Its absence proves nothing, so enforcement stays `unknown` | unit tests only |
| Flannel standalone | DaemonSet `kube-flannel-ds*` in `kube-system` (the `kube-flannel` namespace of newer manifests isn't read); Flannel enforces nothing itself | unit tests only |
| Pod ranges | the nodes' `spec.podCIDR` and `spec.podCIDRs` | live, `10.244.0.0/24` |
| Anything else | plugin `unknown`, enforcement `unknown` | unit tests |
