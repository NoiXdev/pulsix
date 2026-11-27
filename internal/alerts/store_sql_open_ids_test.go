package alerts_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// TestOpenAlertIDs (backlog 4.8 review I1): of the ids asked, only the
// open alerts of the organization asked for come back — not a closed
// one, not another organization's, not an id naming nothing.
func TestOpenAlertIDs(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	other, otherHost := seedHost(t, d)
	at := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	insertAlertAt(t, d, org, host, "cpu", "", "used", at)
	insertAlertAt(t, d, org, host, "disk", "/", "used", at)
	insertAlertAt(t, d, other, otherHost, "cpu", "", "used", at)
	ids := func(orgID uuid.UUID) []uuid.UUID {
		var out []uuid.UUID
		rows, err := d.Pool().Query(context.Background(), `SELECT id FROM alerts WHERE org_id = $1 ORDER BY check_key`, orgID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		return out
	}
	mine, theirs := ids(org), ids(other)
	cpu, disk := mine[0], mine[1]
	if _, err := d.Pool().Exec(context.Background(), `UPDATE alerts SET closed_at = $2, closed_reason = 'resolved' WHERE id = $1`, disk, at); err != nil {
		t.Fatal(err)
	}
	asked := []uuid.UUID{cpu, disk, theirs[0], uuid.Must(uuid.NewV7())}
	got, err := alerts.NewSQLStore(d).OpenAlertIDs(db.WithOrgScope(context.Background(), org), org, asked)
	if err != nil || !slices.Equal(got, []uuid.UUID{cpu}) {
		t.Fatalf("open = %v, %v", got, err)
	}
}
