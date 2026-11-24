package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/serendip-infra/serendip-infra/api"
	"github.com/serendip-infra/serendip-infra/core/policy"
	"github.com/serendip-infra/serendip-infra/core/provider"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store/sqlite"
)

// Spec 026 (secrets into Components): the API's part, T006. Criterion 6
// (authorization), 7 (the delete guard), 10 (validation) and the secrets
// status route.

// world026 is the deploy world with Secrets placed around its Project:
// in its Application, at its Organization, at the root, and in another
// Project.
type world026 struct {
	*deployWorld
	secret, orgSecret, rootSecret, otherSecret string
	changed                                    *changes026
}

// changes026 records the Component secret-variable hook's calls.
type changes026 struct {
	mu  sync.Mutex
	ids []string
}

func (c *changes026) record(_ context.Context, id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ids = append(c.ids, id)
}

func (c *changes026) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.ids
	c.ids = nil
	return out
}

func newWorld026(t *testing.T, opts ...api.ServerOption) *world026 {
	t.Helper()
	ch := &changes026{}
	d := newDeployWorldWith(t, provider.NewRegistry(), slices.Concat(opts, []api.ServerOption{api.WithSecretEnvChanged(ch.record)})...)
	w := &world026{deployWorld: d, changed: ch}
	secrets := sqlite.NewStore[resource.SecretSpec, resource.SecretStatus](d.db, resource.KindSecret)
	for _, s := range []struct {
		name  string
		owner []resource.OwnerRef
		id    *string
	}{
		{"db", []resource.OwnerRef{{Kind: resource.KindApplication, ID: d.app}}, &w.secret},
		{"shared", []resource.OwnerRef{{Kind: resource.KindOrganization, ID: d.id["org"]}}, &w.orgSecret},
		{"platform", nil, &w.rootSecret},
		{"theirs", []resource.OwnerRef{{Kind: resource.KindProject, ID: d.other}}, &w.otherSecret},
	} {
		sec, err := secrets.Create(context.Background(), resource.Secret{
			Metadata: resource.Metadata{Name: s.name, OwnerRefs: s.owner},
			Spec:     resource.SecretSpec{Path: "shop/" + s.name},
		})
		if err != nil {
			t.Fatal(err)
		}
		*s.id = sec.Metadata.ID
	}
	return w
}

func (w *world026) call(t *testing.T, token, method, path string, body any, want int) []byte {
	t.Helper()
	got := w.do(token, method, path, body)
	if got.Code != want {
		t.Fatalf("%s %s %s: %d, want %d (%s)", people[token].Name, method, path, got.Code, want, strings.TrimSpace(got.Body.String()))
	}
	return got.Body.Bytes()
}

func (w *world026) assignRole(t *testing.T, subject, role string, at resource.Scope) {
	t.Helper()
	w.call(t, "tok-owner", http.MethodPost, authzPath+"roleAssignments", assignment(resource.PrincipalPerson, subject, role, at), http.StatusCreated)
}

