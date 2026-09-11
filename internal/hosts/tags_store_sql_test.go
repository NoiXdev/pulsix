package hosts_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/hosts/selector"
)

// insertRawTag writes one host_tags row directly through sqlc, bypassing
// SQLStore.ReplaceManualTags — the only way in this test file to seed a
// "fact" tag, since no production code path writes one yet (that is a
// later task's agent-facts pipeline).
func insertRawTag(t *testing.T, q *gen.Queries, orgID, hostID uuid.UUID, key, value string, source hosts.TagSource) {
	t.Helper()
	if err := q.InsertHostTag(context.Background(), gen.InsertHostTagParams{
		HostID: hostID, OrgID: orgID, Key: key, Value: value, Source: string(source),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSQLStoreListHostTagsAndReplaceManualTags(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(q, d.Pool())
	org := seedOrg(t, q, "tags-crud-org")
	zone := seedZone(t, store, org, "fra")
	h := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "web-01"}
	if err := store.CreateHost(ctx, h); err != nil {
		t.Fatal(err)
	}

	insertRawTag(t, q, org, h.ID, "os", "debian", hosts.TagSourceFact)

	if err := store.ReplaceManualTags(ctx, org, h.ID, []hosts.Tag{
		{Key: "role", Value: "db", Source: hosts.TagSourceManual},
		{Key: "env", Value: "prod", Source: hosts.TagSourceManual},
	}); err != nil {
		t.Fatalf("ReplaceManualTags: %v", err)
	}

	got, err := store.ListHostTags(ctx, org, h.ID)
	if err != nil {
		t.Fatalf("ListHostTags: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("tags = %+v, want 3 (1 fact + 2 manual)", got)
	}

	// Replacing again with a different manual set must drop "env" and
	// "role" but leave "os" (the fact tag) alone.
	if err := store.ReplaceManualTags(ctx, org, h.ID, []hosts.Tag{
		{Key: "role", Value: "web", Source: hosts.TagSourceManual},
	}); err != nil {
		t.Fatalf("ReplaceManualTags (second call): %v", err)
	}
	got, err = store.ListHostTags(ctx, org, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]hosts.Tag{}
	for _, tag := range got {
		byKey[tag.Key+"/"+string(tag.Source)] = tag
	}
	if len(got) != 2 {
		t.Fatalf("tags after replace = %+v, want exactly os/fact and role/manual", got)
	}
	if tag, ok := byKey["os/fact"]; !ok || tag.Value != "debian" {
		t.Fatalf("fact tag must survive a manual replace untouched: %+v", got)
	}
	if tag, ok := byKey["role/manual"]; !ok || tag.Value != "web" {
		t.Fatalf("manual tag should now be role=web: %+v", got)
	}
	if _, ok := byKey["env/manual"]; ok {
		t.Fatalf("env manual tag should have been replaced away: %+v", got)
	}
}

// TestSQLStoreReplaceManualTagsUnknownHostFailsOnForeignKey proves
// ReplaceManualTags itself does no existence check of its own (that is
// Service.SetTags' job, via GetHost, before this is ever called) — calling
// it directly against a host id that was never created still fails, but
// only because host_tags.host_id has a foreign key to hosts(id), not
// because the store checked anything. This pins the store method's own
// contract (a plain DB operation relying on the schema's own constraints)
// as distinct from the service-level ErrNotFound behavior already covered
// by TestSetTagsUnknownHost (tags_test.go).
func TestSQLStoreReplaceManualTagsUnknownHostFailsOnForeignKey(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(q, d.Pool())
	org := seedOrg(t, q, "tags-replace-unknown-org")

	err := store.ReplaceManualTags(ctx, org, uuid.Must(uuid.NewV7()), []hosts.Tag{{Key: "role", Value: "db", Source: hosts.TagSourceManual}})
	if err == nil {
		t.Fatal("expected the foreign-key violation on an unknown host_id to surface as an error")
	}
}

func TestSQLStoreListTagKeyValueCounts(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(q, d.Pool())
	org := seedOrg(t, q, "tags-counts-org")
	zone := seedZone(t, store, org, "fra")

	h1 := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "a-host"}
	h2 := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "b-host"}
	for _, h := range []hosts.Host{h1, h2} {
		if err := store.CreateHost(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ReplaceManualTags(ctx, org, h1.ID, []hosts.Tag{{Key: "role", Value: "db", Source: hosts.TagSourceManual}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceManualTags(ctx, org, h2.ID, []hosts.Tag{{Key: "role", Value: "web", Source: hosts.TagSourceManual}}); err != nil {
		t.Fatal(err)
	}
	// h1 also carries role=db as a fact tag — ListTagKeyValueCounts counts
	// DISTINCT host_id per (key, value), so this must not double-count h1.
	insertRawTag(t, q, org, h1.ID, "role", "db", hosts.TagSourceFact)

	counts, err := store.ListTagKeyValueCounts(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	byValue := map[string]int{}
	for _, c := range counts {
		if c.Key != "role" {
			t.Fatalf("unexpected key %q in counts: %+v", c.Key, counts)
		}
		byValue[c.Value] = c.Count
	}
	if byValue["db"] != 1 {
		t.Fatalf("count for role=db = %d, want 1 (h1 counted once despite two rows)", byValue["db"])
	}
	if byValue["web"] != 1 {
		t.Fatalf("count for role=web = %d, want 1", byValue["web"])
	}
}

// TestSQLStoreQueryHostsFiltersByRoleAndZone is the brief's own step-3
// example: QueryHosts must return the right hosts for
// "role = db and zone in (fra)" against a real database.
func TestSQLStoreQueryHostsFiltersByRoleAndZone(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(q, d.Pool())
	org := seedOrg(t, q, "tags-query-org")
	fra := seedZone(t, store, org, "fra")
	hel := seedZone(t, store, org, "hel")

	dbFra := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: fra.ID, Name: "db-fra"}
	webFra := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: fra.ID, Name: "web-fra"}
	dbHel := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: hel.ID, Name: "db-hel"}
	for _, h := range []hosts.Host{dbFra, webFra, dbHel} {
		if err := store.CreateHost(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	for _, seed := range []struct {
		host hosts.Host
		role string
		zone string
	}{
		{dbFra, "db", "fra"}, {webFra, "web", "fra"}, {dbHel, "db", "hel"},
	} {
		if err := store.ReplaceManualTags(ctx, org, seed.host.ID, []hosts.Tag{
			{Key: "role", Value: seed.role, Source: hosts.TagSourceManual},
			{Key: "zone", Value: seed.zone, Source: hosts.TagSourceManual},
		}); err != nil {
			t.Fatal(err)
		}
	}

	sel, err := selector.Parse(`role = db and zone in (fra)`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.QueryHosts(ctx, org, sel)
	if err != nil {
		t.Fatalf("QueryHosts: %v", err)
	}
	if len(got) != 1 || got[0].ID != dbFra.ID {
		t.Fatalf("QueryHosts(role = db and zone in (fra)) = %+v, want just db-fra", got)
	}
}

// TestSQLStoreQueryHostsMergesFactAndManual is
// TestQueryHostsMergesFactAndManual (tags_test.go) against the real
// database: a manual tag overriding a fact tag on the same key must be
// what QueryHosts actually matches against, not the fact value underneath
// it. This is the case that would fail if the CTE in QueryHosts' own doc
// comment (tags_store_sql.go) were ever dropped in favor of querying
// host_tags directly.
func TestSQLStoreQueryHostsMergesFactAndManual(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(q, d.Pool())
	org := seedOrg(t, q, "tags-merge-org")
	zone := seedZone(t, store, org, "fra")

	dbHost := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "db-01"}
	webHost := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "web-01"}
	for _, h := range []hosts.Host{dbHost, webHost} {
		if err := store.CreateHost(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	// dbHost: fact says role=web, but a manual tag overrides it to role=db.
	insertRawTag(t, q, org, dbHost.ID, "role", "web", hosts.TagSourceFact)
	if err := store.ReplaceManualTags(ctx, org, dbHost.ID, []hosts.Tag{{Key: "role", Value: "db", Source: hosts.TagSourceManual}}); err != nil {
		t.Fatal(err)
	}
	// webHost: fact value applies as-is, no manual override.
	insertRawTag(t, q, org, webHost.ID, "role", "web", hosts.TagSourceFact)

	sel, err := selector.Parse(`role = db`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.QueryHosts(ctx, org, sel)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != dbHost.ID {
		t.Fatalf("QueryHosts(role = db) = %+v, want just db-01 (its overridden fact value must not leak through)", got)
	}

	sel, err = selector.Parse(`role = web`)
	if err != nil {
		t.Fatal(err)
	}
	got, err = store.QueryHosts(ctx, org, sel)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != webHost.ID {
		t.Fatalf("QueryHosts(role = web) = %+v, want just web-01", got)
	}
}

// TestSQLStoreQueryHostsAgreesWithMatches is the brief's central equivalence
// proof: for a matrix of hosts carrying a mix of manual and fact tags
// (including hosts where the two conflict on the same key, the trickiest
// case — see TestSQLStoreQueryHostsMergesFactAndManual above) and a list of
// selectors covering every operator, QueryHosts (SQL, against the real
// database) and sel.Matches (Go, against the same host's merged tags) must
// agree on every host for every selector. If either ever drifted from the
// other — the SQL fragment mishandling an operator, or the CTE's merge
// precedence disagreeing with the Go merge — this test would fail on the
// selector/host pair that first exposes it.
func TestSQLStoreQueryHostsAgreesWithMatches(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(q, d.Pool())
	org := seedOrg(t, q, "tags-equivalence-org")
	zone := seedZone(t, store, org, "fra")

	type seedTag struct {
		key, value string
		source     hosts.TagSource
	}
	hostSpecs := []struct {
		name string
		tags []seedTag
	}{
		{"h-db-prod-fra", []seedTag{
			{"role", "db", hosts.TagSourceManual}, {"env", "prod", hosts.TagSourceManual}, {"zone", "fra", hosts.TagSourceFact},
		}},
		{"h-web-prod-hel", []seedTag{
			{"role", "web", hosts.TagSourceManual}, {"env", "prod", hosts.TagSourceFact}, {"zone", "hel", hosts.TagSourceFact},
		}},
		{"h-conflict-role", []seedTag{
			// role is set by both sources with DIFFERENT values — the case
			// that only a correct merge (manual wins) gets right.
			{"role", "cache", hosts.TagSourceManual}, {"role", "db", hosts.TagSourceFact},
			{"env", "staging", hosts.TagSourceManual},
		}},
		{"h-fact-only", []seedTag{
			{"role", "lb", hosts.TagSourceFact}, {"arch", "arm64", hosts.TagSourceFact},
		}},
		{"h-no-tags", nil},
	}

	hostIDs := make(map[string]uuid.UUID, len(hostSpecs))
	mergedByHost := make(map[string]map[string]string, len(hostSpecs))
	for _, spec := range hostSpecs {
		h := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: spec.name}
		if err := store.CreateHost(ctx, h); err != nil {
			t.Fatal(err)
		}
		hostIDs[spec.name] = h.ID

		var manual []hosts.Tag
		merged := map[string]string{}
		// Fact rows first, so a manual tag on the same key overrides the
		// merged map's own entry afterward — the same precedence the CTE
		// in QueryHosts (tags_store_sql.go) computes in SQL.
		for _, tg := range spec.tags {
			if tg.source == hosts.TagSourceFact {
				insertRawTag(t, q, org, h.ID, tg.key, tg.value, hosts.TagSourceFact)
				merged[tg.key] = tg.value
			}
		}
		for _, tg := range spec.tags {
			if tg.source == hosts.TagSourceManual {
				manual = append(manual, hosts.Tag{Key: tg.key, Value: tg.value, Source: hosts.TagSourceManual})
				merged[tg.key] = tg.value
			}
		}
		if len(manual) > 0 {
			if err := store.ReplaceManualTags(ctx, org, h.ID, manual); err != nil {
				t.Fatal(err)
			}
		}
		mergedByHost[spec.name] = merged
	}

	selectors := []string{
		`role = db`,
		`role != db`,
		`role in (db, web, lb)`,
		`env exists`,
		`env exists and role = db`,
		`zone in (fra, hel)`,
		`role = cache and env = staging`,
		`arch exists and role != lb`,
	}

	for _, expr := range selectors {
		sel, err := selector.Parse(expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		sqlMatches, err := store.QueryHosts(ctx, org, sel)
		if err != nil {
			t.Fatalf("%s: QueryHosts: %v", expr, err)
		}
		sqlMatchedNames := map[string]bool{}
		for _, h := range sqlMatches {
			sqlMatchedNames[h.Name] = true
		}

		for _, spec := range hostSpecs {
			want := sel.Matches(mergedByHost[spec.name])
			got := sqlMatchedNames[spec.name]
			if got != want {
				t.Errorf("selector %q, host %s: SQL matched=%v, Go Matches=%v (merged tags %+v)",
					expr, spec.name, got, want, mergedByHost[spec.name])
			}
		}
	}
}

// TestHostTagsAreIsolatedPerOrgByRLS mirrors TestHostsAreIsolatedPerOrgByRLS
// (store_sql_test.go): it exercises host_tags' own org_isolation RLS policy
// directly via d.WithOrg, the one test in this suite that would fail if
// that policy were ever weakened to USING (true).
func TestHostTagsAreIsolatedPerOrgByRLS(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(q, d.Pool())
	orgA := seedOrg(t, q, "tags-rls-org-a")
	orgB := seedOrg(t, q, "tags-rls-org-b")
	zoneA := seedZone(t, store, orgA, "fra")
	zoneB := seedZone(t, store, orgB, "fra")

	hA := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: orgA, ZoneID: zoneA.ID, Name: "a-host"}
	hB := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: orgB, ZoneID: zoneB.ID, Name: "b-host"}
	if err := store.CreateHost(ctx, hA); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateHost(ctx, hB); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceManualTags(ctx, orgA, hA.ID, []hosts.Tag{{Key: "role", Value: "db", Source: hosts.TagSourceManual}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceManualTags(ctx, orgB, hB.ID, []hosts.Tag{{Key: "role", Value: "web", Source: hosts.TagSourceManual}}); err != nil {
		t.Fatal(err)
	}

	var seen []uuid.UUID
	err := d.WithOrg(ctx, orgA, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT org_id FROM host_tags`)
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

// TestSQLStoreQueryHostsUnknownKeyMatchesNothing is a small sanity check
// that a selector naming a key no host carries returns no hosts rather than
// erroring — proving the EXISTS sub-query, not just the happy path, behaves
// against an empty result set.
func TestSQLStoreQueryHostsUnknownKeyMatchesNothing(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(q, d.Pool())
	org := seedOrg(t, q, "tags-unknown-key-org")
	zone := seedZone(t, store, org, "fra")
	h := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "web-01"}
	if err := store.CreateHost(ctx, h); err != nil {
		t.Fatal(err)
	}

	sel, err := selector.Parse(`nope exists`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.QueryHosts(ctx, org, sel)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("QueryHosts = %+v, want none", got)
	}
}
