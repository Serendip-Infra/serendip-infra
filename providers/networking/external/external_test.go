package external

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
)

const (
	dsPath    = "/apis/apps/v1/namespaces/kube-system/daemonsets"
	nodesPath = "/api/v1/nodes"
)

type fakeReader map[string]any // path -> JSON string or error

func (f fakeReader) GetRaw(_ context.Context, path string) (json.RawMessage, error) {
	switch v := f[path].(type) {
	case string:
		return json.RawMessage(v), nil
	case error:
		return nil, v
	}
	return nil, errors.New("unexpected path " + path)
}

// The kindnet answer as seen on serendip-t018 (kind v1.34.0), trimmed.
const kindDaemonSets = `{"items":[
 {"metadata":{"name":"kindnet"},"spec":{"template":{"spec":{"containers":[{"name":"kindnet-cni","image":"docker.io/kindest/kindnetd:v20250512-df8de77b"}]}}}},
 {"metadata":{"name":"kube-proxy"},"spec":{"template":{"spec":{"containers":[{"name":"kube-proxy","image":"registry.k8s.io/kube-proxy:v1.34.0"}]}}}}]}`

const kindNodes = `{"items":[{"metadata":{"annotations":{"node.alpha.kubernetes.io/ttl":"0"}},
 "spec":{"podCIDR":"10.244.0.0/24","podCIDRs":["10.244.0.0/24"]}}]}`

// K3s embeds Flannel: no DaemonSet, only node annotations.
const k3sDaemonSets = `{"items":[{"metadata":{"name":"svclb-traefik"},"spec":{"template":{"spec":{"containers":[{"name":"lb","image":"rancher/klipper-lb:v0.4.13"}]}}}}]}`

func k3sNodes(args string) string {
	b, _ := json.Marshal(args)
	return `{"items":[{"metadata":{"annotations":{
 "flannel.alpha.coreos.com/backend-type":"vxlan",
 "flannel.alpha.coreos.com/kube-subnet-manager":"true",
 "k3s.io/hostname":"t470",
 "k3s.io/node-args":` + string(b) + `}},
 "spec":{"podCIDR":"10.42.0.0/24","podCIDRs":["10.42.0.0/24","fd00:42::/64"]}},
 {"metadata":{"annotations":{"flannel.alpha.coreos.com/backend-type":"vxlan","k3s.io/hostname":"agent1"}},
 "spec":{"podCIDRs":["10.42.1.0/24"]}}]}`
}

