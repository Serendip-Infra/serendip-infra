package kubernetes

import (
	"context"
	"net/http"
	"net/netip"
	"slices"
	"testing"
)

func TestClusterRanges(t *testing.T) {
	serviceCIDRs := `{"items":[{"spec":{"cidrs":["10.43.0.0/16","fd43::/112"]}}]}`
	c, _ := newTestServerClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/nodes":
			_, _ = w.Write([]byte(`{"items":[
				{"spec":{"podCIDR":"10.42.0.0/24","podCIDRs":["10.42.0.0/24","fd42::/64"]},
				 "status":{"addresses":[{"type":"InternalIP","address":"192.168.1.20"},{"type":"Hostname","address":"t470"},{"type":"InternalDNS","address":"t470.lan"},{"type":"InternalIP","address":"::ffff:192.168.1.21"}]}},
				{"spec":{"podCIDRs":["10.42.1.0/24"]},"status":{"addresses":[{"type":"ExternalIP","address":"203.0.113.9"}]}}]}`))
		case "/apis/networking.k8s.io/v1/servicecidrs":
			if serviceCIDRs == "" {
				http.Error(w, `{"kind":"Status","code":404}`, http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(serviceCIDRs))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	got, err := c.ClusterRanges(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"10.42.0.0/24", "fd42::/64", "10.42.1.0/24", "192.168.1.20/32", "192.168.1.21/32", "203.0.113.9/32", "10.43.0.0/16", "fd43::/112"} {
		if !slices.Contains(got, netip.MustParsePrefix(want)) {
			t.Errorf("missing %s in %v", want, got)
		}
	}

	// Without the Service range, the nodes' are still returned, with the
	// error.
	serviceCIDRs = ""
	got, err = c.ClusterRanges(context.Background())
	if err == nil || !slices.Contains(got, netip.MustParsePrefix("192.168.1.20/32")) {
		t.Errorf("no ServiceCIDRs: %v, %v", got, err)
	}
}
