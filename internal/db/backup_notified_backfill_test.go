package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// TestBackupNotifiedBackfill: a scheduled slot whose attempts were all
// interrupted before 00078 counts as already mailed, so the first tick after
// the upgrade does not mail the whole history. Other rows keep notified_at
// NULL, and a slot interrupted after the upgrade is still reported.
func TestBackupNotifiedBackfill(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, testdb.FreshDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.MigrateTo(ctx, 77); err != nil {
		t.Fatal(err)
	}

	day := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO backup_runs (kind, slot, started_at, finished_at, state) VALUES
		  ('scheduled', $1, $1, $1, 'abandoned'),
		  ('scheduled', $2, $2, $2, 'succeeded'),
		  ('manual', NULL, $2, $2, 'abandoned')`,
		day, day.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}

	if err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	q := gen.New(d.Pool())
	before := pgtype.Timestamptz{Time: day.AddDate(0, 1, 0), Valid: true}
	args := gen.ListInterruptedBackupSlotsParams{Before: before, RestoredReason: "restored"}
	slots, err := q.ListInterruptedBackupSlots(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 0 {
		t.Fatalf("interrupted slots after the upgrade = %v; want none: the old ones count as mailed", slots)
	}

	var notified int
	if err := d.Pool().QueryRow(ctx,
		`SELECT count(*) FROM backup_runs WHERE notified_at IS NOT NULL`).Scan(&notified); err != nil {
		t.Fatal(err)
	}
	if notified != 1 {
		t.Errorf("rows with notified_at set = %d; want 1 (only the abandoned scheduled attempt)", notified)
	}

	later := day.AddDate(0, 0, 2)
	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO backup_runs (kind, slot, started_at, finished_at, state)
		VALUES ('scheduled', $1, $1, $1, 'abandoned')`, later); err != nil {
		t.Fatal(err)
	}
	slots, err = q.ListInterruptedBackupSlots(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 1 || !slots[0].Slot.Time.Equal(later) {
		t.Fatalf("interrupted slots = %v; want only the one interrupted after the upgrade (%s)", slots, later)
	}
}
