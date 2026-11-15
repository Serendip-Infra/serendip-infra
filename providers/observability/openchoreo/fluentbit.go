package openchoreo

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// Fluent Bit's configuration (RP-064). The logs module runs Fluent Bit on
// the host network by default, where the plane's NetworkPolicies don't
// apply and its HTTP server (:2020) answers every pod on the node. Off the
// host network, the chart's hard-coded `Use_Kubelet true` would look pods
// up at the pod's own 127.0.0.1, and records would lose the UIDs the
// queries are scoped by. So Serendip applies its own copy of the chart's
// ConfigMap, the chart's render with the kubernetes filter reading the API
// server instead (the chart's RBAC already allows it), and points the
// chart at it with existingConfigMap. The chart still renders its own,
// unused.
const fluentBitConfigName = "serendip-fluent-bit"

// The chart's render of its Fluent Bit ConfigMap at the logs module's
// version in VERSIONS.md, verbatim, with clusterInstance=serendip
// (fluentBitInstance). Re-render on every upgrade:
//
//	helm template x <logs chart> --set fluentBitCustomizations.clusterInstance=serendip \
//	  --show-only templates/fluent-bit/config.yaml
var (
	//go:embed fluent-bit/fluent-bit.conf
	chartFluentBitConf string
	//go:embed fluent-bit/parsers.conf
	chartParsersConf string
)

// fluentBitEdits are the only changes to the chart's render. Pod metadata
// comes from the API server, whose certificate is verified (the kubelet's
// is self-signed, hence the chart's tls.verify Off); the filter's defaults
// find the service account's token and CA.
var fluentBitEdits = []struct{ old, new string }{
	{"    tls.verify Off\n    Use_Kubelet true\n", "    tls.verify On\n    Use_Kubelet false\n"},
}

// fluentBitConf is the chart's fluent-bit.conf with fluentBitEdits made,
// each of which must match exactly once, or a chart upgrade has moved
// what it edits.
var fluentBitConf = sync.OnceValues(func() (string, error) { return editFluentBitConf(chartFluentBitConf) })

func editFluentBitConf(conf string) (string, error) {
	for _, e := range fluentBitEdits {
		if n := strings.Count(conf, e.old); n != 1 {
			return "", fmt.Errorf("the logs chart's Fluent Bit config has %d copies of %q, want 1; re-check fluentBitEdits", n, e.old)
		}
		conf = strings.Replace(conf, e.old, e.new, 1)
	}
	return conf, nil
}

// fluentBitConfig is the ConfigMap the chart's Fluent Bit mounts.
func fluentBitConfig() kubernetes.Object {
	conf, _ := fluentBitConf() // its error stops Install before this is applied
	return kubernetes.Object{
		APIVersion: "v1", Kind: "ConfigMap",
		Metadata: kubernetes.ObjectMeta{Name: fluentBitConfigName, Namespace: PlaneNamespace, Labels: managedBy},
		Data:     mustJSON(map[string]string{"fluent-bit.conf": conf, "parsers.conf": chartParsersConf}),
	}
}

// fluentBitConfigHash goes in Fluent Bit's pod annotations, so a changed
// configuration changes the pod template and Helm rolls the DaemonSet:
// Fluent Bit reads its config only at start, and the chart's own checksum
// covers only the ConfigMap it renders.
func fluentBitConfigHash() string {
	sum := sha256.Sum256(fluentBitConfig().Data)
	return hex.EncodeToString(sum[:8])
}

const configMapsPath = "/api/v1/namespaces/" + PlaneNamespace + "/configmaps"
