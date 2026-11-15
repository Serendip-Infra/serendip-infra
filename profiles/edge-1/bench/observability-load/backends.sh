#!/usr/bin/env bash
# Install and remove the observability backends spec 023 T003 measures,
# by hand, with the values plan 023 and its spike settled. Sourced by
# measure.sh. Pinned versions are in providers/observability/openchoreo/
# VERSIONS.md. These are bench recipes, not the product's installer
# (that's T005).
#
#   A  OpenChoreo plane 1.3.1 + logs-opensearch 0.8.1 + metrics-prometheus 0.9.3
#   B  OpenChoreo plane 1.3.1 + logs-openobserve 0.8.0 + metrics-prometheus 0.9.3
#   C  SigNoz 0.149.0 + k8s-infra (logs and kubelet metrics), on its own
#
# Needs: helm, $KUBECTL (with its context), $HELM_KUBE (e.g.
# "--kubeconfig file"), and the OpenChoreo control plane installed (A, B).

PLANE_NS=openchoreo-observability-plane
SIGNOZ_NS=signoz
OC_CHARTS=oci://ghcr.io/openchoreo/helm-charts
MOD_CHARTS=oci://cr.openchoreo.dev/openchoreo/helm-charts
PLANE_VERSION=1.3.1
OPENOBSERVE_VERSION=0.8.0
OPENSEARCH_VERSION=0.8.1
PROMETHEUS_VERSION=0.9.3
SIGNOZ_VERSION=0.149.0

h() { helm $HELM_KUBE "$@"; }
rand() { LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 24; }
# password: OpenObserve and OpenSearch both refuse one without an upper
# and lower case letter, a digit and a symbol (T003: OpenObserve
# crash-looped on a random alphanumeric one).
password() { echo "Sd1-$(rand)"; }

# node_rule NAME SELECTOR_KEY SELECTOR_VALUE PORT: admit the nodes'
# current IPs to a store's ingest port (plan 023 spike findings 8 and 15).
# Fluent Bit runs with hostNetwork, and the plane chart's own policy
# admits only pods in the namespace.
node_rule() {
	local from="" ip
	for ip in $($KUBECTL get nodes -o jsonpath='{range .items[*]}{.status.addresses[?(@.type=="InternalIP")].address}{"\n"}{end}'); do
		from="$from{\"ipBlock\":{\"cidr\":\"$ip/32\"}},"
	done
	cat <<EOF | $KUBECTL apply -f -
{"apiVersion":"networking.k8s.io/v1","kind":"NetworkPolicy",
 "metadata":{"name":"$1","namespace":"$PLANE_NS"},
 "spec":{"podSelector":{"matchLabels":{"$2":"$3"}},"policyTypes":["Ingress"],
  "ingress":[{"from":[${from%,}],"ports":[{"port":$4,"protocol":"TCP"}]}]}}
EOF
}

plane_install() {
	$KUBECTL create namespace $PLANE_NS --dry-run=client -o yaml | $KUBECTL apply -f -
	local ca
	ca=$($KUBECTL -n openchoreo-control-plane get secret cluster-gateway-ca -o jsonpath='{.data.ca\.crt}' | base64 -d)
	$KUBECTL -n $PLANE_NS create configmap cluster-gateway-ca --from-literal=ca.crt="$ca" --dry-run=client -o yaml | $KUBECTL apply -f -
	$KUBECTL -n $PLANE_NS create secret generic observer-secret --from-literal=UID_RESOLVER_OAUTH_CLIENT_SECRET=unused-security-disabled --dry-run=client -o yaml | $KUBECTL apply -f -
	h upgrade --install openchoreo-observability-plane $OC_CHARTS/openchoreo-observability-plane --version $PLANE_VERSION -n $PLANE_NS \
		--set security.enabled=false --set gateway.enabled=false \
		--set observer.controlPlaneApiUrl=http://openchoreo-api.openchoreo-control-plane:8080 \
		--set observer.secretName=observer-secret \
		--set 'observer.extraEnvs[0].name=OBSERVER_BASE_URL' --set "observer.extraEnvs[0].value=http://observer.$PLANE_NS:8080" \
		--set 'observer.extraEnvs[1].name=AUTHZ_TIMEOUT' --set 'observer.extraEnvs[1].value=30s' \
		--wait --timeout 10m
	cat <<EOF | $KUBECTL apply -f -
{"apiVersion":"openchoreo.dev/v1alpha1","kind":"ObservabilityPlane",
 "metadata":{"name":"default","namespace":"openchoreo-control-plane"},
 "spec":{"planeID":"default","observerURL":"http://observer.$PLANE_NS:8080",
  "clusterAgent":{"clientCA":{"secretKeyRef":{"name":"cluster-agent-tls","namespace":"$PLANE_NS","key":"ca.crt"}}}}}
EOF
}

