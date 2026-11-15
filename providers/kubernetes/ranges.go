package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// ClusterRanges reads the cluster's own address ranges, which an outbound
// notification must never reach (spec 024, core/notify's Checker): each
// node's pod ranges and addresses, and the Service ranges from the
// ServiceCIDR API (networking.k8s.io/v1, GA in Kubernetes 1.33; our K3s
// pin is 1.36). Node addresses are returned as single-address prefixes.
//
// What could be read is returned even when part failed, with the error,
// so a missing Service range doesn't also drop the nodes'.
func (c *Client) ClusterRanges(ctx context.Context) ([]netip.Prefix, error) {
	var out []netip.Prefix
	var errs []error
	add := func(s string, single bool) {
		if single {
			if a, err := netip.ParseAddr(s); err == nil {
				a = a.Unmap().WithZone("")
				out = append(out, netip.PrefixFrom(a, a.BitLen()))
			} else {
				errs = append(errs, fmt.Errorf("a node address %q: %w", s, err))
			}
			return
		}
		if s == "" {
			return
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
		} else {
			errs = append(errs, fmt.Errorf("a range %q: %w", s, err))
		}
	}

	raw, err := c.GetRaw(ctx, "/api/v1/nodes")
	if err != nil {
		errs = append(errs, fmt.Errorf("list nodes: %w", err))
	} else {
		var nodes struct {
			Items []struct {
				Spec struct {
					PodCIDR  string   `json:"podCIDR"`
					PodCIDRs []string `json:"podCIDRs"`
				} `json:"spec"`
				Status struct {
					Addresses []struct {
						Type    string `json:"type"`
						Address string `json:"address"`
					} `json:"addresses"`
				} `json:"status"`
			} `json:"items"`
		}
		if err := json.Unmarshal(raw, &nodes); err != nil {
			errs = append(errs, fmt.Errorf("decode nodes: %w", err))
			nodes.Items = nil
		}
		for _, n := range nodes.Items {
			add(n.Spec.PodCIDR, false)
			for _, p := range n.Spec.PodCIDRs {
				add(p, false)
			}
			for _, a := range n.Status.Addresses {
				// InternalIP and ExternalIP; Hostname and the DNS types are
				// names, not addresses.
				if strings.HasSuffix(a.Type, "IP") {
					add(a.Address, true)
				}
			}
		}
	}

	raw, err = c.GetRaw(ctx, "/apis/networking.k8s.io/v1/servicecidrs")
	if err != nil {
		errs = append(errs, fmt.Errorf("list ServiceCIDRs: %w", err))
	} else {
		var cidrs struct {
			Items []struct {
				Spec struct {
					CIDRs []string `json:"cidrs"`
				} `json:"spec"`
			} `json:"items"`
		}
		if err := json.Unmarshal(raw, &cidrs); err != nil {
			errs = append(errs, fmt.Errorf("decode ServiceCIDRs: %w", err))
			cidrs.Items = nil
		}
		n := 0
		for _, item := range cidrs.Items {
			for _, p := range item.Spec.CIDRs {
				add(p, false)
				n++
			}
		}
		if n == 0 {
			errs = append(errs, errors.New("the cluster lists no Service range"))
		}
	}
	return out, errors.Join(errs...)
}
