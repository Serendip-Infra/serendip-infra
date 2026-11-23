package openchoreo

import (
	_ "embed"
	"fmt"
	"slices"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
)

// The charts this package installs, pinned in VERSIONS.md, in plan
// decision 1's install order (as amended by ADR 0021).
const (
	ocCharts  = "oci://ghcr.io/openchoreo/helm-charts"
	modCharts = "oci://cr.openchoreo.dev/openchoreo/helm-charts"

	// PlaneNamespace holds every release and the stores' volumes.
	PlaneNamespace = "openchoreo-observability-plane"

	planeRelease      = "openchoreo-observability-plane"
	logsRelease       = "observability-logs-openobserve"
	prometheusRelease = "prometheus"
	metricsRelease    = "observability-metrics-prometheus"

	helmInstallTimeout   = "15m" // the first start pulls every image (T003 finding 3)
	helmUninstallTimeout = "5m"

	// The logs store's admin user. Its password is generated once and
	// kept only in adminSecretName (plan decision 1.2).
	adminSecretName = "openobserve-admin-credentials"
	adminEmail      = "admin@serendip.local"
	// observerSecretName holds the placeholder the plane chart requires
	// even with security off (spike finding 2).
	observerSecretName = "observer-secret"

	// Retention and volume sizes (plan decision 8). The logs module's
	// own default retention is 3650 days, so it's always set.
	logRetentionDays = 3
	logVolumeSize    = "5Gi"

	// fluentBitInstance names this cluster in Fluent Bit's records.
	fluentBitInstance = "serendip"
)

// prometheusValues is plain Prometheus' values (ADR 0021), shaped so the
// metrics module's adapter reads it as it would kube-prometheus-stack
// (RP-056). It's the bench's prometheus-plain-values.yaml, kept identical
// by a test, so a measured footprint and the product use the same values.
//
//go:embed prometheus-values.yaml
var prometheusValues []byte

// valuesFileArg stands in Helm's arguments for the file prometheusValues
// is written to at run time, so the arguments (and the fingerprint) don't
// depend on a temporary path.
const valuesFileArg = "<prometheus-values.yaml>"

// release is one Helm release this package installs.
type release struct {
	name, chart, version string
	// what names it in messages for people.
	what string
	// sets are --set flags; strings are --set-string flags.
	sets, strings []string
	// valuesFile passes prometheusValues with -f.
	valuesFile bool
	// noWait leaves out Helm's --wait: the installer checks readiness
	// itself, so a pod still crash-looping past Helm's timeout doesn't
	// mark the release failed (T001's first tracing install).
	noWait bool
}

// releases are installed in this order and removed in the reverse.
var releases = []release{
	{
		name: planeRelease, chart: ocCharts + "/openchoreo-observability-plane", version: "1.3.1",
		what: "the observability plane",
		sets: []string{
			// The Observer's own auth stays off; NetworkPolicies close the
			// plane, and Serendip's RBAC is the gate (plan decision 4).
			"security.enabled=false",
			// edge-1 has its own gateway, and nothing outside the cluster
			// reaches the plane.
			"gateway.enabled=false",
			"observer.controlPlaneApiUrl=http://openchoreo-api.openchoreo-control-plane:8080",
			"observer.secretName=" + observerSecretName,
			// The chart refuses its default .invalid URL (spike finding 2).
			"observer.extraEnvs[0].name=OBSERVER_BASE_URL",
			"observer.extraEnvs[0].value=" + observerURL,
			"observer.extraEnvs[1].name=AUTHZ_TIMEOUT",
			"observer.extraEnvs[1].value=30s",
		},
	},
	{
		name: logsRelease, chart: modCharts + "/observability-logs-openobserve", version: "0.8.0",
		what: "the logs store",
		sets: []string{
			"fluentBitCustomizations.clusterInstance=" + fluentBitInstance,
			"openobserve-standalone.persistence.size=" + logVolumeSize,
			// Off the host network, so the plane's NetworkPolicies cover
			// Fluent Bit's :2020 and its flushes come from a pod in the
			// namespace (RP-064). Its config is Serendip's (fluentbit.go).
			"fluent-bit.hostNetwork=false",
			"fluent-bit.dnsPolicy=ClusterFirst",
			"fluent-bit.existingConfigMap=" + fluentBitConfigName,
		},
		strings: []string{
			fmt.Sprintf("openobserve-standalone.config.ZO_COMPACT_DATA_RETENTION_DAYS=%d", logRetentionDays),
			// On by default: a heartbeat to the vendor every 30 minutes.
			// Nothing leaves the machine unless asked (T003).
			"openobserve-standalone.config.ZO_TELEMETRY=false",
			// Also on by default, even with telemetry off (seen live in
			// T006): GeoLite databases from the vendor (60 MB, refreshed
			// every 30 days) and LLM pricing from GitHub at start.
			"openobserve-standalone.config.ZO_MMDB_DISABLE_DOWNLOAD=true",
			// Not a key the chart's config renders, so it goes in as an
			// extra variable (checked live). A third variable takes index 2.
			"openobserve-standalone.extraEnv[0].name=ZO_MODEL_PRICING_ENABLED",
			"openobserve-standalone.extraEnv[0].value=false",
			// OpenObserve's SSRF guard refuses alert destinations at
			// private addresses, so on a fresh store the chart's setup Job
			// can't create its in-cluster webhook (logs-adapter, a
			// ClusterIP) and the install never finishes (T012, RP-062).
			// The module's newer releases set the same. At v0.92.2 it
			// skips the create-time check entirely; only Serendip and the
			// chart can reach OpenObserve's API (plan decision 4).
			"openobserve-standalone.extraEnv[1].name=ZO_SSRF_ALLOW_LOOPBACK",
			"openobserve-standalone.extraEnv[1].value=true",
			// A stream's deletion (the tracing package's removal) is done
			// by the retention job, hourly by default; until it runs the
			// stream can't be searched and spans written to it are lost
			// (spec 025 T007, live). Every minute it's done within one.
			"openobserve-standalone.extraEnv[2].name=ZO_COMPACT_DATA_RETENTION_INTERVAL",
			"openobserve-standalone.extraEnv[2].value=60",
		},
	},
	{
		name: prometheusRelease, chart: "oci://ghcr.io/prometheus-community/charts/prometheus", version: "29.47.2",
		what: "the metrics store", valuesFile: true,
	},
	{
		name: metricsRelease, chart: modCharts + "/observability-metrics-prometheus", version: "0.9.3",
		what: "the metrics adapter",
		// Only its adapter: Prometheus is the release above, so no
		// Prometheus Operator and none of its CRDs (ADR 0021).
		sets: []string{"kube-prometheus-stack.enabled=false"},
	},
}

