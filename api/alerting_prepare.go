package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store"
)

// The generic routes' checks for the alerting kinds (spec 024 T002): each
// validates its owner and spec, and derives its depends-on edges from the
// spec, so the store refuses deleting what's still in use. A client's own
// relationships are dropped, and status is the server's on create. T007
// replaces these routes' handlers with the alerting handlers.

func prepareAlertRule(r *resource.AlertRule) error {
	if err := resource.ValidateAlertRule(r.Metadata, r.Spec); err != nil {
		return err
	}
	r.Metadata.Relationships = resource.AlertRuleRelationships(r.Spec)
	return nil
}

// builtInKept refuses a person setting builtIn on a rule, or clearing it
// (plan decision 4): only serendipd creates built-in rules, and a cleared
// flag would let one be moved to a Component and deleted alone.
func builtInKept(rules store.Store[resource.AlertRuleSpec, resource.AlertRuleStatus]) func(ctx context.Context, id string, spec resource.AlertRuleSpec) error {
	return func(ctx context.Context, id string, spec resource.AlertRuleSpec) error {
		if id == "" {
			if spec.BuiltIn {
				return fmt.Errorf("%w: builtIn: only serendipd creates built-in rules", resource.ErrValidation)
			}
			return nil
		}
		stored, err := rules.Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return nil // the update itself answers 404
		}
		if err != nil {
			return err
		}
		if stored.Spec.BuiltIn != spec.BuiltIn {
			return fmt.Errorf("%w: builtIn can't be changed", resource.ErrValidation)
		}
		return nil
	}
}

func prepareNotificationChannel(r *resource.NotificationChannel) error {
	if err := resource.ValidateNotificationChannel(r.Metadata, r.Spec); err != nil {
		return err
	}
	r.Metadata.Relationships = resource.NotificationChannelRelationships(r.Spec)
	return nil
}

func prepareSMTPServer(r *resource.SMTPServer) error {
	if err := resource.ValidateSMTPServer(r.Metadata, r.Spec); err != nil {
		return err
	}
	r.Metadata.Relationships = resource.SMTPServerRelationships(r.Spec)
	return nil
}
