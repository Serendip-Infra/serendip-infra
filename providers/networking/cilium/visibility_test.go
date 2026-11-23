package cilium

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

const ns = "dp-openchoreo-co-app-7781ea963-env-8582189a-5f623755"

// srvPolicy is OpenChoreo 1.3.1's NetworkPolicy for an exposed Component,
// as live on t025 (trimmed): its port from the same namespace, and from
// the platform's system components in any namespace.
const srvPolicy = `{"metadata":{"name":"openchoreo-cmp-ac7df9f12c","namespace":"` + ns + `","uid":"ec880068-6845-47e2-bbfd-cc6285908414"},
 "spec":{"ingress":[
  {"from":[{"podSelector":{}}],"ports":[{"port":80,"protocol":"TCP"}]},
  {"from":[{"namespaceSelector":{},"podSelector":{"matchExpressions":[{"key":"openchoreo.dev/system-component","operator":"Exists"}]}}],"ports":[{"port":80,"protocol":"TCP"}]}],
 "podSelector":{"matchLabels":{"openchoreo.dev/component":"cmp-ac7df9f12c","openchoreo.dev/component-uid":"2f248807-64e8-456f-9a73-8daf80436acd"}},
 "policyTypes":["Ingress"]}}`

// wantSrv is T001's hand-written policy (testdata/t001-l7-policy.yaml),
// with "k8s:" on every label key.
const wantSrv = `{"enableDefaultDeny":{"ingress":false,"egress":false},
 "endpointSelector":{"matchLabels":{"k8s:openchoreo.dev/component":"cmp-ac7df9f12c","k8s:openchoreo.dev/component-uid":"2f248807-64e8-456f-9a73-8daf80436acd"}},
 "ingress":[
  {"fromEndpoints":[{"matchLabels":{"k8s:io.kubernetes.pod.namespace":"` + ns + `"}}],"toPorts":[{"ports":[{"port":"80","protocol":"TCP"}],"rules":{"http":[{}]}}]},
  {"fromEndpoints":[{"matchExpressions":[{"key":"k8s:io.kubernetes.pod.namespace","operator":"Exists"},{"key":"k8s:openchoreo.dev/system-component","operator":"Exists"}]}],"toPorts":[{"ports":[{"port":"80","protocol":"TCP"}],"rules":{"http":[{}]}}]}]}`

func policy(ingress string) networkPolicy {
	var np networkPolicy
	raw := `{"metadata":{"name":"np","namespace":"ns1","uid":"u1"},"spec":{"podSelector":{"matchLabels":{"app":"a"}},"policyTypes":["Ingress"],"ingress":` + ingress + `}}`
	if err := json.Unmarshal([]byte(raw), &np); err != nil {
		panic(err)
	}
	return np
}

