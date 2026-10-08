package hosts_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// Backlog 1.30. Every table built after migration 00014 ties its tenant
// column to its owner with a composite foreign key — workers, jobs,
// worker identities, agent tokens all do. hosts was built before that
// convention existed and kept a plain zone_id reference, so nothing in
// the schema stopped a host in one organization from pointing at another
// organization's zone.
func TestHostCannotReferenceAnotherOrgsZone(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	orgA := seedOrg(t, q, "fk-zone-a")
	orgB := seedOrg(t, q, "fk-zone-b")

	zoneB := uuid.Must(uuid.NewV7())
	if _, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: zoneB, OrgID: orgB, Slug: "b", Name: "B"}); err != nil {
		t.Fatal(err)
	}

	_, err := q.CreateHost(ctx, gen.CreateHostParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgA, ZoneID: zoneB,
		Name: "crossing", Managed: true, Facts: []byte(`{}`),
	})
	if err == nil {
		t.Fatal("a host in org A was placed in org B's zone; the composite foreign key is missing")
	}
}

// The one that matters more. host_tags.org_id is NOT NULL and carries the
// row's tenancy — row-level security on this table filters by it — but it
// referenced nothing at all, so the column that decides who may see a tag
// was free to disagree with the host the tag belongs to. A write that got
// it wrong would have shown the tag to the wrong tenant, and the database
// could not have refused.
func TestHostTagCannotClaimAnotherOrgThanItsHost(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	orgA := seedOrg(t, q, "fk-tag-a")
	orgB := seedOrg(t, q, "fk-tag-b")

	zoneA := uuid.Must(uuid.NewV7())
	if _, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: zoneA, OrgID: orgA, Slug: "a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	host := uuid.Must(uuid.NewV7())
	if _, err := q.CreateHost(ctx, gen.CreateHostParams{
		ID: host, OrgID: orgA, ZoneID: zoneA, Name: "web1", Managed: true, Facts: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}

	// Straight to SQL: the point is what the schema permits, not what the
	// application happens to write today.
	_, err := d.Pool().Exec(ctx,
		`INSERT INTO host_tags (host_id, org_id, key, value, source) VALUES ($1, $2, 'env', 'prod', 'manual')`,
		host, orgB)
	if err == nil {
		t.Fatal("a tag on org A's host claimed org B; row-level security on this table filters by exactly that column")
	}
}
