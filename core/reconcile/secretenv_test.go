package reconcile_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/graph"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store"
	"github.com/serendip-infra/serendip-infra/core/store/sqlite"
)

// secretPlatform is a DeploymentProvider that also delivers secret
// variables, recording what it was given.
type secretPlatform struct {
	fakeDeployments
	held       string // the version it holds
	versionErr error
	writeErr   error
	writes     []map[string]string
	versions   []string // versions written
	deployedAs []string // secretEnvVersion given to Deploy
}

func (p *secretPlatform) Deploy(ctx context.Context, app resource.Application, comp resource.Component, env resource.Environment, d resource.Deployment, v string) (reconcile.DeploymentObservation, error) {
	p.deployedAs = append(p.deployedAs, v)
	return p.fakeDeployments.Deploy(ctx, app, comp, env, d, v)
}

func (p *secretPlatform) SecretEnvVersion(context.Context, resource.Component, resource.Environment, resource.Deployment) (string, error) {
	return p.held, p.versionErr
}

func (p *secretPlatform) WriteSecretEnv(_ context.Context, _ resource.Component, _ resource.Environment, _ resource.Deployment, vars map[string]string, version string) error {
	if p.writeErr != nil {
		return p.writeErr
	}
	p.writes = append(p.writes, maps(vars))
	p.versions = append(p.versions, version)
	p.held = version
	return nil
}

