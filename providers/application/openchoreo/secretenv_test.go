package openchoreo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/serendip-infra/serendip-infra/core/jsonschema"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

const testDataPlaneNS = "dp-shop-staging"

func secretEnvPath() string {
	return secretsPath(testDataPlaneNS) + "/" + secretEnvName(testCompID)
}

// withDataPlaneNamespace stores the project binding with the namespace it
// made, as the platform reports it once NamespaceReady.
func (f *fakeAPI) withDataPlaneNamespace() *fakeAPI {
	f.put(prbPath(), kubernetes.Object{APIVersion: apiVersion, Kind: "ProjectReleaseBinding",
		Metadata: kubernetes.ObjectMeta{Name: projectEnvBindingName(projectName(testAppID), testEnvID)},
		Status:   mustJSON(map[string]any{"namespace": testDataPlaneNS})})
	return f
}

func TestSecretEnv_PendingUntilTheNamespaceExists(t *testing.T) {
	api := newFakeAPI()
	p := NewProvider(newTestKubeClient(t, api.handler(t)), testGateway)
	ctx, comp, d := context.Background(), testComp(webSpec), testDeployment(resource.DeploymentSpec{})
	if _, err := p.SecretEnvVersion(ctx, comp, testEnv(), d); !errors.Is(err, reconcile.ErrSecretEnvPending) {
		t.Errorf("no project binding: SecretEnvVersion = %v, want pending", err)
	}
	if err := p.WriteSecretEnv(ctx, comp, testEnv(), d, map[string]string{"A": "x"}, "v1"); !errors.Is(err, reconcile.ErrSecretEnvPending) {
		t.Errorf("no project binding: WriteSecretEnv = %v, want pending", err)
	}
	// Removing them needs nothing: without a namespace there's no Secret.
	if err := p.WriteSecretEnv(ctx, comp, testEnv(), d, nil, ""); err != nil {
		t.Errorf("removing with no namespace = %v", err)
	}
	// The binding exists but hasn't made the namespace yet.
	api.put(prbPath(), kubernetes.Object{Metadata: kubernetes.ObjectMeta{Name: "prb"}})
	if _, err := p.SecretEnvVersion(ctx, comp, testEnv(), d); !errors.Is(err, reconcile.ErrSecretEnvPending) {
		t.Errorf("namespace not reported: SecretEnvVersion = %v, want pending", err)
	}
}

