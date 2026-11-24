package openchoreo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// Secret variables (specs/026-secrets-into-components plan decisions 1,
// 3, 8 and 12). Serendip writes one plain Kubernetes Secret per
// Deployment, <component>-secret-env, into the Deployment's data-plane
// namespace; the component type loads it with envFrom. Only the Secret's
// name and a metadata-only version reach OpenChoreo's objects, in the
// ReleaseBinding's componentTypeEnvironmentConfigs.serendipSecretEnv. The
// version is also a pod-template annotation, so a new one rolls the pods.
//
// No error from here may contain a value (reconcile.SecretEnvProvider):
// the Secret's write and read errors are reduced to their status code.

var _ reconcile.SecretEnvProvider = (*Provider)(nil)

// secretEnvProperty is the component type's reserved environment-config
// property carrying {name, version}. It's stripped from the schema
// ConfigSchemas reports, so a user's config can't set it.
const secretEnvProperty = "serendipSecretEnv"

// secretEnvVersionAnnotation is the version on the pod template (set by the
// component type) and on the cluster Secret (set here).
const secretEnvVersionAnnotation = "serendip.dev/secret-env-version"

const (
	releaseNeedsNewMessage = "this release predates secret variables: deploy a newer release"
	// typeNeedsUpdateMessage: the Component's current release was cut from
	// an installed type that predates them, until the platform defaults
	// pass updates it.
	typeNeedsUpdateMessage = "the platform's component type predates secret variables: waiting for it to be updated"
)

// secretEnvName names a Component's cluster Secret in a data-plane
// namespace; the namespace is per Application and Environment, so the
// name is unique per Deployment.
func secretEnvName(compID string) string { return secretEnvNameOf(componentName(compID)) }

// secretEnvNameOf is secretEnvName from the platform component's name.
func secretEnvNameOf(component string) string { return component + "-secret-env" }

func secretsPath(namespace string) string { return "/api/v1/namespaces/" + namespace + "/secrets" }

// dataPlaneNamespace is the namespace the project binding of project in
// envName (the platform's Environment name) made, "" while it hasn't.
func (p *Provider) dataPlaneNamespace(ctx context.Context, project, envName string) (string, error) {
	prb, err := p.client.Get(ctx, namespacedPath("projectreleasebindings")+"/"+project+"-"+envName)
	if errors.Is(err, kubernetes.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get the project binding %s-%s: %w", project, envName, err)
	}
	var st struct {
		Namespace string `json:"namespace"`
	}
	if len(prb.Status) > 0 {
		if err := json.Unmarshal(prb.Status, &st); err != nil {
			return "", fmt.Errorf("decode the project binding %s-%s's status: %w", project, envName, err)
		}
	}
	return st.Namespace, nil
}

// secretEnvNamespace is comp's data-plane namespace in env, "" while it
// doesn't exist yet.
func (p *Provider) secretEnvNamespace(ctx context.Context, comp resource.Component, env resource.Environment) (string, error) {
	project, err := componentProject(comp)
	if err != nil {
		return "", err
	}
	return p.dataPlaneNamespace(ctx, project, environmentObjectName(env.Metadata.ID))
}

// SecretEnvVersion is the version annotation on d's cluster Secret, ""
// when there's no Secret. ErrSecretEnvPending before the data-plane
// namespace exists.
func (p *Provider) SecretEnvVersion(ctx context.Context, comp resource.Component, env resource.Environment, d resource.Deployment) (string, error) {
	ns, err := p.secretEnvNamespace(ctx, comp, env)
	if err != nil {
		return "", err
	}
	if ns == "" {
		return "", reconcile.ErrSecretEnvPending
	}
	sec, err := p.client.Get(ctx, secretsPath(ns)+"/"+secretEnvName(comp.Metadata.ID))
	if errors.Is(err, kubernetes.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get the secret variables of Deployment %s: %w", d.Metadata.ID, valueFreeError(err))
	}
	return sec.Metadata.Annotations[secretEnvVersionAnnotation], nil
}

