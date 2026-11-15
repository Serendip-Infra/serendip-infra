package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/policy"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store/sqlite"
)

// Spec 024 T007: Alerts, acknowledgement and silences through the API.

// alertsWorld has a rule on Project A's web Component and one on Project
// B's Application, each with Alerts serendipd would have opened.
type alertsWorld struct {
	*authzWorld
	ruleA, ruleB string
	alerts       map[string]string // name → ID
}

func newAlertsWorld(t *testing.T) *alertsWorld {
	w := &alertsWorld{authzWorld: newAuthzWorld(t, func(w *authzWorld) {
		w.grant("tok-reader", policy.ReaderRoleID, "projA")
		w.grant("tok-contrib", policy.ContributorRoleID, "projA")
	}), alerts: map[string]string{}}
	w.ruleA = w.mkAs("key-ci", "/v1/alertrules", alertRule("cpu-a", w.scope("web"), w.id["prod"]))
	w.ruleB = w.mkAs("key-ci", "/v1/alertrules", alertRule("cpu-b", w.scope("appB"), w.id["prod"]))
	store := sqlite.NewStore[resource.AlertSpec, resource.AlertStatus](w.db, resource.KindAlert)
	start := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	mk := func(name, rule, comp string, sev resource.Severity, state string, at int) {
		t.Helper()
		env := w.id["prod"]
		a, err := store.Create(context.Background(), resource.Alert{
			Metadata: resource.Metadata{Name: name, OwnerRefs: []resource.OwnerRef{{Kind: resource.KindAlertRule, ID: rule}}},
			Spec: resource.AlertSpec{RuleID: rule, ComponentID: comp, EnvironmentID: env, Severity: sev, Summary: name,
				Fingerprint: resource.AlertFingerprint(rule, comp, env)},
			Status: resource.AlertStatus{State: state, StartedAt: start.Add(time.Duration(at) * time.Minute), Value: 97, Threshold: 90},
		})
		if err != nil {
			t.Fatal(err)
		}
		w.alerts[name] = a.Metadata.ID
	}
	mk("a-critical", w.ruleA, w.id["web"], resource.SeverityCritical, resource.AlertFiring, 3)
	mk("a-warning", w.ruleA, w.id["web"], resource.SeverityWarning, resource.AlertFiring, 2)
	mk("a-resolved", w.ruleA, w.id["web"], resource.SeverityWarning, resource.AlertResolved, 1)
	mk("b-critical", w.ruleB, "", resource.SeverityCritical, resource.AlertFiring, 4)
	return w
}

func (w *alertsWorld) list(token, query string) []string {
	w.t.Helper()
	got := w.do(token, http.MethodGet, "/v1/alerts"+query, nil)
	if got.Code != http.StatusOK {
		w.t.Fatalf("list %s: %d %s", query, got.Code, got.Body)
	}
	var items []resource.Alert
	if err := json.Unmarshal(got.Body.Bytes(), &items); err != nil {
		w.t.Fatal(err)
	}
	out := []string{}
	for _, a := range items {
		out = append(out, a.Metadata.Name)
	}
	return out
}

func TestAlerts_ScopedToTheCallersProjects(t *testing.T) {
	w := newAlertsWorld(t)
	all := []string{"b-critical", "a-critical", "a-warning", "a-resolved"}
	mine := []string{"a-critical", "a-warning", "a-resolved"}
	for _, tt := range []struct {
		token, query string
		want         []string
	}{
		{"key-ci", "", all},
		{"tok-reader", "", mine},
		{"tok-nobody", "", []string{}},
		{"tok-reader", "?state=firing", []string{"a-critical", "a-warning"}},
		{"tok-reader", "?severity=critical", []string{"a-critical"}},
		{"key-ci", "?severity=critical", []string{"b-critical", "a-critical"}},
		{"key-ci", "?componentId=" + w.id["web"], mine},
		{"key-ci", "?applicationId=" + w.id["app"], mine},
		{"key-ci", "?applicationId=" + w.id["devapp"], []string{}},
		{"key-ci", "?environmentId=" + w.id["dev"], []string{}},
		{"key-ci", "?limit=2", all[:2]},
	} {
		if got := w.list(tt.token, tt.query); !slices.Equal(got, tt.want) {
			t.Errorf("%s %q: %v, want %v", people[tt.token].Name, tt.query, got, tt.want)
		}
	}
	for _, q := range []string{"?state=open", "?severity=page", "?limit=0", "?limit=501"} {
		w.expect("key-ci", http.MethodGet, "/v1/alerts"+q, nil, http.StatusBadRequest)
	}

	w.expect("tok-reader", http.MethodGet, "/v1/alerts/"+w.alerts["a-critical"], nil, http.StatusOK)
	w.expect("tok-reader", http.MethodGet, "/v1/alerts/"+w.alerts["b-critical"], nil, http.StatusForbidden)

	var sum struct {
		Firing         map[string]int `json:"firing"`
		Total          int            `json:"total"`
		Unacknowledged int            `json:"unacknowledged"`
	}
	got := w.do("tok-reader", http.MethodGet, "/v1/alerts/summary", nil)
	if err := json.Unmarshal(got.Body.Bytes(), &sum); err != nil || got.Code != http.StatusOK {
		t.Fatalf("summary: %d %s", got.Code, got.Body)
	}
	if sum.Total != 2 || sum.Firing["critical"] != 1 || sum.Firing["warning"] != 1 || sum.Firing["info"] != 0 || sum.Unacknowledged != 2 {
		t.Errorf("a Reader's summary = %+v, want Project A's two firing", sum)
	}
}

