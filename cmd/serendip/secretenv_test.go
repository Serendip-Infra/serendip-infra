package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/serendip-infra/serendip-infra/core/policy"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store/sqlite"
)

// Spec 026 T007: --secret-env and --remove-secret-env edit a Component's
// secret variables (and a Deployment's overrides) without a hand-written
// document; on update the CLI reads the resource first and sends back the
// version it read.
func TestSecretEnvFlags(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	env, err := sqlite.NewStore[resource.EnvironmentSpec, resource.EnvironmentStatus](w.db, resource.KindEnvironment).Get(ctx, w.envID)
	if err != nil {
		t.Fatal(err)
	}
	projID := env.Metadata.OwnerRefs[0].ID
	app, err := sqlite.NewStore[resource.ApplicationSpec, resource.ApplicationStatus](w.db, resource.KindApplication).Create(ctx,
		resource.Resource[resource.ApplicationSpec, resource.ApplicationStatus]{Metadata: resource.Metadata{Name: "store",
			OwnerRefs: []resource.OwnerRef{{Kind: resource.KindProject, ID: projID}}}})
	if err != nil {
		t.Fatal(err)
	}
	comps := sqlite.NewStore[resource.ComponentSpec, resource.ComponentStatus](w.db, resource.KindComponent)
	comp, err := comps.Create(ctx, resource.Component{Metadata: resource.Metadata{Name: "web",
		OwnerRefs: []resource.OwnerRef{{Kind: resource.KindApplication, ID: app.Metadata.ID}}}, Spec: resource.ComponentSpec{Image: "nginx"}})
	if err != nil {
		t.Fatal(err)
	}
	sec, err := sqlite.NewStore[resource.SecretSpec, resource.SecretStatus](w.db, resource.KindSecret).Create(ctx,
		resource.Secret{Metadata: resource.Metadata{Name: "db", OwnerRefs: []resource.OwnerRef{{Kind: resource.KindProject, ID: projID}}},
			Spec: resource.SecretSpec{Path: "app/db"}})
	if err != nil {
		t.Fatal(err)
	}
	// Naming a Secret needs reading its value: the key is Owner and Secrets
	// User.
	for _, role := range []string{policy.OwnerRoleID, policy.SecretsUserRoleID} {
		if _, err := sqlite.NewStore[resource.RoleAssignmentSpec, resource.RoleAssignmentStatus](w.db, resource.KindRoleAssignment).Create(ctx,
			resource.RoleAssignment{Metadata: resource.Metadata{Name: "ops-" + role}, Spec: resource.RoleAssignmentSpec{
				Principal: resource.PrincipalRef{Type: resource.PrincipalAPIKey, ID: "ops"}, RoleDefinitionID: role, Scope: resource.RootScope,
			}}); err != nil {
			t.Fatal(err)
		}
	}
	id, sid := comp.Metadata.ID, sec.Metadata.ID
	list := func() []resource.ComponentSecretEnv {
		t.Helper()
		c, err := comps.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return c.Spec.SecretEnv
	}

	before := len(w.sent())
	r := w.cli(ops, "", "components", "update", id, "--secret-env", "DB_PASSWORD="+sid+":password", "--secret-env=DB_USER="+sid+":user")
	if r.code != exitOK {
		t.Fatalf("add: %d %s %s", r.code, r.stdout, r.stderr)
	}
	want := []resource.ComponentSecretEnv{{Env: "DB_PASSWORD", Secret: sid, Key: "password"}, {Env: "DB_USER", Secret: sid, Key: "user"}}
	if got := list(); !slices.Equal(got, want) {
		t.Errorf("after add: %v, want %v", got, want)
	}
	if sent := w.sent()[before:]; len(sent) != 2 || !strings.HasPrefix(sent[0], "GET /v1/components/"+id) || !strings.HasPrefix(sent[1], "PUT /v1/components/"+id) {
		t.Errorf("update sent %v, want a read then a write", sent)
	}
	if c, _ := comps.Get(ctx, id); c.Spec.Image != "nginx" {
		t.Errorf("the rest of the spec changed: %+v", c.Spec)
	}

	r = w.cli(ops, "", "components", "update", id, "--remove-secret-env", "DB_USER", "--secret-env", "DB_PASSWORD="+sid+":pw2")
	if want := []resource.ComponentSecretEnv{{Env: "DB_PASSWORD", Secret: sid, Key: "pw2"}}; r.code != exitOK || !slices.Equal(list(), want) {
		t.Errorf("change and remove: %d %v %s", r.code, list(), r.stderr)
	}

	// Mistakes are usage errors, and nothing is written. A malformed flag
	// is caught before the read; a missing name only once it's read.
	before = len(w.sent())
	if r := w.cli(ops, "", "components", "update", "no-such-component", "--secret-env", "DB_HOST="+sid); r.code != exitUsage {
		t.Errorf("malformed: %d, want usage (%s)", r.code, r.stderr)
	}
	if n := len(w.sent()) - before; n != 0 {
		t.Errorf("a malformed flag sent %d requests", n)
	}
	if r := w.cli(ops, "", "components", "update", id, "--remove-secret-env", "NOPE"); r.code != exitUsage {
		t.Errorf("remove missing: %d, want usage (%s)", r.code, r.stderr)
	}
	for _, s := range w.sent()[before:] {
		if strings.HasPrefix(s, "PUT") {
			t.Errorf("a mistaken edit was sent: %s", s)
		}
	}
	// A dry run without --file reads, then prints the write it would send.
	before = len(w.sent())
	r = w.cli(ops, "", "--dry-run", "components", "update", id, "--secret-env", "X="+sid+":k")
	if r.code != exitOK || !strings.Contains(r.stdout, "PUT "+w.api.URL+"/v1/components/"+id) || !strings.Contains(r.stdout, `"env": "X"`) ||
		!strings.Contains(r.stdout, `"resourceVersion"`) || strings.Contains(r.stdout, `"status"`) {
		t.Errorf("dry run: %d %q %s", r.code, r.stdout, r.stderr)
	}
	if sent := w.sent()[before:]; len(sent) != 1 || !strings.HasPrefix(sent[0], "GET ") {
		t.Errorf("dry run sent %v, want one read", sent)
	}
	// The API still decides: a Secret that doesn't exist is refused.
	if r := w.cli(ops, "", "components", "update", id, "--secret-env", "X=no-such-secret:k"); r.code == exitOK {
		t.Errorf("a missing Secret was accepted: %s", r.stdout)
	}
	if r := w.cli(ops, "", "components", "update", "no-such-component", "--secret-env", "X="+sid+":k"); r.code != exitNotFound {
		t.Errorf("a missing Component: %d %s", r.code, r.stderr)
	}

	// With --file, the flags edit the file's document: no read first.
	file := filepath.Join(t.TempDir(), "dep.json")
	os.WriteFile(file, []byte(fmt.Sprintf(`{"metadata":{"name":"web-env","ownerRefs":[{"kind":"Component","id":%q}]},"spec":{"environmentId":%q}}`, id, w.envID)), 0o600)
	before = len(w.sent())
	r = w.cli(ops, "", "--dry-run", "deployments", "create", "--file", file, "--secret-env", "API_TOKEN="+sid+":token")
	if r.code != exitOK || !strings.Contains(r.stdout, `"overrides": {`) || !strings.Contains(r.stdout, `"env": "API_TOKEN"`) {
		t.Errorf("deployment create: %d %q %s", r.code, r.stdout, r.stderr)
	}
	if len(w.sent()) != before {
		t.Error("a dry run with --file sent a request")
	}

	if r := w.cli(ops, "", "deployments", "update", "--help"); r.code != exitOK || !strings.Contains(r.stdout, "--secret-env VALUE (repeatable)") || !strings.Contains(r.stdout, "--remove-secret-env") {
		t.Errorf("help: %q", r.stdout)
	}
}
