package cilium

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// The node guard (spec 025 T014, security review): Cilium's agent, operator
// and Envoy run on the host network and serve their metrics, unauthenticated,
// on every node's address. Hubble's request metrics there carry every
// Component's source and destination pods, so any workload could read the
// cluster's whole traffic picture that the flows and traffic reads scope by
// permission. One cluster-wide policy denies every pod egress to those
// ports on the nodes, except pods in metricsReaders: kube-system (Cilium's
// own) and the observability plane, whose Prometheus scrapes them. Builds
// run user code too, so the guard isn't limited to Components. It only
// denies: enableDefaultDeny is off, so it adds no allow-list to the pods it
// selects (RP-091).

const (
	guardName  = "serendip-node-metrics-guard"
	guardValue = "networking-node-guard"
)

// metricsReaders are the namespaces whose pods may read the node's
// metrics ports. The observability plane's name is the observability
// package's: cmd/serendipd's tests check they agree (RP-091).
var metricsReaders = []string{"kube-system", "openchoreo-observability-plane"}

// MetricsReaders returns the namespaces the node guard exempts.
func MetricsReaders() []string { return slices.Clone(metricsReaders) }

// guardPorts are the host-network metrics ports Cilium 1.20's chart uses:
// the agent (9962), the operator (9963), Envoy (9964) and Hubble (9965).
var guardPorts = []string{"9962", "9963", "9964", "9965"}

func guardPath() string {
	return "/apis/cilium.io/v2/ciliumclusterwidenetworkpolicies/" + guardName
}

type guardPolicy struct {
	Metadata struct {
		Labels map[string]string `json:"labels,omitempty"`
	} `json:"metadata"`
	Spec guardSpec `json:"spec"`
}

type guardSpec struct {
	EnableDefaultDeny defaultDeny     `json:"enableDefaultDeny"`
	EndpointSelector  labelSelector   `json:"endpointSelector"`
	EgressDeny        []guardDenyRule `json:"egressDeny"`
}

type guardDenyRule struct {
	ToEntities []string        `json:"toEntities"`
	ToPorts    []guardPortRule `json:"toPorts"`
}

type guardPortRule struct {
	Ports []cnpPort `json:"ports"`
}

func desiredGuard() guardSpec {
	ports := make([]cnpPort, 0, len(guardPorts))
	for _, p := range guardPorts {
		ports = append(ports, cnpPort{Port: p, Protocol: "TCP"})
	}
	return guardSpec{
		EndpointSelector: labelSelector{MatchExpressions: []selectorExpr{{Key: namespaceKey, Operator: "NotIn", Values: metricsReaders}}},
		EgressDeny:       []guardDenyRule{{ToEntities: []string{"host", "remote-node"}, ToPorts: []guardPortRule{{Ports: ports}}}},
	}
}

// ensureGuard makes the node guard match desiredGuard, writing only when
// it differs. It reports whether it wrote. A guard someone added fields to
// is deleted and made again, as the L7 policies are (RP-090).
func (p *Provider) ensureGuard(ctx context.Context) (bool, error) {
	want := desiredGuard()
	raw, err := p.client.GetRaw(ctx, guardPath())
	switch {
	case errors.Is(err, kubernetes.ErrNotFound):
	case err != nil:
		return false, fmt.Errorf("read CiliumClusterwideNetworkPolicy %s: %w", guardName, err)
	default:
		var have guardPolicy
		if err := json.Unmarshal(raw, &have); err != nil {
			return false, fmt.Errorf("decode CiliumClusterwideNetworkPolicy %s: %w", guardName, err)
		}
		var spec struct {
			Spec json.RawMessage `json:"spec"`
		}
		_ = json.Unmarshal(raw, &spec) // decoded above; a spec that won't decode strictly is foreign
		var strict guardSpec
		foreign := len(spec.Spec) > 0 && strictDecode(spec.Spec, &strict) != nil
		a, _ := json.Marshal(have.Spec)
		b, _ := json.Marshal(want)
		if !foreign && string(a) == string(b) && have.Metadata.Labels[managedLabel] == guardValue {
			return false, nil
		}
		if foreign {
			if err := p.client.Delete(ctx, guardPath()); err != nil && !errors.Is(err, kubernetes.ErrNotFound) {
				return false, fmt.Errorf("delete CiliumClusterwideNetworkPolicy %s to remake it: %w", guardName, err)
			}
		}
	}
	spec, err := json.Marshal(want)
	if err != nil {
		return false, err
	}
	obj := kubernetes.Object{
		APIVersion: cnpAPI, Kind: "CiliumClusterwideNetworkPolicy",
		Metadata: kubernetes.ObjectMeta{Name: guardName, Labels: map[string]string{managedLabel: guardValue}},
		Spec:     spec,
	}
	if _, err := p.client.Apply(ctx, guardPath(), obj, kubernetes.FieldManager); err != nil {
		return false, fmt.Errorf("apply CiliumClusterwideNetworkPolicy %s: %w", guardName, err)
	}
	return true, nil
}

// guardKey is how the guard is named in a Report's Applied list.
const guardKey = "cluster/" + guardName
