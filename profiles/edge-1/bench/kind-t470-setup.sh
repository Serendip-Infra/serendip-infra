#!/usr/bin/env bash
# Creates a kind node constrained to approximate a ThinkPad T470's CPU class
# (spec 013, plan.md decision 1). Kind-specific setup only: the benchmark
# harness itself (cmd/bench-edge1) makes no kind assumptions.
#
#   kind-t470-setup.sh up   <cluster-name> <kubeconfig-out>
#   kind-t470-setup.sh down <cluster-name>
#
# A simulation never satisfies the reference-hardware gate (constitution
# rules 6 and 9).
#
# CNI=cilium (specs/025-networking T005) creates the cluster without
# kindnet and installs Cilium with Helm, using the chart version and values
# the networking provider pins (providers/networking/cilium: ChartVersion
# in cilium.go, values.yaml), the same ones serendip-agent's K3s bootstrap
# installs. The default, CNI=kindnet, is unchanged.
set -euo pipefail

SIM_CPUS="${SIM_CPUS:-2}"
SIM_CPUSET="${SIM_CPUSET:-0-3}"
NODE_IMAGE="${NODE_IMAGE:-kindest/node:v1.34.0}"
here="$(cd "$(dirname "$0")" && pwd)"
# KIND_CONFIG replaces kind-t470.yaml, for example to map the gateway to
# another host port when another cluster already holds 30080. The harness
# never uses the host port, so results are unaffected.
KIND_CONFIG="${KIND_CONFIG:-$here/kind-t470.yaml}"
CNI="${CNI:-kindnet}"
cilium_dir="$here/../../../providers/networking/cilium"

case "$CNI" in
kindnet | cilium) ;;
*)
  echo "CNI=$CNI: want kindnet or cilium" >&2
  exit 2
  ;;
esac

case "${1:-}" in
up)
  name="${2:?cluster name required}"
  out="${3:?kubeconfig output path required}"
  config="$KIND_CONFIG"
  wait=(--wait 120s)
  if [ "$CNI" = cilium ]; then
    if grep -q '^networking:' "$KIND_CONFIG"; then
      echo "CNI=cilium: $KIND_CONFIG already has a networking block; set disableDefaultCNI in it and use CNI=kindnet" >&2
      exit 2
    fi
    config="$(mktemp)"
    trap 'rm -f "$config"' EXIT
    { printf 'networking:\n  disableDefaultCNI: true\n'; cat "$KIND_CONFIG"; } > "$config"
    # Without a CNI the node stays NotReady: wait for it after Cilium.
    wait=()
  fi
  kind create cluster --name "$name" --image "$NODE_IMAGE" --config "$config" "${wait[@]}"
  node="$name-control-plane"
  docker update --cpus "$SIM_CPUS" --cpuset-cpus "$SIM_CPUSET" "$node" > /dev/null
  kind get kubeconfig --name "$name" > "$out"
  # Local image caches (registry-cache.sh), when they're running: pulls
  # then come from this machine after the first time.
  if "$here/registry-cache.sh" running; then
    "$here/registry-cache.sh" wire "$name"
  else
    echo "image caches not running (registry-cache.sh up): pulling from the internet"
  fi
  if [ "$CNI" = cilium ]; then
    version="$(sed -n 's/^[[:space:]]*ChartVersion[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$cilium_dir/cilium.go")"
    [ -n "$version" ] || { echo "no ChartVersion in $cilium_dir/cilium.go" >&2; exit 1; }
    echo "installing Cilium $version (providers/networking/cilium/values.yaml)"
    # An empty repository config and cache: the user's own Helm repos (and
    # a stale cache for one of them) can't break --repo.
    helm_tmp="$(mktemp -d)"
    trap 'rm -rf "$config" "$helm_tmp"' EXIT
    HELM_REPOSITORY_CONFIG="$helm_tmp/repositories.yaml" HELM_REPOSITORY_CACHE="$helm_tmp/cache" \
      helm upgrade --install cilium cilium --repo https://helm.cilium.io --version "$version" \
      --namespace kube-system --kubeconfig "$out" -f "$cilium_dir/values.yaml"
    # MSYS_NO_PATHCONV: Git Bash on Windows would rewrite the node's path.
    MSYS_NO_PATHCONV=1 docker exec "$node" kubectl --kubeconfig=/etc/kubernetes/admin.conf \
      wait --for=condition=Ready node --all --timeout=600s
  fi
  echo "applied limits (from docker inspect):"
  docker inspect "$node" --format '  NanoCpus={{.HostConfig.NanoCpus}} CpusetCpus={{.HostConfig.CpusetCpus}} Memory={{.HostConfig.Memory}}'
  echo "kubeconfig written to $out"
  echo "run the harness with: --sim-cpus $SIM_CPUS --sim-cpuset $SIM_CPUSET"
  ;;
down)
  kind delete cluster --name "${2:?cluster name required}"
  ;;
*)
  echo "usage: $0 up <cluster-name> <kubeconfig-out> | down <cluster-name>" >&2
  exit 2
  ;;
esac
