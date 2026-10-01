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

// TestDirtyMarkerMarksHostsDirtyInOneBatch proves MarkHostsDirty is a real
// multi-row upsert, not a disguised loop: several hosts (one with no prior
// cursor row at all) are all marked by one call, and a second batched call
// advances dirty_at for every one of them. It also pins the documented
// no-op shape of an empty id slice — no error, nothing written — since
// markDirtyForDecision (internal/api/item_handlers.go) relies on that to
// make "no monitored hosts in this organization" a safe, ordinary case
// rather than one it has to special-case itself.
func TestDirtyMarkerMarksHostsDirtyInOneBatch(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "dirty-marker-batch")
	a := seedHost(t, d, org, "a")
	b := seedHost(t, d, org, "b")
	marker := rules.NewDirtyMarker(d)

	if err := marker.MarkHostsDirty(context.Background(), org, nil); err != nil {
		t.Fatalf("MarkHostsDirty(nil): %v", err)
	}
	if _, hasRow := cursorRowIfAny(t, d, a); hasRow {
		t.Fatal("an empty/nil id slice wrote a cursor row")
	}

	if err := marker.MarkHostsDirty(context.Background(), org, []uuid.UUID{a, b}); err != nil {
		t.Fatalf("MarkHostsDirty: %v", err)
	}
	aDirty, _, _ := cursorRow(t, d, a)
	bDirty, _, _ := cursorRow(t, d, b)
	if aDirty.IsZero() || bDirty.IsZero() {
		t.Fatalf("one batched call did not mark both hosts: a=%v b=%v", aDirty, bDirty)
	}

	// The ON CONFLICT (host_id) DO UPDATE branch, for a whole batch at
	// once: a second call on the same two hosts must advance both, not
	// error on the one that now already has a row.
	time.Sleep(10 * time.Millisecond)
	if err := marker.MarkHostsDirty(context.Background(), org, []uuid.UUID{a, b}); err != nil {
		t.Fatalf("second MarkHostsDirty: %v", err)
	}
	aDirty2, _, _ := cursorRow(t, d, a)
	bDirty2, _, _ := cursorRow(t, d, b)
	if !aDirty2.After(aDirty) || !bDirty2.After(bDirty) {
		t.Fatalf("second batched mark did not advance dirty_at for both: a %v->%v, b %v->%v", aDirty, aDirty2, bDirty, bDirty2)
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
