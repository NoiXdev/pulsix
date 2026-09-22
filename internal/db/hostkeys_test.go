package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// TestHostConnectionTestsAreIsolatedPerOrg proves the host_connection_tests
// table's row-level security pair (migration 00019): every org-owned table
// shares it, and a test-result history leaking across tenants would leak
// which hosts are reachable and what their keys look like.
func TestHostConnectionTestsAreIsolatedPerOrg(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	orgA, orgB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hostA, hostB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, o := range []struct {
		id   uuid.UUID
		slug string
	}{{orgA, "hk-a"}, {orgB, "hk-b"}} {
		if _, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: o.id, Slug: o.slug, Name: o.slug}); err != nil {
			t.Fatal(err)
		}
	}
	zoneA, zoneB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: zoneA, OrgID: orgA, Slug: "default", Name: "Default"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: zoneB, OrgID: orgB, Slug: "default", Name: "Default"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateHost(ctx, gen.CreateHostParams{ID: hostA, OrgID: orgA, ZoneID: zoneA, Name: "host-a", Managed: true, Facts: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateHost(ctx, gen.CreateHostParams{ID: hostB, OrgID: orgB, ZoneID: zoneB, Name: "host-b", Managed: true, Facts: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		org, host uuid.UUID
		key       string
	}{
		{orgA, hostA, "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{orgB, hostB, "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"},
	} {
		if _, err := q.CreateHostConnectionTest(ctx, gen.CreateHostConnectionTestParams{
			ID: uuid.Must(uuid.NewV7()), OrgID: r.org, HostID: r.host,
			Ok: true, HostKey: r.key, HostKeyState: "unpinned", Checks: []byte(`[]`), HopKeys: []byte(`[]`),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// A scoped read sees exactly its own organization's row.
	var seen []uuid.UUID
	if err := d.WithOrg(ctx, orgA, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT org_id FROM host_connection_tests`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			seen = append(seen, id)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != orgA {
		t.Fatalf("RLS leak: saw %v, want only %s", seen, orgA)
	}
}
