package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/serendip-infra/serendip-infra/core/graph"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store"
)

// Secret variables (spec 026): a Deployment's workload gets variables
// from Secrets' values. The reconciler resolves them and hands the values
// to the platform in memory; they never reach Spec, Status, a condition,
// an error or a log line. Messages name a Secret's ID and a key, never a
// value.

// conditionSecretEnvDelivered reports whether a Deployment's secret
// variables are in place. It's present only while the Deployment has any.
const conditionSecretEnvDelivered = "SecretEnvDelivered"

// Reasons of the SecretEnvDelivered condition.
const (
	ReasonSecretEnvDelivered = "Delivered"
	// ReasonSecretHasNoValue: a named Secret has never been written.
	ReasonSecretHasNoValue = "SecretHasNoValue"
	// ReasonSecretKeyNotFound: the key isn't in the Secret's value.
	ReasonSecretKeyNotFound = "SecretKeyNotFound"
	// ReasonSecretNotFound: a named Secret is gone.
	ReasonSecretNotFound = "SecretNotFound"
	// ReasonSecretEnvTooLarge: the variables add up to more than
	// MaxSecretEnvBytes.
	ReasonSecretEnvTooLarge = "SecretEnvTooLarge"
	// ReasonSecretReadFailed: the Secrets capability answered with an
	// error reading a value; retried.
	ReasonSecretReadFailed = "SecretReadFailed"
	// ReasonSecretEnvPending: the Environment's place for them on the
	// platform isn't ready yet (a first deploy); retried.
	ReasonSecretEnvPending = "SecretEnvPending"
	// ReasonNeedsNewRelease: the release the Deployment runs predates
	// secret variables, so the platform would ignore them.
	ReasonNeedsNewRelease = "NeedsNewRelease"
)

// MaxSecretEnvBytes bounds one Deployment's resolved secret variables
// (names plus values).
const MaxSecretEnvBytes = 64 << 10

// ErrSecretEnvPending is a SecretEnvProvider's answer while the
// Environment's place for secret variables doesn't exist yet.
var ErrSecretEnvPending = errors.New("reconcile: the environment isn't ready for secret variables yet")

// ErrSecretEnvUnsupported is a SecretEnvProvider's answer when the
// platform behind it can't deliver secret variables at all.
var ErrSecretEnvUnsupported = errors.New("reconcile: the platform can't deliver secret variables")

// SecretEnvProvider is the part of a DeploymentProvider that delivers
// secret variables. It's optional: a platform without it (or answering
// ErrSecretEnvUnsupported) reports Unsupported for a Deployment that has
// any. Its errors must never contain a value: the reconciler returns
// them, and the engine logs them.
type SecretEnvProvider interface {
	// SecretEnvVersion is the version of d's secret variables on the
	// platform, "" when it holds none. ErrSecretEnvPending before d's
	// Environment can hold them.
	SecretEnvVersion(ctx context.Context, comp resource.Component, env resource.Environment, d resource.Deployment) (string, error)
	// WriteSecretEnv replaces d's secret variables with vars, recording
	// version. Empty vars removes them. Deploy then names version, which
	// is what makes the workload pick them up.
	WriteSecretEnv(ctx context.Context, comp resource.Component, env resource.Environment, d resource.Deployment, vars map[string]string, version string) error
}

// UseSecrets wires the Secrets capability into the Deployment reconciler
// for secret variables: secrets for each Secret's last write, values to
// read them. Without it, a Deployment with secret variables reports
// Unsupported.
func (r *DeploymentReconciler) UseSecrets(secrets store.Store[resource.SecretSpec, resource.SecretStatus], values SecretsProvider) *DeploymentReconciler {
	r.secrets, r.values = secrets, values
	return r
}

// secretEnvResult is what resolving a Deployment's secret variables
// decided:
//   - deployVersion: the version Deploy names ("" for none);
//   - held: the version the platform holds after this step, which status
//     records (removing the last variable relies on it);
//   - cond: the SecretEnvDelivered condition, nil without secret
//     variables;
//   - deploy: whether Deploy may run at all.
type secretEnvResult struct {
	deployVersion string
	held          string
	cond          *resource.Condition
	deploy        bool
}

