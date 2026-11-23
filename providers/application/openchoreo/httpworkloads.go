package openchoreo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
)

// HTTPWorkload is one Component's running copy in one Environment that
// serves HTTP: the NetworkPolicy the platform admits its traffic with, and
// its HTTP ports. cmd/serendipd hands these to the networking provider,
// which turns on request visibility for them (specs/025-networking plan
// decision 4) without knowing this platform.
type HTTPWorkload struct {
	Namespace string
	Policy    string
	Ports     []int
}

// Labels the platform puts on a Component's NetworkPolicy's pod selector
// (checked on OpenChoreo 1.3.1, spec 025 T004).
const (
	labelOCComponent = "openchoreo.dev/component"
	labelOCProject   = "openchoreo.dev/project"
	labelOCNamespace = "openchoreo.dev/namespace"
	labelOCManagedBy = "openchoreo.dev/managed-by"
	renderedRelease  = "renderedrelease-controller"
)

// HTTPWorkloads lists the workloads with an HTTP endpoint. A Component's
// port is an HTTP endpoint when it's exposed (endpointFor); its
// NetworkPolicy, one per Environment it runs in, is found by the Project
// and Component labels on its pod selector (both: a Component name is
// unique only within its Project).
func (p *Provider) HTTPWorkloads(ctx context.Context) ([]HTTPWorkload, error) {
	raw, err := p.client.GetRaw(ctx, namespacedPath("workloads"))
	if err != nil {
		return nil, fmt.Errorf("list Workloads (one read of every Workload in the cluster): %w", err)
	}
	var workloads struct {
		Items []struct {
			Spec struct {
				Owner     ownerRef                    `json:"owner"`
				Endpoints map[string]workloadEndpoint `json:"endpoints"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &workloads); err != nil {
		return nil, fmt.Errorf("decode Workloads: %w", err)
	}
	ports := map[[2]string][]int{} // {project, component} -> HTTP ports
	for _, w := range workloads.Items {
		key := [2]string{w.Spec.Owner.ProjectName, w.Spec.Owner.ComponentName}
		for _, e := range w.Spec.Endpoints {
			if e.Type == "HTTP" && e.Port > 0 {
				ports[key] = append(ports[key], int(e.Port))
			}
		}
	}
	if len(ports) == 0 {
		return nil, nil
	}

	raw, err = p.client.GetRaw(ctx, "/apis/networking.k8s.io/v1/networkpolicies?labelSelector="+url.QueryEscape(labelOCManagedBy+"="+renderedRelease))
	if err != nil {
		return nil, fmt.Errorf("list the platform's NetworkPolicies (one read of all of them): %w", err)
	}
	var policies struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				PodSelector struct {
					MatchLabels map[string]string `json:"matchLabels"`
				} `json:"podSelector"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &policies); err != nil {
		return nil, fmt.Errorf("decode NetworkPolicies: %w", err)
	}
	var out []HTTPWorkload
	for _, np := range policies.Items {
		sel := np.Spec.PodSelector.MatchLabels
		if sel[labelOCNamespace] != ControlPlaneNamespace {
			continue
		}
		ps, ok := ports[[2]string{sel[labelOCProject], sel[labelOCComponent]}]
		if !ok {
			continue
		}
		ps = append([]int(nil), ps...)
		sort.Ints(ps)
		ps = slices.Compact(ps) // two endpoints may share a port
		out = append(out, HTTPWorkload{Namespace: np.Metadata.Namespace, Policy: np.Metadata.Name, Ports: ps})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Policy < out[j].Policy
	})
	return out, nil
}
