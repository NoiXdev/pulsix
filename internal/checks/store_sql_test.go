package checks_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// seedOrg creates a bare organization (monitoring groups need nothing
// else: no zone, no host).
func seedOrg(t *testing.T, d *db.DB, slug string) uuid.UUID {
	t.Helper()
	q := gen.New(d.Pool())
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

// TestGroupStoreRoundTrip exercises SQLStore directly (no Service): create,
// list, update (name, selector, priority all change; created_at survives),
// delete, and the sentinel errors a missing row and a duplicate name
// produce.
func TestGroupStoreRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-groups")
	st := checks.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)

	created, err := st.Create(scoped, checks.Group{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "web", Selector: "role=web", Priority: 3})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "web" || created.Selector != "role=web" || created.Priority != 3 {
		t.Fatalf("created = %+v", created)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("created/updated_at must be stamped by the database, got %+v", created)
	}

	// A second group with the same name in the same org collides with the
	// unique (org_id, name) constraint.
	_, err = st.Create(scoped, checks.Group{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "web", Selector: "role=web2"})
	if !errors.Is(err, checks.ErrNameTaken) {
		t.Fatalf("duplicate name = %v, want ErrNameTaken", err)
	}

	list, err := st.List(scoped, org)
	if err != nil || len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}

	updated, err := st.Update(scoped, checks.Group{ID: created.ID, OrgID: org, Name: "web-servers", Selector: "role=web and env=prod", Priority: 7})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "web-servers" || updated.Selector != "role=web and env=prod" || updated.Priority != 7 {
		t.Fatalf("updated = %+v", updated)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("CreatedAt = %v, want unchanged %v", updated.CreatedAt, created.CreatedAt)
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("UpdatedAt = %v, want after %v", updated.UpdatedAt, created.UpdatedAt)
	}

	// Updating an id that does not exist in this org is ErrNotFound.
	_, err = st.Update(scoped, checks.Group{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "ghost", Selector: "role=x"})
	if !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("update unknown id = %v, want ErrNotFound", err)
	}

	if err := st.Delete(scoped, org, updated.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(scoped, org, updated.ID); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("deleting twice = %v, want ErrNotFound", err)
	}
	list, err = st.List(scoped, org)
	if err != nil || len(list) != 0 {
		t.Fatalf("list after delete = %+v, %v", list, err)
	}
}

// TestGroupStoreOrgIsolation: two organizations may reuse the same group
// name (the unique constraint is (org_id, name), not name alone), and
// neither can list, update or delete the other's group — the store filters
// by org_id in the WHERE clause independently of row-level security, the
// same guarantee hosts.sql's GetHost/GetZone document.
func TestGroupStoreOrgIsolation(t *testing.T) {
	d := testdb.Open(t)
	orgA := seedOrg(t, d, "checks-org-a")
	orgB := seedOrg(t, d, "checks-org-b")
	st := checks.NewSQLStore(d)

	a, err := st.Create(db.WithOrgScope(ctx, orgA), checks.Group{ID: uuid.Must(uuid.NewV7()), OrgID: orgA, Name: "web", Selector: "role=web"})
	if err != nil {
		t.Fatal(err)
	}
	// Same name, different org: no conflict.
	b, err := st.Create(db.WithOrgScope(ctx, orgB), checks.Group{ID: uuid.Must(uuid.NewV7()), OrgID: orgB, Name: "web", Selector: "role=web"})
	if err != nil {
		t.Fatalf("a name taken in another org must not collide: %v", err)
	}

	listA, err := st.List(db.WithOrgScope(ctx, orgA), orgA)
	if err != nil || len(listA) != 1 || listA[0].ID != a.ID {
		t.Fatalf("orgA's list must show only its own group: %+v, %v", listA, err)
	}
	listB, err := st.List(db.WithOrgScope(ctx, orgB), orgB)
	if err != nil || len(listB) != 1 || listB[0].ID != b.ID {
		t.Fatalf("orgB's list must show only its own group: %+v, %v", listB, err)
	}

	// orgB may not update or delete orgA's group by id, even when it
	// (incorrectly) supplies its own org id — the row belongs to orgA.
	if _, err := st.Update(db.WithOrgScope(ctx, orgB), checks.Group{ID: a.ID, OrgID: orgB, Name: "hijacked", Selector: "role=x"}); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("cross-org update = %v, want ErrNotFound", err)
	}
	if err := st.Delete(db.WithOrgScope(ctx, orgB), orgB, a.ID); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("cross-org delete = %v, want ErrNotFound", err)
	}
	// orgA's group survived both attempts.
	listA, err = st.List(db.WithOrgScope(ctx, orgA), orgA)
	if err != nil || len(listA) != 1 {
		t.Fatalf("orgA's group must survive orgB's attempts: %+v, %v", listA, err)
	}
}

