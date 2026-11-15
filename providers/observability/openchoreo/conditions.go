package openchoreo

import (
	"context"
	"errors"
	"time"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
)

// errConditionsPending: the condition queries arrive in spec 024 T006.
// Until then every rule reads Evaluating=False, QueryFailed.
var errConditionsPending = errors.New("openchoreo observability: alert conditions aren't evaluated yet (spec 024 T006)")

// EvaluateCondition reads one alert condition (spec 024 plan decision 3).
func (p *Provider) EvaluateCondition(ctx context.Context, t reconcile.LogTarget, c resource.AlertCondition, window time.Duration) (reconcile.ConditionValue, error) {
	return reconcile.ConditionValue{}, errConditionsPending
}
