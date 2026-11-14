package openchoreo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"

	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// The plane's NetworkPolicies (plan decision 4, spike findings 8, 9 and
// 15). The plane chart's own policy admits only pods in the namespace,
// which keeps workloads away from the Observer, the adapters and the
// stores. Fluent Bit runs with hostNetwork, so its flushes come from the
// node's address, which that policy refuses: Serendip adds one rule
// admitting the nodes' current addresses to the logs store's ingest port,
// and nothing else. The logs store's own credentials still apply.
const (
	// chartPolicyName is the plane chart's ingress policy, whose presence
	// the checks require: without it the plane is open to every pod.
	chartPolicyName = "observability-plane-ingress"
	// NodeRuleName is Serendip's rule for Fluent Bit.
	NodeRuleName  = "serendip-allow-node-to-openobserve"
	logsStorePort = 5080
)

// logsStoreSelector selects the logs store's pod, in the node rule and in
// the readiness check, so a check that passes also shows the rule's
// selector matches a pod (RP-018's relabel risk).
var logsStoreSelector = map[string]string{
	"app.kubernetes.io/name":     "openobserve-standalone",
	"app.kubernetes.io/instance": logsRelease,
}

const networkPoliciesPath = "/apis/networking.k8s.io/v1/namespaces/" + PlaneNamespace + "/networkpolicies"

// nodeRule is the rule admitting ips (from nodeIPs), each as a single-host
// prefix: /32, or /128 on IPv6.
func nodeRule(ips []string) kubernetes.Object {
	from := make([]map[string]any, 0, len(ips))
	for _, ip := range ips {
		from = append(from, map[string]any{"ipBlock": map[string]string{"cidr": hostPrefix(ip)}})
	}
	return kubernetes.Object{
		APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy",
		Metadata: kubernetes.ObjectMeta{Name: NodeRuleName, Namespace: PlaneNamespace, Labels: managedBy},
		Spec: mustJSON(map[string]any{
			"podSelector": map[string]any{"matchLabels": logsStoreSelector},
			"policyTypes": []string{"Ingress"},
			"ingress": []map[string]any{{
				"from":  from,
				"ports": []map[string]any{{"port": logsStorePort, "protocol": "TCP"}},
			}},
		}),
	}
}

// nodeIPs lists every Node's InternalIP addresses, sorted, so a changed
// address (a restarted kind node, a new DHCP lease) is noticed and the
// rule rebuilt within one resync (spike finding 15). No address at all
// is an error: the rule would admit nothing.
func nodeIPs(ctx context.Context, client *kubernetes.Client) ([]string, error) {
	nodes, err := client.List(ctx, "/api/v1/nodes")
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	var ips []string
	for _, n := range nodes {
		var st struct {
			Addresses []struct {
				Type    string `json:"type"`
				Address string `json:"address"`
			} `json:"addresses"`
		}
		if len(n.Status) > 0 {
			if err := json.Unmarshal(n.Status, &st); err != nil {
				return nil, fmt.Errorf("decode node %s status: %w", n.Metadata.Name, err)
			}
		}
		for _, a := range st.Addresses {
			if a.Type != "InternalIP" {
				continue
			}
			addr, err := netip.ParseAddr(a.Address)
			if err != nil {
				return nil, fmt.Errorf("node %s has an InternalIP that isn't an address: %q", n.Metadata.Name, a.Address)
			}
			ips = append(ips, addr.Unmap().String())
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no node has an InternalIP address")
	}
	slices.Sort(ips)
	return slices.Compact(ips), nil
}

// ruleMatches reports whether a live node rule is the one nodeRule(ips)
// would apply: the same selector, port and addresses. Its spec is compared
// as decoded JSON, so field order and server defaults that keep the
// meaning (an added "protocol") don't count as drift.
func ruleMatches(live kubernetes.Object, ips []string) bool {
	return specEqual(live.Spec, nodeRule(ips).Spec)
}

// hostPrefix is ip's single-host CIDR; ip comes from nodeIPs, so it parses.
func hostPrefix(ip string) string {
	addr := netip.MustParseAddr(ip)
	return netip.PrefixFrom(addr, addr.BitLen()).String()
}

func specEqual(live, want json.RawMessage) bool {
	var a, b any
	if json.Unmarshal(live, &a) != nil || json.Unmarshal(want, &b) != nil {
		return false
	}
	la, _ := json.Marshal(a) // encoding/json sorts map keys
	lb, _ := json.Marshal(b)
	return string(la) == string(lb)
}