func TestAlerts_AcknowledgeIsAudited(t *testing.T) {
	w := newAlertsWorld(t)
	path := "/v1/alerts/" + w.alerts["a-critical"] + "/acknowledge"
	w.expect("tok-reader", http.MethodPost, path, nil, http.StatusForbidden)
	w.expect("tok-contrib", http.MethodPost, "/v1/alerts/"+w.alerts["b-critical"]+"/acknowledge", nil, http.StatusForbidden)

	got := w.do("tok-contrib", http.MethodPost, path, nil)
	var a resource.Alert
	if err := json.Unmarshal(got.Body.Bytes(), &a); err != nil || got.Code != http.StatusOK {
		t.Fatalf("acknowledge: %d %s", got.Code, got.Body)
	}
	if a.Status.Acknowledged == nil || a.Status.Acknowledged.By != "person:contrib" {
		t.Errorf("acknowledged = %+v, want by person:contrib", a.Status.Acknowledged)
	}
	// Again changes nothing.
	w.expect("tok-contrib", http.MethodPost, path, nil, http.StatusOK)

	rows, err := w.db.Query(`SELECT actor, interface, COALESCE(policy_decision,''), COALESCE(before,''), COALESCE(after,'') FROM audit_log WHERE resource_id = ? AND actor = 'person:contrib'`, w.alerts["a-critical"])
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var n int
	for rows.Next() {
		var actor, iface, decision, before, after string
		if err := rows.Scan(&actor, &iface, &decision, &before, &after); err != nil {
			t.Fatal(err)
		}
		n++
		if iface != "http" || !strings.HasPrefix(decision, "allow") {
			t.Errorf("audit row: actor %s, interface %q, decision %q; want http and the allowing decision", actor, iface, decision)
		}
		if strings.Contains(before, `"acknowledged"`) || !strings.Contains(after, `"by":"person:contrib"`) {
			t.Errorf("audit row's snapshots don't show the acknowledgement: before %s, after %s", before, after)
		}
	}
	if n != 1 {
		t.Errorf("%d audit rows for the acknowledgement, want 1", n)
	}
}

func TestAlerts_Silence(t *testing.T) {
	w := newAlertsWorld(t)
	silence := "/v1/alertrules/" + w.ruleA + "/silence"
	until := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	ok := map[string]any{"until": until, "reason": "deploying a fix"}
	w.expect("tok-reader", http.MethodPost, silence, ok, http.StatusForbidden)
	for _, bad := range []map[string]any{
		{"until": until},
		{"until": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), "reason": "x"},
		{"until": time.Now().Add(31 * 24 * time.Hour).UTC().Format(time.RFC3339), "reason": "x"},
		{"reason": "no end"},
	} {
		w.expect("tok-contrib", http.MethodPost, silence, bad, http.StatusBadRequest)
	}
	got := w.do("tok-contrib", http.MethodPost, silence, ok)
	var r resource.AlertRule
	if err := json.Unmarshal(got.Body.Bytes(), &r); err != nil || got.Code != http.StatusOK {
		t.Fatalf("silence: %d %s", got.Code, got.Body)
	}
	if s := r.Status.Silence; s == nil || s.By != "person:contrib" || s.Reason != "deploying a fix" {
		t.Errorf("silence = %+v", s)
	}
	unsilence := "/v1/alertrules/" + w.ruleA + "/unsilence"
	w.expect("tok-reader", http.MethodPost, unsilence, nil, http.StatusForbidden)
	got = w.do("tok-contrib", http.MethodPost, unsilence, nil)
	r = resource.AlertRule{}
	if err := json.Unmarshal(got.Body.Bytes(), &r); err != nil || got.Code != http.StatusOK || r.Status.Silence != nil {
		t.Errorf("unsilence: %d %s", got.Code, got.Body)
	}
	w.expect("tok-contrib", http.MethodPost, unsilence, nil, http.StatusOK)
	w.expect("tok-contrib", http.MethodPost, "/v1/alertrules/"+w.ruleB+"/silence", ok, http.StatusForbidden)
}