func (w *world026) component(t *testing.T) resource.Component {
	t.Helper()
	var c resource.Component
	if err := json.Unmarshal(w.call(t, "tok-owner", http.MethodGet, "/v1/components/"+w.web, nil, http.StatusOK), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func (w *world026) putComponent(t *testing.T, token string, c resource.Component, want int) (resource.Component, string) {
	t.Helper()
	body := w.call(t, token, http.MethodPut, "/v1/components/"+c.Metadata.ID, c, want)
	var out resource.Component
	if want == http.StatusOK {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
	}
	return out, string(body)
}

func secretVar(env, secret, key string) resource.ComponentSecretEnv {
	return resource.ComponentSecretEnv{Env: env, Secret: secret, Key: key}
}

// deniedRows counts audited refusals naming the value-read action.
func (w *world026) deniedRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := w.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE policy_decision LIKE 'deny:%serendip/secrets/value/read%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestSpec026Criterion6_Authorization: naming a Secret on a Component or a
// Deployment needs reading its value (403, audited); Secrets User on that
// Secret is enough; removing a reference, or editing other fields of a
// Component that names one, needs nothing more.
func TestSpec026Criterion6_Authorization(t *testing.T) {
	w := newWorld026(t)
	c := w.component(t)
	c.Spec.SecretEnv = []resource.ComponentSecretEnv{secretVar("DB_PASSWORD", w.secret, "password")}

	// A Contributor without a Secrets role.
	if _, body := w.putComponent(t, "tok-devops", c, http.StatusForbidden); !strings.Contains(body, "serendip/secrets/value/read") || !strings.Contains(body, w.secret) {
		t.Errorf("the Component refusal: %s", body)
	}
	if n := w.deniedRows(t); n != 1 {
		t.Errorf("audited refusals after the Component's: %d, want 1", n)
	}

	// With Secrets User on that Secret it succeeds, with the check in the
	// mutation's evidence.
	w.assignRole(t, "sub-devops", policy.SecretsUserRoleID, resource.Scope{Kind: resource.KindSecret, ID: w.secret})
	c, _ = w.putComponent(t, "tok-devops", c, http.StatusOK)
	var decision string
	if err := w.db.QueryRow(`SELECT policy_decision FROM audit_log WHERE resource_id = ? ORDER BY at DESC LIMIT 1`, c.Metadata.ID).Scan(&decision); err != nil || !strings.Contains(decision, "allow serendip/secrets/value/read at Secret/"+w.secret) {
		t.Errorf("the value check's evidence: %q (%v)", decision, err)
	}
	if !hasSecretEdge(c.Metadata.Relationships, w.secret) {
		t.Errorf("relationships = %+v, want a depends-on edge to the Secret", c.Metadata.Relationships)
	}
	if got := w.changed.take(); len(got) != 1 || got[0] != c.Metadata.ID {
		t.Errorf("secret-variable hook calls: %v, want the Component once", got)
	}

	// Another Contributor, without the role, edits other fields freely.
	w.assignRole(t, "sub-reader", policy.ContributorRoleID, resource.Scope{Kind: resource.KindProject, ID: w.proj})
	c.Spec.Ports = []resource.ComponentPort{{Name: "http", Port: 8080}}
	c, _ = w.putComponent(t, "tok-reader", c, http.StatusOK)
	if got := w.changed.take(); len(got) != 0 {
		t.Errorf("an edit that leaves the secret variables: hook called %v", got)
	}
	// Changing only the key is a value choice, so it's checked again; so is
	// a renamed variable.
	c.Spec.SecretEnv[0].Key = "admin-password"
	w.putComponent(t, "tok-reader", c, http.StatusForbidden)
	c.Spec.SecretEnv[0].Key, c.Spec.SecretEnv[0].Env = "password", "PGPASSWORD"
	w.putComponent(t, "tok-reader", c, http.StatusForbidden)
	if got := w.changed.take(); len(got) != 0 {
		t.Errorf("refused updates called the hook: %v", got)
	}
	// Removing the reference needs nothing, and is a change the
	// Deployments must hear of (their copy goes).
	c.Spec.SecretEnv = nil
	c, _ = w.putComponent(t, "tok-reader", c, http.StatusOK)
	if got := w.changed.take(); len(got) != 1 {
		t.Errorf("removal: hook calls %v, want one", got)
	}
	if hasSecretEdge(c.Metadata.Relationships, w.secret) {
		t.Errorf("the Secret edge outlived its reference: %+v", c.Metadata.Relationships)
	}

	// A Deployment's overrides, the same way.
	dep := w.deployment(w.dev, "")
	dep["spec"].(map[string]any)["overrides"] = map[string]any{"secretEnv": []any{map[string]any{"env": "API_TOKEN", "secret": w.secret, "key": "token"}}}
	before := w.deniedRows(t)
	if body := string(w.call(t, "tok-reader", http.MethodPost, "/v1/deployments", dep, http.StatusForbidden)); !strings.Contains(body, "serendip/secrets/value/read") {
		t.Errorf("the Deployment refusal: %s", body)
	}
	if n := w.deniedRows(t); n != before+1 {
		t.Errorf("audited refusals after the Deployment's: %d, want %d", n, before+1)
	}
	created := decodeDeployment(t, w.call(t, "tok-devops", http.MethodPost, "/v1/deployments", dep, http.StatusCreated))
	if !hasSecretEdge(created.Metadata.Relationships, w.secret) {
		t.Errorf("deployment relationships = %+v", created.Metadata.Relationships)
	}
	// Changing its release leaves the reference as it was: no check.
	created.Spec.Overrides.Env = map[string]string{"LOG_LEVEL": "debug"}
	updated := decodeDeployment(t, w.call(t, "tok-reader", http.MethodPut, "/v1/deployments/"+created.Metadata.ID, created, http.StatusOK))
	updated.Spec.Overrides.SecretEnv = nil
	w.call(t, "tok-reader", http.MethodPut, "/v1/deployments/"+created.Metadata.ID, updated, http.StatusOK)
}

func hasSecretEdge(rels []resource.Relationship, id string) bool {
	for _, r := range rels {
		if r.Type == resource.EdgeDependsOn && r.TargetKind == resource.KindSecret && r.TargetID == id {
			return true
		}
	}
	return false
}

// TestSpec026Criterion10_Validation: bad names, duplicates, a variable set
// both plainly and from a Secret, too many references, a Secret that
// doesn't exist or sits outside the Project's path, each refused naming
// the field.
func TestSpec026Criterion10_Validation(t *testing.T) {
	w := newWorld026(t)
	// The owner may read every value, so only the rule under test refuses.
	w.assignRole(t, "sub-owner", policy.SecretsUserRoleID, resource.RootScope)
	base := w.component(t)
	base.Spec.Env = map[string]string{"MODE": "prod"}
	base, _ = w.putComponent(t, "tok-owner", base, http.StatusOK)

	tooMany := make([]resource.ComponentSecretEnv, resource.MaxSecretEnv+1)
	for i := range tooMany {
		tooMany[i] = secretVar(fmt.Sprintf("V%d", i), w.secret, "k")
	}
	for _, tc := range []struct {
		name string
		list []resource.ComponentSecretEnv
		want int
		in   string
	}{
		{"bad variable name", []resource.ComponentSecretEnv{secretVar("1BAD", w.secret, "k")}, http.StatusBadRequest, "secretEnv[0].env"},
		{"duplicate", []resource.ComponentSecretEnv{secretVar("A", w.secret, "k"), secretVar("A", w.secret, "j")}, http.StatusBadRequest, "secretEnv[1].env"},
		{"also a plain variable", []resource.ComponentSecretEnv{secretVar("MODE", w.secret, "k")}, http.StatusBadRequest, "also set in env"},
		{"empty key", []resource.ComponentSecretEnv{secretVar("A", w.secret, "")}, http.StatusBadRequest, "secretEnv[0].key"},
		{"no Secret ID", []resource.ComponentSecretEnv{secretVar("A", "", "k")}, http.StatusBadRequest, "secretEnv[0].secret"},
		{"too many", tooMany, http.StatusBadRequest, "at most"},
		{"missing Secret", []resource.ComponentSecretEnv{secretVar("A", "no-such-secret", "k")}, http.StatusUnprocessableEntity, "secretEnv[0].secret: Secret no-such-secret not found"},
		{"another Project's Secret", []resource.ComponentSecretEnv{secretVar("A", w.secret, "k"), secretVar("B", w.otherSecret, "k")}, http.StatusUnprocessableEntity, "secretEnv[1].secret: the Secret must be in this Project or above it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			c.Spec.SecretEnv = tc.list
			if _, body := w.putComponent(t, "tok-owner", c, tc.want); !strings.Contains(body, tc.in) {
				t.Errorf("answer %s, want it to name %q", body, tc.in)
			}
		})
	}

	// In the Project, at its Organization, or at the root: all fine.
	c := base
	c.Spec.SecretEnv = []resource.ComponentSecretEnv{secretVar("A", w.secret, "k"), secretVar("B", w.orgSecret, "k"), secretVar("C", w.rootSecret, "k")}
	c, _ = w.putComponent(t, "tok-owner", c, http.StatusOK)

	// A caller without the value role learns nothing about a Secret's
	// existence: 403 comes first.
	w.assignRole(t, "sub-reader", policy.ContributorRoleID, resource.Scope{Kind: resource.KindProject, ID: w.proj})
	probe := c
	probe.Spec.SecretEnv = append(append([]resource.ComponentSecretEnv(nil), c.Spec.SecretEnv...), secretVar("D", "no-such-secret", "k"))
	w.putComponent(t, "tok-reader", probe, http.StatusForbidden)

	// Deployments: the overrides' own rules, and those across the layers.
	dep := func(overrides map[string]any) map[string]any {
		d := w.deployment(w.dev, "")
		d["spec"].(map[string]any)["overrides"] = overrides
		return d
	}
	for _, tc := range []struct {
		name      string
		overrides map[string]any
		want      int
		in        string
	}{
		{"shadows a Component plain variable", map[string]any{"secretEnv": []any{map[string]any{"env": "MODE", "secret": w.secret, "key": "k"}}}, http.StatusBadRequest, "plain variable of the Component"},
		{"plain over a Component secret", map[string]any{"env": map[string]any{"A": "x"}}, http.StatusBadRequest, "overrides.env: A is a secret variable"},
		{"both in the overrides", map[string]any{"env": map[string]any{"Z": "x"}, "secretEnv": []any{map[string]any{"env": "Z", "secret": w.secret, "key": "k"}}}, http.StatusBadRequest, "also set in overrides.env"},
		{"another Project's Secret", map[string]any{"secretEnv": []any{map[string]any{"env": "Z", "secret": w.otherSecret, "key": "k"}}}, http.StatusUnprocessableEntity, "overrides.secretEnv[0].secret: the Secret must be in this Project"},
		{"missing Secret", map[string]any{"secretEnv": []any{map[string]any{"env": "Z", "secret": "no-such-secret", "key": "k"}}}, http.StatusUnprocessableEntity, "overrides.secretEnv[0].secret: Secret no-such-secret not found"},
	} {
		t.Run("deployment/"+tc.name, func(t *testing.T) {
			if body := string(w.call(t, "tok-owner", http.MethodPost, "/v1/deployments", dep(tc.overrides), tc.want)); !strings.Contains(body, tc.in) {
				t.Errorf("answer %s, want it to name %q", body, tc.in)
			}
		})
	}
	// Replacing a Component secret variable for one Environment is fine.
	w.call(t, "tok-owner", http.MethodPost, "/v1/deployments", dep(map[string]any{
		"env":       map[string]any{"LOG_LEVEL": "debug"},
		"secretEnv": []any{map[string]any{"env": "A", "secret": w.orgSecret, "key": "dev"}},
	}), http.StatusCreated)

	// A Component edit that would clash with a Deployment's overrides is
	// refused, naming the Deployment.
	clash := c
	clash.Spec.SecretEnv = append(append([]resource.ComponentSecretEnv(nil), c.Spec.SecretEnv...), secretVar("LOG_LEVEL", w.secret, "k"))
	if _, body := w.putComponent(t, "tok-owner", clash, http.StatusConflict); !strings.Contains(body, "Deployment web") {
		t.Errorf("a clash with a Deployment's overrides: %s", body)
	}
	clash = c
	clash.Spec.Env = map[string]string{"MODE": "prod", "A": "plain"}
	clash.Spec.SecretEnv = slices.Clone(c.Spec.SecretEnv[1:])
	if _, body := w.putComponent(t, "tok-owner", clash, http.StatusConflict); !strings.Contains(body, "Deployment web") {
		t.Errorf("a plain variable under a Deployment's secret override: %s", body)
	}

	// The reserved configuration property isn't the API's to know: the
	// provider leaves it out of the schemas it reports, so the ordinary
	// configuration check refuses it, and overwrites it at Deploy
	// (providers/application/openchoreo/secretenv_test.go).
}

// TestSpec026Criterion7_DeleteGuard: a Secret a Component or a Deployment
// uses can't be deleted (409 naming the user); once the reference goes,
// the delete works.
func TestSpec026Criterion7_DeleteGuard(t *testing.T) {
	w := newWorld026(t)
	w.assignRole(t, "sub-owner", policy.SecretsUserRoleID, resource.RootScope)
	c := w.component(t)
	c.Spec.SecretEnv = []resource.ComponentSecretEnv{secretVar("DB_PASSWORD", w.secret, "password")}
	c, _ = w.putComponent(t, "tok-owner", c, http.StatusOK)
	dep := w.deployment(w.dev, "")
	dep["spec"].(map[string]any)["overrides"] = map[string]any{"secretEnv": []any{map[string]any{"env": "API_TOKEN", "secret": w.orgSecret, "key": "token"}}}
	d := decodeDeployment(t, w.call(t, "tok-owner", http.MethodPost, "/v1/deployments", dep, http.StatusCreated))

	if body := string(w.call(t, "tok-owner", http.MethodDelete, "/v1/secrets/"+w.secret, nil, http.StatusConflict)); !strings.Contains(body, "Component web") {
		t.Errorf("deleting the Component's Secret: %s", body)
	}
	if body := string(w.call(t, "tok-owner", http.MethodDelete, "/v1/secrets/"+w.orgSecret, nil, http.StatusConflict)); !strings.Contains(body, "Deployment web") {
		t.Errorf("deleting the Deployment's Secret: %s", body)
	}

	c.Spec.SecretEnv = nil
	w.putComponent(t, "tok-owner", c, http.StatusOK)
	w.call(t, "tok-owner", http.MethodDelete, "/v1/secrets/"+w.secret, nil, http.StatusNoContent)
	d.Spec.Overrides.SecretEnv = nil
	w.call(t, "tok-owner", http.MethodPut, "/v1/deployments/"+d.Metadata.ID, d, http.StatusOK)
	w.call(t, "tok-owner", http.MethodDelete, "/v1/secrets/"+w.orgSecret, nil, http.StatusNoContent)
}

// encryption026 is a fake EncryptionReporter.
type encryption026 struct {
	rep reconcile.EncryptionReport
	err error
}

func (f *encryption026) EncryptionAtRest(context.Context) (reconcile.EncryptionReport, error) {
	return f.rep, f.err
}

// TestSpec026SecretsStatus: GET /v1/secrets/status says whether the
// Secrets capability is ready and whether the cluster encrypts Secrets at
// rest, honestly unknown when it can't say; it's a platform-wide read.
func TestSpec026SecretsStatus(t *testing.T) {
	type status struct {
		Ready   bool   `json:"ready"`
		State   string `json:"encryptionAtRest"`
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	get := func(t *testing.T, w *world026, token string, want int) status {
		t.Helper()
		var s status
		if body := w.call(t, token, http.MethodGet, "/v1/secrets/status", nil, want); want == http.StatusOK {
			if err := json.Unmarshal(body, &s); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}

	w := newWorld026(t)
	if s := get(t, w, "tok-owner", http.StatusOK); s.Ready || s.State != "unknown" || s.Reason != "NotConfigured" {
		t.Errorf("without a Secrets capability: %+v", s)
	}
	// A Project Reader can't read the platform-wide answer.
	get(t, w, "tok-reader", http.StatusForbidden)

	f := &encryption026{rep: reconcile.EncryptionReport{State: reconcile.EncryptionAtRestOn, Reason: "EncryptionConfigured", Message: "on"}}
	w = newWorld026(t, api.WithSecretsStatus(f))
	if s := get(t, w, "tok-owner", http.StatusOK); !s.Ready || s.State != "on" || s.Reason != "EncryptionConfigured" {
		t.Errorf("on: %+v", s)
	}
	f.rep, f.err = reconcile.EncryptionReport{}, reconcile.ErrProviderNotReady
	if s := get(t, w, "tok-owner", http.StatusOK); s.Ready || s.State != "unknown" || s.Reason != "NotReady" {
		t.Errorf("not ready: %+v", s)
	}
	f.err = errors.New("list nodes: 10.43.0.1:443 refused")
	if s := get(t, w, "tok-owner", http.StatusOK); !s.Ready || s.State != "unknown" || s.Reason != "CheckFailed" || strings.Contains(s.Message, "10.43") {
		t.Errorf("a failed check: %+v", s)
	}
}