func secretEnvCond(status resource.ConditionStatus, reason, msg string) *resource.Condition {
	return &resource.Condition{Type: conditionSecretEnvDelivered, Status: status, Reason: reason, Message: msg, LastTransitionTime: time.Now()}
}

const pendingMessage = "waiting for the environment to be ready for secret variables"

// resolveSecretEnv makes the platform hold d's current secret variables.
// It reads values only when the version the platform holds isn't the
// current one, so a pass with nothing changed reads and writes nothing.
// When they can't be delivered, Deploy doesn't run, so the workload keeps
// what it last had.
func (r *DeploymentReconciler) resolveSecretEnv(ctx context.Context, comp resource.Component, env resource.Environment, d resource.Deployment) (secretEnvResult, error) {
	merged := resource.MergeSecretEnv(comp.Spec.SecretEnv, d.Spec.Overrides.SecretEnv)
	had := d.Status.SecretEnvVersion
	// keep is a result that leaves the platform, and so status, as they
	// were.
	keep := func(deploy bool, cond *resource.Condition) secretEnvResult {
		return secretEnvResult{deployVersion: had, held: had, cond: cond, deploy: deploy}
	}
	unsupported := func() secretEnvResult {
		return keep(false, secretEnvCond(resource.ConditionFalse, ReasonUnsupported, "this platform can't deliver secret variables"))
	}
	sp, ok := r.provider.(SecretEnvProvider)
	if len(merged) == 0 {
		if !ok || had == "" {
			return secretEnvResult{deploy: true}, nil
		}
		// The last secret variable went: remove them from the platform.
		// Until that succeeds, status keeps the version, so it's retried.
		switch err := sp.WriteSecretEnv(ctx, comp, env, d, nil, ""); {
		case err == nil, errors.Is(err, ErrSecretEnvUnsupported):
			return secretEnvResult{deploy: true}, nil
		case errors.Is(err, ErrProviderNotReady), errors.Is(err, ErrSecretEnvPending):
			return keep(true, secretEnvCond(resource.ConditionUnknown, ReasonProviderNotReady, providerNotReadyMessage(applicationPlatformCapability))), nil
		default:
			return secretEnvResult{}, fmt.Errorf("remove the secret variables of Deployment %s: %w", d.Metadata.ID, err)
		}
	}
	if !ok || r.secrets == nil || r.values == nil {
		return unsupported(), nil
	}
	secs := map[string]resource.Secret{}
	written := map[string]time.Time{}
	for _, id := range resource.SecretEnvSecretIDs(merged) {
		sec, err := r.secrets.Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return keep(false, secretEnvCond(resource.ConditionFalse, ReasonSecretNotFound, fmt.Sprintf("Secret %s not found", id))), nil
		}
		if err != nil {
			return secretEnvResult{}, err
		}
		if sec.Status.LastSyncedAt == nil {
			return keep(false, secretEnvCond(resource.ConditionFalse, ReasonSecretHasNoValue, fmt.Sprintf("Secret %s has no value yet", id))), nil
		}
		secs[id], written[id] = sec, *sec.Status.LastSyncedAt
	}
	version := resource.SecretEnvVersion(merged, written)
	// pending is a first deploy: Deploy makes the Environment's place and
	// names the version, and the next pass writes the variables. The
	// workload waits for them; the platform holds nothing yet.
	pending := secretEnvResult{deployVersion: version, held: had, deploy: true,
		cond: secretEnvCond(resource.ConditionUnknown, ReasonSecretEnvPending, pendingMessage)}
	notReady := keep(false, secretEnvCond(resource.ConditionUnknown, ReasonProviderNotReady, providerNotReadyMessage(applicationPlatformCapability)))

	held, err := sp.SecretEnvVersion(ctx, comp, env, d)
	switch {
	case errors.Is(err, ErrProviderNotReady):
		return notReady, nil
	case errors.Is(err, ErrSecretEnvPending):
		return pending, nil
	case errors.Is(err, ErrSecretEnvUnsupported):
		return unsupported(), nil
	case err != nil:
		return secretEnvResult{}, fmt.Errorf("read the secret variables' version of Deployment %s: %w", d.Metadata.ID, err)
	}
	delivered := secretEnvResult{deployVersion: version, held: version, deploy: true,
		cond: secretEnvCond(resource.ConditionTrue, ReasonSecretEnvDelivered, "")}
	if held == version {
		return delivered, nil
	}

	values := map[string]map[string]any{}
	vars := make(map[string]string, len(merged))
	defer func() {
		clear(vars)
		for _, v := range values {
			clear(v)
		}
	}()
	size := 0
	for _, e := range merged {
		v, ok := values[e.Secret]
		if !ok {
			read, err := r.values.ReadValue(ctx, secs[e.Secret])
			if errors.Is(err, ErrProviderNotReady) {
				return keep(false, secretEnvCond(resource.ConditionUnknown, ReasonProviderNotReady, providerNotReadyMessage("secrets"))), nil
			}
			if err != nil {
				// The provider's error isn't put in the message: it's
				// the provider's wording, and a message here must only
				// ever name a Secret and a key.
				return keep(false, secretEnvCond(resource.ConditionUnknown, ReasonSecretReadFailed, fmt.Sprintf("can't read Secret %s's value; retrying", e.Secret))), nil
			}
			v, values[e.Secret] = read, read
		}
		raw, ok := v[e.Key]
		if !ok {
			return keep(false, secretEnvCond(resource.ConditionFalse, ReasonSecretKeyNotFound, fmt.Sprintf("Secret %s has no key %q (for %s)", e.Secret, e.Key, e.Env))), nil
		}
		s, ok := raw.(string)
		if !ok {
			b, err := json.Marshal(raw)
			if err != nil {
				return keep(false, secretEnvCond(resource.ConditionFalse, ReasonSecretKeyNotFound, fmt.Sprintf("Secret %s's key %q can't be made a variable", e.Secret, e.Key))), nil
			}
			s = string(b)
		}
		vars[e.Env] = s
		if size += len(e.Env) + len(s); size > MaxSecretEnvBytes {
			return keep(false, secretEnvCond(resource.ConditionFalse, ReasonSecretEnvTooLarge, fmt.Sprintf("the secret variables add up to more than %d bytes", MaxSecretEnvBytes))), nil
		}
	}
	switch err := sp.WriteSecretEnv(ctx, comp, env, d, vars, version); {
	case err == nil:
		return delivered, nil
	case errors.Is(err, ErrProviderNotReady):
		return notReady, nil
	case errors.Is(err, ErrSecretEnvPending):
		return pending, nil
	case errors.Is(err, ErrSecretEnvUnsupported):
		return unsupported(), nil
	default:
		// The provider promises its errors hold no value
		// (SecretEnvProvider).
		return secretEnvResult{}, fmt.Errorf("write the secret variables of Deployment %s: %w", d.Metadata.ID, err)
	}
}