func TestFacts(t *testing.T) {
	ranges := func(s ...string) []netip.Prefix {
		var out []netip.Prefix
		for _, p := range s {
			out = append(out, netip.MustParsePrefix(p))
		}
		return out
	}
	cases := []struct {
		name        string
		reader      fakeReader
		plugin      string
		version     string
		enforcement reconcile.PolicyEnforcement
		ranges      []netip.Prefix
		extra       []string // Unavailable beyond the flows and requests reasons
		wantErr     bool
	}{
		{
			name:        "kindnet",
			reader:      fakeReader{dsPath: kindDaemonSets, nodesPath: kindNodes},
			plugin:      "kindnet",
			version:     "v20250512-df8de77b",
			enforcement: reconcile.PolicyEnforced,
			ranges:      ranges("10.244.0.0/24"),
		},
		{
			name: "kindnet before network policies",
			reader: fakeReader{dsPath: `{"items":[{"metadata":{"name":"kindnet"},"spec":{"template":{"spec":{"containers":[{"image":"kindest/kindnetd:v20230511-dc714da8@sha256:abc"}]}}}}]}`,
				nodesPath: kindNodes},
			plugin:      "kindnet",
			version:     "v20230511-dc714da8",
			enforcement: reconcile.PolicyUnknown,
			ranges:      ranges("10.244.0.0/24"),
		},
		{
			name:        "flannel on K3s",
			reader:      fakeReader{dsPath: k3sDaemonSets, nodesPath: k3sNodes(`["server","--write-kubeconfig-mode","0644"]`)},
			plugin:      "flannel",
			enforcement: reconcile.PolicyUnknown,
			ranges:      ranges("10.42.0.0/24", "10.42.1.0/24", "fd00:42::/64"),
		},
		{
			name:        "flannel on K3s, policy controller off",
			reader:      fakeReader{dsPath: k3sDaemonSets, nodesPath: k3sNodes(`["server","--disable-network-policy"]`)},
			plugin:      "flannel",
			enforcement: reconcile.PolicyNotEnforced,
			ranges:      ranges("10.42.0.0/24", "10.42.1.0/24", "fd00:42::/64"),
		},
		{
			name: "kindnet with an unreadable tag",
			reader: fakeReader{dsPath: `{"items":[{"metadata":{"name":"kindnet"},"spec":{"template":{"spec":{"containers":[{"image":"kindest/kindnetd:latest"}]}}}}]}`,
				nodesPath: kindNodes},
			plugin:      "kindnet",
			version:     "latest",
			enforcement: reconcile.PolicyUnknown,
			ranges:      ranges("10.244.0.0/24"),
		},
		{
			name: "K3s policy controller off, Canal beside Flannel",
			reader: fakeReader{dsPath: `{"items":[{"metadata":{"name":"canal"},"spec":{"template":{"spec":{"containers":[{"image":"calico/node:v3.29.0"}]}}}}]}`,
				nodesPath: k3sNodes(`["server","--disable-network-policy"]`)},
			plugin:      "flannel",
			enforcement: reconcile.PolicyUnknown,
			ranges:      ranges("10.42.0.0/24", "10.42.1.0/24", "fd00:42::/64"),
		},
		{
			name:        "K3s policy controller off, DaemonSets unreadable",
			reader:      fakeReader{dsPath: errors.New("403 forbidden"), nodesPath: k3sNodes(`["server","--disable-network-policy"]`)},
			plugin:      "flannel",
			enforcement: reconcile.PolicyUnknown,
			ranges:      ranges("10.42.0.0/24", "10.42.1.0/24", "fd00:42::/64"),
			extra:       []string{"plugin: kube-system's DaemonSets can't be read"},
			wantErr:     true,
		},
		{
			name: "standalone flannel DaemonSet",
			reader: fakeReader{dsPath: `{"items":[{"metadata":{"name":"kube-flannel-ds"},"spec":{"template":{"spec":{"containers":[{"image":"ghcr.io/flannel-io/flannel:v0.26.1"}]}}}}]}`,
				nodesPath: kindNodes},
			plugin:      "flannel",
			version:     "v0.26.1",
			enforcement: reconcile.PolicyUnknown,
			ranges:      ranges("10.244.0.0/24"),
		},
		{
			name: "unknown",
			reader: fakeReader{dsPath: `{"items":[{"metadata":{"name":"calico-node"},"spec":{"template":{"spec":{"containers":[{"image":"calico/node:v3.29.0"}]}}}}]}`,
				nodesPath: kindNodes},
			plugin:      "unknown",
			enforcement: reconcile.PolicyUnknown,
			ranges:      ranges("10.244.0.0/24"),
		},
		{
			name:        "DaemonSets unreadable",
			reader:      fakeReader{dsPath: errors.New("403 forbidden"), nodesPath: kindNodes},
			plugin:      "unknown",
			enforcement: reconcile.PolicyUnknown,
			ranges:      ranges("10.244.0.0/24"),
			extra:       []string{"plugin: kube-system's DaemonSets can't be read"},
			wantErr:     true,
		},
		{
			name:        "nodes unreadable",
			reader:      fakeReader{dsPath: kindDaemonSets, nodesPath: errors.New("connection refused")},
			plugin:      "kindnet",
			version:     "v20250512-df8de77b",
			enforcement: reconcile.PolicyEnforced,
			extra:       []string{"pod ranges: the nodes can't be read"},
			wantErr:     true,
		},
		{
			name:        "garbage answer",
			reader:      fakeReader{dsPath: `not json`, nodesPath: `{"items":[{"spec":{"podCIDRs":["bogus","10.1.2.3/16"]}}]}`},
			plugin:      "unknown",
			enforcement: reconcile.PolicyUnknown,
			ranges:      ranges("10.1.0.0/16"),
			extra:       []string{"plugin: kube-system's DaemonSets can't be read", "pod ranges: a node's range doesn't parse and was left out"},
			wantErr:     true,
		},
	}
	now := time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := New(tc.reader)
			p.now = func() time.Time { return now }
			f, err := p.Facts(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if f.Plugin != tc.plugin || f.Version != tc.version || f.PolicyEnforcement != tc.enforcement {
				t.Errorf("got %q %q %q, want %q %q %q", f.Plugin, f.Version, f.PolicyEnforcement, tc.plugin, tc.version, tc.enforcement)
			}
			if f.PolicyReason == "" {
				t.Error("no policy reason")
			}
			if f.Flows || f.RequestMetrics {
				t.Error("an external CNI never offers flows or request metrics")
			}
			if !slices.Equal(f.PodRanges, tc.ranges) {
				t.Errorf("ranges %v, want %v", f.PodRanges, tc.ranges)
			}
			want := []string{ReasonNoFlows, ReasonNoRequests}
			if tc.plugin == "unknown" {
				want = []string{ReasonUnreadFlows, ReasonUnreadRequests}
			}
			want = append(want, tc.extra...)
			if !slices.Equal(f.Unavailable, want) {
				t.Errorf("unavailable %q, want %q", f.Unavailable, want)
			}
			if !f.ReadAt.Equal(now) {
				t.Errorf("readAt %v", f.ReadAt)
			}
		})
	}
}

func TestImageTag(t *testing.T) {
	for image, want := range map[string]string{
		"docker.io/kindest/kindnetd:v1":       "v1",
		"localhost:5000/kindnetd":             "",
		"localhost:5000/kindnetd:v2@sha256:0": "v2",
		"kindnetd":                            "",
	} {
		var ds daemonSet
		ds.Spec.Template.Spec.Containers = append(ds.Spec.Template.Spec.Containers, struct {
			Name  string `json:"name"`
			Image string `json:"image"`
		}{Image: image})
		if got := imageTag(ds, "kindnetd"); got != want {
			t.Errorf("imageTag(%q) = %q, want %q", image, got, want)
		}
	}
}