metrics_install() {
	h upgrade --install observability-metrics-prometheus $MOD_CHARTS/observability-metrics-prometheus --version $PROMETHEUS_VERSION -n $PLANE_NS \
		--set kube-prometheus-stack.alertmanager.enabled=false \
		--set kube-prometheus-stack.prometheus.prometheusSpec.retention=3d \
		--set kube-prometheus-stack.prometheus.prometheusSpec.retentionSize=2GiB \
		--set 'kube-prometheus-stack.prometheus.prometheusSpec.storageSpec.volumeClaimTemplate.spec.resources.requests.storage=3Gi' \
		--wait --timeout 10m
}

# metrics_plain_install: plan decision 3's fallback, tried in T003. The
# module with kube-prometheus-stack off (only its adapter), and plain
# Prometheus (prometheus-plain-values.yaml) where the adapter expects it.
PROM_PLAIN_VERSION=29.47.2
metrics_plain_install() {
	h upgrade --install prometheus oci://ghcr.io/prometheus-community/charts/prometheus --version $PROM_PLAIN_VERSION -n $PLANE_NS \
		-f "$here/prometheus-plain-values.yaml" --wait --timeout 10m
	h upgrade --install observability-metrics-prometheus $MOD_CHARTS/observability-metrics-prometheus --version $PROMETHEUS_VERSION -n $PLANE_NS \
		--set kube-prometheus-stack.enabled=false \
		--set "adapter.logLevel=${ADAPTER_LOG_LEVEL:-INFO}" \
		--wait --timeout 10m
}

openobserve_install() {
	$KUBECTL -n $PLANE_NS create secret generic openobserve-admin-credentials \
		--from-literal=ZO_ROOT_USER_EMAIL=admin@serendip.local --from-literal=ZO_ROOT_USER_PASSWORD="$(password)" \
		--dry-run=client -o yaml | $KUBECTL apply -f -
	node_rule serendip-allow-node-to-openobserve app.kubernetes.io/name openobserve-standalone 5080
	h upgrade --install observability-logs-openobserve $MOD_CHARTS/observability-logs-openobserve --version $OPENOBSERVE_VERSION -n $PLANE_NS \
		--set fluentBitCustomizations.clusterInstance=bench \
		--set openobserve-standalone.persistence.size=5Gi \
		--set-string openobserve-standalone.config.ZO_COMPACT_DATA_RETENTION_DAYS=3 \
		--wait --timeout 10m
}

opensearch_install() {
	local pw
	pw=$(password)
	$KUBECTL -n $PLANE_NS create secret generic opensearch-admin-credentials \
		--from-literal=username=admin --from-literal=password="$pw" \
		--dry-run=client -o yaml | $KUBECTL apply -f -
	# The rule goes in before the install: this module's Fluent Bit has an
	# init container that waits for OpenSearch's index template, so with
	# the rule after --wait the install can never finish (T003). OpenSearch
	# took about 13 minutes to start on the simulated T470, hence 30m.
	node_rule serendip-allow-node-to-opensearch app.kubernetes.io/name opensearch 9200
	h upgrade --install observability-logs-opensearch $MOD_CHARTS/observability-logs-opensearch --version $OPENSEARCH_VERSION -n $PLANE_NS \
		--set fluentBitCustomizations.clusterInstance=bench \
		--set 'openSearch.opensearchJavaOpts=-Xmx512M -Xms512M' \
		--set openSearch.persistence.size=5Gi \
		--set openSearch.sysctlInit.enabled=true \
		--set 'openSearch.extraEnvs[0].name=OPENSEARCH_INITIAL_ADMIN_PASSWORD' --set-string "openSearch.extraEnvs[0].value=$pw" \
		--set adapter.openSearchSecretName=opensearch-admin-credentials \
		--set openSearchSetup.openSearchSecretName=opensearch-admin-credentials \
		--set openSearchSetup.dataRetentionTime.containerLogs=3d \
		--set openSearchSetup.dataRetentionTime.k8sEvents=3d \
		--wait --timeout 30m
}

