#!/usr/bin/env bash
# Local pull-through image caches for dev and bench kind clusters, so a
# new cluster doesn't download every image again (time and data). One
# registry:2 container per upstream registry, in Docker beside the kind
# nodes, with its layers in a named Docker volume that outlives clusters.
#
#   registry-cache.sh up              start the caches (idempotent)
#   registry-cache.sh wire <cluster>  point an existing kind cluster's
#                                     containerd at them
#   registry-cache.sh status          caches, and disk used per cache
#   registry-cache.sh down [--purge]  stop them; --purge also deletes the
#                                     cached layers
#
# kind-t470-setup.sh wires new clusters automatically when the caches are
# running. If a cache is down or can't serve an image, containerd falls
# back to the real registry, so the worst case is a normal download.
# Only pinned image tags are used, so a cached tag can't go stale.
set -euo pipefail

CACHE_IMAGE=registry:2.8.3
NETWORK=kind
PREFIX=kind-cache

# upstream registry host -> its real endpoint
declare -A UPSTREAMS=(
	[docker.io]=https://registry-1.docker.io
	[ghcr.io]=https://ghcr.io
	[quay.io]=https://quay.io
	[registry.k8s.io]=https://registry.k8s.io
	[cr.openchoreo.dev]=https://cr.openchoreo.dev
	[cr.kgateway.dev]=https://cr.kgateway.dev
	[o2cr.ai]=https://o2cr.ai
)

name_of() { echo "$PREFIX-${1//./-}"; }

up() {
	docker network inspect $NETWORK >/dev/null 2>&1 || docker network create $NETWORK >/dev/null
	local host n
	for host in "${!UPSTREAMS[@]}"; do
		n=$(name_of "$host")
		if [ "$(docker inspect -f '{{.State.Running}}' "$n" 2>/dev/null)" = "true" ]; then
			continue
		fi
		docker rm -f "$n" >/dev/null 2>&1 || true
		docker run -d --name "$n" --restart unless-stopped --network $NETWORK \
			-e REGISTRY_PROXY_REMOTEURL="${UPSTREAMS[$host]}" \
			-e REGISTRY_STORAGE_DELETE_ENABLED=true \
			-v "$n:/var/lib/registry" \
			--memory 128m \
			$CACHE_IMAGE >/dev/null
		echo "started $n -> ${UPSTREAMS[$host]}"
	done
}

# wire CLUSTER: write containerd hosts.toml files into every node of the
# kind cluster. Needs the node's containerd config_path set to
# /etc/containerd/certs.d (kind-t470.yaml does), which containerd reads
# per pull, so no restart is needed.
wire() {
	local cluster=${1:?cluster name} node host n
	for node in $(kind get nodes --name "$cluster"); do
		for host in "${!UPSTREAMS[@]}"; do
			n=$(name_of "$host")
			docker exec "$node" mkdir -p "/etc/containerd/certs.d/$host"
			printf 'server = "%s"\n\n[host."http://%s:5000"]\n  capabilities = ["pull", "resolve"]\n' \
				"${UPSTREAMS[$host]}" "$n" | docker exec -i "$node" tee "/etc/containerd/certs.d/$host/hosts.toml" >/dev/null
		done
		echo "wired $node to ${#UPSTREAMS[@]} caches"
	done
}

running() {
	local host
	for host in "${!UPSTREAMS[@]}"; do
		[ "$(docker inspect -f '{{.State.Running}}' "$(name_of "$host")" 2>/dev/null)" = "true" ] || return 1
	done
}

status() {
	local host n
	for host in "${!UPSTREAMS[@]}"; do
		n=$(name_of "$host")
		printf '%-20s %-8s %s\n' "$host" "$(docker inspect -f '{{.State.Status}}' "$n" 2>/dev/null || echo absent)" \
			"$(docker exec "$n" du -sh /var/lib/registry 2>/dev/null | cut -f1)"
	done
}

down() {
	local host n
	for host in "${!UPSTREAMS[@]}"; do
		n=$(name_of "$host")
		docker rm -f "$n" >/dev/null 2>&1 || true
		if [ "${1:-}" = "--purge" ]; then docker volume rm "$n" >/dev/null 2>&1 || true; fi
	done
	echo "caches stopped${1:+ and purged}"
}

case "${1:-}" in
up) up ;;
wire) wire "${2:-}" ;;
running) running ;;
status) status ;;
down) down "${2:-}" ;;
*) sed -n '2,19p' "$0"; exit 2 ;;
esac
