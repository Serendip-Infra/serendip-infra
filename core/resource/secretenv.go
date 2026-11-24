package resource

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
)

// ComponentSecretEnv sets one environment variable of a running Component
// from one key of a Secret's value (spec 026). It names the value; it
// never holds it, so it's safe in Spec, the audit log and API responses.
type ComponentSecretEnv struct {
	// Env is the variable's name.
	Env string `json:"env"`
	// Secret is the Secret's ID.
	Secret string `json:"secret"`
	// Key is a key of the Secret's value (a value is a map of keys).
	Key string `json:"key"`
}

// MaxSecretEnv bounds one list of secret variables (a Component's, or a
// Deployment's overrides).
const MaxSecretEnv = 64

const maxSecretKeyLen = 256

// ValidateSecretEnv checks one list on its own: names, IDs, keys, the
// limit and duplicates. field names the list in messages ("secretEnv",
// "overrides.secretEnv").
func ValidateSecretEnv(field string, list []ComponentSecretEnv) error {
	if len(list) > MaxSecretEnv {
		return fmt.Errorf("%w: %s: at most %d variables", ErrValidation, field, MaxSecretEnv)
	}
	seen := map[string]bool{}
	for i, e := range list {
		at := fmt.Sprintf("%s[%d]", field, i)
		switch {
		case !envNameRE.MatchString(e.Env):
			return fmt.Errorf("%w: %s.env: %q isn't a variable name", ErrValidation, at, e.Env)
		case seen[e.Env]:
			return fmt.Errorf("%w: %s.env: %s is set twice", ErrValidation, at, e.Env)
		case !validRefID(e.Secret):
			return fmt.Errorf("%w: %s.secret: a Secret ID is required", ErrValidation, at)
		case !validSecretKey(e.Key):
			return fmt.Errorf("%w: %s.key: 1 to %d bytes, no control characters", ErrValidation, at, maxSecretKeyLen)
		}
		seen[e.Env] = true
	}
	return nil
}

func validSecretKey(k string) bool {
	if k == "" || len(k) > maxSecretKeyLen {
		return false
	}
	return !strings.ContainsFunc(k, unicode.IsControl)
}

// ValidateComponentSecretEnv checks a Component's secret variables: the
// list itself, and that none is also a plain variable or a dependency's
// address variable, so which value a variable gets never depends on
// ordering.
func ValidateComponentSecretEnv(s ComponentSpec) error {
	if err := ValidateSecretEnv("secretEnv", s.SecretEnv); err != nil {
		return err
	}
	for i, e := range s.SecretEnv {
		at := fmt.Sprintf("secretEnv[%d].env", i)
		if _, clash := s.Env[e.Env]; clash {
			return fmt.Errorf("%w: %s: %s is also set in env", ErrValidation, at, e.Env)
		}
		for _, d := range s.Dependencies {
			if d.Env == e.Env {
				return fmt.Errorf("%w: %s: %s is also a dependency's variable", ErrValidation, at, e.Env)
			}
		}
	}
	return nil
}

// ValidateDeploymentSecretEnv checks a Deployment's overrides against its
// Component across the two layers: an override's secret variable may
// replace the Component's secret variable of that name, but it may not
// share a name with a plain variable in either layer or with a
// dependency's variable, and an override's plain variable may not shadow
// a Component's secret variable. The overrides' own list is checked by
// ValidateDeploymentSpec.
func ValidateDeploymentSecretEnv(o DeploymentOverrides, comp ComponentSpec) error {
	for i, e := range o.SecretEnv {
		at := fmt.Sprintf("overrides.secretEnv[%d].env", i)
		if _, clash := comp.Env[e.Env]; clash {
			return fmt.Errorf("%w: %s: %s is a plain variable of the Component", ErrValidation, at, e.Env)
		}
		for _, d := range comp.Dependencies {
			if d.Env == e.Env {
				return fmt.Errorf("%w: %s: %s is a dependency's variable", ErrValidation, at, e.Env)
			}
		}
	}
	for k := range o.Env {
		if slices.ContainsFunc(comp.SecretEnv, func(e ComponentSecretEnv) bool { return e.Env == k }) {
			return fmt.Errorf("%w: overrides.env: %s is a secret variable of the Component; override it in overrides.secretEnv", ErrValidation, k)
		}
	}
	return nil
}

// validateOverridesSecretEnv is the overrides' own check, run by
// ValidateDeploymentSpec.
func validateOverridesSecretEnv(o DeploymentOverrides) error {
	if err := ValidateSecretEnv("overrides.secretEnv", o.SecretEnv); err != nil {
		return err
	}
	for i, e := range o.SecretEnv {
		if _, clash := o.Env[e.Env]; clash {
			return fmt.Errorf("%w: overrides.secretEnv[%d].env: %s is also set in overrides.env", ErrValidation, i, e.Env)
		}
	}
	return nil
}

// MergeSecretEnv is what a Deployment runs with: the Component's secret
// variables, each replaced by the override of the same name, plus the
// overrides' new ones, sorted by variable name.
func MergeSecretEnv(comp []ComponentSecretEnv, overrides []ComponentSecretEnv) []ComponentSecretEnv {
	by := map[string]ComponentSecretEnv{}
	for _, e := range comp {
		by[e.Env] = e
	}
	for _, e := range overrides {
		by[e.Env] = e
	}
	out := make([]ComponentSecretEnv, 0, len(by))
	for _, e := range by {
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b ComponentSecretEnv) int { return strings.Compare(a.Env, b.Env) })
	return out
}

// SecretEnvSecretIDs lists the distinct Secret IDs a list names, in
// first-seen order.
func SecretEnvSecretIDs(list []ComponentSecretEnv) []string {
	var out []string
	for _, e := range list {
		if !slices.Contains(out, e.Secret) {
			out = append(out, e.Secret)
		}
	}
	return out
}

// SecretEnvVersion is the version of a merged list's values: a hash of
// each (variable, Secret, key) and the time its Secret's value was last
// written. It's built from metadata only, so it says nothing about a
// value, and it changes whenever a named value is written again, which is
// what rolls the pods (spec 026 plan decisions 2 and 3). merged must be
// sorted (MergeSecretEnv); written gives each Secret's last write.
func SecretEnvVersion(merged []ComponentSecretEnv, written map[string]time.Time) string {
	h := sha256.New()
	for _, e := range merged {
		w := written[e.Secret]
		fmt.Fprintf(h, "%d:%s|%d:%s|%d:%s|%d\n", len(e.Env), e.Env, len(e.Secret), e.Secret, len(e.Key), e.Key, w.UTC().UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// secretEdges replaces rels' depends-on edges to Secrets with one for each
// Secret list names: the server owns those edges.
func secretEdges(rels []Relationship, list []ComponentSecretEnv) []Relationship {
	var out []Relationship
	for _, r := range rels {
		if r.Type != EdgeDependsOn || r.TargetKind != KindSecret {
			out = append(out, r)
		}
	}
	for _, id := range SecretEnvSecretIDs(list) {
		out = append(out, Relationship{Type: EdgeDependsOn, TargetKind: KindSecret, TargetID: id})
	}
	return out
}