// observerURL is the Observer's in-cluster address, which the plane
// object and the Observer itself are given.
const observerURL = "http://observer." + PlaneNamespace + ":8080"

// installArgs is the Helm command that installs or upgrades r, with
// valuesFileArg where the values file goes.
func (r release) installArgs() []string {
	args := []string{
		"upgrade", "--install", r.name, r.chart, "--version", r.version,
		"--namespace", PlaneNamespace, "--create-namespace",
	}
	if r.valuesFile {
		args = append(args, "-f", valuesFileArg)
	}
	for _, s := range r.sets {
		args = append(args, "--set", s)
	}
	for _, s := range r.strings {
		args = append(args, "--set-string", s)
	}
	if r.noWait {
		return args
	}
	return append(args, "--wait", "--timeout", helmInstallTimeout)
}

// releasesFor are releases as installed with src: the logs release gets
// Fluent Bit's config hash and, with src set, the records' directory as a
// read-only volume (fluentbit.go).
func releasesFor(src reconcile.FlowSource) []release {
	out := slices.Clone(releases)
	for k, r := range out {
		if r.name != logsRelease {
			continue
		}
		// Hex may be all digits, hence a string.
		r.strings = append([]string{"fluent-bit.podAnnotations.serendip-fluent-bit-config=" + fluentBitConfigHash(src)}, r.strings...)
		if !src.IsZero() {
			// Helm replaces a list rather than merging it, so the chart's
			// own entry 0 (Fluent Bit's tail database, which its init
			// container mounts) is restated as the chart has it at 0.8.0
			// (seen live in T006: without it the upgrade is refused).
			r.sets = append(slices.Clone(r.sets),
				"fluent-bit.extraVolumes[0].name=db",
				"fluent-bit.extraVolumes[0].hostPath.path=/var/lib/fluent-bit/db",
				"fluent-bit.extraVolumes[0].hostPath.type=DirectoryOrCreate",
				"fluent-bit.extraVolumes[1].name="+flowVolume,
				"fluent-bit.extraVolumes[1].hostPath.path="+src.HostPath,
				"fluent-bit.extraVolumes[1].hostPath.type=DirectoryOrCreate",
				"fluent-bit.extraVolumeMounts[0].name=db",
				"fluent-bit.extraVolumeMounts[0].mountPath=/var/lib/fluent-bit/db",
				"fluent-bit.extraVolumeMounts[0].readOnly=false",
				"fluent-bit.extraVolumeMounts[1].name="+flowVolume,
				"fluent-bit.extraVolumeMounts[1].mountPath="+flowMountPath,
				"fluent-bit.extraVolumeMounts[1].readOnly=true",
			)
		}
		out[k] = r
	}
	return out
}

func releaseNamed(src reconcile.FlowSource, name string) (release, bool) {
	for _, r := range releasesFor(src) {
		if r.name == name {
			return r, true
		}
	}
	return release{}, false
}
