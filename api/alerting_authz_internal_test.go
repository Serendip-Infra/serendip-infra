package api

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/serendip-infra/serendip-infra/core/resource"
)

// The heartbeat's DELETE names the singleton's ID for its Approval, and
// an approval request may name the heartbeat (spec 024 decisions 10, 15).
func TestHeartbeatDeleteIsDecidedAtTheSingleton(t *testing.T) {
	ru := routeTable()["DELETE /v1/heartbeat"]
	for _, tt := range []struct {
		name   string
		id     string
		wantID string
		want   resource.Scope
	}{
		{"set", "hb1", "hb1", resource.Scope{Kind: resource.KindHeartbeat, ID: "hb1"}},
		{"none", "", "", resource.RootScope},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := &authorizer{singleton: func(_ context.Context, kind resource.Kind) (string, bool, error) {
				if kind != resource.KindHeartbeat {
					t.Errorf("singleton looked up for %s", kind)
				}
				return tt.id, tt.id != "", nil
			}}
			got, id, err := a.checks(t.Context(), httptest.NewRequest("DELETE", "/v1/heartbeat", nil), ru)
			if err != nil {
				t.Fatal(err)
			}
			want := []check{{action(resource.KindHeartbeat, "delete"), tt.want}}
			if id != tt.wantID || !slices.Equal(got, want) {
				t.Errorf("checks = %v, %q; want %v, %q", got, id, want, tt.wantID)
			}
		})
	}

	body := `{"action": {"verb": "delete", "target": {"kind": "Heartbeat", "id": "01a1dda3-0000-7000-8000-000000000001"}}, "reason": "moving monitors"}`
	act, err := approvalBodyAction(httptest.NewRequest("POST", "/v1/approvals", strings.NewReader(body)))
	if err != nil || act.Target.Kind != resource.KindHeartbeat {
		t.Errorf("approval request for the heartbeat: %+v, %v", act, err)
	}
	body = `{"action": {"verb": "delete", "target": {"kind": "Alert", "id": "01a1dda3-0000-7000-8000-000000000001"}}, "reason": "x"}`
	if _, err := approvalBodyAction(httptest.NewRequest("POST", "/v1/approvals", strings.NewReader(body))); err == nil {
		t.Error("an Alert, which has no delete, could be requested")
	}
}
