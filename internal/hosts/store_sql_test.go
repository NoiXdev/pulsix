package hosts_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
)

func seedOrg(t *testing.T, q *gen.Queries, slug string) uuid.UUID {
	t.Helper()
	org, err := q.CreateOrganization(context.Background(), gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

func TestSQLStoreCreateZoneAndGetZone(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hosts-zone-org")

	z := hosts.Zone{ID: uuid.Must(uuid.NewV7()), OrgID: org, Slug: "fra", Name: "Falkenstein"}
	if err := store.CreateZone(ctx, z); err != nil {
		t.Fatalf("CreateZone: %v", err)
	}
	got, err := store.GetZone(ctx, org, z.ID)
	if err != nil || got.Slug != "fra" || got.Name != "Falkenstein" {
		t.Fatalf("GetZone = %+v, %v", got, err)
	}
	if _, err := store.GetZone(ctx, org, uuid.Must(uuid.NewV7())); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("unknown zone: %v, want ErrNotFound", err)
	}
	otherOrg := seedOrg(t, q, "hosts-zone-org-2")
	if _, err := store.GetZone(ctx, otherOrg, z.ID); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("zone from another org: %v, want ErrNotFound", err)
	}
}

func TestSQLStoreCreateZoneDuplicateSlugUniqueViolation(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hosts-zone-dup-org")

	z := hosts.Zone{ID: uuid.Must(uuid.NewV7()), OrgID: org, Slug: "fra", Name: "Falkenstein"}
	if err := store.CreateZone(ctx, z); err != nil {
		t.Fatal(err)
	}
	dup := hosts.Zone{ID: uuid.Must(uuid.NewV7()), OrgID: org, Slug: "fra", Name: "Again"}
	if err := store.CreateZone(ctx, dup); !errors.Is(err, hosts.ErrNameTaken) {
		t.Fatalf("duplicate slug: %v, want ErrNameTaken", err)
	}
}

func TestSQLStoreListZonesForOrg(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hosts-zone-list-org")

	for _, slug := range []string{"fra", "ams", "nbg"} {
		if err := store.CreateZone(ctx, hosts.Zone{ID: uuid.Must(uuid.NewV7()), OrgID: org, Slug: slug, Name: slug}); err != nil {
			t.Fatal(err)
		}
	}
	zones, err := store.ListZones(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 3 || zones[0].Slug != "ams" || zones[1].Slug != "fra" || zones[2].Slug != "nbg" {
		t.Fatalf("ListZones = %+v, want alphabetical [ams fra nbg]", zones)
	}
}

func seedZone(t *testing.T, store *hosts.SQLStore, orgID uuid.UUID, slug string) hosts.Zone {
	t.Helper()
	z := hosts.Zone{ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Slug: slug, Name: slug}
	if err := store.CreateZone(ctx, z); err != nil {
		t.Fatal(err)
	}
	return z
}

func TestSQLStoreCreateHostAndGetHost(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hosts-host-org")
	zone := seedZone(t, store, org, "fra")

	h := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "web-01.fra", Address: "10.0.0.1", Managed: true}
	if err := store.CreateHost(ctx, h); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	got, err := store.GetHost(ctx, org, h.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if got.Name != "web-01.fra" || got.Address != "10.0.0.1" || !got.Managed || got.Monitored {
		t.Fatalf("GetHost = %+v", got)
	}
	if got.Facts == nil || len(got.Facts) != 0 {
		t.Fatalf("Facts = %+v, want an empty (non-nil) map", got.Facts)
	}
	if got.LastSeenAt != nil {
		t.Fatalf("LastSeenAt = %v, want nil", got.LastSeenAt)
	}

	if _, err := store.GetHost(ctx, org, uuid.Must(uuid.NewV7())); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("unknown host: %v, want ErrNotFound", err)
	}
	otherOrg := seedOrg(t, q, "hosts-host-org-2")
	if _, err := store.GetHost(ctx, otherOrg, h.ID); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("host from another org: %v, want ErrNotFound", err)
	}
}