// TestGroupStoreUpdateNameTaken: renaming a group onto a name already used
// by another group of the same organization collides with the unique
// (org_id, name) constraint exactly as Create does.
func TestGroupStoreUpdateNameTaken(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-update-name-taken")
	st := checks.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)

	a, err := st.Create(scoped, checks.Group{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "a", Selector: "role=a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.Create(scoped, checks.Group{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "b", Selector: "role=b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Update(scoped, checks.Group{ID: b.ID, OrgID: org, Name: a.Name, Selector: "role=b"}); !errors.Is(err, checks.ErrNameTaken) {
		t.Fatalf("renaming onto a taken name = %v, want ErrNameTaken", err)
	}
}

// TestGroupStoreGenericErrorsSurface: a store failure that is neither "no
// rows" nor a unique violation (here, a context already canceled before
// the statement runs) must surface as a wrapped error rather than being
// mistaken for ErrNotFound or ErrNameTaken.
func TestGroupStoreGenericErrorsSurface(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-generic-errors")
	st := checks.NewSQLStore(d)
	g, err := st.Create(db.WithOrgScope(ctx, org), checks.Group{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "web", Selector: "role=web"})
	if err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(db.WithOrgScope(ctx, org))
	cancel()

	if _, err := st.Create(canceled, checks.Group{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "other", Selector: "role=x"}); err == nil ||
		errors.Is(err, checks.ErrNameTaken) || errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("Create on a canceled context = %v, want a plain wrapped error", err)
	}
	if _, err := st.List(canceled, org); err == nil {
		t.Fatalf("List on a canceled context must fail")
	}
	if _, err := st.Update(canceled, checks.Group{ID: g.ID, OrgID: org, Name: "web2", Selector: "role=web"}); err == nil ||
		errors.Is(err, checks.ErrNameTaken) || errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("Update on a canceled context = %v, want a plain wrapped error", err)
	}
	if err := st.Delete(canceled, org, g.ID); err == nil || errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("Delete on a canceled context = %v, want a plain wrapped error", err)
	}
}

// seedTuples writes n distinct check_current tuples for host straight into
// the table (seed/item-1..n/m), the cheapest way to put a host at the
// per-host tuple cap without sending four batches of observations.
func seedTuples(t *testing.T, d *db.DB, org, host uuid.UUID, n int) {
	t.Helper()
	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO check_current (org_id, host_id, check_key, item, metric, value, at)
		SELECT $1, $2, 'seed', 'item-' || g, 'm', 1, now() FROM generate_series(1, $3::int) g`,
		org, host, n); err != nil {
		t.Fatal(err)
	}
}

// tupleCount is how many check_current rows host has.
func tupleCount(t *testing.T, d *db.DB, host uuid.UUID) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM check_current WHERE host_id = $1`, host).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// sampleCount is how many check_samples rows host has for one tuple.
func sampleCount(t *testing.T, d *db.DB, host uuid.UUID, key, item, metric string) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(ctx, `
		SELECT count(*) FROM check_samples WHERE host_id = $1 AND check_key = $2 AND item = $3 AND metric = $4`,
		host, key, item, metric).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestRecordObservationsCapsNewTuplesPerHost is backlog 7.57: nothing
