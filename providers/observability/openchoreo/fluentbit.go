package openchoreo

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
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
	if n := strings.Count(conf, flowParsersAfter); n != 1 {
		return "", fmt.Errorf("the logs chart's Fluent Bit config has %d copies of %q, want 1; re-check flowParsersAfter", n, flowParsersAfter)
	}
	for _, e := range fluentBitEdits {
		if n := strings.Count(conf, e.old); n != 1 {
			return "", fmt.Errorf("the logs chart's Fluent Bit config has %d copies of %q, want 1; re-check fluentBitEdits", n, e.old)
		}
		conf = strings.Replace(conf, e.old, e.new, 1)
	}
	return conf, nil
}

// fluentBitConfig is the ConfigMap the chart's Fluent Bit mounts: the
// chart's config with fluentBitEdits, and with src set, the connection
// records' pipeline (flowPipeline).
func fluentBitConfig(src reconcile.FlowSource) kubernetes.Object {
	conf, _ := fluentBitConf() // its error stops Install before this is applied
	data := map[string]string{"fluent-bit.conf": conf, "parsers.conf": chartParsersConf}
	if !src.IsZero() {
		// The chart starts Fluent Bit with --workdir=/fluent-bit/etc, so
		// its "Parsers_File parsers.conf" is the image's own file, not
		// this ConfigMap's (seen live in T006). The flow parser gets its
		// own file, added by absolute path; the logs' parsers don't change.
		data["fluent-bit.conf"] = strings.Replace(conf, flowParsersAfter,
			flowParsersAfter+"    Parsers_File "+fluentBitConfDir+"/"+flowParsersKey+"\n", 1) + flowPipeline(src)
		data[flowParsersKey] = flowParser
		data[flowScriptKey] = flowScript
	}
	return kubernetes.Object{
		APIVersion: "v1", Kind: "ConfigMap",
		Metadata: kubernetes.ObjectMeta{Name: fluentBitConfigName, Namespace: PlaneNamespace, Labels: managedBy},
		Data:     mustJSON(data),
	}
}

// fluentBitConfigHash goes in Fluent Bit's pod annotations, so a changed
// configuration changes the pod template and Helm rolls the DaemonSet:
// Fluent Bit reads its config only at start, and the chart's own checksum
// covers only the ConfigMap it renders.
func fluentBitConfigHash(src reconcile.FlowSource) string {
	sum := sha256.Sum256(fluentBitConfig(src).Data)
	return hex.EncodeToString(sum[:8])
}

// The network layer's connection records (specs/025-networking plan
// decision 9). They're a file on each node (reconcile.FlowSource); the
// logs chart's Fluent Bit mounts its directory read-only (releasesFor)
// and tails it into its own OpenObserve stream. A Lua filter flattens
// each record into the fields flows.go reads: Hubble's ends carry their
// workloads as a list, which OpenObserve would store as a JSON string,
// and the time stored is the record's own, not when it was read.
const (
	// FlowFormat is the one record format the pipeline parses: Hubble's
	// exporter's JSON lines with the field mask and aggregation the
	// networking provider pins (its FlowFormat has the same value).
	FlowFormat = "hubble-export-json"

	flowStream     = "serendip_flows"
	flowVolume     = "serendip-flows"
	flowMountPath  = "/serendip/flows"
	flowScriptKey  = "serendip-flows.lua"
	flowParsersKey = "serendip-parsers.conf"
	// flowParsersAfter is the chart's parsers line in [SERVICE], which
	// the flow parser's file is added after.
	flowParsersAfter = "    Parsers_File  parsers.conf\n"
	flowParserName   = "serendip_hubble_flow"
	// fluentBitConfDir is where the chart mounts the ConfigMap.
	fluentBitConfDir = "/fluent-bit/etc/conf"
)

// flowPathRE and flowFileRE are what a FlowSource's directory and file
// name may be: they go into Fluent Bit's config and Helm's --set, where a
// comma, a space or a newline would change their meaning.
var (
	flowPathRE = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`)
	flowFileRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// checkFlowSource refuses a source this package can't collect.
func checkFlowSource(src reconcile.FlowSource) error {
	if src.IsZero() {
		return nil
	}
	switch {
	case src.Format != FlowFormat:
		return fmt.Errorf("the connection records' format %q isn't one this package reads", src.Format)
	case !flowPathRE.MatchString(src.HostPath) || slices.ContainsFunc(strings.Split(src.HostPath, "/"), func(e string) bool { return e == "." || e == ".." }):
		return errors.New("the connection records' directory isn't a plain absolute path")
	case !flowFileRE.MatchString(src.File) || src.File == "." || src.File == "..":
		return errors.New("the connection records' file name isn't a plain name")
	}
	return nil
}

// flowPipeline is the tail input, the filter and the output for src. Its
// tag isn't kube.*, so the chart's filters and output leave it alone.
func flowPipeline(src reconcile.FlowSource) string {
	return `
# Spec 025: the network layer's connection records (Serendip's own).
[INPUT]
    Name tail
    Tag serendip.flows
    Path ` + flowMountPath + "/" + src.File + `
    DB /var/lib/fluent-bit/db/tail-serendip-flows.db
    Parser ` + flowParserName + `
    Buffer_Chunk_Size 32KB
    Buffer_Max_Size 1MB
    Mem_Buf_Limit 10MB
    Inotify_Watcher false
    Read_from_Head On
    Refresh_Interval 5
    Rotate_Wait 10
    Skip_Long_Lines On

[FILTER]
    Name lua
    Match serendip.flows
    script ` + fluentBitConfDir + "/" + flowScriptKey + `
    call serendip_flow

[OUTPUT]
    Name http
    Match serendip.flows
    Host ` + logsStoreService + `
    Port ` + strconv.Itoa(logsStorePort) + `
    URI /api/` + logsStoreOrg + "/" + flowStream + `/_json
    Format json
    json_date_key false
    http_user ${OPENOBSERVE_USERNAME}
    http_passwd ${OPENOBSERVE_PASSWORD}
    tls Off
`
}

// flowParser reads a record's own time, which Hubble writes as RFC 3339
// in UTC with nanoseconds.
const flowParser = `[PARSER]
    Name ` + flowParserName + `
    Format json
    Time_Key time
    Time_Format %Y-%m-%dT%H:%M:%S.%LZ
    Time_Keep Off
`

// flowScript flattens one record. It drops what isn't a flow record
// (Hubble also writes lost-event notices), and the L7 proxy's own
// upstream leg: a forwarded record with a cluster endpoint's identity but
// no namespace, which the client's own record already counts (seen live
// on t025 in T006). Identities below 256 are reserved (host, world, ...)
// and from 2^24 local CIDR ones; between them are cluster endpoints.
//
//go:embed fluent-bit/serendip-flows.lua
var flowScript string

const configMapsPath = "/api/v1/namespaces/" + PlaneNamespace + "/configmaps"
