package resource_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/serendip-infra/serendip-infra/core/resource"
)

func se(env, secret, key string) resource.ComponentSecretEnv {
	return resource.ComponentSecretEnv{Env: env, Secret: secret, Key: key}
}

func checkErr(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	if err == nil || !errors.Is(err, resource.ErrValidation) || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want a validation error containing %q", err, want)
	}
}

func TestValidateComponentSecretEnv(t *testing.T) {
	many := make([]resource.ComponentSecretEnv, resource.MaxSecretEnv+1)
	for i := range many {
		many[i] = se("V"+strings.Repeat("A", i), "s1", "k")
	}
	tests := []struct {
		name string
		spec resource.ComponentSpec
		err  string
	}{
		{name: "none"},
		{name: "two", spec: resource.ComponentSpec{SecretEnv: []resource.ComponentSecretEnv{se("DB_PASSWORD", "s1", "password"), se("API_TOKEN", "s2", "token")}}},
		{name: "one secret twice", spec: resource.ComponentSpec{SecretEnv: []resource.ComponentSecretEnv{se("A", "s1", "a"), se("B", "s1", "b")}}},
		{name: "key with spaces and slashes", spec: resource.ComponentSpec{SecretEnv: []resource.ComponentSecretEnv{se("A", "s1", "db/main password")}}},
		{name: "bad variable", spec: resource.ComponentSpec{SecretEnv: []resource.ComponentSecretEnv{se("1A", "s1", "k")}}, err: "isn't a variable name"},
		{name: "variable twice", spec: resource.ComponentSpec{SecretEnv: []resource.ComponentSecretEnv{se("A", "s1", "k"), se("A", "s2", "k")}}, err: "set twice"},
		{name: "no secret", spec: resource.ComponentSpec{SecretEnv: []resource.ComponentSecretEnv{se("A", "", "k")}}, err: "secret: a Secret ID"},
		{name: "bad secret", spec: resource.ComponentSpec{SecretEnv: []resource.ComponentSecretEnv{se("A", "a/b", "k")}}, err: "secret: a Secret ID"},
		{name: "no key", spec: resource.ComponentSpec{SecretEnv: []resource.ComponentSecretEnv{se("A", "s1", "")}}, err: "key"},
		{name: "control in key", spec: resource.ComponentSpec{SecretEnv: []resource.ComponentSecretEnv{se("A", "s1", "a\nb")}}, err: "key"},
		{name: "long key", spec: resource.ComponentSpec{SecretEnv: []resource.ComponentSecretEnv{se("A", "s1", strings.Repeat("k", 257))}}, err: "key"},
		{name: "too many", spec: resource.ComponentSpec{SecretEnv: many}, err: "at most"},
		{name: "clashes with env", spec: resource.ComponentSpec{Env: map[string]string{"A": "x"}, SecretEnv: []resource.ComponentSecretEnv{se("A", "s1", "k")}}, err: "also set in env"},
		{name: "clashes with a dependency", spec: resource.ComponentSpec{
			Dependencies: []resource.ComponentDependency{{Component: "c2", Port: "http", Env: "A"}},
			SecretEnv:    []resource.ComponentSecretEnv{se("A", "s1", "k")}}, err: "dependency's variable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkErr(t, resource.ValidateComponentSecretEnv(tt.spec), tt.err)
		})
	}
}

func TestValidateDeploymentSecretEnv(t *testing.T) {
	comp := resource.ComponentSpec{
		Env:          map[string]string{"PLAIN": "x"},
		Dependencies: []resource.ComponentDependency{{Component: "c2", Port: "http", Env: "API_URL"}},
		SecretEnv:    []resource.ComponentSecretEnv{se("DB_PASSWORD", "s1", "password")},
	}
	tests := []struct {
		name string
		o    resource.DeploymentOverrides
		err  string // from ValidateDeploymentSpec, then ValidateDeploymentSecretEnv
	}{
		{name: "none"},
		{name: "replace a secret variable", o: resource.DeploymentOverrides{SecretEnv: []resource.ComponentSecretEnv{se("DB_PASSWORD", "s9", "password")}}},
		{name: "a new one", o: resource.DeploymentOverrides{SecretEnv: []resource.ComponentSecretEnv{se("EXTRA", "s9", "k")}}},
		{name: "plain override of a plain variable", o: resource.DeploymentOverrides{Env: map[string]string{"PLAIN": "y"}}},
		{name: "secret over a plain variable", o: resource.DeploymentOverrides{SecretEnv: []resource.ComponentSecretEnv{se("PLAIN", "s9", "k")}}, err: "plain variable of the Component"},
		{name: "secret over a dependency", o: resource.DeploymentOverrides{SecretEnv: []resource.ComponentSecretEnv{se("API_URL", "s9", "k")}}, err: "dependency's variable"},
		{name: "plain over a secret variable", o: resource.DeploymentOverrides{Env: map[string]string{"DB_PASSWORD": "y"}}, err: "secret variable of the Component"},
		{name: "same name in both override lists", o: resource.DeploymentOverrides{Env: map[string]string{"X": "y"}, SecretEnv: []resource.ComponentSecretEnv{se("X", "s9", "k")}}, err: "also set in overrides.env"},
		{name: "bad override list", o: resource.DeploymentOverrides{SecretEnv: []resource.ComponentSecretEnv{se("X", "s9", "")}}, err: "overrides.secretEnv[0].key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := resource.ValidateDeploymentSpec(resource.DeploymentSpec{Component: "c1", Overrides: tt.o})
			if err == nil {
				err = resource.ValidateDeploymentSecretEnv(tt.o, comp)
			}
			checkErr(t, err, tt.err)
		})
	}
}