// secretEnvUnchanged reports whether conditions already hold want (nil:
// no SecretEnvDelivered condition at all). Messages name Serendip's own
// state, so a new one is news.
func secretEnvUnchanged(conditions []resource.Condition, want *resource.Condition) bool {
	existing := findCondition(conditions, conditionSecretEnvDelivered)
	if want == nil {
		return existing == nil
	}
	return existing != nil && existing.Status == want.Status && existing.Reason == want.Reason && existing.Message == want.Message
}

// DependentsLister lists what a Secret's value reaches through secret
// through depends-on edges; the graph implements it.
type DependentsLister interface {
	Dependents(ctx context.Context, ref graph.ResourceRef) ([]graph.ResourceRef, error)
}

// EnqueueSecretUsers enqueues every Deployment whose secret variables use
// secretID's value, directly or through its Component. Errors are
// dropped: resync reaches them anyway.
func EnqueueSecretUsers(ctx context.Context, g DependentsLister, enqueue func(graph.ResourceRef), secretID string) {
	users, err := g.Dependents(ctx, graph.ResourceRef{Kind: resource.KindSecret, ID: secretID})
	if err != nil {
		return
	}
	seen := map[graph.ResourceRef]bool{}
	add := func(ref graph.ResourceRef) {
		if !seen[ref] {
			seen[ref] = true
			enqueue(ref)
		}
	}
	for _, u := range users {
		switch u.Kind {
		case resource.KindDeployment:
			add(u)
		case resource.KindComponent:
			deps, err := g.Dependents(ctx, u)
			if err != nil {
				continue
			}
			for _, d := range deps {
				if d.Kind == resource.KindDeployment {
					add(d)
				}
			}
		}
	}
}
