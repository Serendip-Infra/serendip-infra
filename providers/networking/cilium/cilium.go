// Package cilium is the networking provider for a cluster whose CNI is
// Cilium (specs/025-networking plan decision 4). It reads what Cilium runs
// and how it's configured, reports drift from the values Serendip pins
// (values.yaml), names where Hubble writes its flow records, and writes one
// thing: per-workload CiliumNetworkPolicies that turn on request
// visibility (visibility.go). It never installs, upgrades or reconfigures
// Cilium itself; that's the install path's job (decision 5).
package cilium

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// The chart Serendip installs (VERSIONS.md).
const (
	ChartName    = "cilium"
	ChartRepo    = "https://helm.cilium.io"
	ChartVersion = "1.20.5"
)

//go:embed values.yaml
var values []byte

// Values returns the Helm values Serendip pins for Cilium, as YAML.
func Values() []byte { return append([]byte(nil), values...) }

// minMinor is the oldest Cilium minor whose flow export Serendip reads:
// fieldAggregate and aggregationInterval were checked at 1.20.5 only.
const minMinor = 20

// The pinned settings the provider checks, as cilium-config holds them
// (values.yaml, rendered by the chart; TestPinnedMatchValues keeps the two
// in step).
const (
	pinnedHTTPMetric = "httpV2:labelsContext=source_namespace,source_pod,destination_namespace,destination_pod"
	pinnedExportPath = "/var/run/cilium/hubble/events.log"
	pinnedFieldMask  = "time verdict drop_reason_desc traffic_direction source.namespace source.workloads source.identity destination.namespace destination.workloads destination.identity l4"
	pinnedFieldAgg   = "verdict drop_reason_desc traffic_direction source.namespace source.workloads source.identity destination.namespace destination.workloads destination.identity l4.TCP.destination_port l4.UDP.destination_port"
	pinnedInterval   = "60s"
	pinnedAllowList  = `{"verdict":["DROPPED"],"source_label":["k8s:openchoreo.dev/component-uid"]} ` +
		`{"verdict":["DROPPED"],"destination_label":["k8s:openchoreo.dev/component-uid"]} ` +
		`{"verdict":["FORWARDED"],"tcp_flags":[{"SYN":true}],"reply":[false],"source_label":["k8s:openchoreo.dev/component-uid"]} ` +
		`{"verdict":["FORWARDED"],"tcp_flags":[{"SYN":true}],"reply":[false],"destination_label":["k8s:openchoreo.dev/component-uid"]} ` +
		`{"verdict":["FORWARDED"],"protocol":["UDP"],"reply":[false],"source_label":["k8s:openchoreo.dev/component-uid"]} ` +
		`{"verdict":["FORWARDED"],"protocol":["UDP"],"reply":[false],"destination_label":["k8s:openchoreo.dev/component-uid"]}`
	pinnedDenyList      = `{"destination_port":["53"]} {"source_port":["53"]} {"protocol":["ICMPv6"]}`
	pinnedRetryCount    = "0"
	pinnedNormalizePath = "false"
)

// FlowFormat names the record format FlowSource points at: Hubble's
// exporter's JSON lines, masked and aggregated as values.yaml sets.
const FlowFormat = "hubble-export-json"

// Client is what the provider calls; *kubernetes.Client has all three.
type Client interface {
	GetRaw(ctx context.Context, path string) (json.RawMessage, error)
	Apply(ctx context.Context, path string, obj kubernetes.Object, fieldManager string) (kubernetes.Object, error)
	Delete(ctx context.Context, path string) error
}

// Provider implements reconcile.NetworkingProvider for Cilium.
type Provider struct {
	client Client
	now    func() time.Time

	mu   sync.Mutex
	last *Report // the last visibility pass, for Facts
}

var _ reconcile.NetworkingProvider = (*Provider)(nil)

// New returns a provider reading and writing through client.
func New(client Client) *Provider {
	return &Provider{client: client, now: time.Now}
}

const (
	configPath    = "/api/v1/namespaces/kube-system/configmaps/cilium-config"
	daemonSetPath = "/apis/apps/v1/namespaces/kube-system/daemonsets/cilium"
	nodesPath     = "/api/v1/nodes"
	ciliumNodes   = "/apis/cilium.io/v2/ciliumnodes"
)