func TestSecretEnv_WriteReadRemove(t *testing.T) {
	api := newFakeAPI().withDataPlaneNamespace()
	p := NewProvider(newTestKubeClient(t, api.handler(t)), testGateway)
	ctx, comp, d := context.Background(), testComp(webSpec), testDeployment(resource.DeploymentSpec{})
	if v, err := p.SecretEnvVersion(ctx, comp, testEnv(), d); err != nil || v != "" {
		t.Fatalf("before any write = (%q, %v), want none", v, err)
	}
	if err := p.WriteSecretEnv(ctx, comp, testEnv(), d, map[string]string{"DB_PASSWORD": "s3cret", "TOKEN": "t"}, "v1"); err != nil {
		t.Fatal(err)
	}
	sec := api.objects[secretEnvPath()]
	var data map[string][]byte
	if err := json.Unmarshal(sec.Data, &data); err != nil {
		t.Fatal(err)
	}
	if string(data["DB_PASSWORD"]) != "s3cret" || string(data["TOKEN"]) != "t" || len(data) != 2 {
		t.Errorf("Secret data = %q", data)
	}
	if sec.Kind != "Secret" || sec.Metadata.Namespace != testDataPlaneNS || sec.Metadata.Labels[labelSerendipID] != d.Metadata.ID {
		t.Errorf("Secret = %+v", sec.Metadata)
	}
	if v, err := p.SecretEnvVersion(ctx, comp, testEnv(), d); err != nil || v != "v1" {
		t.Errorf("after the write = (%q, %v), want v1", v, err)
	}
	// The same write again stores nothing.
	api.writes = 0
	if err := p.WriteSecretEnv(ctx, comp, testEnv(), d, map[string]string{"DB_PASSWORD": "s3cret", "TOKEN": "t"}, "v1"); err != nil || api.writes != 0 {
		t.Errorf("repeat write = %v, %d writes", err, api.writes)
	}
	// Removing the last one deletes the Secret; again is fine.
	for range 2 {
		if err := p.WriteSecretEnv(ctx, comp, testEnv(), d, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := api.objects[secretEnvPath()]; ok {
		t.Error("the Secret is still there")
	}
	if v, err := p.SecretEnvVersion(ctx, comp, testEnv(), d); err != nil || v != "" {
		t.Errorf("after removing = (%q, %v)", v, err)
	}
}

// The API server's answer is dropped from errors: it's about a request
// that held values.
func TestSecretEnv_ErrorsHoldNoAnswer(t *testing.T) {
	api := newFakeAPI().withDataPlaneNamespace()
	p := NewProvider(newTestKubeClient(t, api.handler(t)), testGateway)
	api.failPath = secretsPath(testDataPlaneNS)
	ctx, comp, d := context.Background(), testComp(webSpec), testDeployment(resource.DeploymentSpec{})
	errs := []error{
		p.WriteSecretEnv(ctx, comp, testEnv(), d, map[string]string{"A": "s3cret"}, "v1"),
		p.WriteSecretEnv(ctx, comp, testEnv(), d, nil, ""),
	}
	_, err := p.SecretEnvVersion(ctx, comp, testEnv(), d)
	errs = append(errs, err)
	for i, err := range errs {
		if err == nil {
			t.Errorf("call %d succeeded against a failing API", i)
			continue
		}
		if msg := err.Error(); strings.Contains(msg, "simulated failure") || strings.Contains(msg, "s3cret") || !strings.Contains(msg, "500") {
			t.Errorf("call %d's error = %q, want the status only", i, msg)
		}
	}
}

func bindingOf(t *testing.T, api *fakeAPI) bindingSpec {
	t.Helper()
	var b bindingSpec
	if err := json.Unmarshal(api.objects[bindingPath()].Spec, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDeploy_NamesTheSecretEnvInTheBinding(t *testing.T) {
	api := newFakeAPI().withComponentType()
	p := NewProvider(newTestKubeClient(t, api.handler(t)), testGateway)
	// A user's own value for the reserved property is never sent.
	d := testDeployment(resource.DeploymentSpec{Overrides: resource.DeploymentOverrides{
		Config: map[string]any{"replicas": 2, secretEnvProperty: map[string]any{"name": "someone-elses", "version": "x"}}}})
	if _, err := p.Deploy(context.Background(), testApp(), testComp(webSpec), testEnv(), d, "v1"); err != nil {
		t.Fatal(err)
	}
	b := bindingOf(t, api)
	got, _ := json.Marshal(b.ComponentTypeEnvironmentConfigs[secretEnvProperty])
	want, _ := json.Marshal(map[string]string{"name": secretEnvName(testCompID), "version": "v1"})
	if string(got) != string(want) || b.ComponentTypeEnvironmentConfigs["replicas"] != float64(2) {
		t.Errorf("config = %v, want replicas and %s", b.ComponentTypeEnvironmentConfigs, want)
	}

	// Without secret variables the property isn't sent, so a Deployment
	// that never had any has the binding it had before spec 026.
	deploy(t, p, d)
	if _, ok := bindingOf(t, api).ComponentTypeEnvironmentConfigs[secretEnvProperty]; ok {
		t.Error("the property was sent with no secret variables")
	}
	deploy(t, p, testDeployment(resource.DeploymentSpec{}))
	if c := bindingOf(t, api).ComponentTypeEnvironmentConfigs; c != nil {
		t.Errorf("config = %v, want none", c)
	}
}

// A release whose type snapshot predates the property would ignore it:
// reported, and the binding keeps what it runs.
func TestDeploy_OldReleaseNeedsANewOne(t *testing.T) {
	api := newFakeAPI().withComponentType()
	p := NewProvider(newTestKubeClient(t, api.handler(t)), testGateway)
	deploy(t, p, testDeployment(resource.DeploymentSpec{}))
	before := bindingOf(t, api)

	old := componentTypeSpec()
	delete(old["environmentConfigs"].(map[string]any)["openAPIV3Schema"].(map[string]any)["properties"].(map[string]any), secretEnvProperty)
	api.put(nsItem("componentreleases", "cmp-old-1"), kubernetes.Object{Metadata: kubernetes.ObjectMeta{Name: "cmp-old-1"},
		Spec: mustJSON(map[string]any{"owner": ownerRef{ComponentName: componentName(testCompID)},
			"componentType": map[string]any{"kind": "ClusterComponentType", "name": "x", "spec": old}})})
	pinned := testDeployment(resource.DeploymentSpec{Release: "cmp-old-1"})
	obs, err := p.Deploy(context.Background(), testApp(), testComp(webSpec), testEnv(), pinned, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if obs.Ready || obs.Reason != reconcile.ReasonNeedsNewRelease || obs.Message != releaseNeedsNewMessage {
		t.Errorf("old release = %+v", obs)
	}
	if after := bindingOf(t, api); after.ReleaseName != before.ReleaseName {
		t.Errorf("binding moved to %q", after.ReleaseName)
	}
	// Without secret variables the old release deploys as before.
	deploy(t, p, pinned)
	if b := bindingOf(t, api); b.ReleaseName != "cmp-old-1" {
		t.Errorf("rollback bound %q", b.ReleaseName)
	}
	// The Component's current release has the property.
	if obs, err := p.Deploy(context.Background(), testApp(), testComp(webSpec), testEnv(), testDeployment(resource.DeploymentSpec{}), "v1"); err != nil || obs.Reason == reconcile.ReasonNeedsNewRelease {
		t.Errorf("current release = %+v, %v", obs, err)
	}
}

func TestUndeploy_RemovesTheSecretEnv(t *testing.T) {
	api := newFakeAPI().withComponentType().withDataPlaneNamespace()
	p := NewProvider(newTestKubeClient(t, api.handler(t)), testGateway)
	ctx, comp, d := context.Background(), testComp(webSpec), testDeployment(resource.DeploymentSpec{})
	if _, err := p.Deploy(ctx, testApp(), comp, testEnv(), d, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := p.WriteSecretEnv(ctx, comp, testEnv(), d, map[string]string{"A": "x"}, "v1"); err != nil {
		t.Fatal(err)
	}
	// A failing delete stops before the bindings, so a retry finds the
	// namespace again.
	api.failPath = secretsPath(testDataPlaneNS)
	if err := p.Undeploy(ctx, d); err == nil {
		t.Fatal("Undeploy succeeded with the Secret undeletable")
	}
	if _, ok := api.objects[prbPath()]; !ok {
		t.Fatal("the project binding went before the Secret")
	}
	api.failPath = ""
	if err := p.Undeploy(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, ok := api.objects[secretEnvPath()]; ok {
		t.Error("the Secret is still there")
	}
	if _, ok := api.objects[bindingPath()]; ok {
		t.Error("the binding is still there")
	}
}

// The reserved property is in the installed type, and the template uses
// it, but the schema reported (and so validated against) doesn't have it.
func TestSecretEnv_ReservedInTheSchema(t *testing.T) {
	ct := componentTypeSpec()
	props := ct["environmentConfigs"].(map[string]any)["openAPIV3Schema"].(map[string]any)["properties"].(map[string]any)
	if _, ok := props[secretEnvProperty]; !ok {
		t.Fatal("the component type lacks the property")
	}
	tmpl := string(mustJSON(deploymentTemplate()))
	for _, want := range []string{
		`dyn(configurations.toContainerEnvFrom())`,
		`environmentConfigs.` + secretEnvProperty + `.name`,
		`"` + secretEnvVersionAnnotation + `":"${environmentConfigs.` + secretEnvProperty + `.version}"`,
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("deployment template lacks %s", want)
		}
	}
	if !releaseTakesSecretEnv(kubernetes.Object{Spec: mustJSON(map[string]any{"componentType": map[string]any{"spec": ct}})}) {
		t.Error("a release of the current type is read as old")
	}

	api := newFakeAPI().withComponentType()
	p := NewProvider(newTestKubeClient(t, api.handler(t)), testGateway)
	got, err := p.ConfigSchemas(context.Background(), resource.Component{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got.Environment), secretEnvProperty) {
		t.Errorf("reported schema has the reserved property: %s", got.Environment)
	}
	s, err := jsonschema.Parse(got.Environment)
	if err != nil {
		t.Fatal(err)
	}
	if problems := s.Validate(map[string]any{secretEnvProperty: map[string]any{"name": "x"}}); len(problems) == 0 {
		t.Error("a user's config could set the reserved property")
	}
	if problems := s.Validate(map[string]any{"replicas": float64(1)}); len(problems) != 0 {
		t.Errorf("plain config refused: %v", problems)
	}
}

// The project binding names a namespace that isn't there yet (or is
// going): pending, not an error. A Secret written by hand, with no version,
// is read as none, so it's rewritten.
func TestSecretEnv_NamespaceNamedButAbsent(t *testing.T) {
	api := newFakeAPI().withDataPlaneNamespace()
	p := NewProvider(newTestKubeClient(t, api.handler(t)), testGateway)
	ctx, comp, d := context.Background(), testComp(webSpec), testDeployment(resource.DeploymentSpec{})
	api.missingPath = secretsPath(testDataPlaneNS)
	if err := p.WriteSecretEnv(ctx, comp, testEnv(), d, map[string]string{"A": "x"}, "v1"); !errors.Is(err, reconcile.ErrSecretEnvPending) {
		t.Errorf("WriteSecretEnv = %v, want pending", err)
	}
	if err := p.WriteSecretEnv(ctx, comp, testEnv(), d, nil, ""); err != nil {
		t.Errorf("removing from an absent namespace = %v", err)
	}
	api.missingPath = ""
	api.put(secretEnvPath(), kubernetes.Object{Kind: "Secret", Metadata: kubernetes.ObjectMeta{Name: secretEnvName(testCompID)}})
	if v, err := p.SecretEnvVersion(ctx, comp, testEnv(), d); err != nil || v != "" {
		t.Errorf("unannotated Secret = (%q, %v), want none", v, err)
	}
	// An unreadable status is an error, not pending.
	api.put(prbPath(), kubernetes.Object{Status: json.RawMessage(`{"namespace": 7}`)})
	if _, err := p.SecretEnvVersion(ctx, comp, testEnv(), d); err == nil || errors.Is(err, reconcile.ErrSecretEnvPending) {
		t.Errorf("bad status = %v, want an error", err)
	}
}

// A Deployment of the current release, cut from an installed type that
// predates secret variables, says it waits for the type.
func TestDeploy_StaleInstalledType(t *testing.T) {
	api := newFakeAPI().withComponentType()
	old := componentTypeSpec()
	delete(old["environmentConfigs"].(map[string]any)["openAPIV3Schema"].(map[string]any)["properties"].(map[string]any), secretEnvProperty)
	api.put(clusterPath("clustercomponenttypes")+"/"+componentTypeName, kubernetes.Object{Metadata: kubernetes.ObjectMeta{Name: componentTypeName}, Spec: mustJSON(old)})
	p := NewProvider(newTestKubeClient(t, api.handler(t)), testGateway)
	obs, err := p.Deploy(context.Background(), testApp(), testComp(webSpec), testEnv(), testDeployment(resource.DeploymentSpec{}), "v1")
	if err != nil || obs.Reason != reconcile.ReasonNeedsNewRelease || obs.Message != typeNeedsUpdateMessage {
		t.Errorf("stale type = %+v, %v", obs, err)
	}
	if _, ok := api.objects[bindingPath()]; ok {
		t.Error("bound a release that would ignore the secret variables")
	}
}

// With no namespace reported, Undeploy has no Secret to remove and still
// removes the bindings.
func TestUndeploy_NoNamespaceStillCleansUp(t *testing.T) {
	api := newFakeAPI().withComponentType()
	p := NewProvider(newTestKubeClient(t, api.handler(t)), testGateway)
	d := testDeployment(resource.DeploymentSpec{})
	deploy(t, p, d)
	if err := p.Undeploy(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if _, ok := api.objects[bindingPath()]; ok {
		t.Error("the binding is still there")
	}
	if api.count("DELETE "+secretsPath("")) != 0 {
		t.Error("deleted a Secret with no namespace")
	}
}

func TestWithoutReserved(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"empty", ``, ``},
		{"no properties", `{"type":"object"}`, `{"type":"object"}`},
		{"not reserved", `{"properties":{"replicas":{"type":"integer"}}}`, `{"properties":{"replicas":{"type":"integer"}}}`},
		{"reserved", `{"type":"object","properties":{"replicas":{"type":"integer"},"serendipSecretEnv":{"type":"object"}}}`, `{"type":"object","properties":{"replicas":{"type":"integer"}}}`},
	} {
		got, err := withoutReserved(json.RawMessage(tc.in))
		if err != nil || !jsonEqual(got, json.RawMessage(tc.want)) {
			t.Errorf("%s: withoutReserved = (%s, %v), want %s", tc.name, got, err, tc.want)
		}
	}
	if _, err := withoutReserved(json.RawMessage(`{"properties":[]}`)); err == nil {
		t.Error("bad properties accepted")
	}
}

func TestBindingConfig(t *testing.T) {
	ref := map[string]string{"name": secretEnvName(testCompID), "version": "v1"}
	for _, tc := range []struct {
		name    string
		config  map[string]any
		version string
		want    map[string]any
	}{
		{"nothing", nil, "", nil},
		{"version only", nil, "v1", map[string]any{secretEnvProperty: ref}},
		{"reserved without a version", map[string]any{secretEnvProperty: "x"}, "", nil},
		{"reserved replaced", map[string]any{secretEnvProperty: "x", "replicas": 2}, "v1", map[string]any{secretEnvProperty: ref, "replicas": 2}},
	} {
		got := bindingConfig(tc.config, testCompID, tc.version)
		if !jsonEqual(mustJSON(got), mustJSON(tc.want)) || (got == nil) != (tc.want == nil) {
			t.Errorf("%s: bindingConfig = %v, want %v", tc.name, got, tc.want)
		}
	}
}
