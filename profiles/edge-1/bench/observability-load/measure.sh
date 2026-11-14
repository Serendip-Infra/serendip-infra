#!/usr/bin/env bash
# Spec 023 T003: measure one observability backend on an edge-1 cluster,
# against a baseline taken the same way, with bench-edge1 sample.
#
#   measure.sh <A|B|C> <out-dir> [disk-samples]
#
# Phases (each a bench-edge1 sample run in <out-dir>/<phase>/):
#   baseline   the cluster before the install, 5 min
#   [B only] logs-idle   plane + logs module, before the metrics module,
#              so the metrics module's own cost (and its CRDs') shows
#   idle       installed, nothing logging beyond the platform, 10 min
#   load       the five loggers and the query driver (load.sh), 10 min
#   disk       still under load, disk-samples × 10 min (default 7: 1 h),
#              with the du probe at both ends
#   after      after removal, 5 min, to check nothing is left running
#
# Env: KUBECONFIG_FILE (required), BENCH (bench-edge1 binary, required),
# For C, the driver's LOGS_URL / LOGS_BODY / METRICS_URL / METRICS_BODY
# (see driver.sh) come from signoz-query.env, since SigNoz isn't queried
# through OpenChoreo's adapters.
set -euo pipefail
export MSYS_NO_PATHCONV=1

cfg=${1:?A, B or C}
out=${2:?out dir}
disk_samples=${3:-7}
: "${KUBECONFIG_FILE:?}" "${BENCH:?}"
here=$(cd "$(dirname "$0")" && { pwd -W 2>/dev/null || pwd; })
export KUBECTL="kubectl --kubeconfig $KUBECONFIG_FILE"
export HELM_KUBE="--kubeconfig $KUBECONFIG_FILE"
# shellcheck source=backends.sh
. "$here/backends.sh"
mkdir -p "$out"
notes="$out/notes.md"

sim=(--sim-cpus 2 --sim-cpuset 0-3
	--sim-note "kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t)"
	--not-simulated "16-32 GB RAM (only measured, not limited); SATA/NVMe disk speed")

phase() { # NAME LABEL SAMPLES INTERVAL
	echo "== $(date -u +%T) phase $1: $3 × $4"
	"$BENCH" sample --kubeconfig "$KUBECONFIG_FILE" --label "$cfg: $2" --out "$out/$1" --samples "$3" --interval "$4" "${sim[@]}"
}
# timed LABEL CMD...: runs CMD, which must succeed (bash -ec, so a failed
# helm step inside an install function stops it, unlike in an && list
# where errexit doesn't apply), and records how long it took.
timed() {
	local t0=$SECONDS
	"${@:2}"
	echo "- $1: $((SECONDS - t0)) s" | tee -a "$notes"
}
ready() { $KUBECTL wait pods --all -n "$1" --for=condition=Ready --timeout=10m --field-selector=status.phase!=Succeeded; }

echo "# $cfg — $(date -u +%FT%TZ)" >"$notes"
phase baseline "baseline (before install)" 15 20s

case $cfg in
A)
	timed "install (plane, logs-opensearch, metrics)" bash -ec '. "'"$here"'/backends.sh"; plane_install; opensearch_install; metrics_install'
	ns=$PLANE_NS
	;;
B)
	timed "install (plane, logs-openobserve)" bash -ec '. "'"$here"'/backends.sh"; plane_install; openobserve_install'
	ready $PLANE_NS
	sleep 120
	phase logs-idle "plane and logs-openobserve, idle (no metrics module)" 15 20s
	timed "install (metrics-prometheus)" bash -ec '. "'"$here"'/backends.sh"; metrics_install'
	ns=$PLANE_NS
	;;
C)
	timed "install (SigNoz, k8s-infra)" bash -ec '. "'"$here"'/backends.sh"; signoz_install'
	ns=$SIGNOZ_NS
	# shellcheck source=signoz-query.env
	. "$here/signoz-query.env"
	;;
*) echo "unknown config $cfg" >&2; exit 2 ;;
esac
ready "$ns"
sleep 120
phase idle "installed, idle" 30 20s

"$here/load.sh" start "$ns"
sleep 120
"$here/load.sh" rate | tee -a "$notes"
phase load "under load (5 × 10 lines/s, a search every 10 s, metrics every 30 s)" 30 20s
phase disk "under load, disk growth" "$disk_samples" 10m
{
	echo "- query results over the run:"
	"$here/load.sh" latency "$ns" | sed 's/^/  - /'
} | tee -a "$notes"
"$here/load.sh" stop "$ns"

case $cfg in
A | B) timed "uninstall" bash -ec '. "'"$here"'/backends.sh"; plane_uninstall' ;;
C) timed "uninstall" bash -ec '. "'"$here"'/backends.sh"; signoz_uninstall' ;;
esac
sleep 60
phase after "after removal" 15 20s
echo "== done $(date -u +%T)"
