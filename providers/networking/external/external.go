// Package external is the networking provider for a cluster whose network
// layer Serendip didn't install (specs/025-networking plan decision 3):
// kindnet on the development kind clusters, Flannel on a default K3s, or
// anything else. It reads what it can tell from kube-system's DaemonSets
// and the nodes, reports what the cluster lacks, and never writes.
package external

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
)

// Reader is the one call the provider makes; *kubernetes.Client has it.
type Reader interface {
	GetRaw(ctx context.Context, path string) (json.RawMessage, error)
}

// Provider implements reconcile.NetworkingProvider for an external CNI.
type Provider struct {
	client Reader
	now    func() time.Time
}

var _ reconcile.NetworkingProvider = (*Provider)(nil)

// New returns a provider reading through client.
func New(client Reader) *Provider {
	return &Provider{client: client, now: time.Now}
}

// The reasons Facts gives for what an external CNI lacks (spec criterion 2).
const (
	ReasonNoFlows    = "flows: the cluster's CNI doesn't record connections"
	ReasonNoRequests = "requests: the cluster's CNI doesn't provide request metrics"
)

// kindnetPolicySince is the first kindnetd image that enforces network
// policies (kube-network-policies, kind v0.24.0). kindnetd has no flag to
// turn them off (checked on kindnetd v20250512, spec 025 T003).
const kindnetPolicySince = "20240813"

// Facts reports the CNI. What could be read is returned even when a read
// failed, with the error: the facts then say "unknown" where the failed
// read would have answered, and Unavailable names it.
func (p *Provider) Facts(ctx context.Context) (reconcile.NetworkFacts, error) {
	f := reconcile.NetworkFacts{
		Plugin:            "unknown",
		PolicyEnforcement: reconcile.PolicyUnknown,
		PolicyReason:      "Serendip can't tell which CNI the cluster runs",
		Unavailable:       []string{ReasonNoFlows, ReasonNoRequests},
		ReadAt:            p.now().UTC(),
	}
	var errs []error

	daemonSets, dsErr := p.daemonSets(ctx)
	if dsErr != nil {
		errs = append(errs, dsErr)
		f.Unavailable = append(f.Unavailable, "plugin: kube-system's DaemonSets can't be read")
	}
	nodes, err := p.nodes(ctx)
	if err != nil {
		errs = append(errs, err)
		f.Unavailable = append(f.Unavailable, "pod ranges: the nodes can't be read")
	}

	detect(&f, daemonSets, dsErr == nil, nodes)
	var skipped bool
	f.PodRanges, skipped = podRanges(nodes)
	if skipped {
		f.Unavailable = append(f.Unavailable, "pod ranges: a node's range doesn't parse and was left out")
	}
	return f, errors.Join(errs...)
}

type daemonSet struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
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
}

type node struct {
	Metadata struct {
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		PodCIDR  string   `json:"podCIDR"`
		PodCIDRs []string `json:"podCIDRs"`
	} `json:"spec"`
}

func (p *Provider) daemonSets(ctx context.Context) ([]daemonSet, error) {
	raw, err := p.client.GetRaw(ctx, "/apis/apps/v1/namespaces/kube-system/daemonsets")
	if err != nil {
		return nil, fmt.Errorf("list kube-system DaemonSets: %w", err)
	}
	var list struct {
		Items []daemonSet `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decode kube-system DaemonSets: %w", err)
	}
	return list.Items, nil
}

func (p *Provider) nodes(ctx context.Context) ([]node, error) {
	raw, err := p.client.GetRaw(ctx, "/api/v1/nodes")
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	var list struct {
		Items []node `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decode nodes: %w", err)
	}
	return list.Items, nil
}

