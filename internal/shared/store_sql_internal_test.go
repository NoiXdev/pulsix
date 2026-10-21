package shared

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

var internalCtx = context.Background()

// TestCollisionFromCheckDefinitionRaceRebuildsTheRichError drives
// collisionFromCheckDefinitionRace directly — the unexported backstop
// CreateCheckDefinitionWithProjections' own InsertProjectedCheckDefinitions
// call reaches only on a genuine unique-violation race (an organization
// created between Service.CreateCheckDefinition's pre-flight
// ExistingCheckDefinitionNames read and this call's own transaction).
// Driving it directly rather than reproducing the literal race is the same
// honest substitute internal/checks/store_sql_internal_test.go's own doc
// comment explains: there is no seam to pause CreateWithProjections at the
// exact microsecond a real race would need, and this is the closest thing
// to it — the exact private function the race calls, through both of its
// branches.
func TestCollisionFromCheckDefinitionRaceRebuildsTheRichError(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	orgA := seedOrg(t, q, "shared-check-race-internal")

	// org A's own, unrelated check at the default ('org', nil, nil) scope —
	// the row a race would have let slip past the pre-flight check.
	seedOtherCheckDefinition(t, d, orgA, "taken-by-race")

	def, err := NewSQLStore(d).CreateCheckDefinitionWithProjections(internalCtx, CheckDefinition{
		ID: uuid.Must(uuid.NewV7()), Key: "disk-data", Plugin: "disk", Interval: 300 * time.Second,
	})
	if err != nil {
		t.Fatalf("seed CreateCheckDefinitionWithProjections: %v", err)
	}

	err = collisionFromCheckDefinitionRace(internalCtx, q, def.ID, "taken-by-race")
	var collision *NameCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("collisionFromCheckDefinitionRace() error = %v, want *NameCollisionError", err)
	}
	if collision.Name != "taken-by-race" || len(collision.Orgs) != 1 || collision.Orgs[0] != orgA {
		t.Fatalf("collisionFromCheckDefinitionRace() = %+v, want Name=taken-by-race, Orgs=[%v]", collision, orgA)
	}

	// A key that collides with nothing at all: the fallback bare
	// ErrNameTaken — the unique violation was real (why this function was
	// called in the first place) but a fresh read can no longer reproduce
	// which row it was, so there is nothing left to name.
	err = collisionFromCheckDefinitionRace(internalCtx, q, def.ID, "collides-with-nothing")
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("collisionFromCheckDefinitionRace() with no reproducible collision error = %v, want ErrNameTaken", err)
	}
	var noCollision *NameCollisionError
	if errors.As(err, &noCollision) {
		t.Fatalf("collisionFromCheckDefinitionRace() with no reproducible collision = %+v, want the BARE sentinel, not the richer form", noCollision)
	}
}

// seedOrg and seedOtherCheckDefinition are store_sql_test.go's own helpers
// (package shared_test) — duplicated here in their minimal form rather than
// exported across the internal/external test-package boundary, the same
// split internal/checks/store_sql_internal_test.go draws from its own
// store_sql_test.go.
func seedOrg(t *testing.T, q *gen.Queries, slug string) uuid.UUID {
	t.Helper()
	org, err := q.CreateOrganization(internalCtx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

func seedOtherCheckDefinition(t *testing.T, d *db.DB, orgID uuid.UUID, key string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(db.WithOrgScope(internalCtx, orgID),
		`INSERT INTO check_definitions (id, org_id, key, plugin, interval_seconds, binding_kind, origin)
		 VALUES ($1, $2, $3, 'cpu', 60, 'org', 'custom')`,
		id, orgID, key); err != nil {
		t.Fatal(err)
	}
	return id
}
