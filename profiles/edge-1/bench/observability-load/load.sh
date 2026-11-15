#!/usr/bin/env bash
# Spec 023 observability load (plan decision 9). Works from Git Bash on
# Windows and from Linux.
#
#   load.sh start [DRIVER_NAMESPACE]   apply the five loggers, and start the
#                                      query driver in DRIVER_NAMESPACE
#                                      (default openchoreo-observability-plane)
#   load.sh rate                       lines per logger over the last 10 s
#   load.sh latency                    the driver's query results so far
#   load.sh stop [DRIVER_NAMESPACE]    remove the driver and the loggers
#
# KUBECTL (default "kubectl") may carry a context, e.g.
#   KUBECTL="kubectl --context kind-serendip-t018" ./load.sh start
# The driver's environment can be overridden for other backends (see
# driver.sh): LOGS_URL, LOGS_BODY, METRICS_URL, METRICS_BODY, AUTH_HEADER.
set -euo pipefail
export MSYS_NO_PATHCONV=1

# pwd -W gives Git Bash's Windows form (D:/...), which a Windows kubectl
# can read; elsewhere it fails and plain pwd is right.
here=$(cd "$(dirname "$0")" && { pwd -W 2>/dev/null || pwd; })
k=${KUBECTL:-kubectl}
ns=serendip-obs-load
driver_ns=${2:-openchoreo-observability-plane}
driver_image=curlimages/curl:8.11.1

start() {
	$k apply -f "$here/loggers.yaml"
	$k -n "$driver_ns" create configmap obs-load-driver --from-file=driver.sh="$here/driver.sh" --dry-run=client -o yaml | $k apply -f -
	# The env goes inside --overrides: its containers list replaces the
	# one kubectl run generates, so --env flags would be dropped (T003:
	# config C's driver ran with no LOGS_URL).
	local env="" v val
	for v in LOGS_URL LOGS_BODY METRICS_URL METRICS_BODY AUTH_HEADER SEARCH_EVERY METRICS_EVERY; do
		val=${!v:-}
		[ -n "$val" ] || continue
		val=${val//\\/\\\\}
		val=${val//\"/\\\"}
		env="$env{\"name\":\"$v\",\"value\":\"$val\"},"
	done
	$k -n "$driver_ns" delete pod obs-load-driver --ignore-not-found --wait=true
	$k -n "$driver_ns" run obs-load-driver --image="$driver_image" --restart=Never \
		--overrides='{"spec":{"terminationGracePeriodSeconds":1,"containers":[{"name":"obs-load-driver","image":"'"$driver_image"'","command":["sh","/load/driver.sh"],"env":['"${env%,}"'],"volumeMounts":[{"name":"load","mountPath":"/load"}]}],"volumes":[{"name":"load","configMap":{"name":"obs-load-driver"}}]}}'
	$k -n "$ns" rollout status deploy --timeout=180s
	$k -n "$driver_ns" wait --for=condition=Ready pod/obs-load-driver --timeout=120s
}

rate() {
	for i in 1 2 3 4 5; do
		printf 'obs-load-%s: %s lines in 10 s\n' "$i" "$($k -n "$ns" logs deploy/obs-load-$i --since=10s | wc -l)"
	done
}

latency() {
	$k -n "$driver_ns" logs obs-load-driver | awk '
		{n[$1]++; if ($2 !~ /^2/) bad[$1]++; t[$1]+=$3; if ($3>max[$1]) max[$1]=$3}
		END {for (q in n) printf "%s: %d queries, %d not 2xx, mean %.3f s, max %.3f s\n", q, n[q], bad[q], t[q]/n[q], max[q]}'
}

stop() {
	$k -n "$driver_ns" delete pod obs-load-driver --ignore-not-found
	$k -n "$driver_ns" delete configmap obs-load-driver --ignore-not-found
	$k delete -f "$here/loggers.yaml" --ignore-not-found
}

case "${1:-}" in
start) start ;;
rate) rate ;;
latency) latency ;;
stop) stop ;;
*) sed -n '2,16p' "$0"; exit 2 ;;
esac
