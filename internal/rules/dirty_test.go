package rules_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/rules"
)

// TestDirtyMarkerMarksAHostWithNoCursorRowYet proves DirtyMarker is the
// exact upsert MarkHostDirty's own generated doc comment describes: it
// creates a cursor row for a host that never had one (insert), and marking
// an already-dirty host again still succeeds and advances dirty_at
// (update), through the one ON CONFLICT (host_id) statement. This is
// v0.3b-3's own caller of MarkHostDirty from outside the ingest/evaluator
// packages (the item-decisions API, internal/api/item_handlers.go), proved
// here the same way TestMarkHostDirtyMakesAHostDue (due_sql_test.go)
// proves the ingest path's own call.
func TestDirtyMarkerMarksAHostWithNoCursorRowYet(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "dirty-marker")
	host := seedHost(t, d, org, "h")
	marker := rules.NewDirtyMarker(d)

	if _, hasRow := cursorRowIfAny(t, d, host); hasRow {
		t.Fatal("a host that never ingested or was marked already has a cursor row")
	}
	if err := marker.MarkHostDirty(context.Background(), org, host); err != nil {
		t.Fatalf("MarkHostDirty: %v", err)
	}
	dirtyAt, _, _ := cursorRow(t, d, host)
	if dirtyAt.IsZero() {
		t.Fatal("dirty_at was not set by the first mark")
	}

	// A second mark on the same host is the ON CONFLICT (host_id) DO UPDATE
	// branch: it must succeed (not violate the primary key) and advance
	// dirty_at, not merely leave the first mark's value standing.
	time.Sleep(10 * time.Millisecond)
	if err := marker.MarkHostDirty(context.Background(), org, host); err != nil {
		t.Fatalf("second MarkHostDirty: %v", err)
	}
	dirtyAt2, _, _ := cursorRow(t, d, host)
	if !dirtyAt2.After(dirtyAt) {
		t.Fatalf("second mark's dirty_at = %v, want it after the first mark's %v", dirtyAt2, dirtyAt)
	}
}

// cursorRowIfAny is cursorRow (due_sql_test.go) without the test-fatal on a
// missing row, for the one assertion here that wants to know "does a
// cursor row exist at all" rather than read one it already expects to be
// there.
func cursorRowIfAny(t *testing.T, d *db.DB, host uuid.UUID) (dirtyAt time.Time, ok bool) {
	t.Helper()
	var evaluatedAt, nextDueAt time.Time
	err := d.Pool().QueryRow(context.Background(),
		`SELECT dirty_at, evaluated_at, next_due_at FROM check_eval_cursor WHERE host_id = $1`, host).
		Scan(&dirtyAt, &evaluatedAt, &nextDueAt)
	return dirtyAt, err == nil
}
