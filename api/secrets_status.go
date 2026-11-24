package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/serendip-infra/serendip-infra/core/audit"
	"github.com/serendip-infra/serendip-infra/core/reconcile"
)

// secretsStatusTimeout bounds asking the cluster about encryption at rest.
const secretsStatusTimeout = 10 * time.Second

// secretsStatus is GET /v1/secrets/status's answer (spec 026 plan
// decision 9): whether the Secrets capability is ready, and whether the
// cluster encrypts the copies of values delivered into it at rest. It
// never names a value.
type secretsStatus struct {
	Ready bool `json:"ready"`
	reconcile.EncryptionReport
}

// secretsStatusHandler serves GET /v1/secrets/status. reporter is nil
// without a Secrets capability.
type secretsStatusHandler struct {
	reporter reconcile.EncryptionReporter
}

func (h secretsStatusHandler) Register(mux Mux) {
	mux.HandleFunc("GET /v1/secrets/status", h.get)
}

func (h secretsStatusHandler) get(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.status(r.Context()))
}

func (h secretsStatusHandler) status(ctx context.Context) secretsStatus {
	if h.reporter == nil {
		return secretsStatus{EncryptionReport: reconcile.EncryptionReport{
			State: reconcile.EncryptionAtRestUnknown, Reason: "NotConfigured",
			Message: "no Secrets capability is configured",
		}}
	}
	ctx, cancel := context.WithTimeout(ctx, secretsStatusTimeout)
	defer cancel()
	rep, err := h.reporter.EncryptionAtRest(ctx)
	switch {
	case errors.Is(err, reconcile.ErrProviderNotReady):
		return secretsStatus{EncryptionReport: reconcile.EncryptionReport{
			State: reconcile.EncryptionAtRestUnknown, Reason: "NotReady",
			Message: "the Secrets capability isn't ready yet",
		}}
	case err != nil:
		// The capability is up and only the cluster question failed, so
		// ready stays true and the state is unknown. The cause can name
		// cluster internals: it's logged, not answered.
		log.Printf("api: read secrets encryption at rest (request %s): %v", audit.RequestIDFrom(ctx), err)
		return secretsStatus{Ready: true, EncryptionReport: reconcile.EncryptionReport{
			State: reconcile.EncryptionAtRestUnknown, Reason: "CheckFailed",
			Message: "the cluster couldn't be asked whether it encrypts Secrets at rest; retry later",
		}}
	}
	return secretsStatus{Ready: true, EncryptionReport: rep}
}
