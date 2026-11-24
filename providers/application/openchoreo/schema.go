package openchoreo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
)

var _ reconcile.ConfigSchemaProvider = (*Provider)(nil)

// ConfigSchemas reads what can be configured on comp from the platform's
// own component type: its per-environment schema
// (spec.environmentConfigs.openAPIV3Schema), less the property Serendip
// reserves. Read live, so a type changed
// by an upgrade is what's checked. Serendip's Components have no trait
// instances, so there are no trait schemas.
func (p *Provider) ConfigSchemas(ctx context.Context, comp resource.Component) (reconcile.ConfigSchemas, error) {
	ct, err := p.client.Get(ctx, clusterPath("clustercomponenttypes")+"/"+componentTypeName)
	if err != nil {
		return reconcile.ConfigSchemas{}, fmt.Errorf("get the component type %s: %w", componentTypeName, err)
	}
	var spec struct {
		EnvironmentConfigs struct {
			OpenAPIV3Schema json.RawMessage `json:"openAPIV3Schema"`
		} `json:"environmentConfigs"`
	}
	if err := json.Unmarshal(ct.Spec, &spec); err != nil {
		return reconcile.ConfigSchemas{}, fmt.Errorf("decode the component type %s: %w", componentTypeName, err)
	}
	env, err := withoutReserved(spec.EnvironmentConfigs.OpenAPIV3Schema)
	if err != nil {
		return reconcile.ConfigSchemas{}, fmt.Errorf("decode the component type %s's schema: %w", componentTypeName, err)
	}
	return reconcile.ConfigSchemas{Environment: env}, nil
}

// withoutReserved removes the property Serendip sets itself
// (secretEnvProperty) from an environment-config schema. The validator
// refuses properties a schema doesn't list, so a user's config can't set
// it, and the console's form doesn't show it.
func withoutReserved(schema json.RawMessage) (json.RawMessage, error) {
	if len(schema) == 0 {
		return schema, nil
	}
	var s map[string]json.RawMessage
	if err := json.Unmarshal(schema, &s); err != nil {
		return nil, err
	}
	var props map[string]json.RawMessage
	if raw, ok := s["properties"]; ok {
		if err := json.Unmarshal(raw, &props); err != nil {
			return nil, err
		}
	}
	if _, ok := props[secretEnvProperty]; !ok {
		return schema, nil
	}
	delete(props, secretEnvProperty)
	raw, err := json.Marshal(props)
	if err != nil {
		return nil, err
	}
	s["properties"] = raw
	return json.Marshal(s)
}