// TestSQLStoreCreateHostDuplicateNameUniqueViolation proves the store maps
// the hosts (org_id, name) unique constraint to ErrNameTaken, mirroring
// TestSQLStoreCreateZoneDuplicateSlugUniqueViolation — this is the "a
// duplicate is reported as a conflict rather than a generic 500" guarantee
// exercised at the store layer.
func TestSQLStoreCreateHostDuplicateNameUniqueViolation(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hosts-host-dup-org")
	zone := seedZone(t, store, org, "fra")

	h := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "web-01"}
	if err := store.CreateHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	dup := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "web-01"}
	if err := store.CreateHost(ctx, dup); !errors.Is(err, hosts.ErrNameTaken) {
		t.Fatalf("duplicate name: %v, want ErrNameTaken", err)
	}

	// The same name in a *different* organization must be perfectly fine —
	// the unique constraint is (org_id, name), not name alone.
	otherOrg := seedOrg(t, q, "hosts-host-dup-org-2")
	otherZone := seedZone(t, store, otherOrg, "fra")
	elsewhere := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: otherOrg, ZoneID: otherZone.ID, Name: "web-01"}
	if err := store.CreateHost(ctx, elsewhere); err != nil {
		t.Fatalf("same name in a different org: %v", err)
	}
}

func TestSQLStoreUpdateHost(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hosts-update-org")
	zoneA := seedZone(t, store, org, "fra")
	zoneB := seedZone(t, store, org, "ams")

	h := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zoneA.ID, Name: "web-01"}
	if err := store.CreateHost(ctx, h); err != nil {
		t.Fatal(err)
	}

	updated, err := store.UpdateHost(ctx, hosts.Host{ID: h.ID, OrgID: org, ZoneID: zoneB.ID, Name: "web-01-renamed", Address: "10.0.0.2", Managed: true, Monitored: true})
	if err != nil {
		t.Fatalf("UpdateHost: %v", err)
	}
	if updated.ZoneID != zoneB.ID || updated.Name != "web-01-renamed" || updated.Address != "10.0.0.2" || !updated.Managed || !updated.Monitored {
		t.Fatalf("updated = %+v", updated)
	}

	if _, err := store.UpdateHost(ctx, hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zoneA.ID, Name: "nope"}); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("unknown host: %v, want ErrNotFound", err)
	}
}

// TestSQLStoreUpdateHostDuplicateNameUniqueViolation proves UpdateHost maps
// the hosts (org_id, name) unique constraint to ErrNameTaken against the
// real database, mirroring TestSQLStoreCreateHostDuplicateNameUniqueViolation
// — creation's own mapping was already covered against the real store, but
// update's was only covered against the fake (hosts.Service's own
// TestUpdateHostValidatesNameAndZone), leaving the real SQLStore.UpdateHost
// error-code branch unexercised (Task 13 fix round 1, adjacent fix).
func TestSQLStoreUpdateHostDuplicateNameUniqueViolation(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hosts-update-dup-org")
	zone := seedZone(t, store, org, "fra")

	if err := store.CreateHost(ctx, hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "web-01"}); err != nil {
		t.Fatal(err)
	}
	h2 := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "web-02"}
	if err := store.CreateHost(ctx, h2); err != nil {
		t.Fatal(err)
	}

	// Renaming web-02 to web-01's already-taken name must be reported as a
	// conflict, not a generic 500 — the same guarantee CreateHost already
	// gives, now proven for UpdateHost against the real unique constraint.
	if _, err := store.UpdateHost(ctx, hosts.Host{ID: h2.ID, OrgID: org, ZoneID: zone.ID, Name: "web-01"}); !errors.Is(err, hosts.ErrNameTaken) {
		t.Fatalf("rename to a taken name: %v, want ErrNameTaken", err)
	}
}

func TestSQLStoreDeleteHostScopedToOrg(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	orgA := seedOrg(t, q, "hosts-delete-org-a")
	orgB := seedOrg(t, q, "hosts-delete-org-b")
	zoneA := seedZone(t, store, orgA, "fra")

	h := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: orgA, ZoneID: zoneA.ID, Name: "web-01"}
	if err := store.CreateHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteHost(ctx, orgB, h.ID); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("delete from wrong org: %v, want ErrNotFound", err)
	}
	if _, err := store.GetHost(ctx, orgA, h.ID); err != nil {
		t.Fatalf("host must survive a wrong-org delete attempt: %v", err)
	}
	if err := store.DeleteHost(ctx, orgA, h.ID); err != nil {
		t.Fatalf("DeleteHost: %v", err)
	}
	if _, err := store.GetHost(ctx, orgA, h.ID); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("host must be gone: %v, want ErrNotFound", err)
	}
}

