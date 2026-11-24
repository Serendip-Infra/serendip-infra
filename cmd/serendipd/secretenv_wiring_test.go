package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/serendip-infra/serendip-infra/core/graph"
	"github.com/serendip-infra/serendip-infra/core/resource"
)

type dependents map[graph.ResourceRef][]graph.ResourceRef

func (d dependents) Dependents(_ context.Context, ref graph.ResourceRef) ([]graph.ResourceRef, error) {
	if ref.ID == "broken" {
		return nil, errors.New("db gone")
	}
	return d[ref], nil
}

// TestEnqueueComponentDeployments: a Component's secret variables changing
// reconciles its Deployments, and only them (spec 026 plan decision 4).
func TestEnqueueComponentDeployments(t *testing.T) {
	web := graph.ResourceRef{Kind: resource.KindComponent, ID: "web"}
	dev := graph.ResourceRef{Kind: resource.KindDeployment, ID: "dev"}
	prod := graph.ResourceRef{Kind: resource.KindDeployment, ID: "prod"}
	caller := graph.ResourceRef{Kind: resource.KindComponent, ID: "shop"}
	g := dependents{
		web: {dev, caller, prod},
		{Kind: resource.KindComponent, ID: "lib"}: {caller},
	}
	for _, tc := range []struct {
		name, component string
		want            []graph.ResourceRef
	}{
		{"its Deployments", "web", []graph.ResourceRef{dev, prod}},
		{"no dependents", "lonely", nil},
		{"only other Components", "lib", nil},
		{"a failed read: resync covers it", "broken", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []graph.ResourceRef
			enqueueComponentDeployments(g, func(r graph.ResourceRef) { got = append(got, r) })(context.Background(), tc.component)
			if !slices.Equal(got, tc.want) {
				t.Errorf("enqueued %v, want %v", got, tc.want)
			}
		})
	}
}
