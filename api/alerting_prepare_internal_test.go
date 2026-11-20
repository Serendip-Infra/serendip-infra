package api

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store/sqlite"
)

// Only serendipd sets builtIn (spec 024 T006 review): a person can't
// create a built-in rule, or clear the flag to move one to a Component
// and delete it alone.
func TestBuiltInKept(t *testing.T) {
	ctx := t.Context()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "rules.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	insts := sqlite.NewStore[resource.ProviderInstanceSpec, resource.ProviderInstanceStatus](db, resource.KindProviderInstance)
	rules := sqlite.NewStore[resource.AlertRuleSpec, resource.AlertRuleStatus](db, resource.KindAlertRule)
	inst, err := insts.Create(ctx, resource.ProviderInstance{Metadata: resource.Metadata{Name: "obs"},
		Spec: resource.ProviderInstanceSpec{CapabilityID: "observability", ProviderID: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	spec := resource.AlertRuleSpec{
		Condition: resource.AlertCondition{Type: resource.ConditionTypePackageDisk, PackageDisk: &resource.PackageDiskCondition{PercentOfBudget: 85}},
		Severity:  resource.SeverityWarning, Summary: "disk", BuiltIn: true,
	}
	rule, err := rules.Create(ctx, resource.AlertRule{Metadata: resource.Metadata{Name: "disk",
		OwnerRefs: []resource.OwnerRef{{Kind: resource.KindProviderInstance, ID: inst.Metadata.ID}}}, Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	check := builtInKept(rules)
	cleared := spec
	cleared.BuiltIn = false
	for _, tt := range []struct {
		name string
		id   string
		spec resource.AlertRuleSpec
		ok   bool
	}{
		{"a person creating a built-in rule", "", spec, false},
		{"a person creating a rule", "", cleared, true},
		{"clearing builtIn", rule.Metadata.ID, cleared, false},
		{"editing a built-in rule", rule.Metadata.ID, spec, true},
		{"an unknown rule: the update answers", "missing", cleared, true},
	} {
		err := check(ctx, tt.id, tt.spec)
		if (err == nil) != tt.ok || (err != nil && !errors.Is(err, resource.ErrValidation)) {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
}