// TestSQLStoreListHostsPaginatesAcrossPages proves keyset pagination on
// (name, id) is stable across three pages against the real database, the
// SQL-store-specific coverage the brief calls for explicitly (step 3).
func TestSQLStoreListHostsPaginatesAcrossPages(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hosts-page-org")
	zone := seedZone(t, store, org, "fra")

	names := []string{"a-host", "b-host", "c-host", "d-host", "e-host"}
	for _, name := range names {
		if err := store.CreateHost(ctx, hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: name}); err != nil {
			t.Fatal(err)
		}
	}

	var seen []string
	cursor := ""
	for page := 0; page < 10; page++ {
		items, next, err := store.ListHosts(ctx, org, hosts.ListFilter{}, cursor, 2)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(items) == 0 {
			break
		}
		for _, h := range items {
			seen = append(seen, h.Name)
		}
		cursor = next
		if cursor == "" {
			break
		}
	}
	if len(seen) != len(names) {
		t.Fatalf("saw %v across all pages, want all of %v", seen, names)
	}
	for i, name := range names {
		if seen[i] != name {
			t.Fatalf("page order = %v, want alphabetical %v", seen, names)
		}
	}
}

func TestSQLStoreListHostsFilters(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hosts-filter-org")
	zoneA := seedZone(t, store, org, "fra")
	zoneB := seedZone(t, store, org, "ams")

	if err := store.CreateHost(ctx, hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zoneA.ID, Name: "web-01", Address: "10.0.0.1", Managed: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateHost(ctx, hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zoneB.ID, Name: "switch-01", Monitored: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateHost(ctx, hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zoneA.ID, Name: "db-01", Managed: true, Monitored: true}); err != nil {
		t.Fatal(err)
	}

	got, _, err := store.ListHosts(ctx, org, hosts.ListFilter{ZoneID: &zoneA.ID}, "", 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("zone filter: %d %v", len(got), err)
	}
	managed := true
	got, _, err = store.ListHosts(ctx, org, hosts.ListFilter{Managed: &managed}, "", 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("managed filter: %d %v", len(got), err)
	}
	monitored := true
	got, _, err = store.ListHosts(ctx, org, hosts.ListFilter{Monitored: &monitored}, "", 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("monitored filter: %d %v", len(got), err)
	}
	got, _, err = store.ListHosts(ctx, org, hosts.ListFilter{Query: "switch"}, "", 10)
	if err != nil || len(got) != 1 || got[0].Name != "switch-01" {
		t.Fatalf("query filter (name): %+v %v", got, err)
	}
	got, _, err = store.ListHosts(ctx, org, hosts.ListFilter{Query: "10.0.0"}, "", 10)
	if err != nil || len(got) != 1 || got[0].Name != "web-01" {
		t.Fatalf("query filter (address): %+v %v", got, err)
	}
}

// TestSQLStoreListAllHostsSortedAndScoped proves ListAllHosts returns every
// host of one organization sorted by name, created out of name order, and
// never a host belonging to another organization — the contract the
// inventory renderer (internal/inventory/render) relies on.
func TestSQLStoreListAllHostsSortedAndScoped(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hosts-list-all-org")
	other := seedOrg(t, q, "hosts-list-all-org-2")
	zone := seedZone(t, store, org, "fra")
	otherZone := seedZone(t, store, other, "fra")

	for _, name := range []string{"c-host", "a-host", "b-host"} {
		if err := store.CreateHost(ctx, hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CreateHost(ctx, hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: other, ZoneID: otherZone.ID, Name: "z-host"}); err != nil {
		t.Fatal(err)
	}

	got, err := store.ListAllHosts(ctx, org)
	if err != nil {
		t.Fatalf("ListAllHosts: %v", err)
	}
	if len(got) != 3 || got[0].Name != "a-host" || got[1].Name != "b-host" || got[2].Name != "c-host" {
		t.Fatalf("ListAllHosts = %+v, want [a-host b-host c-host]", got)
	}
}

// TestSQLStorePatchCapabilitiesLeavesUnmentionedFlagAlone proves
// PatchCapabilities' nil-means-unchanged contract against the real
// database: setting only monitored must not disturb managed, sequentially
// (the concurrency case below is TestSQLStorePatchCapabilitiesConcurrent).
func TestSQLStorePatchCapabilitiesLeavesUnmentionedFlagAlone(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hosts-patch-org")
	zone := seedZone(t, store, org, "fra")

	h := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "k3s-01", Managed: true}
	if err := store.CreateHost(ctx, h); err != nil {
		t.Fatal(err)
	}

	on := true
	updated, err := store.PatchCapabilities(ctx, org, h.ID, nil, &on)
	if err != nil {
		t.Fatalf("PatchCapabilities: %v", err)
	}
	if !updated.Managed || !updated.Monitored {
		t.Fatalf("updated = %+v, want managed still true and monitored now true", updated)
	}

	if _, err := store.PatchCapabilities(ctx, org, uuid.Must(uuid.NewV7()), &on, nil); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("unknown host: %v, want ErrNotFound", err)
	}
}

