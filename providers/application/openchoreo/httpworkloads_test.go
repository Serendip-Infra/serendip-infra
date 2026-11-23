package openchoreo

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
)

func TestHTTPWorkloads(t *testing.T) {
	// As live on t025 (trimmed): srv's port is exposed (HTTP), caller's
	// isn't; srv runs in two Environments; another Project's cmp-srv is
	// TCP-only; one policy is another namespace's platform object.
	const workloads = `{"items":[
	 {"spec":{"owner":{"projectName":"app-1","componentName":"cmp-srv"},"endpoints":{"http":{"type":"HTTP","port":80,"visibility":["external"]},"admin":{"type":"TCP","port":9000},"api":{"type":"HTTP","port":8080}}}},
	 {"spec":{"owner":{"projectName":"app-1","componentName":"cmp-caller"},"endpoints":{"http":{"type":"TCP","port":80}}}},
	 {"spec":{"owner":{"projectName":"app-2","componentName":"cmp-srv"},"endpoints":{"raw":{"type":"TCP","port":9000}}}}]}`
	const policies = `{"items":[
	 {"metadata":{"name":"openchoreo-cmp-srv","namespace":"dp-b"},"spec":{"podSelector":{"matchLabels":{"openchoreo.dev/component":"cmp-srv","openchoreo.dev/project":"app-1","openchoreo.dev/namespace":"openchoreo-control-plane"}}}},
	 {"metadata":{"name":"openchoreo-cmp-srv","namespace":"dp-a"},"spec":{"podSelector":{"matchLabels":{"openchoreo.dev/component":"cmp-srv","openchoreo.dev/project":"app-1","openchoreo.dev/namespace":"openchoreo-control-plane"}}}},
	 {"metadata":{"name":"openchoreo-cmp-caller","namespace":"dp-a"},"spec":{"podSelector":{"matchLabels":{"openchoreo.dev/component":"cmp-caller","openchoreo.dev/project":"app-1","openchoreo.dev/namespace":"openchoreo-control-plane"}}}},
	 {"metadata":{"name":"openchoreo-cmp-srv","namespace":"dp-other-project"},"spec":{"podSelector":{"matchLabels":{"openchoreo.dev/component":"cmp-srv","openchoreo.dev/project":"app-2","openchoreo.dev/namespace":"openchoreo-control-plane"}}}},
	 {"metadata":{"name":"openchoreo-cmp-srv","namespace":"dp-x"},"spec":{"podSelector":{"matchLabels":{"openchoreo.dev/component":"cmp-srv","openchoreo.dev/project":"app-1","openchoreo.dev/namespace":"someone-else"}}}}]}`
	var query string
	client := newTestKubeClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case namespacedPath("workloads"):
			w.Write([]byte(workloads))
		case "/apis/networking.k8s.io/v1/networkpolicies":
			query = r.URL.Query().Get("labelSelector")
			w.Write([]byte(policies))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	got, err := NewProvider(client, reconcile.GatewayEndpoint{}).HTTPWorkloads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []HTTPWorkload{
		{Namespace: "dp-a", Policy: "openchoreo-cmp-srv", Ports: []int{80, 8080}},
		{Namespace: "dp-b", Policy: "openchoreo-cmp-srv", Ports: []int{80, 8080}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("HTTPWorkloads() = %+v, want %+v", got, want)
	}
	if query != "openchoreo.dev/managed-by=renderedrelease-controller" {
		t.Errorf("labelSelector = %q", query)
	}
}
