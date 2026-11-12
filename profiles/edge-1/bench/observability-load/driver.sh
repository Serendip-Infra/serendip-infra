#!/bin/sh
# Spec 023 load driver (plan decision 9), run inside the backend's own
# namespace by load.sh, so the backend's NetworkPolicies need no change:
#   - a log search every SEARCH_EVERY seconds (default 10), each naming
#     one logger in turn by its component UID;
#   - a 1-hour metrics query every METRICS_EVERY seconds (default 30).
#
# Defaults talk to OpenChoreo's adapters (configs A and B). For another
# backend (config C, SigNoz), set LOGS_URL, LOGS_BODY, METRICS_URL,
# METRICS_BODY and, if it needs one, AUTH_HEADER. Bodies may use the
# placeholders __FROM__ and __TO__ (RFC 3339), __FROM_MS__ and __TO_MS__
# (Unix milliseconds) and __N__ (the logger, 1-5).
#
# Each query prints one line: "<kind> <http code> <seconds>", so latency
# and errors can be read back with kubectl logs.

LOGS_URL=${LOGS_URL:-http://logs-adapter:9098/api/v1/logs/query}
METRICS_URL=${METRICS_URL:-http://metrics-adapter:9099/api/v1/metrics/query}
SEARCH_EVERY=${SEARCH_EVERY:-10}
METRICS_EVERY=${METRICS_EVERY:-30}
SCOPE='"namespace":"serendip-obs-load","projectUid":"00000000-0000-4000-8000-0000000000a0","environmentUid":"00000000-0000-4000-8000-0000000000e0","componentUid":"00000000-0000-4000-8000-00000000000__N__"'
LOGS_BODY=${LOGS_BODY:-'{"startTime":"__FROM__","endTime":"__TO__","limit":100,"sortOrder":"desc","searchPhrase":"seq=","searchScope":{'"$SCOPE"'}}'}
METRICS_BODY=${METRICS_BODY:-'{"metric":"resource","startTime":"__FROM__","endTime":"__TO__","step":"1m","searchScope":{'"$SCOPE"'}}'}

# body TEMPLATE WINDOW_SECONDS N: fills the placeholders for the last
# WINDOW_SECONDS, using busybox date.
body() {
	now=$(date +%s)
	from=$((now - $2))
	printf '%s' "$1" | sed \
		-e "s/__FROM__/$(date -u -d "@$from" +%Y-%m-%dT%H:%M:%SZ)/g" \
		-e "s/__TO__/$(date -u -d "@$now" +%Y-%m-%dT%H:%M:%SZ)/g" \
		-e "s/__FROM_MS__/${from}000/g" \
		-e "s/__TO_MS__/${now}000/g" \
		-e "s/__N__/$3/g"
}

query() { # KIND URL BODY
	if [ -n "$AUTH_HEADER" ]; then
		r=$(curl -s -o /dev/null -m 30 -w '%{http_code} %{time_total}' -X POST "$2" -H 'content-type: application/json' -H "$AUTH_HEADER" -d "$3")
	else
		r=$(curl -s -o /dev/null -m 30 -w '%{http_code} %{time_total}' -X POST "$2" -H 'content-type: application/json' -d "$3")
	fi
	echo "$1 $r"
}

t=0
n=1
while true; do
	if [ $((t % SEARCH_EVERY)) -eq 0 ]; then
		query logs "$LOGS_URL" "$(body "$LOGS_BODY" 900 $n)"
		n=$((n % 5 + 1))
	fi
	if [ $((t % METRICS_EVERY)) -eq 0 ]; then
		query metrics "$METRICS_URL" "$(body "$METRICS_BODY" 3600 $n)"
	fi
	sleep 1
	t=$((t + 1))
done
