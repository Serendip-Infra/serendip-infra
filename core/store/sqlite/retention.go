package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/serendip-infra/serendip-infra/core/audit"
	"github.com/serendip-infra/serendip-infra/core/event"
	"github.com/serendip-infra/serendip-infra/core/graph"
	"github.com/serendip-infra/serendip-infra/core/id"
	"github.com/serendip-infra/serendip-infra/core/resource"
	"github.com/serendip-infra/serendip-infra/core/store"
)

// PurgeResolvedAlerts removes up to limit resolved Alerts that resolved
// before cutoff, oldest first (specs/024-alerts plan decision 11, RP-071).
//
// It's system retention, and still a delete, so it records an Approval
// like every delete does (constitution 5, ADR 0016): one per rule per
// pass, decided by policy and Executed in the same transaction, naming
// the removed Alerts in its blast radius. Grouping by rule keeps each
// Approval readable by exactly those who can read that rule. Each removed
// Alert gets its audit row, naming the Approval. It removes nothing but
// resolved Alerts, which nothing depends on and no route deletes. bus may
// be nil; it's told of each Approval and removed Alert.
func PurgeResolvedAlerts(ctx context.Context, db *sql.DB, bus event.Bus, cutoff time.Time, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("sqlite: purge resolved alerts: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	rows, err := tx.QueryContext(ctx, `
		SELECT id, name, resource_version, json_extract(spec, '$.ruleId') FROM resources
		WHERE kind = ?
		  AND json_extract(status, '$.state') = ?
		  AND julianday(json_extract(status, '$.resolvedAt')) < julianday(?)
		ORDER BY julianday(json_extract(status, '$.resolvedAt')), id
		LIMIT ?`,
		string(resource.KindAlert), resource.AlertResolved, cutoff.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return 0, fmt.Errorf("sqlite: purge resolved alerts: select: %w", err)
	}
	type old struct {
		meta resource.Metadata
		rule string
	}
	byRule := map[string][]old{}
	var order []string
	for rows.Next() {
		var o old
		var rule sql.NullString
		if err := rows.Scan(&o.meta.ID, &o.meta.Name, &o.meta.ResourceVersion, &rule); err != nil {
			rows.Close()
			return 0, err
		}
		o.meta.Kind, o.rule = resource.KindAlert, rule.String
		if _, ok := byRule[o.rule]; !ok {
			order = append(order, o.rule)
		}
		byRule[o.rule] = append(byRule[o.rule], o)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	now := time.Now().UTC()
	actor := audit.ActorFrom(ctx)
	evidence := fmt.Sprintf("retention: resolved before %s", cutoff.UTC().Format(time.RFC3339))
	var events []event.Event
	n := 0
	for _, rule := range order {
		group := byRule[rule]
		target := group[0].meta
		ref := graph.ResourceRef{Kind: resource.KindAlert, ID: target.ID}
		// Read before the edges go: the Approval is read at the nearest
		// owner that still exists (the rule).
		owners, err := graph.Ancestors(ctx, tx, ref)
		if err != nil {
			return 0, err
		}
		approvalID := id.New()
		decision := fmt.Sprintf("approved: %s by policy (%s)", approvalID, evidence)
		removed := make([]removedRow, 0, len(group))
		for _, o := range group {
			r := graph.ResourceRef{Kind: resource.KindAlert, ID: o.meta.ID}
			if _, err := tx.ExecContext(ctx, `DELETE FROM resources WHERE id = ? AND kind = ?`, o.meta.ID, string(resource.KindAlert)); err != nil {
				return 0, fmt.Errorf("sqlite: purge alert %s: %w", o.meta.ID, err)
			}
			if err := graph.DeleteEdgesFor(ctx, tx, r); err != nil {
				return 0, err
			}
			if err := writeAudit(ctx, tx, audit.Record{
				ID: id.New(), Actor: actor, Interface: audit.InterfaceFrom(ctx), Kind: string(resource.KindAlert),
				ResourceID: o.meta.ID, At: now, PolicyDecision: &decision, RequestID: audit.RequestIDFrom(ctx),
			}); err != nil {
				return 0, err
			}
			// No LastSpec: an Alert has no clean-up to replay.
			removed = append(removed, removedRow{ref: r, name: o.meta.Name})
			events = append(events, event.Event{Type: event.EventDeleted, Kind: resource.KindAlert, ID: o.meta.ID, Actor: actor, At: now})
		}
		intent := store.ApprovalIntent{Approval: resource.Approval{
			Metadata: resource.Metadata{ID: approvalID, Name: fmt.Sprintf("retention of %d resolved Alert(s)", len(group))},
			Spec: resource.ApprovalSpec{
				Action: resource.ApprovalAction{Verb: resource.ApprovalVerbDelete, Target: resource.Scope{Kind: resource.KindAlert, ID: target.ID}},
				Reason: "Alert retention: resolved Alerts are kept 30 days",
			},
			Status: resource.ApprovalStatus{
				Phase:           resource.ApprovalApproved,
				RequestedBy:     resource.ApprovalActor{Actor: actor},
				RequestDecision: evidence,
				Decision:        &resource.ApprovalDecision{ByPolicy: true, Evidence: evidence},
			},
		}}
		ev, err := recordApproval(ctx, tx, intent, target, owners, removed, now)
		if err != nil {
			return 0, err
		}
		events = append(events, ev)
		n += len(group)
	}
	if n == 0 {
		return 0, nil
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("sqlite: purge resolved alerts: commit: %w", err)
	}
	if bus != nil {
		for _, ev := range events {
			bus.Publish(ev)
		}
	}
	return n, nil
}
