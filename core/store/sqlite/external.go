package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/serendip-infra/serendip-infra/core/audit"
	"github.com/serendip-infra/serendip-infra/core/id"
)

// RecordExternalChange writes one audit row for a change Serendip made
// outside the store: objects a capability keeps in the platform for
// itself, derived from resources, such as the networking capability's
// request-visibility policies (specs/025-networking T008). There's no
// store mutation to share a transaction with, so the row is its own.
// kind and resourceID name what was changed, in Serendip's words; after
// says what the change was. Who, through what interface and under what
// request come from ctx, as for every other row (constitution 8).
func RecordExternalChange(ctx context.Context, db *sql.DB, kind, resourceID string, after json.RawMessage) error {
	if kind == "" || resourceID == "" {
		return errors.New("sqlite: an external change's audit row needs a kind and a resource ID")
	}
	if after != nil && !json.Valid(after) {
		return errors.New("sqlite: an external change's audit row needs valid JSON")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin: %w", err)
	}
	defer tx.Rollback()
	if err := writeAudit(ctx, tx, audit.Record{
		ID: id.New(), Actor: audit.ActorFrom(ctx), Interface: audit.InterfaceFrom(ctx),
		Kind: kind, ResourceID: resourceID, After: after, At: time.Now().UTC(),
		PolicyDecision: audit.PolicyDecisionFrom(ctx), RequestID: audit.RequestIDFrom(ctx),
		ActorSubject: audit.SubjectFrom(ctx), ActorSponsor: audit.SponsorFrom(ctx),
	}); err != nil {
		return err
	}
	return tx.Commit()
}