// prunes check_current, so a host's agent could otherwise grow its tuple
// set by a thousand rows per batch without end. A host already holding
// MaxTuplesPerHost tuples gets no new one: neither a check_current row nor
// a sample, and the observation is counted as OverCap.
func TestRecordObservationsCapsNewTuplesPerHost(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-tuple-cap")
	host := seedHost(t, d, org, "tuple-cap-host")
	seedTuples(t, d, org, host, checks.MaxTuplesPerHost)
	now := time.Now().UTC().Truncate(time.Second)
	svc := checks.NewService(checks.NewSQLStore(d), fixedClock(now))
	scoped := db.WithOrgScope(ctx, org)

	res, err := svc.RecordObservations(scoped, org, []checks.Observation{
		{HostID: host, CheckKey: "cpu", Metric: "usage", At: now, Value: f64(1)},
		{HostID: host, CheckKey: "cpu", Metric: "errors", At: now, Err: "boom"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 0 || res.OverCap != 2 || res.Rejected() != 2 {
		t.Fatalf("ingest = %+v, want both new tuples counted over the cap and nothing accepted", res)
	}
	if got := tupleCount(t, d, host); got != checks.MaxTuplesPerHost {
		t.Fatalf("host has %d tuples, want it to stay at the cap %d", got, checks.MaxTuplesPerHost)
	}
	if got := sampleCount(t, d, host, "cpu", "", "usage"); got != 0 {
		t.Fatalf("over-cap tuple stored %d samples, want none", got)
	}
	if _, marked := cursorDirtyAt(t, d, host); marked {
		t.Fatal("a batch that stored nothing marked the host dirty")
	}
}

// TestRecordObservationsAdmitsNewTuplesUpToTheCap: one below the cap, the
// first new tuple of a batch is admitted (the host then holds exactly
// MaxTuplesPerHost) and a second one is not. A repeat of the admitted tuple
// later in the same batch is an existing tuple by then and still lands.
func TestRecordObservationsAdmitsNewTuplesUpToTheCap(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-tuple-cap-edge")
	host := seedHost(t, d, org, "tuple-cap-edge-host")
	seedTuples(t, d, org, host, checks.MaxTuplesPerHost-1)
	now := time.Now().UTC().Truncate(time.Second)
	svc := checks.NewService(checks.NewSQLStore(d), fixedClock(now))
	scoped := db.WithOrgScope(ctx, org)

	res, err := svc.RecordObservations(scoped, org, []checks.Observation{
		{HostID: host, CheckKey: "cpu", Metric: "usage", At: now.Add(-time.Second), Value: f64(1)},
		{HostID: host, CheckKey: "cpu", Metric: "load1", At: now, Value: f64(2)},
		{HostID: host, CheckKey: "cpu", Metric: "usage", At: now, Value: f64(3)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 2 || res.OverCap != 1 {
		t.Fatalf("ingest = %+v, want 2 accepted (cpu/usage twice) and 1 over the cap (cpu/load1)", res)
	}
	if got := tupleCount(t, d, host); got != checks.MaxTuplesPerHost {
		t.Fatalf("host has %d tuples, want exactly the cap %d", got, checks.MaxTuplesPerHost)
	}
	if got := sampleCount(t, d, host, "cpu", "", "usage"); got != 2 {
		t.Fatalf("cpu/usage has %d samples, want 2", got)
	}
	if got := sampleCount(t, d, host, "cpu", "", "load1"); got != 0 {
		t.Fatalf("cpu/load1 has %d samples, want none", got)
	}
	if _, marked := cursorDirtyAt(t, d, host); !marked {
		t.Fatal("a batch that stored something did not mark the host dirty")
	}
}

// TestRecordObservationsUpdatesExistingTuplesAtCap is Review Focus 5: a
// host that already holds exactly the cap keeps updating the tuples it
// has — value, label and error alike, with their samples — and another
// host of the same organization is not affected by its neighbour's cap.
func TestRecordObservationsUpdatesExistingTuplesAtCap(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-tuple-cap-update")
	host := seedHost(t, d, org, "tuple-cap-full-host")
	other := seedHost(t, d, org, "tuple-cap-other-host")
	seedTuples(t, d, org, host, checks.MaxTuplesPerHost)
	now := time.Now().UTC().Truncate(time.Second)
	svc := checks.NewService(checks.NewSQLStore(d), fixedClock(now))
	scoped := db.WithOrgScope(ctx, org)
	label := "degraded"

	res, err := svc.RecordObservations(scoped, org, []checks.Observation{
		{HostID: host, CheckKey: "seed", Item: "item-1", Metric: "m", At: now, Value: f64(99)},
		{HostID: host, CheckKey: "seed", Item: "item-2", Metric: "m", At: now, Err: "boom"},
		{HostID: host, CheckKey: "seed", Item: "item-3", Metric: "m", At: now, Label: &label},
		{HostID: other, CheckKey: "cpu", Metric: "usage", At: now, Value: f64(7)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 4 || res.Rejected() != 0 {
		t.Fatalf("ingest = %+v, want all 4 accepted", res)
	}
	current, err := svc.CurrentForHost(scoped, org, host)
	if err != nil {
		t.Fatal(err)
	}
	byItem := map[string]checks.Current{}
	for _, c := range current {
		byItem[c.Item] = c
	}
	if c := byItem["item-1"]; c.Value == nil || *c.Value != 99 || !c.At.Equal(now) {
		t.Fatalf("item-1 current = %+v, want value 99 at %v", c, now)
	}
	if c := byItem["item-2"]; c.Err != "boom" || c.Value != nil {
		t.Fatalf("item-2 current = %+v, want the collection error", c)
	}
	if c := byItem["item-3"]; c.Label == nil || *c.Label != label || c.Value != nil {
		t.Fatalf("item-3 current = %+v, want label %q and no value", c, label)
	}
	if got := sampleCount(t, d, host, "seed", "item-1", "m"); got != 1 {
		t.Fatalf("item-1 has %d samples, want 1", got)
	}
	if got := tupleCount(t, d, host); got != checks.MaxTuplesPerHost {
		t.Fatalf("host has %d tuples, want %d", got, checks.MaxTuplesPerHost)
	}
	if got := sampleCount(t, d, other, "cpu", "", "usage"); got != 1 {
		t.Fatalf("other host has %d samples, want 1", got)
	}
}

// TestRecordObservationsStoresABatchWithANULError is backlog 7.115: a NUL
// byte in one observation's error text used to make Postgres reject the
// whole batch. The text is stripped instead and every observation lands.
func TestRecordObservationsStoresABatchWithANULError(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-nul-error")
	host := seedHost(t, d, org, "nul-error-host")
	now := time.Now().UTC().Truncate(time.Second)
	svc := checks.NewService(checks.NewSQLStore(d), fixedClock(now))
	scoped := db.WithOrgScope(ctx, org)

	res, err := svc.RecordObservations(scoped, org, []checks.Observation{
		{HostID: host, CheckKey: "cpu", Metric: "usage", At: now, Value: f64(1)},
		{HostID: host, CheckKey: "disk", Item: "/", Metric: "used_pct", At: now, Err: "read /proc\x00/x: denied"},
		{HostID: host, CheckKey: "cpu", Metric: "load1", At: now, Value: f64(2)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 3 {
		t.Fatalf("ingest = %+v, want all 3 accepted", res)
	}
	cur, err := svc.CurrentForHost(scoped, org, host)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	found := false
	for _, c := range cur {
		if c.CheckKey == "disk" {
			got, found = c.Err, true
		}
	}
	if !found || got != "read /proc/x: denied" {
		t.Fatalf("stored error = %q (found %v), want %q", got, found, "read /proc/x: denied")
	}
}

// TestRecordObservationsCountsANULOnlyErrorAsMalformed: an error text made
// only of NUL bytes is empty once cleaned, which leaves an observation with
// no value, label or error. It is counted Malformed instead of reaching the
// store's CHECK constraints, and the rest of the batch still lands.
func TestRecordObservationsCountsANULOnlyErrorAsMalformed(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-nul-only-error")
	host := seedHost(t, d, org, "nul-only-error-host")
	now := time.Now().UTC().Truncate(time.Second)
	svc := checks.NewService(checks.NewSQLStore(d), fixedClock(now))
	scoped := db.WithOrgScope(ctx, org)

	res, err := svc.RecordObservations(scoped, org, []checks.Observation{
		{HostID: host, CheckKey: "cpu", Metric: "usage", At: now, Value: f64(1)},
		{HostID: host, CheckKey: "disk", Item: "/", Metric: "used_pct", At: now, Err: "\x00"},
		{HostID: host, CheckKey: "cpu", Metric: "load1", At: now, Value: f64(2)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 2 || res.Malformed != 1 {
		t.Fatalf("ingest = %+v, want 2 accepted and 1 malformed", res)
	}
}