// Facts reports Cilium's version, whether policies hold, whether flows and
// request metrics can be read, and the pod ranges. What could be read is
// returned with the error of what couldn't, and Unavailable names it.
func (p *Provider) Facts(ctx context.Context) (reconcile.NetworkFacts, error) {
	f := reconcile.NetworkFacts{
		Plugin:            "cilium",
		PolicyEnforcement: reconcile.PolicyUnknown,
		PolicyReason:      "Cilium's configuration can't be read",
		ReadAt:            p.now().UTC(),
	}
	var errs []error

	ds, dsErr := p.daemonSet(ctx)
	if dsErr != nil {
		errs = append(errs, dsErr)
		f.Unavailable = append(f.Unavailable, "version: the cilium DaemonSet can't be read")
	} else {
		f.Version = ds.version()
		if ds.Status.DesiredNumberScheduled > 0 && ds.Status.NumberReady < ds.Status.DesiredNumberScheduled {
			f.Unavailable = append(f.Unavailable, fmt.Sprintf("agent: Cilium is ready on %d of %d nodes; policies and records are missing where it isn't",
				ds.Status.NumberReady, ds.Status.DesiredNumberScheduled))
		}
	}

	cfg, cfgErr := p.config(ctx)
	if cfgErr != nil {
		errs = append(errs, cfgErr)
		f.Unavailable = append(f.Unavailable,
			"flows: Cilium's configuration can't be read",
			"requests: Cilium's configuration can't be read")
	} else {
		f.PolicyEnforcement, f.PolicyReason = enforcement(cfg)
		var notes []string
		f.Flows, notes = flowsReadable(cfg, f.Version, dsErr == nil)
		f.Unavailable = append(f.Unavailable, notes...)
		f.RequestMetrics, notes = requestsReadable(cfg)
		f.Unavailable = append(f.Unavailable, notes...)
		f.Unavailable = append(f.Unavailable, otherDrift(cfg)...)
	}

	ranges, notes, err := p.podRanges(ctx, cfg)
	f.PodRanges = ranges
	f.Unavailable = append(f.Unavailable, notes...)
	if err != nil {
		errs = append(errs, err)
	}

	if f.RequestMetrics {
		f.Unavailable = append(f.Unavailable, p.visibilityNotes()...)
	}
	return f, errors.Join(errs...)
}

// visibilityNotes says what the last visibility pass left without request
// metrics: until one has run, or where it skipped or failed, the metrics
// Cilium could make aren't being made.
func (p *Provider) visibilityNotes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.last
	if r == nil {
		return []string{"requests: Serendip hasn't routed any Component's requests through Cilium's proxy yet"}
	}
	var notes []string
	if len(r.Skipped) > 0 {
		notes = append(notes, fmt.Sprintf(
			"requests: %d workloads' network policies can't be mirrored, so they have no request metrics", len(r.Skipped)))
	}
	if len(r.Failed) > 0 {
		notes = append(notes, fmt.Sprintf(
			"requests: the last visibility pass failed %d times, so some Components may have no request metrics", len(r.Failed)))
	}
	return notes
}

// FlowSource says where Hubble writes its flow records on each node, or
// the zero value when they aren't written the way Serendip reads them.
func (p *Provider) FlowSource(ctx context.Context) (reconcile.FlowSource, error) {
	cfg, err := p.config(ctx)
	if err != nil {
		return reconcile.FlowSource{}, err
	}
	ds, err := p.daemonSet(ctx)
	if err != nil {
		return reconcile.FlowSource{}, err
	}
	if ok, _ := flowsReadable(cfg, ds.version(), true); !ok {
		return reconcile.FlowSource{}, nil
	}
	file := cfg["hubble-export-file-path"]
	return reconcile.FlowSource{HostPath: path.Dir(file), File: path.Base(file), Format: FlowFormat}, nil
}

type ciliumConfig map[string]string

