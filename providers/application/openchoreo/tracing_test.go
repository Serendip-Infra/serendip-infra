package openchoreo

import (
	"context"
	"encoding/json"
	"maps"
	"testing"

	"github.com/serendip-infra/serendip-infra/core/resource"
)

const testTraceEndpoint = "http://collector.example:4318"

func releaseEnv(t *testing.T, api *fakeAPI, name string) map[string]string {
	t.Helper()
	var spec struct {
		Workload struct {
			Container workloadContainer `json:"container"`
		} `json:"workload"`
	}
	if err := json.Unmarshal(api.objects[nsItem("componentreleases", name)].Spec, &spec); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, v := range spec.Workload.Container.Env {
		out[v.Key] = v.Value
	}
	return out
}

// The trace variables join a Component's next new release, never one it
// already has: setting the endpoint rolls nothing out (plan decision 11).
func TestTraceEnv_NextNewRelease(t *testing.T) {
	ctx := context.Background()
	api := newFakeAPI().withComponentType()
	p := NewProvider(newTestKubeClient(t, api.handler(t)), testGateway)
	before, err := p.CreateRelease(ctx, testComp(webSpec))
	if err != nil {
		t.Fatal(err)
	}

	p.SetTraceEndpoint(testTraceEndpoint)
	api.writes = 0
	same, err := p.CreateRelease(ctx, testComp(webSpec))
	if err != nil || same.Name != before.Name || api.writes != 0 {
		t.Fatalf("with tracing on, the unchanged spec = %+v, %v, %d writes; want the existing release, no write", same, err, api.writes)
	}
	if env := releaseEnv(t, api, before.Name); env[envOTLPEndpoint] != "" {
		t.Errorf("the existing release gained %s", envOTLPEndpoint)
	}

	changed := webSpec
	changed.Image = "nginx:1.28-alpine"
	next, err := p.CreateRelease(ctx, testComp(changed))
	if err != nil {
		t.Fatal(err)
	}
	env := releaseEnv(t, api, next.Name)
	if env[envOTLPEndpoint] != testTraceEndpoint || env[envServiceName] != "web" || env["A"] != "1" || env["B"] != "2" {
		t.Errorf("the next release's env = %v", env)
	}
	// Again: the same release, by its content.
	if again, err := p.CreateRelease(ctx, testComp(changed)); err != nil || again.Name != next.Name {
		t.Errorf("the traced release again = %+v, %v; want %s", again, err, next.Name)
	}

	// Off again: new releases don't carry it.
	p.SetTraceEndpoint("")
	other := webSpec
	other.Image = "nginx:1.29-alpine"
	off, err := p.CreateRelease(ctx, testComp(other))
	if err != nil {
		t.Fatal(err)
	}
	if env := releaseEnv(t, api, off.Name); env[envOTLPEndpoint] != "" || env[envServiceName] != "" {
		t.Errorf("with tracing off, the release's env = %v", env)
	}
}

// A Component that sets a variable itself keeps its own.
func TestTraceEnv_OwnValueKept(t *testing.T) {
	comp := testComp(resource.ComponentSpec{Image: "web:1", Env: map[string]string{envServiceName: "checkout", "A": "1"}})
	c, ok := withTraceEnv(workloadContainer{Image: "web:1", Env: []envVar{{Key: "A", Value: "1"}, {Key: envServiceName, Value: "checkout"}}}, testTraceEndpoint, comp)
	if !ok {
		t.Fatal("nothing added")
	}
	got := map[string]string{}
	for _, v := range c.Env {
		got[v.Key] = v.Value
	}
	if want := map[string]string{"A": "1", envServiceName: "checkout", envOTLPEndpoint: testTraceEndpoint}; !maps.Equal(got, want) {
		t.Errorf("env = %v, want %v", got, want)
	}
	for k := 1; k < len(c.Env); k++ {
		if c.Env[k-1].Key > c.Env[k].Key {
			t.Errorf("env not sorted: %v", c.Env)
		}
	}
	both := testComp(resource.ComponentSpec{Env: map[string]string{envServiceName: "x", envOTLPEndpoint: "http://mine:4318"}})
	if _, ok := withTraceEnv(workloadContainer{}, testTraceEndpoint, both); ok {
		t.Error("added a variable to a Component setting both")
	}
	if _, ok := withTraceEnv(workloadContainer{}, "", comp); ok {
		t.Error("added variables with no endpoint")
	}
}