// WriteSecretEnv applies d's cluster Secret holding vars, annotated with
// version; empty vars deletes it. Deploy then names the version in the
// ReleaseBinding, which is what rolls the pods, so new pods never start
// against an older Secret.
func (p *Provider) WriteSecretEnv(ctx context.Context, comp resource.Component, env resource.Environment, d resource.Deployment, vars map[string]string, version string) error {
	ns, err := p.secretEnvNamespace(ctx, comp, env)
	if err != nil {
		return err
	}
	name := secretEnvName(comp.Metadata.ID)
	if len(vars) == 0 {
		if ns == "" {
			return nil // no namespace, so no Secret
		}
		return p.deleteSecretEnv(ctx, ns, name)
	}
	if ns == "" {
		return reconcile.ErrSecretEnvPending
	}
	data := make(map[string][]byte, len(vars))
	for k, v := range vars {
		data[k] = []byte(v)
	}
	// Clearing these is best effort: the client's request body is a copy.
	raw, _ := json.Marshal(data) // a map of byte slices always encodes
	clear(data)
	_, err = p.client.Apply(ctx, secretsPath(ns)+"/"+name, kubernetes.Object{
		APIVersion: "v1", Kind: "Secret",
		Metadata: kubernetes.ObjectMeta{Name: name, Namespace: ns,
			Labels:      serendipLabels(d.Metadata.ID, d.Metadata.Name),
			Annotations: map[string]string{secretEnvVersionAnnotation: version}},
		Data: raw,
	}, kubernetes.FieldManager)
	clear(raw)
	if errors.Is(err, kubernetes.ErrNotFound) {
		// The namespace is named but not there yet (or is going).
		return reconcile.ErrSecretEnvPending
	}
	if err != nil {
		return fmt.Errorf("write the secret variables of Deployment %s: %w", d.Metadata.ID, valueFreeError(err))
	}
	return nil
}

func (p *Provider) deleteSecretEnv(ctx context.Context, ns, name string) error {
	err := p.client.Delete(ctx, secretsPath(ns)+"/"+name)
	if err == nil || errors.Is(err, kubernetes.ErrNotFound) {
		return nil
	}
	return fmt.Errorf("delete Secret %s/%s: %w", ns, name, valueFreeError(err))
}

// valueFreeError drops an API answer's body, which is the server's
// wording about a request that held values: only its status is kept, still
// as a *kubernetes.StatusError.
func valueFreeError(err error) error {
	var se *kubernetes.StatusError
	if errors.As(err, &se) {
		return &kubernetes.StatusError{Code: se.Code}
	}
	return err
}

// releaseTakesSecretEnv reports whether a release's component type
// snapshot has the secret-variables property. A release pins its type,
// and one cut before the property existed silently ignores it (T001).
func releaseTakesSecretEnv(release kubernetes.Object) bool {
	var s struct {
		ComponentType struct {
			Spec struct {
				EnvironmentConfigs struct {
					OpenAPIV3Schema struct {
						Properties map[string]json.RawMessage `json:"properties"`
					} `json:"openAPIV3Schema"`
				} `json:"environmentConfigs"`
			} `json:"spec"`
		} `json:"componentType"`
	}
	if json.Unmarshal(release.Spec, &s) != nil {
		return false
	}
	_, ok := s.ComponentType.Spec.EnvironmentConfigs.OpenAPIV3Schema.Properties[secretEnvProperty]
	return ok
}

// bindingConfig is the ReleaseBinding's componentTypeEnvironmentConfigs:
// the user's config, with the reserved property Serendip's alone (the
// schema ConfigSchemas reports leaves it out, so validation refuses it in a
// user's config; any such value is dropped here too), nil when empty.
func bindingConfig(config map[string]any, compID, secretEnvVersion string) map[string]any {
	out := make(map[string]any, len(config)+1)
	for k, v := range config {
		if k != secretEnvProperty {
			out[k] = v
		}
	}
	if secretEnvVersion != "" {
		out[secretEnvProperty] = map[string]string{"name": secretEnvName(compID), "version": secretEnvVersion}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