// detect names the CNI: kindnet by its DaemonSet, Flannel by its
// DaemonSet or, embedded in K3s, by the annotations it puts on the nodes.
// dsRead says the DaemonSet list was read, so the absence of another
// policy controller in it means something.
func detect(f *reconcile.NetworkFacts, daemonSets []daemonSet, dsRead bool, nodes []node) {
	for _, ds := range daemonSets {
		name := ds.Metadata.Name
		switch {
		case name == "kindnet":
			f.Plugin = "kindnet"
			f.Version = imageTag(ds, "kindnetd")
			if date, ok := kindnetDate(f.Version); ok && date >= kindnetPolicySince {
				f.PolicyEnforcement = reconcile.PolicyEnforced
				f.PolicyReason = "kindnet's own policy controller"
			} else {
				f.PolicyEnforcement = reconcile.PolicyUnknown
				f.PolicyReason = "kindnet releases before August 2024 don't enforce network policies; Serendip can't tell this one's release"
			}
			return
		case strings.HasPrefix(name, "kube-flannel"):
			f.Plugin = "flannel"
			f.Version = imageTag(ds, "flannel")
			f.PolicyEnforcement = reconcile.PolicyUnknown
			f.PolicyReason = "Flannel doesn't enforce network policies itself; Serendip can't see whether another controller does"
			return
		}
	}

	flannel, k3s, policyOff := false, false, false
	for _, n := range nodes {
		for k, v := range n.Metadata.Annotations {
			switch {
			case strings.HasPrefix(k, "flannel.alpha.coreos.com/"):
				flannel = true
			case k == "k3s.io/node-args":
				k3s = true
				policyOff = policyOff || k3sArg(v, "--disable-network-policy")
			case strings.HasPrefix(k, "k3s.io/"):
				k3s = true
			}
		}
	}
	if !flannel {
		return
	}
	f.Plugin = "flannel"
	f.PolicyEnforcement = reconcile.PolicyUnknown
	switch {
	case k3s && policyOff && dsRead && !policyController(daemonSets):
		f.PolicyEnforcement = reconcile.PolicyNotEnforced
		f.PolicyReason = "K3s was started with its network policy controller turned off"
	case k3s:
		f.PolicyReason = "K3s's own policy controller, unless it was turned off at install; Serendip can't see"
	default:
		f.PolicyReason = "Flannel doesn't enforce network policies itself; Serendip can't see whether another controller does"
	}
}

// policyController reports whether a DaemonSet that may enforce policies
// on its own (beside Flannel, as Canal does) runs in kube-system.
func policyController(daemonSets []daemonSet) bool {
	for _, ds := range daemonSets {
		for _, prefix := range []string{"calico", "canal", "cilium", "antrea", "weave", "kube-router", "kube-network-policies"} {
			if strings.HasPrefix(ds.Metadata.Name, prefix) {
				return true
			}
		}
	}
	return false
}

// imageTag returns the tag of the container whose image names repo, or of
// the first container, without a digest.
func imageTag(ds daemonSet, repo string) string {
	cs := ds.Spec.Template.Spec.Containers
	if len(cs) == 0 {
		return ""
	}
	image := cs[0].Image
	for _, c := range cs {
		if strings.Contains(c.Image, repo) {
			image = c.Image
			break
		}
	}
	image, _, _ = strings.Cut(image, "@")
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon <= slash {
		return ""
	}
	return image[colon+1:]
}

// kindnetDate reads the date from a kindnetd tag such as
// "v20250512-df8de77b".
func kindnetDate(tag string) (string, bool) {
	date, _, _ := strings.Cut(strings.TrimPrefix(tag, "v"), "-")
	if len(date) != 8 {
		return "", false
	}
	for _, r := range date {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return date, true
}

// k3sArg reports whether K3s's node-args annotation, a JSON array of the
// arguments the server was started with, holds flag (alone or as
// flag=true). Only a server node records it; its absence proves nothing.
func k3sArg(annotation, flag string) bool {
	var args []string
	if json.Unmarshal([]byte(annotation), &args) != nil {
		return false
	}
	for _, a := range args {
		if a == flag || a == flag+"=true" {
			return true
		}
	}
	return false
}

// podRanges collects the nodes' pod ranges, sorted and without repeats,
// and says whether a range that doesn't parse was skipped.
func podRanges(nodes []node) (out []netip.Prefix, skipped bool) {
	seen := map[netip.Prefix]bool{}
	add := func(s string) {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			skipped = true
			return
		}
		p = p.Masked()
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, n := range nodes {
		if n.Spec.PodCIDR != "" {
			add(n.Spec.PodCIDR)
		}
		for _, s := range n.Spec.PodCIDRs {
			add(s)
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
