package jobs

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
)

// The hosts an approval enrols agents on (spec 2026-11-25 B, migration
// 00127) round-trip through the store, the empty list told apart from no
// list: an approved list with no host on it is stored as such.
func TestEnrollHostIDsRoundTripThroughTheStore(t *testing.T) {
	w := newAtomicWorld(t, "jobs-enroll-hosts-store")
	zone := testZone(t, w.d, w.org)
	scoped := db.WithOrgScope(context.Background(), w.org)
	store := NewSQLStore(w.d)
	for name, list := range map[string][]uuid.UUID{
		"no list":    nil,
		"empty list": {},
		"two hosts":  {w.host, uuid.Must(uuid.NewV7())},
	} {
		t.Run(name, func(t *testing.T) {
			j := Job{ID: uuid.Must(uuid.NewV7()), Trigger: TriggerManual, OrgID: w.org, ZoneID: zone, Kind: KindRun,
				HostIDs: []uuid.UUID{w.host}, Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), EnrollHostIDs: list}
			if err := store.CreateJob(scoped, j, nil); err != nil {
				t.Fatal(err)
			}
			got, err := store.GetJob(scoped, w.org, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if (got.EnrollHostIDs == nil) != (list == nil) || !slices.Equal(got.EnrollHostIDs, list) {
				t.Fatalf("EnrollHostIDs = %#v, want %#v", got.EnrollHostIDs, list)
			}
		})
	}
}