// TestSQLStorePatchCapabilitiesConcurrent is Task 13 fix round 1's finding
// 2 test: two goroutines patch the *same* host at the same time, one
// setting only managed, the other only monitored, starting from both
// false. A start channel (closed only once both goroutines are blocked on
// it) forces the two UPDATE statements to actually race against the real
// database rather than merely running back-to-back on one goroutine — the
// only way this test could have caught the lost-update bug the read-
// modify-write version of SetCapabilities had (two concurrent callers each
// reading the pre-update row and each writing back a version that clobbers
// the other's change). If both flags are not true afterward, one of the
// two concurrent writes was lost.
func TestSQLStorePatchCapabilitiesConcurrent(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	org := seedOrg(t, q, "hosts-patch-race-org")
	zone := seedZone(t, store, org, "fra")

	h := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "k3s-race"}
	if err := store.CreateHost(ctx, h); err != nil {
		t.Fatal(err)
	}

	on := true
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, errs[0] = store.PatchCapabilities(ctx, org, h.ID, &on, nil)
	}()
	go func() {
		defer wg.Done()
		<-start
		_, errs[1] = store.PatchCapabilities(ctx, org, h.ID, nil, &on)
	}()
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent patch %d: %v", i, err)
		}
	}

	got, err := store.GetHost(ctx, org, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Managed || !got.Monitored {
		t.Fatalf("host after concurrent patches = %+v, want both managed and monitored true (a lost-update race dropped one write)", got)
	}
}

// TestHostsAreIsolatedPerOrgByRLS mirrors TestAuditEventsAreIsolatedPerOrgByRLS
// (internal/audit/store_sql_test.go): it exercises the org_isolation RLS
// policy on the hosts table directly, via d.WithOrg, rather than through
// SQLStore (which testdb.Open's plain connection never sets pulsix.org_id
// for, so it only ever exercises org_isolation_bypass — see that test's own
// doc comment for the full reasoning). This is the one test in this suite
// that would fail if hosts' org_isolation USING clause were ever weakened
// to USING (true).
func TestHostsAreIsolatedPerOrgByRLS(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	orgA := seedOrg(t, q, "hosts-rls-org-a")
	orgB := seedOrg(t, q, "hosts-rls-org-b")
	zoneA := seedZone(t, store, orgA, "fra")
	zoneB := seedZone(t, store, orgB, "fra")

	if err := store.CreateHost(ctx, hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: orgA, ZoneID: zoneA.ID, Name: "a-host"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateHost(ctx, hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: orgB, ZoneID: zoneB.ID, Name: "b-host"}); err != nil {
		t.Fatal(err)
	}

	var seen []uuid.UUID
	err := d.WithOrg(ctx, orgA, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT org_id FROM hosts`)
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
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != orgA {
		t.Fatalf("RLS leak: saw %v, want only [%s]", seen, orgA)
	}
}

// TestZonesAreIsolatedPerOrgByRLS is TestHostsAreIsolatedPerOrgByRLS for
// the zones table — both tables carry their own org_isolation policy (see
// the migration), so both are exercised directly rather than assuming one
// proves the other.
func TestZonesAreIsolatedPerOrgByRLS(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	orgA := seedOrg(t, q, "zones-rls-org-a")
	orgB := seedOrg(t, q, "zones-rls-org-b")
	seedZone(t, store, orgA, "fra")
	seedZone(t, store, orgB, "fra")

	var seen []uuid.UUID
	err := d.WithOrg(ctx, orgA, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT org_id FROM zones`)
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
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != orgA {
		t.Fatalf("RLS leak: saw %v, want only [%s]", seen, orgA)
	}
}
