package api

import "github.com/serendip-infra/serendip-infra/core/resource"

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