func maps(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// valueStore is a SecretsProvider holding a value per Secret ID.
type valueStore struct {
	values map[string]map[string]any
	err    error
	reads  int
}

func (v *valueStore) Exists(context.Context, resource.Secret) (bool, error) { return true, nil }
func (v *valueStore) WriteValue(_ context.Context, sec resource.Secret, val map[string]any) error {
	v.values[sec.Metadata.ID] = val
	return nil
}
func (v *valueStore) ReadValue(_ context.Context, sec resource.Secret) (map[string]any, error) {
	v.reads++
	if v.err != nil {
		return nil, v.err
	}
	val, ok := v.values[sec.Metadata.ID]
	if !ok {
		return nil, errors.New("no value")
	}
	return val, nil
}
func (v *valueStore) Delete(context.Context, resource.Secret) error { return nil }

type secretEnvFixture struct {
	*deployStores
	secrets  store.Store[resource.SecretSpec, resource.SecretStatus]
	platform *secretPlatform
	values   *valueStore
	r        *reconcile.DeploymentReconciler
	d        resource.Deployment
}

func newSecretEnvFixture(t *testing.T) *secretEnvFixture {
	t.Helper()
	s := newDeployStores(t)
	f := &secretEnvFixture{deployStores: s,
		secrets:  sqlite.NewStore[resource.SecretSpec, resource.SecretStatus](s.db, resource.KindSecret),
		platform: &secretPlatform{fakeDeployments: fakeDeployments{obs: reconcile.DeploymentObservation{Release: "r1", Ready: true, Reason: reconcile.ReasonDeployed}}},
		values:   &valueStore{values: map[string]map[string]any{}},
	}
	f.r = reconcile.NewDeploymentReconciler(s.deployments, s.envs, s.components, s.apps, f.platform).UseSecrets(f.secrets, f.values)
	f.d = s.deployment(t, s.env)
	return f
}

// secret makes a Secret, written (with value) unless value is nil.
func (f *secretEnvFixture) secret(t *testing.T, name string, value map[string]any) string {
	t.Helper()
	sec, err := f.secrets.Create(context.Background(), resource.Secret{Metadata: resource.Metadata{Name: name}, Spec: resource.SecretSpec{Path: name}})
	if err != nil {
		t.Fatal(err)
	}
	if value != nil {
		f.write(t, sec.Metadata.ID, value)
	}
	return sec.Metadata.ID
}

func (f *secretEnvFixture) write(t *testing.T, id string, value map[string]any) {
	t.Helper()
	if _, err := reconcile.NewSecretReconciler(f.secrets, f.values).WriteValue(context.Background(), id, value); err != nil {
		t.Fatal(err)
	}
}

func (f *secretEnvFixture) setComponentSecretEnv(t *testing.T, list ...resource.ComponentSecretEnv) {
	t.Helper()
	c, err := f.components.Get(context.Background(), f.comp.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	c.Spec.SecretEnv = list
	if f.comp, err = f.components.Update(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

func (f *secretEnvFixture) reconcile(t *testing.T) resource.Deployment {
	t.Helper()
	ref := graph.ResourceRef{Kind: resource.KindDeployment, ID: f.d.Metadata.ID}
	if err := f.r.Reconcile(context.Background(), ref); err != nil {
		var rq reconcile.RequeueAfter
		if !errors.As(err, &rq) {
			t.Fatal(err)
		}
	}
	d, err := f.deployments.Get(context.Background(), f.d.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func cond(d resource.Deployment, typ string) *resource.Condition {
	for i := range d.Status.Conditions {
		if d.Status.Conditions[i].Type == typ {
			return &d.Status.Conditions[i]
		}
	}
	return nil
}

func sev(env, secret, key string) resource.ComponentSecretEnv {
	return resource.ComponentSecretEnv{Env: env, Secret: secret, Key: key}
}

func TestSecretEnv_Delivered(t *testing.T) {
	f := newSecretEnvFixture(t)
	db := f.secret(t, "db", map[string]any{"password": "s3cret-pw", "port": 5432.0})
	f.setComponentSecretEnv(t, sev("DB_PASSWORD", db, "password"), sev("DB_PORT", db, "port"))

	d := f.reconcile(t)
	if len(f.platform.writes) != 1 {
		t.Fatalf("writes = %d, want 1", len(f.platform.writes))
	}
	if got := f.platform.writes[0]; got["DB_PASSWORD"] != "s3cret-pw" || got["DB_PORT"] != "5432" || len(got) != 2 {
		t.Fatalf("vars = %v", got)
	}
	c := cond(d, "SecretEnvDelivered")
	if c == nil || c.Status != resource.ConditionTrue || c.Reason != reconcile.ReasonSecretEnvDelivered {
		t.Fatalf("condition = %+v", c)
	}
	if d.Status.SecretEnvVersion == "" || d.Status.SecretEnvVersion != f.platform.versions[0] {
		t.Fatalf("status version %q, written %v", d.Status.SecretEnvVersion, f.platform.versions)
	}
	if last := f.platform.deployedAs[len(f.platform.deployedAs)-1]; last != d.Status.SecretEnvVersion {
		t.Fatalf("Deploy got version %q, want %q", last, d.Status.SecretEnvVersion)
	}
	if r := cond(d, "Ready"); r == nil || r.Status != resource.ConditionTrue {
		t.Fatalf("Ready = %+v", r)
	}

	// Unchanged: no read, no write, no store write.
	reads, rv := f.values.reads, d.Metadata.ResourceVersion
	for range 3 {
		d = f.reconcile(t)
	}
	if f.values.reads != reads || len(f.platform.writes) != 1 || d.Metadata.ResourceVersion != rv {
		t.Fatalf("an unchanged pass read %d, wrote %d, resourceVersion %d -> %d", f.values.reads-reads, len(f.platform.writes)-1, rv, d.Metadata.ResourceVersion)
	}

	// Rotation: a new write changes the version and is delivered.
	time.Sleep(2 * time.Millisecond)
	f.write(t, db, map[string]any{"password": "rotated", "port": 5432.0})
	d = f.reconcile(t)
	if len(f.platform.writes) != 2 || f.platform.writes[1]["DB_PASSWORD"] != "rotated" {
		t.Fatalf("rotation not delivered: %v", f.platform.writes)
	}
	if d.Status.SecretEnvVersion != f.platform.versions[1] || f.platform.versions[0] == f.platform.versions[1] {
		t.Fatalf("versions %v, status %q", f.platform.versions, d.Status.SecretEnvVersion)
	}
}

func TestSecretEnv_OverridesReplace(t *testing.T) {
	f := newSecretEnvFixture(t)
	dev := f.secret(t, "dev", map[string]any{"pw": "dev-pw"})
	prod := f.secret(t, "prod", map[string]any{"pw": "prod-pw"})
	f.setComponentSecretEnv(t, sev("PW", dev, "pw"))
	d, _ := f.deployments.Get(context.Background(), f.d.Metadata.ID)
	d.Spec.Overrides.SecretEnv = []resource.ComponentSecretEnv{sev("PW", prod, "pw")}
	if _, err := f.deployments.Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	f.reconcile(t)
	if got := f.platform.writes[0]["PW"]; got != "prod-pw" {
		t.Fatalf("PW = %q, want the override's", got)
	}
}

func TestSecretEnv_NotDelivered(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(t *testing.T, f *secretEnvFixture) []resource.ComponentSecretEnv
		reason string
		status resource.ConditionStatus
	}{
		{name: "no value yet", reason: reconcile.ReasonSecretHasNoValue, status: resource.ConditionFalse,
			setup: func(t *testing.T, f *secretEnvFixture) []resource.ComponentSecretEnv {
				return []resource.ComponentSecretEnv{sev("A", f.secret(t, "empty", nil), "k")}
			}},
		{name: "missing key", reason: reconcile.ReasonSecretKeyNotFound, status: resource.ConditionFalse,
			setup: func(t *testing.T, f *secretEnvFixture) []resource.ComponentSecretEnv {
				return []resource.ComponentSecretEnv{sev("A", f.secret(t, "s", map[string]any{"other": "x"}), "k")}
			}},
		{name: "secret gone", reason: reconcile.ReasonSecretNotFound, status: resource.ConditionFalse,
			setup: func(t *testing.T, f *secretEnvFixture) []resource.ComponentSecretEnv {
				return []resource.ComponentSecretEnv{sev("A", "0190d1c0-0000-7000-8000-00000000dead", "k")}
			}},
		{name: "too large", reason: reconcile.ReasonSecretEnvTooLarge, status: resource.ConditionFalse,
			setup: func(t *testing.T, f *secretEnvFixture) []resource.ComponentSecretEnv {
				return []resource.ComponentSecretEnv{sev("A", f.secret(t, "big", map[string]any{"k": strings.Repeat("x", reconcile.MaxSecretEnvBytes)}), "k")}
			}},
		{name: "secrets not ready", reason: reconcile.ReasonProviderNotReady, status: resource.ConditionUnknown,
			setup: func(t *testing.T, f *secretEnvFixture) []resource.ComponentSecretEnv {
				id := f.secret(t, "s", map[string]any{"k": "v"})
				f.values.err = reconcile.ErrProviderNotReady
				return []resource.ComponentSecretEnv{sev("A", id, "k")}
			}},
		{name: "platform not ready", reason: reconcile.ReasonProviderNotReady, status: resource.ConditionUnknown,
			setup: func(t *testing.T, f *secretEnvFixture) []resource.ComponentSecretEnv {
				id := f.secret(t, "s", map[string]any{"k": "v"})
				f.platform.versionErr = reconcile.ErrProviderNotReady
				return []resource.ComponentSecretEnv{sev("A", id, "k")}
			}},
		{name: "read fails", reason: reconcile.ReasonSecretReadFailed, status: resource.ConditionUnknown,
			setup: func(t *testing.T, f *secretEnvFixture) []resource.ComponentSecretEnv {
				id := f.secret(t, "s", map[string]any{"k": "v"})
				f.values.err = errors.New("boom: the-value-must-not-leak")
				return []resource.ComponentSecretEnv{sev("A", id, "k")}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSecretEnvFixture(t)
			f.setComponentSecretEnv(t, tt.setup(t, f)...)
			d := f.reconcile(t)
			c := cond(d, "SecretEnvDelivered")
			if c == nil || c.Reason != tt.reason || c.Status != tt.status {
				t.Fatalf("condition = %+v, want %s %s", c, tt.status, tt.reason)
			}
			if r := cond(d, "Ready"); r == nil || r.Status == resource.ConditionTrue || r.Reason != tt.reason {
				t.Fatalf("Ready = %+v", r)
			}
			if len(f.platform.deployed) != 0 || len(f.platform.writes) != 0 {
				t.Fatalf("deployed %d, wrote %d: nothing should change on the platform", len(f.platform.deployed), len(f.platform.writes))
			}
			if strings.Contains(c.Message, "leak") || d.Status.SecretEnvVersion != "" {
				t.Fatalf("message %q / version %q", c.Message, d.Status.SecretEnvVersion)
			}
			// The Secret's problems name what uses it (T009 F1).
			if (tt.reason == reconcile.ReasonSecretHasNoValue || tt.reason == reconcile.ReasonSecretNotFound) &&
				!strings.Contains(c.Message, `for A, key "k"`) {
				t.Fatalf("message %q doesn't name the variable and key", c.Message)
			}
		})
	}
}

func TestSecretEnv_NoValueInStatusOrMessages(t *testing.T) {
	f := newSecretEnvFixture(t)
	const value = "VALUE-6f1c2a-never-shown"
	id := f.secret(t, "s", map[string]any{"k": value})
	f.setComponentSecretEnv(t, sev("A", id, "k"), sev("B", id, "missing"))
	d := f.reconcile(t) // missing key
	f.setComponentSecretEnv(t, sev("A", id, "k"))
	d2 := f.reconcile(t) // delivered
	for _, x := range []resource.Deployment{d, d2} {
		for _, c := range x.Status.Conditions {
			if strings.Contains(c.Message, value) {
				t.Fatalf("condition %s carries the value: %q", c.Type, c.Message)
			}
		}
		if strings.Contains(x.Status.SecretEnvVersion, value) {
			t.Fatal("version carries the value")
		}
	}
}

func TestSecretEnv_PendingStillDeploys(t *testing.T) {
	f := newSecretEnvFixture(t)
	id := f.secret(t, "s", map[string]any{"k": "v"})
	f.setComponentSecretEnv(t, sev("A", id, "k"))
	f.platform.versionErr = reconcile.ErrSecretEnvPending
	d := f.reconcile(t)
	if c := cond(d, "SecretEnvDelivered"); c == nil || c.Reason != reconcile.ReasonSecretEnvPending {
		t.Fatalf("condition = %+v", c)
	}
	if len(f.platform.deployed) != 1 || f.platform.deployedAs[0] == "" {
		t.Fatalf("a first deploy must still run, naming the version: %v", f.platform.deployedAs)
	}
	if r := cond(d, "Ready"); r == nil || r.Status == resource.ConditionTrue {
		t.Fatalf("Ready = %+v, want not ready while pending", r)
	}
	f.platform.versionErr = nil
	d = f.reconcile(t)
	if c := cond(d, "SecretEnvDelivered"); c == nil || c.Status != resource.ConditionTrue {
		t.Fatalf("after the namespace appears: %+v", c)
	}
}

func TestSecretEnv_RemovedWhenEmpty(t *testing.T) {
	f := newSecretEnvFixture(t)
	id := f.secret(t, "s", map[string]any{"k": "v"})
	f.setComponentSecretEnv(t, sev("A", id, "k"))
	f.reconcile(t)
	f.setComponentSecretEnv(t)
	d := f.reconcile(t)
	if n := len(f.platform.writes); n != 2 || f.platform.writes[1] != nil || f.platform.versions[1] != "" {
		t.Fatalf("writes %v versions %v: want a removal", f.platform.writes, f.platform.versions)
	}
	if cond(d, "SecretEnvDelivered") != nil || d.Status.SecretEnvVersion != "" {
		t.Fatalf("condition and version should be gone: %+v %q", d.Status.Conditions, d.Status.SecretEnvVersion)
	}
	if last := f.platform.deployedAs[len(f.platform.deployedAs)-1]; last != "" {
		t.Fatalf("Deploy got %q, want none", last)
	}
}

func TestSecretEnv_NeedsNewRelease(t *testing.T) {
	f := newSecretEnvFixture(t)
	id := f.secret(t, "s", map[string]any{"k": "v"})
	f.setComponentSecretEnv(t, sev("A", id, "k"))
	f.platform.obs = reconcile.DeploymentObservation{Release: "old", Reason: reconcile.ReasonNeedsNewRelease, Message: "deploy a newer release"}
	d := f.reconcile(t)
	if c := cond(d, "SecretEnvDelivered"); c == nil || c.Status != resource.ConditionFalse || c.Reason != reconcile.ReasonNeedsNewRelease {
		t.Fatalf("condition = %+v", c)
	}
}

func TestSecretEnv_UnsupportedPlatform(t *testing.T) {
	s := newDeployStores(t)
	secrets := sqlite.NewStore[resource.SecretSpec, resource.SecretStatus](s.db, resource.KindSecret)
	plain := &fakeDeployments{obs: reconcile.DeploymentObservation{Ready: true}}
	r := reconcile.NewDeploymentReconciler(s.deployments, s.envs, s.components, s.apps, plain).UseSecrets(secrets, &valueStore{})
	c, _ := s.components.Get(context.Background(), s.comp.Metadata.ID)
	c.Spec.SecretEnv = []resource.ComponentSecretEnv{sev("A", "s1", "k")}
	if _, err := s.components.Update(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	d := s.deployment(t, s.env)
	if err := r.Reconcile(context.Background(), graph.ResourceRef{Kind: resource.KindDeployment, ID: d.Metadata.ID}); err != nil {
		t.Fatal(err)
	}
	d, _ = s.deployments.Get(context.Background(), d.Metadata.ID)
	if got := cond(d, "SecretEnvDelivered"); got == nil || got.Reason != reconcile.ReasonUnsupported {
		t.Fatalf("condition = %+v", got)
	}
	if len(plain.deployed) != 0 {
		t.Fatal("deployed without its secret variables")
	}
}

type fakeDependents map[graph.ResourceRef][]graph.ResourceRef

func (f fakeDependents) Dependents(_ context.Context, ref graph.ResourceRef) ([]graph.ResourceRef, error) {
	return f[ref], nil
}

func TestEnqueueSecretUsers(t *testing.T) {
	sec := graph.ResourceRef{Kind: resource.KindSecret, ID: "s1"}
	comp := graph.ResourceRef{Kind: resource.KindComponent, ID: "c1"}
	d1 := graph.ResourceRef{Kind: resource.KindDeployment, ID: "d1"}
	d2 := graph.ResourceRef{Kind: resource.KindDeployment, ID: "d2"}
	ch := graph.ResourceRef{Kind: resource.KindNotificationChannel, ID: "n1"}
	g := fakeDependents{sec: {comp, d1, ch}, comp: {d1, d2}}
	var got []graph.ResourceRef
	reconcile.EnqueueSecretUsers(context.Background(), g, func(r graph.ResourceRef) { got = append(got, r) }, "s1")
	if !slices.Equal(got, []graph.ResourceRef{d1, d2}) {
		t.Fatalf("enqueued %v, want d1 then d2 once each", got)
	}
}

func TestSecretReconciler_OnValueWritten(t *testing.T) {
	s := newDeployStores(t)
	secrets := sqlite.NewStore[resource.SecretSpec, resource.SecretStatus](s.db, resource.KindSecret)
	sec, err := secrets.Create(context.Background(), resource.Secret{Metadata: resource.Metadata{Name: "s"}, Spec: resource.SecretSpec{Path: "s"}})
	if err != nil {
		t.Fatal(err)
	}
	var told []string
	r := reconcile.NewSecretReconciler(secrets, &valueStore{values: map[string]map[string]any{}}).
		OnValueWritten(func(_ context.Context, id string) { told = append(told, id) })
	if _, err := r.WriteValue(context.Background(), sec.Metadata.ID, map[string]any{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(told, []string{sec.Metadata.ID}) {
		t.Fatalf("told %v", told)
	}
}

// A failed pass keeps the version the platform holds, so removing the
// last variable afterwards still removes them (review finding).
func TestSecretEnv_VersionSurvivesAFailedPassThenRemoves(t *testing.T) {
	f := newSecretEnvFixture(t)
	id := f.secret(t, "s", map[string]any{"k": "v"})
	f.setComponentSecretEnv(t, sev("A", id, "k"))
	d := f.reconcile(t)
	held := d.Status.SecretEnvVersion
	if held == "" {
		t.Fatal("not delivered")
	}
	// A failed Deploy, then a key that's gone: status keeps the version.
	f.platform.err = errors.New("platform down")
	if d = f.reconcile(t); d.Status.SecretEnvVersion != held {
		t.Fatalf("after a failed Deploy: version %q, want %q", d.Status.SecretEnvVersion, held)
	}
	f.platform.err = nil
	f.setComponentSecretEnv(t, sev("A", id, "missing"))
	if d = f.reconcile(t); d.Status.SecretEnvVersion != held {
		t.Fatalf("after a missing key: version %q, want %q", d.Status.SecretEnvVersion, held)
	}
	// The platform is down when the last variable goes: retried.
	f.setComponentSecretEnv(t)
	f.platform.writeErr = reconcile.ErrProviderNotReady
	if d = f.reconcile(t); d.Status.SecretEnvVersion != held {
		t.Fatalf("removal not done yet, but version is %q", d.Status.SecretEnvVersion)
	}
	f.platform.writeErr = nil
	d = f.reconcile(t)
	if last := f.platform.versions[len(f.platform.versions)-1]; last != "" || f.platform.writes[len(f.platform.writes)-1] != nil {
		t.Fatalf("no removal write: %v", f.platform.versions)
	}
	if d.Status.SecretEnvVersion != "" || cond(d, "SecretEnvDelivered") != nil {
		t.Fatalf("after removal: %q %+v", d.Status.SecretEnvVersion, cond(d, "SecretEnvDelivered"))
	}
}

// A platform handle answering ErrSecretEnvUnsupported is reported as
// Unsupported, not retried as a failure (review finding).
func TestSecretEnv_UnsupportedSentinel(t *testing.T) {
	f := newSecretEnvFixture(t)
	id := f.secret(t, "s", map[string]any{"k": "v"})
	f.setComponentSecretEnv(t, sev("A", id, "k"))
	f.platform.versionErr = reconcile.ErrSecretEnvUnsupported
	d := f.reconcile(t)
	if c := cond(d, "SecretEnvDelivered"); c == nil || c.Reason != reconcile.ReasonUnsupported {
		t.Fatalf("condition = %+v", c)
	}
	if len(f.platform.deployed) != 0 {
		t.Fatal("deployed without its secret variables")
	}
}