func compact(t *testing.T, s string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("bad JSON %s: %v", s, err)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func TestMirrorOpenChoreoPolicy(t *testing.T) {
	var np networkPolicy
	if err := json.Unmarshal([]byte(srvPolicy), &np); err != nil {
		t.Fatal(err)
	}
	c, reason := mirror(np, []int{80})
	if reason != "" {
		t.Fatalf("mirror() skipped: %s", reason)
	}
	got, _ := json.Marshal(c.Spec)
	if compact(t, string(got)) != compact(t, wantSrv) {
		t.Errorf("spec =\n %s\nwant\n %s", got, compact(t, wantSrv))
	}
	if c.Metadata.Name != "serendip-l7-openchoreo-cmp-ac7df9f12c" || c.Metadata.Namespace != ns {
		t.Errorf("name = %s/%s", c.Metadata.Namespace, c.Metadata.Name)
	}
	if o := c.Metadata.OwnerReferences; len(o) != 1 || o[0].Kind != "NetworkPolicy" || o[0].UID != "ec880068-6845-47e2-bbfd-cc6285908414" || o[0].Name != np.Metadata.Name {
		t.Errorf("owner = %+v, want the NetworkPolicy", o)
	}
}

// TestMirrorNeverWidens: the mirror keeps each rule's own peers, and only
// the HTTP ports among its own ports. It never adds a peer or a port.
func TestMirrorNeverWidens(t *testing.T) {
	tests := []struct {
		name    string
		ingress string
		ports   []int
		want    string // the ingress rules, compact JSON
	}{
		{
			name:    "a TCP-only port is left out, the HTTP one kept",
			ingress: `[{"from":[{"podSelector":{"matchLabels":{"role":"web"}}}],"ports":[{"port":80,"protocol":"TCP"},{"port":5432,"protocol":"TCP"}]}]`,
			ports:   []int{80},
			want:    `[{"fromEndpoints":[{"matchLabels":{"k8s:io.kubernetes.pod.namespace":"ns1","k8s:role":"web"}}],"toPorts":[{"ports":[{"port":"80","protocol":"TCP"}],"rules":{"http":[{}]}}]}]`,
		},
		{
			name:    "an HTTP port the policy doesn't admit isn't added",
			ingress: `[{"from":[{"podSelector":{}}],"ports":[{"port":80}]}]`,
			ports:   []int{80, 8080},
			want:    `[{"fromEndpoints":[{"matchLabels":{"k8s:io.kubernetes.pod.namespace":"ns1"}}],"toPorts":[{"ports":[{"port":"80","protocol":"TCP"}],"rules":{"http":[{}]}}]}]`,
		},
		{
			name:    "a rule without ports admits every port: so the HTTP ones",
			ingress: `[{"from":[{"podSelector":{}}]}]`,
			ports:   []int{8080, 80},
			want:    `[{"fromEndpoints":[{"matchLabels":{"k8s:io.kubernetes.pod.namespace":"ns1"}}],"toPorts":[{"ports":[{"port":"80","protocol":"TCP"},{"port":"8080","protocol":"TCP"}],"rules":{"http":[{}]}}]}]`,
		},
		{
			name:    "a UDP port is never HTTP",
			ingress: `[{"from":[{"podSelector":{}}],"ports":[{"port":80,"protocol":"UDP"}]},{"from":[{"podSelector":{"matchLabels":{"x":"y"}}}],"ports":[{"port":80}]}]`,
			ports:   []int{80},
			want:    `[{"fromEndpoints":[{"matchLabels":{"k8s:io.kubernetes.pod.namespace":"ns1","k8s:x":"y"}}],"toPorts":[{"ports":[{"port":"80","protocol":"TCP"}],"rules":{"http":[{}]}}]}]`,
		},
		{
			name:    "several peers in one rule stay one rule",
			ingress: `[{"from":[{"podSelector":{"matchLabels":{"a":"1"}}},{"namespaceSelector":{}}],"ports":[{"port":80}]}]`,
			ports:   []int{80},
			want:    `[{"fromEndpoints":[{"matchLabels":{"k8s:a":"1","k8s:io.kubernetes.pod.namespace":"ns1"}},{"matchExpressions":[{"key":"k8s:io.kubernetes.pod.namespace","operator":"Exists"}]}],"toPorts":[{"ports":[{"port":"80","protocol":"TCP"}],"rules":{"http":[{}]}}]}]`,
		},
		{
			name:    "In and NotIn keep their values",
			ingress: `[{"from":[{"podSelector":{"matchExpressions":[{"key":"tier","operator":"In","values":["a","b"]},{"key":"env","operator":"NotIn","values":["x"]}]}}],"ports":[{"port":80}]}]`,
			ports:   []int{80},
			want:    `[{"fromEndpoints":[{"matchLabels":{"k8s:io.kubernetes.pod.namespace":"ns1"},"matchExpressions":[{"key":"k8s:tier","operator":"In","values":["a","b"]},{"key":"k8s:env","operator":"NotIn","values":["x"]}]}],"toPorts":[{"ports":[{"port":"80","protocol":"TCP"}],"rules":{"http":[{}]}}]}]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, reason := mirror(policy(tt.ingress), tt.ports)
			if reason != "" {
				t.Fatalf("mirror() skipped: %s", reason)
			}
			got, _ := json.Marshal(c.Spec.Ingress)
			if compact(t, string(got)) != compact(t, tt.want) {
				t.Errorf("ingress =\n %s\nwant\n %s", got, compact(t, tt.want))
			}
			if c.Spec.EnableDefaultDeny.Ingress || c.Spec.EnableDefaultDeny.Egress {
				t.Error("the policy turns on default deny")
			}
		})
	}
}

// TestMirrorSkips: a policy mirror can't copy faithfully gets no policy
// and a reason, never a guess.
func TestMirrorSkips(t *testing.T) {
	tests := []struct {
		name, ingress, reason string
		edit                  func(*networkPolicy)
	}{
		{name: "address range", ingress: `[{"from":[{"ipBlock":{"cidr":"10.0.0.0/8"}}],"ports":[{"port":80}]}]`, reason: "address-range peer"},
		{name: "namespace labels", ingress: `[{"from":[{"namespaceSelector":{"matchLabels":{"team":"a"}}}],"ports":[{"port":80}]}]`, reason: "namespace labels"},
		{name: "every source", ingress: `[{"ports":[{"port":80}]}]`, reason: "every source"},
		{name: "named port", ingress: `[{"from":[{"podSelector":{}}],"ports":[{"port":"http"}]}]`, reason: "named port"},
		{name: "port range", ingress: `[{"from":[{"podSelector":{}}],"ports":[{"port":79,"endPort":81}]}]`, reason: "port range"},
		{name: "unknown peer field", ingress: `[{"from":[{"podSelector":{},"serviceAccountSelector":{}}],"ports":[{"port":80}]}]`, reason: "peer Serendip doesn't know"},
		{name: "unknown rule field", ingress: `[{"from":[{"podSelector":{}}],"ports":[{"port":80}],"action":"Deny"}]`, reason: "ingress rule Serendip doesn't know"},
		{name: "unknown operator", ingress: `[{"from":[{"podSelector":{"matchExpressions":[{"key":"a","operator":"Gt","values":["1"]}]}}],"ports":[{"port":80}]}]`, reason: "operator"},
		{name: "sourced key", ingress: `[{"from":[{"podSelector":{"matchLabels":{"reserved:host":""}}}],"ports":[{"port":80}]}]`, reason: "key"},
		{name: "no HTTP port admitted", ingress: `[{"from":[{"podSelector":{}}],"ports":[{"port":5432}]}]`, reason: "admits no traffic to its HTTP ports"},
		{name: "no rules", ingress: `[]`, reason: "admits no traffic"},
		{name: "ingress not restricted", ingress: `[{"from":[{"podSelector":{}}]}]`, reason: "doesn't restrict ingress",
			edit: func(np *networkPolicy) { np.Spec.PolicyTypes = []string{"Egress"} }},
		{name: "every pod selected", ingress: `[{"from":[{"podSelector":{}}]}]`, reason: "every pod",
			edit: func(np *networkPolicy) { np.Spec.PodSelector = &labelSelector{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			np := policy(tt.ingress)
			if tt.edit != nil {
				tt.edit(&np)
			}
			_, reason := mirror(np, []int{80})
			if !strings.Contains(reason, tt.reason) {
				t.Errorf("reason = %q, want %q", reason, tt.reason)
			}
		})
	}
}

const srvPath = "/apis/networking.k8s.io/v1/namespaces/" + ns + "/networkpolicies/openchoreo-cmp-ac7df9f12c"

var srvTarget = Target{Namespace: ns, Policy: "openchoreo-cmp-ac7df9f12c", HTTPPorts: []int{80}}

const srvCNP = ns + "/serendip-l7-openchoreo-cmp-ac7df9f12c"

func TestEnsureVisibility(t *testing.T) {
	ctx := context.Background()
	bodies := baseBodies(pinnedConfig())
	bodies[srvPath] = srvPolicy
	f := newFake(bodies)
	p := New(f)

	facts, _ := p.Facts(ctx)
	if !slices.ContainsFunc(facts.Unavailable, func(s string) bool { return strings.Contains(s, "hasn't routed any Component's requests") }) {
		t.Errorf("before any pass, Facts().Unavailable = %q, want it said", facts.Unavailable)
	}

	// First pass creates the policy; a missing NetworkPolicy is a skip.
	missing := Target{Namespace: ns, Policy: "openchoreo-cmp-gone", HTTPPorts: []int{80}}
	rep, err := p.EnsureVisibility(ctx, []Target{srvTarget, missing})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Applied, []string{guardKey, srvCNP}) || rep.Unchanged != 0 || len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0].Reason, "doesn't exist yet") {
		t.Fatalf("first pass = %+v", rep)
	}
	stored := f.cnps[cnpPath(ns, "serendip-l7-openchoreo-cmp-ac7df9f12c")]
	if compact(t, string(stored.Spec)) != compact(t, wantSrv) || stored.Metadata.Labels[managedLabel] != managedValue {
		t.Errorf("stored %s, labels %v", stored.Spec, stored.Metadata.Labels)
	}
	facts, _ = p.Facts(ctx)
	if !slices.ContainsFunc(facts.Unavailable, func(s string) bool { return strings.Contains(s, "1 workloads' network policies can't be mirrored") }) {
		t.Errorf("Facts().Unavailable = %q, want the skip counted", facts.Unavailable)
	}

	// Nothing changed: no writes at all.
	rep, err = p.EnsureVisibility(ctx, []Target{srvTarget})
	if err != nil || len(rep.Applied) != 0 || rep.Unchanged != 1 || len(rep.Deleted) != 0 {
		t.Fatalf("second pass = %+v, %v; want unchanged", rep, err)
	}
	if len(f.applies) != 1 || len(f.deletes) != 0 {
		t.Errorf("writes = %d applies, %d deletes; want 1 and 0", len(f.applies), len(f.deletes))
	}
	facts, _ = p.Facts(ctx)
	if slices.ContainsFunc(facts.Unavailable, func(s string) bool { return strings.HasPrefix(s, "requests:") }) {
		t.Errorf("after a clean pass, Facts().Unavailable = %q, want no requests note", facts.Unavailable)
	}

	// The NetworkPolicy changes: re-derived.
	bodies[srvPath] = strings.Replace(srvPolicy, `"port":80,"protocol":"TCP"}]},`, `"port":80,"protocol":"TCP"}]},{"from":[{"podSelector":{"matchLabels":{"k":"v"}}}],"ports":[{"port":80}]},`, 1)
	rep, err = p.EnsureVisibility(ctx, []Target{srvTarget})
	if err != nil || len(rep.Applied) != 1 {
		t.Fatalf("after a policy change = %+v, %v; want applied", rep, err)
	}
	if !strings.Contains(string(f.cnps[cnpPath(ns, "serendip-l7-openchoreo-cmp-ac7df9f12c")].Spec), `"k8s:k":"v"`) {
		t.Error("the new peer wasn't mirrored")
	}

	// The NetworkPolicy is re-created (a new UID): the owner follows.
	bodies[srvPath] = strings.Replace(srvPolicy, "ec880068-6845-47e2-bbfd-cc6285908414", "11111111-2222-3333-4444-555555555555", 1)
	rep, err = p.EnsureVisibility(ctx, []Target{srvTarget})
	if err != nil || len(rep.Applied) != 1 {
		t.Fatalf("after a new owner UID = %+v, %v; want applied", rep, err)
	}
	if o := f.cnps[cnpPath(ns, "serendip-l7-openchoreo-cmp-ac7df9f12c")].Metadata.OwnerReferences; len(o) != 1 || o[0].UID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("owner = %+v, want the new UID", o)
	}

	// No target any more (the port isn't exposed): deleted, by name.
	rep, err = p.EnsureVisibility(ctx, nil)
	if err != nil || !slices.Equal(rep.Deleted, []string{srvCNP}) || len(f.cnps) != 0 {
		t.Fatalf("no targets = %+v, %v, %d left; want deleted", rep, err, len(f.cnps))
	}
}

// TestEnsureVisibilityPorts: duplicate and unsorted ports, and targets
// naming one NetworkPolicy twice, are merged, and the caller's slices are
// left as they were.
func TestEnsureVisibilityPorts(t *testing.T) {
	bodies := baseBodies(pinnedConfig())
	// Two rules: the first admits every port, the second port 8080 only.
	bodies[srvPath] = strings.Replace(srvPolicy, `"ports":[{"port":80,"protocol":"TCP"}]},`, `"ports":[]},`, 1)
	bodies[srvPath] = strings.Replace(bodies[srvPath].(string), `"ports":[{"port":80,"protocol":"TCP"}]}],`, `"ports":[{"port":8080,"protocol":"TCP"}]}],`, 1)
	f := newFake(bodies)
	a := []int{8080, 80, 8080, 0, 70000}
	b := []int{80}
	rep, err := New(f).EnsureVisibility(context.Background(), []Target{
		{Namespace: ns, Policy: srvTarget.Policy, HTTPPorts: a},
		{Namespace: ns, Policy: srvTarget.Policy, HTTPPorts: b},
	})
	if err != nil || !slices.Equal(rep.Applied, []string{guardKey, srvCNP}) {
		t.Fatalf("pass = %+v, %v", rep, err)
	}
	if !slices.Equal(a, []int{8080, 80, 8080, 0, 70000}) || !slices.Equal(b, []int{80}) {
		t.Errorf("the caller's ports changed: %v, %v", a, b)
	}
	var spec cnpSpec
	json.Unmarshal(f.cnps[cnpPath(ns, "serendip-l7-openchoreo-cmp-ac7df9f12c")].Spec, &spec)
	var got [][]string
	for _, r := range spec.Ingress {
		var ports []string
		for _, pr := range r.ToPorts {
			for _, port := range pr.Ports {
				ports = append(ports, port.Port)
			}
		}
		got = append(got, ports)
	}
	if want := [][]string{{"80", "8080"}, {"8080"}}; len(got) != 2 || !slices.Equal(got[0], want[0]) || !slices.Equal(got[1], want[1]) {
		t.Errorf("ports per rule = %v, want %v", got, want)
	}
}

// TestEnsureVisibilityForeignFields: a policy someone added a field to is
// deleted and made again, since an apply wouldn't remove that field.
func TestEnsureVisibilityForeignFields(t *testing.T) {
	ctx := context.Background()
	bodies := baseBodies(pinnedConfig())
	bodies[srvPath] = srvPolicy
	f := newFake(bodies)
	p := New(f)
	if _, err := p.EnsureVisibility(ctx, []Target{srvTarget}); err != nil {
		t.Fatal(err)
	}
	path := cnpPath(ns, "serendip-l7-openchoreo-cmp-ac7df9f12c")
	obj := f.cnps[path]
	obj.Spec = json.RawMessage(strings.Replace(string(obj.Spec), `"ingress":[`, `"ingress":[{"fromEntities":["world"]},`, 1))
	f.cnps[path] = obj
	rep, err := p.EnsureVisibility(ctx, []Target{srvTarget})
	if err != nil || len(rep.Applied) != 1 || len(f.deletes) != 1 {
		t.Fatalf("pass = %+v, %v, %d deletes; want remade", rep, err, len(f.deletes))
	}
	if strings.Contains(string(f.cnps[path].Spec), "fromEntities") {
		t.Error("the added field survived")
	}
}

// TestEnsureVisibilityDisabledOnProxyDrift: a proxy that would retry
// failures mustn't be put in front of anything, so every policy goes.
func TestEnsureVisibilityDisabledOnProxyDrift(t *testing.T) {
	ctx := context.Background()
	bodies := baseBodies(pinnedConfig())
	bodies[srvPath] = srvPolicy
	f := newFake(bodies)
	p := New(f)
	if _, err := p.EnsureVisibility(ctx, []Target{srvTarget}); err != nil || len(f.cnps) != 1 {
		t.Fatalf("setup: %v, %d policies", err, len(f.cnps))
	}
	cfg := pinnedConfig()
	cfg["http-retry-count"] = "3"
	bodies[configPath] = configBody(cfg)
	rep, err := p.EnsureVisibility(ctx, []Target{srvTarget})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Deleted) != 1 || len(f.cnps) != 0 || !strings.Contains(rep.Disabled, "retries failed requests") {
		t.Errorf("report = %+v, %d left; want all removed with the reason", rep, len(f.cnps))
	}
}

func TestEnsureVisibilityErrors(t *testing.T) {
	ctx := context.Background()

	// One target's NetworkPolicy can't be read: its policy is kept, the
	// other target still gets its own, and Facts says so.
	otherPath := "/apis/networking.k8s.io/v1/namespaces/" + ns + "/networkpolicies/openchoreo-cmp-other"
	other := Target{Namespace: ns, Policy: "openchoreo-cmp-other", HTTPPorts: []int{80}}
	bodies := baseBodies(pinnedConfig())
	bodies[srvPath] = srvPolicy
	bodies[otherPath] = strings.ReplaceAll(srvPolicy, "openchoreo-cmp-ac7df9f12c", "openchoreo-cmp-other")
	f := newFake(bodies)
	p := New(f)
	if _, err := p.EnsureVisibility(ctx, []Target{srvTarget, other}); err != nil || len(f.cnps) != 2 {
		t.Fatalf("setup: %v, %d policies", err, len(f.cnps))
	}
	bodies[srvPath] = errors.New("forbidden")
	bodies[otherPath] = strings.Replace(bodies[otherPath].(string), `"ports":[{"port":80,"protocol":"TCP"}]}],`, `"ports":[{"port":80,"protocol":"TCP"}]},{"from":[{"podSelector":{"matchLabels":{"k":"v"}}}],"ports":[{"port":80}]}],`, 1)
	rep, err := p.EnsureVisibility(ctx, []Target{srvTarget, other})
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("unreadable NetworkPolicy: err = %v", err)
	}
	if len(rep.Applied) != 1 || len(rep.Deleted) != 0 || len(rep.Failed) != 1 || len(f.cnps) != 2 {
		t.Errorf("report = %+v, %d policies; want the other applied and srv's kept", rep, len(f.cnps))
	}
	facts, _ := p.Facts(ctx)
	if !slices.ContainsFunc(facts.Unavailable, func(s string) bool { return strings.Contains(s, "failed 1 times") }) {
		t.Errorf("Facts().Unavailable = %q, want the failure counted", facts.Unavailable)
	}

	bodies = baseBodies(pinnedConfig())
	bodies[srvPath] = srvPolicy
	bodies["cnps"] = errors.New("the server could not find the requested resource")
	if _, err := New(newFake(bodies)).EnsureVisibility(ctx, []Target{srvTarget}); err == nil {
		t.Error("no CiliumNetworkPolicy API: want an error")
	}

	bodies = baseBodies(pinnedConfig())
	bodies[srvPath] = srvPolicy
	f = newFake(bodies)
	f.applyErr = errors.New("admission denied")
	rep, err = New(f).EnsureVisibility(ctx, []Target{srvTarget})
	// The guard's apply and the policy's both fail.
	if err == nil || len(rep.Applied) != 0 || len(rep.Failed) != 2 {
		t.Errorf("failed apply: %+v, %v", rep, err)
	}

	rep, err = New(newFake(baseBodies(pinnedConfig()))).EnsureVisibility(ctx, []Target{{Namespace: ns, Policy: "p"}})
	if err != nil || len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0].Reason, "no HTTP port") {
		t.Errorf("target without ports: %+v, %v", rep, err)
	}

	rep, err = New(newFake(baseBodies(pinnedConfig()))).EnsureVisibility(ctx, []Target{{Namespace: ns, Policy: strings.Repeat("p", 250), HTTPPorts: []int{80}}})
	if err != nil || len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0].Reason, "too long") {
		t.Errorf("long name: %+v, %v", rep, err)
	}

	// Already gone when deleted: not counted as a delete.
	bodies = baseBodies(pinnedConfig())
	f = newFake(bodies)
	f.cnps[cnpPath(ns, "serendip-l7-x")] = kubernetes.Object{Metadata: kubernetes.ObjectMeta{Name: "serendip-l7-x", Namespace: ns}}
	f.goneOnDelete = true
	rep, err = New(f).EnsureVisibility(ctx, nil)
	if err != nil || len(rep.Deleted) != 0 {
		t.Errorf("delete of a gone policy: %+v, %v", rep, err)
	}

	// Cancelled: stops, says why, writes nothing.
	bodies = baseBodies(pinnedConfig())
	bodies[srvPath] = srvPolicy
	f = newFake(bodies)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := New(f).EnsureVisibility(cctx, []Target{srvTarget}); !errors.Is(err, context.Canceled) || len(f.applies) != 0 {
		t.Errorf("cancelled: err = %v, %d applies", err, len(f.applies))
	}
}