# plane_uninstall: plan 023 decision 13 as amended by T001: the plane
# object, the releases, the namespace (PVCs, Secrets, jobs), and the
# CRDs the charts leave.
plane_uninstall() {
	$KUBECTL -n openchoreo-control-plane delete observabilityplane default --ignore-not-found
	local r
	for r in observability-metrics-prometheus prometheus observability-logs-openobserve observability-logs-opensearch openchoreo-observability-plane; do
		h uninstall $r -n $PLANE_NS --wait --timeout 5m 2>/dev/null || true
	done
	$KUBECTL delete namespace $PLANE_NS --ignore-not-found --wait=true --timeout=5m
	$KUBECTL get crd -o name | grep -E 'monitoring\.coreos\.com|observabilityalertrules\.openchoreo\.dev' | xargs -r $KUBECTL delete
}

signoz_install() {
	helm repo add signoz https://charts.signoz.io >/dev/null 2>&1 || true
	helm repo update signoz >/dev/null
	# SigNoz has no organization until a user exists, and until then its
	# OpAMP server refuses the collector ("cannot create agent without
	# orgId"), which then runs only its extensions: no OTLP receivers, so
	# nothing is ingested (T003). The root user (v0.112.0+) creates the
	# org at startup. The stats reporter is on by default (every 6 h:
	# version, counts, ingest volume, and with identities, user emails);
	# Segment analytics is off by default and stays off.
	h upgrade --install signoz signoz/signoz --version $SIGNOZ_VERSION -n $SIGNOZ_NS --create-namespace \
		--set clickhouse.persistence.size=5Gi \
		--set telemetryStoreMigrator.enableReplication=false \
		--set-string signoz.env.signoz_statsreporter_enabled=false \
		--set-string signoz.env.signoz_statsreporter_collect_identities=false \
		--set-string signoz.env.signoz_analytics_enabled=false \
		--set-string signoz.env.signoz_user_root_enabled=true \
		--set-string signoz.env.signoz_user_root_email=admin@serendip.local \
		--set-string "signoz.env.signoz_user_root_password=$(password)" \
		--set-string signoz.env.signoz_user_root_org_name=bench \
		--wait --timeout 20m
	# The collector may have asked before the org existed; restart it so
	# it fetches its config now.
	$KUBECTL -n $SIGNOZ_NS rollout restart deploy/signoz-otel-collector
	$KUBECTL -n $SIGNOZ_NS rollout status deploy/signoz-otel-collector --timeout=5m
	h upgrade --install k8s-infra signoz/k8s-infra -n $SIGNOZ_NS \
		--set otelCollectorEndpoint=http://signoz-otel-collector.$SIGNOZ_NS.svc.cluster.local:4318 \
		--set otelInsecure=true --set clusterName=bench \
		--set presets.logsCollection.enabled=true \
		--set presets.kubeletMetrics.enabled=true \
		--set presets.hostMetrics.enabled=false \
		--wait --timeout 10m
}

signoz_uninstall() {
	# The ClickHouseInstallation carries the operator's finalizer, so it
	# goes first, while the operator is still running; otherwise helm
	# removes the operator, the finalizer is never cleared, and the
	# namespace hangs in Terminating (T003).
	$KUBECTL -n $SIGNOZ_NS delete clickhouseinstallations --all --wait=true --timeout=5m 2>/dev/null || true
	h uninstall k8s-infra -n $SIGNOZ_NS --wait --timeout 5m 2>/dev/null || true
	h uninstall signoz -n $SIGNOZ_NS --wait --timeout 5m 2>/dev/null || true
	$KUBECTL delete namespace $SIGNOZ_NS --ignore-not-found --wait=true --timeout=5m
	# ClickHouse operator CRDs, if the chart installed any.
	$KUBECTL get crd -o name | grep -E 'clickhouse' | xargs -r $KUBECTL delete
}
