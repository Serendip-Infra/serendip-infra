package cilium

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

// wantGuard is the node guard's spec as the server should hold it.
const wantGuard = `{"enableDefaultDeny":{"ingress":false,"egress":false},
 "endpointSelector":{"matchExpressions":[{"key":"k8s:io.kubernetes.pod.namespace","operator":"NotIn","values":["kube-system","openchoreo-observability-plane"]}]},
 "egressDeny":[{"toEntities":["host","remote-node"],"toPorts":[{"ports":[
  {"port":"9962","protocol":"TCP"},{"port":"9963","protocol":"TCP"},{"port":"9964","protocol":"TCP"},{"port":"9965","protocol":"TCP"}]}]}]}`

func TestNodeGuard(t *testing.T) {
	ctx := context.Background()
	f := newFake(baseBodies(pinnedConfig()))
	p := New(f)

	// Made on the first pass, with no targets at all.
	rep, err := p.EnsureVisibility(ctx, nil)
	if err != nil || !slices.Equal(rep.Applied, []string{guardKey}) || f.guardApplies != 1 {
		t.Fatalf("first pass = %+v, %v; %d guard writes", rep, err, f.guardApplies)
	}
	if compact(t, string(f.guard.Spec)) != compact(t, wantGuard) || f.guard.Metadata.Labels[managedLabel] != guardValue ||
		f.guard.Kind != "CiliumClusterwideNetworkPolicy" || f.guard.Metadata.Namespace != "" {
		t.Errorf("guard = %+v, spec %s", f.guard, f.guard.Spec)
	}

	// Unchanged: no write.
	if rep, err := p.EnsureVisibility(ctx, nil); err != nil || len(rep.Applied) != 0 || f.guardApplies != 1 {
		t.Errorf("second pass = %+v, %v; %d guard writes", rep, err, f.guardApplies)
	}

	// A field someone added (an allow) is removed: the guard is remade.
	var spec map[string]any
	_ = json.Unmarshal(f.guard.Spec, &spec)
	spec["egress"] = []any{map[string]any{"toEntities": []string{"host"}}}
	f.guard.Spec, _ = json.Marshal(spec)
	if rep, err := p.EnsureVisibility(ctx, nil); err != nil || !slices.Equal(rep.Applied, []string{guardKey}) || f.guardApplies != 2 {
		t.Errorf("foreign field: %+v, %v; %d guard writes", rep, err, f.guardApplies)
	}
	if compact(t, string(f.guard.Spec)) != compact(t, wantGuard) {
		t.Errorf("remade guard = %s", f.guard.Spec)
	}

	// A changed spec is put back.
	f.guard.Spec = []byte(`{"enableDefaultDeny":{"ingress":false,"egress":false},"endpointSelector":{},"egressDeny":[]}`)
	if rep, _ := p.EnsureVisibility(ctx, nil); !slices.Equal(rep.Applied, []string{guardKey}) || compact(t, string(f.guard.Spec)) != compact(t, wantGuard) {
		t.Errorf("drifted guard: %+v, %s", rep, f.guard.Spec)
	}
}

// The guard stays while request visibility is off: the metrics ports
// are open either way.
func TestNodeGuardKeptWhenDisabled(t *testing.T) {
	cfg := pinnedConfig()
	cfg["http-retry-count"] = "3"
	f := newFake(baseBodies(cfg))
	rep, err := New(f).EnsureVisibility(context.Background(), []Target{srvTarget})
	if err != nil || rep.Disabled == "" || f.guard == nil {
		t.Errorf("pass = %+v, %v; guard %v", rep, err, f.guard)
	}
}

// A guard that can't be read is a failure the pass names, and the rest of
// the pass goes on.
func TestNodeGuardReadError(t *testing.T) {
	bodies := baseBodies(pinnedConfig())
	bodies[srvPath] = srvPolicy
	bodies[guardPath()] = errors.New("the server could not find the requested resource")
	f := newFake(bodies)
	rep, err := New(f).EnsureVisibility(context.Background(), []Target{srvTarget})
	if err == nil || len(rep.Failed) != 1 || !slices.Equal(rep.Applied, []string{srvCNP}) {
		t.Errorf("pass = %+v, %v", rep, err)
	}
}
