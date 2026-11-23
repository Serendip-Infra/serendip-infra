package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/serendip-infra/serendip-infra/core/audit"
	"github.com/serendip-infra/serendip-infra/core/store/sqlite"
)

func TestRecordExternalChange(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "external.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, kind, id string
		after          json.RawMessage
	}{
		{"no kind", "", "x", nil},
		{"no id", "K", "", nil},
		{"bad json", "K", "x", json.RawMessage(`{`)},
	} {
		if err := sqlite.RecordExternalChange(ctx, db, tc.kind, tc.id, tc.after); err == nil {
			t.Errorf("%s: want an error", tc.name)
		}
	}

	actx := audit.WithInterface(audit.WithActor(ctx, "system:reconciler"), "reconciler")
	after := json.RawMessage(`{"applied":["ns/a"]}`)
	if err := sqlite.RecordExternalChange(actx, db, "NetworkVisibility", "cluster", after); err != nil {
		t.Fatal(err)
	}
	var actor, iface, kind, rid, got string
	var before sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT actor, interface, kind, resource_id, before, after FROM audit_log`).
		Scan(&actor, &iface, &kind, &rid, &before, &got); err != nil {
		t.Fatal(err)
	}
	if actor != "system:reconciler" || iface != "reconciler" || kind != "NetworkVisibility" || rid != "cluster" || before.Valid || got != string(after) {
		t.Errorf("row = %q %q %q %q %v %q", actor, iface, kind, rid, before, got)
	}
}
