package items_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/items"
)

// seedOrg creates a bare organization, the same minimal fixture every
// other package's own store tests use (see e.g. rules' store_sql_test.go).
func seedOrg(t *testing.T, d *db.DB, slug string) uuid.UUID {
	t.Helper()
	q := gen.New(d.Pool())
	org, err := q.CreateOrganization(context.Background(), gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

// seedHost creates a bare zone and host in org — check_items' and
// check_item_decisions' own host binding needs a real host to satisfy
// their composite FK.
func seedHost(t *testing.T, d *db.DB, org uuid.UUID, name string) uuid.UUID {
	t.Helper()
	q := gen.New(d.Pool())
	zone, err := q.CreateZone(context.Background(), gen.CreateZoneParams{ID: uuid.Must(uuid.NewV7()), OrgID: org, Slug: name, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	host, err := q.CreateHost(context.Background(), gen.CreateHostParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: name, Facts: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return host.ID
}

// insertCheckCurrent writes one check_current row directly — bypassing
// internal/checks entirely, since this package only needs a reading to
// exist at a known `at` for ItemsForHost/ItemsForOrg's LastSeen join, not
// anything RecordObservations itself validates.
func insertCheckCurrent(t *testing.T, d *db.DB, org, host uuid.UUID, checkKey, item, metric string, value float64, at time.Time) {
	t.Helper()
	if _, err := d.Pool().Exec(context.Background(), `
		INSERT INTO check_current (org_id, host_id, check_key, item, metric, value, at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		org, host, checkKey, item, metric, value, at); err != nil {
		t.Fatal(err)
	}
}

// insertCheckItem writes one check_items row directly with a caller-chosen
// first_seen, bypassing Discover (which always stamps now()) — the only
// way to simulate an item discovered long ago for the "survives a gap"
// test (review focus 7).
func insertCheckItem(t *testing.T, d *db.DB, org, host uuid.UUID, checkKey, item string, firstSeen time.Time) {
	t.Helper()
	if _, err := d.Pool().Exec(context.Background(), `
		INSERT INTO check_items (org_id, host_id, check_key, item, first_seen)
		VALUES ($1, $2, $3, $4, $5)`,
		org, host, checkKey, item, firstSeen); err != nil {
		t.Fatal(err)
	}
}

// isCheckViolation reports SQLSTATE 23514 (check_violation) — the same
// helper rules' own store_sql_test.go defines for the same reason: db
// only exposes helpers for unique and foreign key violations.
func isCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

// decision returns a ready-to-create items.Decision, the baseline every
// decision test mutates.
func decision(org uuid.UUID, hostID *uuid.UUID, scope items.Scope) items.Decision {
	now := time.Now().UTC().Truncate(time.Microsecond)
	d := items.Decision{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Scope: scope, HostID: hostID,
		CheckKey: "disk", MatchKind: items.MatchExact, Pattern: "/var",
		Verdict: items.Ignore, Note: "deliberately full backup volume",
		CreatedAt: now, UpdatedAt: now,
	}
	return d
}

// TestItemStoreDiscoverFirstSeenDoesNotMoveOnRediscovery is review focus 7's
// idempotency half: a second Discover of the same item must leave
// first_seen exactly as it was, proven by reading the timestamp back and
// comparing — not by counting rows.
func TestItemStoreDiscoverFirstSeenDoesNotMoveOnRediscovery(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "items-discover-idempotent")
	host := seedHost(t, d, org, "h1")
	st := items.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	if err := st.Discover(scoped, org, host, []items.Seen{{CheckKey: "disk", Name: "/var"}}); err != nil {
		t.Fatal(err)
	}
	first, err := st.ItemsForHost(scoped, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 {
		t.Fatalf("ItemsForHost after one Discover = %d items, want 1", len(first))
	}
	firstSeen1 := first[0].FirstSeen

	// A real gap, so a wrongly-moved first_seen is not disguised by two
	// now() calls landing in the same microsecond.
	time.Sleep(20 * time.Millisecond)

	if err := st.Discover(scoped, org, host, []items.Seen{{CheckKey: "disk", Name: "/var"}}); err != nil {
		t.Fatal(err)
	}
	second, err := st.ItemsForHost(scoped, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 {
		t.Fatalf("ItemsForHost after a second Discover = %d items, want 1 (ON CONFLICT DO NOTHING must not duplicate the row)", len(second))
	}
	firstSeen2 := second[0].FirstSeen
	if !firstSeen1.Equal(firstSeen2) {
		t.Fatalf("first_seen moved on rediscovery: was %v, now %v", firstSeen1, firstSeen2)
	}
}

// TestItemStoreDiscoverSurvivesGap is review focus 7's other half: a mount
// gone for two weeks and back is the same item, so its first_seen must not
// jump to "now" when Discover sees it again.
func TestItemStoreDiscoverSurvivesGap(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "items-discover-gap")
	host := seedHost(t, d, org, "h1")
	st := items.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	twoWeeksAgo := time.Now().UTC().Add(-14 * 24 * time.Hour).Truncate(time.Microsecond)
	insertCheckItem(t, d, org, host, "disk", "/var", twoWeeksAgo)

	if err := st.Discover(scoped, org, host, []items.Seen{{CheckKey: "disk", Name: "/var"}}); err != nil {
		t.Fatal(err)
	}

	views, err := st.ItemsForHost(scoped, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("ItemsForHost = %d items, want 1", len(views))
	}
	if !views[0].FirstSeen.Equal(twoWeeksAgo) {
		t.Fatalf("first_seen after a gap = %v, want the original %v (unmoved)", views[0].FirstSeen, twoWeeksAgo)
	}
}

// TestItemStoreDiscoverEmptySlice pins Discover's documented no-op shape
// for an empty batch: no error, no row.
func TestItemStoreDiscoverEmptySlice(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "items-discover-empty")
	host := seedHost(t, d, org, "h1")
	st := items.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	if err := st.Discover(scoped, org, host, nil); err != nil {
		t.Fatalf("Discover(nil) = %v, want no error", err)
	}
	views, err := st.ItemsForHost(scoped, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 0 {
		t.Fatalf("ItemsForHost after Discover(nil) = %d items, want 0", len(views))
	}
}

// TestItemStoreItemsForHostReportedAndLastSeen exercises the LEFT-JOIN-ish
// shape of ItemsForHost: an item with current readings reports LastSeen as
// the max(at) across every metric the tuple carries, and an item with no
// current row at all comes back Reported: false with the Go zero
// time.Time, never a scan error on the SQL-side sentinel.
func TestItemStoreItemsForHostReportedAndLastSeen(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "items-for-host")
	host := seedHost(t, d, org, "h1")
	st := items.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	if err := st.Discover(scoped, org, host, []items.Seen{
		{CheckKey: "disk", Name: "/var"},
		{CheckKey: "disk", Name: "/tmp"},
	}); err != nil {
		t.Fatal(err)
	}

	older := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	newer := time.Now().UTC().Truncate(time.Microsecond)
	// /var reports two metrics; LastSeen must be the later of the two, not
	// whichever happens to be scanned first.
	insertCheckCurrent(t, d, org, host, "disk", "/var", "used_pct", 95.0, older)
	insertCheckCurrent(t, d, org, host, "disk", "/var", "free_bytes", 1024, newer)
	// /tmp gets no check_current row at all.

	views, err := st.ItemsForHost(scoped, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("ItemsForHost = %d items, want 2", len(views))
	}

	byName := make(map[string]items.View, len(views))
	for _, v := range views {
		byName[v.Name] = v
	}

	varView, ok := byName["/var"]
	if !ok {
		t.Fatalf("missing /var in %+v", views)
	}
	if !varView.Reported {
		t.Fatalf("/var Reported = false, want true")
	}
	if !varView.LastSeen.Equal(newer) {
		t.Fatalf("/var LastSeen = %v, want the later reading %v", varView.LastSeen, newer)
	}
	if varView.OrgID != org || varView.HostID != host || varView.CheckKey != "disk" {
		t.Fatalf("/var identity = %+v", varView.Item)
	}

	tmpView, ok := byName["/tmp"]
	if !ok {
		t.Fatalf("missing /tmp in %+v", views)
	}
	if tmpView.Reported {
		t.Fatalf("/tmp Reported = true, want false (no check_current row)")
	}
	if !tmpView.LastSeen.IsZero() {
		t.Fatalf("/tmp LastSeen = %v, want the Go zero time (no SQL sentinel leaking through)", tmpView.LastSeen)
	}
}

// TestItemStoreItemsForOrgFilters exercises Filter across hosts: no
// filter returns everything in the organization, CheckKey narrows to one
// check, and ItemGlob is translated to LIKE such that a literal '%' or '_'
// in the OPERATOR's own pattern matches only itself — never the SQL
// wildcard it would otherwise become.
func TestItemStoreItemsForOrgFilters(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "items-for-org")
	hostA := seedHost(t, d, org, "a")
	hostB := seedHost(t, d, org, "b")
	st := items.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	if err := st.Discover(scoped, org, hostA, []items.Seen{
		{CheckKey: "disk", Name: "/data"},
		{CheckKey: "disk", Name: "/data2"},
		{CheckKey: "service", Name: "sshd"},
		// A literal '%' and '_' in the item name — the exact character set
		// LIKE treats specially, and our own glob syntax never does.
		{CheckKey: "disk", Name: "50%_full"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Discover(scoped, org, hostB, []items.Seen{
		{CheckKey: "disk", Name: "/other"},
		// Would match the unescaped LIKE translation of "50%_full"
		// ('%' -> "any run", '_' -> "any one char") but must NOT match the
		// escaped, literal pattern this item's name is not identical to.
		{CheckKey: "disk", Name: "50Xfull"},
	}); err != nil {
		t.Fatal(err)
	}

	all, err := st.ItemsForOrg(scoped, org, items.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 6 {
		t.Fatalf("ItemsForOrg(no filter) = %d items, want 6 (every item on every host)", len(all))
	}

	byCheck, err := st.ItemsForOrg(scoped, org, items.Filter{CheckKey: "service"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byCheck) != 1 || byCheck[0].Name != "sshd" {
		t.Fatalf("ItemsForOrg(CheckKey=service) = %+v, want exactly [sshd]", byCheck)
	}

	byGlob, err := st.ItemsForOrg(scoped, org, items.Filter{ItemGlob: "/data*"})
	if err != nil {
		t.Fatal(err)
	}
	gotNames := make(map[string]bool, len(byGlob))
	for _, v := range byGlob {
		gotNames[v.Name] = true
	}
	if len(byGlob) != 2 || !gotNames["/data"] || !gotNames["/data2"] {
		t.Fatalf("ItemsForOrg(ItemGlob=/data*) = %+v, want exactly [/data, /data2]", byGlob)
	}

	// The review-focus translation: '%' and '_' in the operator's own
	// pattern, with no '*' of our own glob syntax at all, must match only
	// the one item whose name is that literal string.
	literal, err := st.ItemsForOrg(scoped, org, items.Filter{ItemGlob: "50%_full"})
	if err != nil {
		t.Fatal(err)
	}
	if len(literal) != 1 || literal[0].Name != "50%_full" {
		t.Fatalf("ItemsForOrg(ItemGlob=%q) = %+v, want exactly one item named %q (not the decoy 50Xfull)",
			"50%_full", literal, "50%_full")
	}
}

// TestItemStoreOrgIsolation is the application-level counterpart of the
// RLS tests below: SQLStore's own WHERE org_id filtering must keep one
// organization's items and decisions out of another's reads, independent
// of whatever row-level security additionally enforces.
func TestItemStoreOrgIsolation(t *testing.T) {
	d := testdb.Open(t)
	orgA := seedOrg(t, d, "items-org-isolation-a")
	orgB := seedOrg(t, d, "items-org-isolation-b")
	hostA := seedHost(t, d, orgA, "h1")
	hostB := seedHost(t, d, orgB, "h1")
	st := items.NewSQLStore(d)
	scopedA := db.WithOrgScope(context.Background(), orgA)
	scopedB := db.WithOrgScope(context.Background(), orgB)

	if err := st.Discover(scopedA, orgA, hostA, []items.Seen{{CheckKey: "disk", Name: "/var"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Discover(scopedB, orgB, hostB, []items.Seen{{CheckKey: "disk", Name: "/var"}}); err != nil {
		t.Fatal(err)
	}
	dA := decision(orgA, nil, items.ScopeOrg)
	if err := st.CreateDecision(scopedA, dA); err != nil {
		t.Fatal(err)
	}
	dB := decision(orgB, nil, items.ScopeOrg)
	if err := st.CreateDecision(scopedB, dB); err != nil {
		t.Fatal(err)
	}

	if viewsB, err := st.ItemsForHost(scopedB, orgB, hostA); err != nil || len(viewsB) != 0 {
		t.Fatalf("orgB reading orgA's host = %+v, %v, want empty", viewsB, err)
	}
	if orgWide, err := st.ItemsForOrg(scopedB, orgB, items.Filter{}); err != nil || len(orgWide) != 1 {
		t.Fatalf("orgB's ItemsForOrg = %+v, %v, want only orgB's own item", orgWide, err)
	}
	if _, err := st.GetDecision(scopedB, orgB, dA.ID); !errors.Is(err, items.ErrNotFound) {
		t.Fatalf("orgB getting orgA's decision = %v, want ErrNotFound", err)
	}
	if err := st.DeleteDecision(scopedB, orgB, dA.ID); !errors.Is(err, items.ErrNotFound) {
		t.Fatalf("orgB deleting orgA's decision = %v, want ErrNotFound", err)
	}
	listB, err := st.ListDecisions(scopedB, orgB)
	if err != nil || len(listB) != 1 || listB[0].ID != dB.ID {
		t.Fatalf("orgB's ListDecisions = %+v, %v, want only orgB's own", listB, err)
	}
	evalB, err := st.Decisions(scopedB, orgB, &hostB)
	if err != nil || len(evalB) != 1 || evalB[0].ID != dB.ID {
		t.Fatalf("orgB's Decisions = %+v, %v, want only orgB's own", evalB, err)
	}

	// orgA's own reads still see everything it created.
	listA, err := st.ListDecisions(scopedA, orgA)
	if err != nil || len(listA) != 1 || listA[0].ID != dA.ID {
		t.Fatalf("orgA's ListDecisions = %+v, %v, want only orgA's own", listA, err)
	}
}

// TestItemsAreIsolatedPerOrgByRLS exercises the org_isolation policy on
// check_items directly, via a raw query over d.WithOrg's transaction
// rather than through SQLStore (which always adds its own WHERE org_id —
// see TestItemStoreOrgIsolation above). testdb.Open connects as
// pulsix_app without ever setting pulsix.org_id, so a plain SQLStore-based
// test only ever exercises org_isolation_bypass; this is what proves the
// org_isolation USING clause itself, mirroring
// rules.TestRulesAreIsolatedPerOrgByRLS.
func TestItemsAreIsolatedPerOrgByRLS(t *testing.T) {
	d := testdb.Open(t)
	orgA := seedOrg(t, d, "items-rls-a")
	orgB := seedOrg(t, d, "items-rls-b")
	hostA := seedHost(t, d, orgA, "h1")
	hostB := seedHost(t, d, orgB, "h1")
	st := items.NewSQLStore(d)

	if err := st.Discover(context.Background(), orgA, hostA, []items.Seen{{CheckKey: "disk", Name: "/var"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Discover(context.Background(), orgB, hostB, []items.Seen{{CheckKey: "disk", Name: "/var"}}); err != nil {
		t.Fatal(err)
	}

	var seen []uuid.UUID
	err := d.WithOrg(context.Background(), orgA, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `SELECT org_id FROM check_items`)
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
		t.Fatalf("RLS leak on check_items: saw %v, want only [%s]", seen, orgA)
	}
}

// TestItemDecisionsAreIsolatedPerOrgByRLS is TestItemsAreIsolatedPerOrgByRLS's
// counterpart for check_item_decisions, the other table migration 00048
// carries its own RLS pair on.
func TestItemDecisionsAreIsolatedPerOrgByRLS(t *testing.T) {
	d := testdb.Open(t)
	orgA := seedOrg(t, d, "item-decisions-rls-a")
	orgB := seedOrg(t, d, "item-decisions-rls-b")
	st := items.NewSQLStore(d)

	if err := st.CreateDecision(context.Background(), decision(orgA, nil, items.ScopeOrg)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateDecision(context.Background(), decision(orgB, nil, items.ScopeOrg)); err != nil {
		t.Fatal(err)
	}

	var seen []uuid.UUID
	err := d.WithOrg(context.Background(), orgA, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `SELECT org_id FROM check_item_decisions`)
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
		t.Fatalf("RLS leak on check_item_decisions: saw %v, want only [%s]", seen, orgA)
	}
}

// TestItemDecisionStoreRoundTrip exercises the CRUD half of Store directly:
// create, get, list, update (every mutable column changes; created_at
// survives, the caller-stamped updated_at is what is stored), delete, and
// the ErrNotFound a missing row produces — the same shape
// rules.TestRuleStoreRoundTrip exercises for check_rules.
func TestItemDecisionStoreRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "item-decisions-roundtrip")
	host := seedHost(t, d, org, "h1")
	st := items.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	dec := decision(org, &host, items.ScopeHost)
	if err := st.CreateDecision(scoped, dec); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetDecision(scoped, org, dec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Scope != dec.Scope || got.CheckKey != dec.CheckKey || got.MatchKind != dec.MatchKind ||
		got.Pattern != dec.Pattern || got.Verdict != dec.Verdict || got.Note != dec.Note {
		t.Fatalf("got = %+v, want fields matching %+v", got, dec)
	}
	if got.HostID == nil || *got.HostID != host {
		t.Fatalf("HostID = %v, want %v", got.HostID, host)
	}
	if !got.CreatedAt.Equal(dec.CreatedAt) || !got.UpdatedAt.Equal(dec.UpdatedAt) {
		t.Fatalf("timestamps = %v/%v, want %v/%v (the caller's own clock)", got.CreatedAt, got.UpdatedAt, dec.CreatedAt, dec.UpdatedAt)
	}

	list, err := st.ListDecisions(scoped, org)
	if err != nil || len(list) != 1 || list[0].ID != dec.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}

	updated := got
	updated.Pattern = "/mnt/scratch-*"
	updated.MatchKind = items.MatchGlob
	updated.Verdict = items.Watch
	updated.Note = "overruled: this one matters"
	updated.UpdatedAt = time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	if err := st.UpdateDecision(scoped, updated); err != nil {
		t.Fatal(err)
	}
	reGot, err := st.GetDecision(scoped, org, dec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reGot.Pattern != updated.Pattern || reGot.MatchKind != updated.MatchKind ||
		reGot.Verdict != updated.Verdict || reGot.Note != updated.Note {
		t.Fatalf("updated = %+v", reGot)
	}
	if !reGot.CreatedAt.Equal(dec.CreatedAt) {
		t.Fatalf("CreatedAt = %v, want unchanged %v", reGot.CreatedAt, dec.CreatedAt)
	}
	if !reGot.UpdatedAt.Equal(updated.UpdatedAt) {
		t.Fatalf("UpdatedAt = %v, want the caller's own stamp %v", reGot.UpdatedAt, updated.UpdatedAt)
	}

	ghost := decision(org, &host, items.ScopeHost)
	if err := st.UpdateDecision(scoped, ghost); !errors.Is(err, items.ErrNotFound) {
		t.Fatalf("update unknown id = %v, want ErrNotFound", err)
	}

	if err := st.DeleteDecision(scoped, org, dec.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteDecision(scoped, org, dec.ID); !errors.Is(err, items.ErrNotFound) {
		t.Fatalf("deleting twice = %v, want ErrNotFound", err)
	}
	if _, err := st.GetDecision(scoped, org, dec.ID); !errors.Is(err, items.ErrNotFound) {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
}

// TestItemDecisionStoreScopeCheckConstraint exercises the CHECK tying
// scope to host_id directly against SQLStore, bypassing the service (a
// later task) that would refuse the same input itself — the same shape
// rules.TestRuleStoreScopeCheckConstraint uses for check_rules' own CHECK.
func TestItemDecisionStoreScopeCheckConstraint(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "item-decisions-scope-check")
	host := seedHost(t, d, org, "h1")
	st := items.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	// Valid: each scope with exactly its own columns set.
	orgDec := decision(org, nil, items.ScopeOrg)
	if err := st.CreateDecision(scoped, orgDec); err != nil {
		t.Fatalf("org scope: %v", err)
	}
	hostDec := decision(org, &host, items.ScopeHost)
	if err := st.CreateDecision(scoped, hostDec); err != nil {
		t.Fatalf("host scope: %v", err)
	}

	// Invalid: scope 'org' but host_id set anyway.
	bad := decision(org, &host, items.ScopeOrg)
	if err := st.CreateDecision(scoped, bad); !isCheckViolation(err) {
		t.Fatalf("org scope with a host set = %v, want a check_violation", err)
	}
	// Invalid: scope 'host' but host_id left nil.
	bad2 := decision(org, nil, items.ScopeHost)
	if err := st.CreateDecision(scoped, bad2); !isCheckViolation(err) {
		t.Fatalf("host scope without a host = %v, want a check_violation", err)
	}
}

// TestItemDecisionStoreForeignKeyViolation: binding a decision to a host id
// that does not exist in the organization is refused as ErrInvalidInput,
// for both Create and Update, the same posture
// rules.TestRuleStoreForeignKeyViolation takes for check_rules.
func TestItemDecisionStoreForeignKeyViolation(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "item-decisions-fk")
	host := seedHost(t, d, org, "h1")
	st := items.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	ghostHost := uuid.Must(uuid.NewV7())
	bad := decision(org, &ghostHost, items.ScopeHost)
	if err := st.CreateDecision(scoped, bad); !errors.Is(err, items.ErrInvalidInput) {
		t.Fatalf("create with an unknown host = %v, want ErrInvalidInput", err)
	}

	good := decision(org, &host, items.ScopeHost)
	if err := st.CreateDecision(scoped, good); err != nil {
		t.Fatal(err)
	}
	good.HostID = &ghostHost
	if err := st.UpdateDecision(scoped, good); !errors.Is(err, items.ErrInvalidInput) {
		t.Fatalf("update onto an unknown host = %v, want ErrInvalidInput", err)
	}
}

// TestItemStoreDecisions pins the evaluator's own lookup: the
// organization's decisions plus, when hostID is non-nil, that host's — and
// a nil hostID returns only the organization-scoped ones, never a
// different host's.
func TestItemStoreDecisions(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "item-decisions-for-eval")
	hostA := seedHost(t, d, org, "a")
	hostB := seedHost(t, d, org, "b")
	st := items.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	orgDec := decision(org, nil, items.ScopeOrg)
	if err := st.CreateDecision(scoped, orgDec); err != nil {
		t.Fatal(err)
	}
	hostADec := decision(org, &hostA, items.ScopeHost)
	if err := st.CreateDecision(scoped, hostADec); err != nil {
		t.Fatal(err)
	}
	hostBDec := decision(org, &hostB, items.ScopeHost)
	if err := st.CreateDecision(scoped, hostBDec); err != nil {
		t.Fatal(err)
	}

	nilHost, err := st.Decisions(scoped, org, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(nilHost) != 1 || nilHost[0].ID != orgDec.ID {
		t.Fatalf("Decisions(nil host) = %+v, want only the org-scoped decision", nilHost)
	}

	forA, err := st.Decisions(scoped, org, &hostA)
	if err != nil {
		t.Fatal(err)
	}
	gotIDs := map[uuid.UUID]bool{}
	for _, dd := range forA {
		gotIDs[dd.ID] = true
	}
	if len(forA) != 2 || !gotIDs[orgDec.ID] || !gotIDs[hostADec.ID] {
		t.Fatalf("Decisions(hostA) = %+v, want [org, hostA] and not hostB's", forA)
	}

	forB, err := st.Decisions(scoped, org, &hostB)
	if err != nil {
		t.Fatal(err)
	}
	gotIDs = map[uuid.UUID]bool{}
	for _, dd := range forB {
		gotIDs[dd.ID] = true
	}
	if len(forB) != 2 || !gotIDs[orgDec.ID] || !gotIDs[hostBDec.ID] {
		t.Fatalf("Decisions(hostB) = %+v, want [org, hostB] and not hostA's", forB)
	}
}

// TestItemStoreCascadesOnHostDelete pins "deleting a host takes its items
// and decisions with it" — the same ON DELETE CASCADE check_rules' own
// host binding uses (00045), applied here to both of 00048's tables.
func TestItemStoreCascadesOnHostDelete(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "items-cascade-host")
	host := seedHost(t, d, org, "h1")
	st := items.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	if err := st.Discover(scoped, org, host, []items.Seen{{CheckKey: "disk", Name: "/var"}}); err != nil {
		t.Fatal(err)
	}
	dec := decision(org, &host, items.ScopeHost)
	if err := st.CreateDecision(scoped, dec); err != nil {
		t.Fatal(err)
	}

	q := gen.New(d.Pool())
	if _, err := q.DeleteHost(context.Background(), gen.DeleteHostParams{OrgID: org, ID: host}); err != nil {
		t.Fatal(err)
	}

	if views, err := st.ItemsForOrg(scoped, org, items.Filter{}); err != nil || len(views) != 0 {
		t.Fatalf("ItemsForOrg after host delete = %+v, %v, want empty", views, err)
	}
	if list, err := st.ListDecisions(scoped, org); err != nil || len(list) != 0 {
		t.Fatalf("ListDecisions after host delete = %+v, %v, want empty", list, err)
	}
}
