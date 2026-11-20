package api

import (
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/serendip-infra/serendip-infra/core/event"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/store/sqlite"
)

type recordingMux struct{ patterns []string }

func (m *recordingMux) HandleFunc(pattern string, _ func(http.ResponseWriter, *http.Request)) {
	m.patterns = append(m.patterns, pattern)
}

// TestEveryRouteHasARule is acceptance criterion 9: every /v1 route
// NewServer registers, with the optional secret routes included, has an
// authorization rule, and the table names no route that doesn't exist.
func TestEveryRouteHasARule(t *testing.T) {
	ctx := t.Context()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m := &recordingMux{}
	// A zero reconciler is enough: registering never calls it.
	registerAPI(m, db, event.NewInProcessBus(), nil, nil, &reconcile.SecretReconciler{}, nil, nil, serverOptions{})

	table := routeTable()
	for _, p := range m.patterns {
		if _, ok := table[p]; !ok {
			t.Errorf("route %q has no authorization rule", p)
		}
	}
	for p := range table {
		if !slices.Contains(m.patterns, p) {
			t.Errorf("rule for %q matches no registered route", p)
		}
	}
	if len(m.patterns) < 80 {
		t.Errorf("only %d routes registered; the enumeration looks incomplete", len(m.patterns))
	}
}

// TestAlertingRouteRules is spec 024's rows in the route table (plan
// decisions 9, 12 and 15): each route's action and where it's decided.
func TestAlertingRouteRules(t *testing.T) {
	type want struct {
		action string
		target targetKind
		list   bool
	}
	rows := map[string]want{
		"POST /v1/alertrules":                     {"serendip/alertrules/write", targetParent, false},
		"GET /v1/alertrules":                      {"serendip/alertrules/read", targetResource, true},
		"GET /v1/alertrules/{id}":                 {"serendip/alertrules/read", targetResource, false},
		"PUT /v1/alertrules/{id}":                 {"serendip/alertrules/write", targetResource, false},
		"DELETE /v1/alertrules/{id}":              {"serendip/alertrules/delete", targetResource, false},
		"POST /v1/alertrules/{id}/silence":        {"serendip/alertrules/silence/action", targetResource, false},
		"POST /v1/alertrules/{id}/unsilence":      {"serendip/alertrules/silence/action", targetResource, false},
		"GET /v1/alerts":                          {"serendip/alerts/read", targetResource, true},
		"GET /v1/alerts/summary":                  {"serendip/alerts/read", targetResource, true},
		"GET /v1/alerts/{id}":                     {"serendip/alerts/read", targetResource, false},
		"POST /v1/alerts/{id}/acknowledge":        {"serendip/alerts/acknowledge/action", targetResource, false},
		"POST /v1/notificationchannels":           {"serendip/notificationchannels/write", targetParent, false},
		"GET /v1/notificationchannels":            {"serendip/notificationchannels/read", targetResource, true},
		"GET /v1/notificationchannels/{id}":       {"serendip/notificationchannels/read", targetResource, false},
		"PUT /v1/notificationchannels/{id}":       {"serendip/notificationchannels/write", targetResource, false},
		"DELETE /v1/notificationchannels/{id}":    {"serendip/notificationchannels/delete", targetResource, false},
		"POST /v1/notificationchannels/{id}/test": {"serendip/notificationchannels/test/action", targetResource, false},
		"POST /v1/smtpservers":                    {"serendip/smtpservers/write", targetRoot, false},
		"GET /v1/smtpservers":                     {"serendip/smtpservers/read", targetResource, true},
		"GET /v1/smtpservers/{id}":                {"serendip/smtpservers/read", targetResource, false},
		"PUT /v1/smtpservers/{id}":                {"serendip/smtpservers/write", targetRoot, false},
		"DELETE /v1/smtpservers/{id}":             {"serendip/smtpservers/delete", targetResource, false},
		"GET /v1/heartbeat":                       {"serendip/heartbeat/read", targetRoot, false},
		"PUT /v1/heartbeat":                       {"serendip/heartbeat/write", targetRoot, false},
		"DELETE /v1/heartbeat":                    {"serendip/heartbeat/delete", targetSingleton, false},
		"POST /v1/heartbeat/test":                 {"serendip/heartbeat/write", targetRoot, false},
	}
	table := routeTable()
	for route, w := range rows {
		ru, ok := table[route]
		if !ok {
			t.Errorf("%s: no rule", route)
			continue
		}
		if ru.action.Name != w.action || ru.list != w.list || (!w.list && ru.target != w.target) || ru.authOnly {
			t.Errorf("%s: rule %+v, want action %s, target %d, list %t", route, ru, w.action, w.target, w.list)
		}
	}
	for route := range table {
		for _, prefix := range []string{"/v1/alert", "/v1/notificationchannels", "/v1/smtpservers", "/v1/heartbeat"} {
			if _, ok := rows[route]; !ok && containsPath(route, prefix) {
				t.Errorf("%s has a rule but no row in this test", route)
			}
		}
	}
}

func containsPath(route, prefix string) bool {
	_, path, _ := strings.Cut(route, " ")
	return strings.HasPrefix(path, prefix)
}

// TestUnmappedRouteFailsAtStartup: registering a route the table doesn't
// know panics, so a server can't start with an unchecked route.
func TestUnmappedRouteFailsAtStartup(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("registering an unmapped route didn't panic")
		}
	}()
	m := &authzMux{mux: http.NewServeMux(), a: &authorizer{table: routeTable()}}
	m.HandleFunc("GET /v1/unmapped", func(http.ResponseWriter, *http.Request) {})
}
