package main

import (
	"context"
	"testing"

	"github.com/serendip-infra/serendip-infra/core/reconcile"
	"github.com/serendip-infra/serendip-infra/core/resource"
)

type plainSecrets struct{}

func (plainSecrets) Exists(context.Context, resource.Secret) (bool, error) { return false, nil }
func (plainSecrets) WriteValue(context.Context, resource.Secret, map[string]any) error {
	return nil
}
func (plainSecrets) ReadValue(context.Context, resource.Secret) (map[string]any, error) {
	return nil, nil
}
func (plainSecrets) Delete(context.Context, resource.Secret) error { return nil }

type reportingSecrets struct{ plainSecrets }

func (reportingSecrets) EncryptionAtRest(context.Context) (reconcile.EncryptionReport, error) {
	return reconcile.EncryptionReport{State: reconcile.EncryptionAtRestOn, Reason: "EncryptionConfigured"}, nil
}

// The lazy handle forwards the optional report, and answers unknown for a
// provider that has none (spec 026 T005).
func TestLazySecrets_EncryptionAtRest(t *testing.T) {
	ctx := context.Background()
	var l lazySecrets
	l.set(reportingSecrets{})
	if got, err := l.EncryptionAtRest(ctx); err != nil || got.State != reconcile.EncryptionAtRestOn {
		t.Errorf("with a reporter = %+v, %v; want on", got, err)
	}
	l.set(plainSecrets{})
	if got, err := l.EncryptionAtRest(ctx); err != nil || got != reconcile.EncryptionNotReported() {
		t.Errorf("without a reporter = %+v, %v; want NotReported", got, err)
	}
}