func TestMergeSecretEnv(t *testing.T) {
	got := resource.MergeSecretEnv(
		[]resource.ComponentSecretEnv{se("B", "s1", "b"), se("A", "s1", "a")},
		[]resource.ComponentSecretEnv{se("B", "s2", "b2"), se("C", "s3", "c")})
	want := []resource.ComponentSecretEnv{se("A", "s1", "a"), se("B", "s2", "b2"), se("C", "s3", "c")}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if len(resource.MergeSecretEnv(nil, nil)) != 0 {
		t.Fatal("empty merge isn't empty")
	}
}

func TestSecretEnvVersion(t *testing.T) {
	t0 := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	list := []resource.ComponentSecretEnv{se("A", "s1", "a"), se("B", "s2", "b")}
	v := resource.SecretEnvVersion(list, map[string]time.Time{"s1": t0, "s2": t0})
	if len(v) != 16 {
		t.Fatalf("version %q isn't 16 hex characters", v)
	}
	if again := resource.SecretEnvVersion(list, map[string]time.Time{"s1": t0, "s2": t0.In(time.FixedZone("x", 3600))}); again != v {
		t.Fatal("the same instants give a different version")
	}
	changed := []struct {
		name    string
		list    []resource.ComponentSecretEnv
		written map[string]time.Time
	}{
		{"a value written again", list, map[string]time.Time{"s1": t0, "s2": t0.Add(time.Nanosecond)}},
		{"another key", []resource.ComponentSecretEnv{se("A", "s1", "a2"), se("B", "s2", "b")}, map[string]time.Time{"s1": t0, "s2": t0}},
		{"another secret", []resource.ComponentSecretEnv{se("A", "s3", "a"), se("B", "s2", "b")}, map[string]time.Time{"s3": t0, "s2": t0}},
		{"a variable renamed", []resource.ComponentSecretEnv{se("A2", "s1", "a"), se("B", "s2", "b")}, map[string]time.Time{"s1": t0, "s2": t0}},
		{"a variable removed", list[:1], map[string]time.Time{"s1": t0}},
		{"no value yet", list, map[string]time.Time{"s1": t0}},
	}
	for _, c := range changed {
		if got := resource.SecretEnvVersion(c.list, c.written); got == v {
			t.Errorf("%s: version unchanged", c.name)
		}
	}
	// Field boundaries are length-prefixed: shifting a character between
	// fields changes the version.
	a := resource.SecretEnvVersion([]resource.ComponentSecretEnv{se("AB", "s", "k")}, nil)
	b := resource.SecretEnvVersion([]resource.ComponentSecretEnv{se("A", "Bs", "k")}, nil)
	if a == b {
		t.Fatal("field boundaries don't change the version")
	}
}

func TestSecretEdges(t *testing.T) {
	keep := resource.Relationship{Type: resource.EdgeRunsOn, TargetKind: "Cluster", TargetID: "k1"}
	old := resource.Relationship{Type: resource.EdgeDependsOn, TargetKind: resource.KindSecret, TargetID: "gone"}
	got := resource.ComponentRelationships([]resource.Relationship{keep, old}, resource.ComponentSpec{
		Dependencies: []resource.ComponentDependency{{Component: "c2", Port: "http", Env: "U"}},
		SecretEnv:    []resource.ComponentSecretEnv{se("A", "s1", "a"), se("B", "s1", "b"), se("C", "s2", "c")},
	})
	want := []resource.Relationship{keep,
		{Type: resource.EdgeDependsOn, TargetKind: resource.KindComponent, TargetID: "c2"},
		{Type: resource.EdgeDependsOn, TargetKind: resource.KindSecret, TargetID: "s1"},
		{Type: resource.EdgeDependsOn, TargetKind: resource.KindSecret, TargetID: "s2"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("component edges: got %v, want %v", got, want)
	}
	d := resource.DeploymentRelationships(resource.DeploymentSpec{Component: "c1",
		Overrides: resource.DeploymentOverrides{SecretEnv: []resource.ComponentSecretEnv{se("A", "s3", "a")}}})
	wantD := []resource.Relationship{
		{Type: resource.EdgeDependsOn, TargetKind: resource.KindComponent, TargetID: "c1"},
		{Type: resource.EdgeDependsOn, TargetKind: resource.KindSecret, TargetID: "s3"},
	}
	if !slices.Equal(d, wantD) {
		t.Fatalf("deployment edges: got %v, want %v", d, wantD)
	}
}