func (p *Provider) config(ctx context.Context) (ciliumConfig, error) {
	raw, err := p.client.GetRaw(ctx, configPath)
	if err != nil {
		return nil, fmt.Errorf("read cilium-config: %w", err)
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(raw, &cm); err != nil {
		return nil, fmt.Errorf("decode cilium-config: %w", err)
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	return cm.Data, nil
}

type daemonSet struct {
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
	Status struct {
		DesiredNumberScheduled int `json:"desiredNumberScheduled"`
		NumberReady            int `json:"numberReady"`
	} `json:"status"`
}

func (p *Provider) daemonSet(ctx context.Context) (daemonSet, error) {
	var ds daemonSet
	raw, err := p.client.GetRaw(ctx, daemonSetPath)
	if err != nil {
		return ds, fmt.Errorf("read the cilium DaemonSet: %w", err)
	}
	if err := json.Unmarshal(raw, &ds); err != nil {
		return ds, fmt.Errorf("decode the cilium DaemonSet: %w", err)
	}
	return ds, nil
}

// version is the agent container's image tag without its "v", or "".
func (ds daemonSet) version() string {
	for _, c := range ds.Spec.Template.Spec.Containers {
		if c.Name != "cilium-agent" && len(ds.Spec.Template.Spec.Containers) > 1 {
			continue
		}
		image, _, _ := strings.Cut(c.Image, "@")
		slash := strings.LastIndex(image, "/")
		colon := strings.LastIndex(image, ":")
		if colon <= slash {
			return ""
		}
		return strings.TrimPrefix(image[colon+1:], "v")
	}
	return ""
}

// minor reads the minor release from a "1.20.5" version.
func minor(version string) (int, bool) {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 || parts[0] != "1" {
		return 0, false
	}
	m, err := strconv.Atoi(parts[1])
	return m, err == nil
}

// enforcement reads Cilium's policy mode. "default" and "always" enforce;
// "never" doesn't, and neither does a turned-off NetworkPolicy watcher.
func enforcement(cfg ciliumConfig) (reconcile.PolicyEnforcement, string) {
	if cfg["enable-k8s-networkpolicy"] == "false" {
		return reconcile.PolicyNotEnforced, "Cilium was configured to ignore Kubernetes network policies"
	}
	switch mode := cfg["enable-policy"]; mode {
	case "", "default", "always":
		return reconcile.PolicyEnforced, "Cilium's own policy enforcement"
	case "never":
		return reconcile.PolicyNotEnforced, "Cilium was configured never to enforce network policies"
	default:
		return reconcile.PolicyUnknown, fmt.Sprintf("Cilium's policy mode %q isn't one Serendip knows", mode)
	}
}

// flowsReadable says whether Hubble writes its records the way Serendip
// reads them, and why not. versionKnown false means the version couldn't
// be read, which isn't held against the export (Facts names it already).
func flowsReadable(cfg ciliumConfig, version string, versionKnown bool) (bool, []string) {
	switch {
	case cfg["enable-hubble"] != "true":
		return false, []string{"flows: Hubble is turned off in Cilium's configuration"}
	case cfg["hubble-export-file-path"] == "":
		return false, []string{"flows: Hubble's flow export is turned off in Cilium's configuration"}
	case cfg["hubble-export-fieldmask"] != pinnedFieldMask || cfg["hubble-export-fieldaggregate"] != pinnedFieldAgg:
		return false, []string{"flows: Hubble's flow export records other fields than Serendip reads; set the values Serendip pins"}
	}
	if versionKnown {
		if m, ok := minor(version); !ok || m < minMinor {
			return false, []string{fmt.Sprintf("flows: Serendip reads the flow export from Cilium 1.%d on; this cluster runs %q", minMinor, version)}
		}
	}
	var notes []string
	if cfg["hubble-export-file-path"] != pinnedExportPath {
		notes = append(notes, "flows: Hubble writes its records to another file than Serendip pins; the collector must be pointed at it")
	}
	if cfg["hubble-export-aggregation-interval"] != pinnedInterval ||
		cfg["hubble-export-allowlist"] != pinnedAllowList || cfg["hubble-export-denylist"] != pinnedDenyList {
		notes = append(notes, "flows: Hubble's export filters differ from Serendip's, so the records' volume and coverage may differ")
	}
	return true, notes
}

// requestsReadable says whether Hubble makes the HTTP metrics Serendip
// reads, and whether the L7 proxy is set not to change requests. A proxy
// that retries failures hides them, so then there are no request metrics
// (and the visibility pass removes its policies, visibility.go).
func requestsReadable(cfg ciliumConfig) (bool, []string) {
	switch {
	case cfg["enable-hubble"] != "true":
		return false, []string{"requests: Hubble is turned off in Cilium's configuration"}
	case !hasField(cfg["hubble-metrics"], pinnedHTTPMetric):
		return false, []string{"requests: Hubble doesn't make the HTTP metrics Serendip reads (the httpV2 metric with pod labels)"}
	case cfg["enable-l7-proxy"] == "false":
		return false, []string{"requests: Cilium's L7 proxy is turned off"}
	}
	if reason := proxyDrift(cfg); reason != "" {
		return false, []string{"requests: " + reason}
	}
	return true, nil
}

// proxyDrift says how the L7 proxy would change requests, or "". Unset
// means Cilium's default, which retries 5xx 3 times and rewrites paths.
func proxyDrift(cfg ciliumConfig) string {
	var what []string
	if cfg["http-retry-count"] != pinnedRetryCount {
		what = append(what, "retries failed requests (http-retry-count)")
	}
	if cfg["http-normalize-path"] != pinnedNormalizePath {
		what = append(what, "rewrites request paths (http-normalize-path)")
	}
	if len(what) == 0 {
		return ""
	}
	return "Cilium's L7 proxy " + strings.Join(what, " and ") + ", so Serendip doesn't route requests through it"
}

// otherDrift names pinned settings that don't change what's readable.
func otherDrift(cfg ciliumConfig) []string {
	var notes []string
	if v := cfg["kube-proxy-replacement"]; v != "" && v != "false" {
		notes = append(notes, "kube-proxy replacement: on, which Serendip hasn't measured (it pins it off)")
	}
	return notes
}

func hasField(list, want string) bool {
	for _, f := range strings.Fields(list) {
		if f == want {
			return true
		}
	}
	return false
}

// podRanges reads the pod ranges: the nodes' podCIDRs under IPAM
// "kubernetes" (or when the mode can't be read), else the CiliumNodes'
// allocations. It returns notes for what it couldn't read.
func (p *Provider) podRanges(ctx context.Context, cfg ciliumConfig) ([]netip.Prefix, []string, error) {
	var ranges []string
	mode := cfg["ipam"]
	if cfg == nil || mode == "kubernetes" {
		raw, err := p.client.GetRaw(ctx, nodesPath)
		if err != nil {
			return nil, []string{"pod ranges: the nodes can't be read"}, fmt.Errorf("list nodes: %w", err)
		}
		var list struct {
			Items []struct {
				Spec struct {
					PodCIDR  string   `json:"podCIDR"`
					PodCIDRs []string `json:"podCIDRs"`
				} `json:"spec"`
			} `json:"items"`
		}
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, []string{"pod ranges: the nodes can't be read"}, fmt.Errorf("decode nodes: %w", err)
		}
		for _, n := range list.Items {
			ranges = append(ranges, n.Spec.PodCIDR)
			ranges = append(ranges, n.Spec.PodCIDRs...)
		}
	} else {
		raw, err := p.client.GetRaw(ctx, ciliumNodes)
		if err != nil {
			return nil, []string{"pod ranges: Cilium's node allocations can't be read"}, fmt.Errorf("list CiliumNodes: %w", err)
		}
		var list struct {
			Items []struct {
				Spec struct {
					IPAM struct {
						PodCIDRs []string `json:"podCIDRs"`
						Pools    struct {
							Allocated []struct {
								CIDRs []string `json:"cidrs"`
							} `json:"allocated"`
						} `json:"pools"`
					} `json:"ipam"`
				} `json:"spec"`
			} `json:"items"`
		}
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, []string{"pod ranges: Cilium's node allocations can't be read"}, fmt.Errorf("decode CiliumNodes: %w", err)
		}
		for _, n := range list.Items {
			ranges = append(ranges, n.Spec.IPAM.PodCIDRs...)
			for _, a := range n.Spec.IPAM.Pools.Allocated {
				ranges = append(ranges, a.CIDRs...)
			}
		}
	}
	out, skipped := parseRanges(ranges)
	var notes []string
	if skipped {
		notes = append(notes, "pod ranges: a node's range doesn't parse and was left out")
	}
	if len(out) == 0 {
		notes = append(notes, "pod ranges: no node has a pod range yet")
	}
	return out, notes, nil
}

// parseRanges parses, masks, sorts and de-duplicates ranges, skipping
// empty ones and saying whether one didn't parse.
func parseRanges(ranges []string) (out []netip.Prefix, skipped bool) {
	seen := map[netip.Prefix]bool{}
	for _, s := range ranges {
		if s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			skipped = true
			continue
		}
		p = p.Masked()
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if c := out[i].Addr().Compare(out[j].Addr()); c != 0 {
			return c < 0
		}
		return out[i].Bits() < out[j].Bits()
	})
	return out, skipped
}
