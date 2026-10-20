package shared_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/shared"
)

func seedOrg(t *testing.T, q *gen.Queries, slug string) uuid.UUID {
	t.Helper()
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

// seedOtherRepo inserts an ordinary, non-projection git_repos row directly —
// standing in for a repository an organization created on its own, the raw
// material decision 7's collision check must see and a dependant must be
// able to point at.
func seedOtherRepo(t *testing.T, d *db.DB, orgID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(db.WithOrgScope(ctx, orgID),
		`INSERT INTO git_repos (id, org_id, name, url, branch, subpath) VALUES ($1, $2, $3, 'https://example.com/other.git', 'main', '')`,
		id, orgID, name); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedPlaybook inserts a playbook referencing repoID — the one dependant
// table whose own foreign key to git_repos has no ON DELETE clause at all
// (migration 00018, i.e. NO ACTION), the only one of the three that would
// ever raise the violation DeleteProjectedRepo used to rely on alone.
func seedPlaybook(t *testing.T, d *db.DB, orgID, repoID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(db.WithOrgScope(ctx, orgID),
		`INSERT INTO playbooks (id, org_id, repo_id, path, source) VALUES ($1, $2, $3, 'site.yml', 'repo')`,
		id, orgID, repoID); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedJobTemplateForPlaybook inserts a job template naming playbookID —
// the customer configuration that makes deleting that playbook (and so,
// transitively, the repository it was discovered from) refused rather
// than silently taking the template's own source out from under it.
func seedJobTemplateForPlaybook(t *testing.T, d *db.DB, orgID, playbookID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(db.WithOrgScope(ctx, orgID),
		`INSERT INTO job_templates (id, org_id, name, playbook_id, scope) VALUES ($1, $2, 'deploy', $3, '{}'::jsonb)`,
		id, orgID, playbookID); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedJobTrigger inserts a repo_sync webhook trigger referencing repoID —
// migration 00026's own ON DELETE CASCADE off git_repos, which means a
// DELETE against repoID would succeed and silently take this row with it
// rather than ever raising a foreign-key violation. The columns beyond
// what the CHECK constraints and NOT NULLs require are arbitrary; nothing
// about this test exercises the trigger itself, only its mere existence.
func seedJobTrigger(t *testing.T, d *db.DB, orgID, repoID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(db.WithOrgScope(ctx, orgID),
		`INSERT INTO job_triggers (id, org_id, kind, repo_id, name, preset, auth, signature_header, secret, rate_limit)
		 VALUES ($1, $2, 'repo_sync', $3, 'push', 'pulsix', 'hmac', 'X-Signature', $4, '{}'::jsonb)`,
		id, orgID, repoID, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedVaultCredentialBinding inserts a labelled vault credential binding for
// repoID — migration 00059's own ON DELETE CASCADE off git_repos, the same
// silent-loss shape seedJobTrigger exercises for job_triggers.
func seedVaultCredentialBinding(t *testing.T, d *db.DB, orgID, repoID uuid.UUID) {
	t.Helper()
	credID := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(db.WithOrgScope(ctx, orgID),
		`INSERT INTO credentials (id, org_id, name, kind, secret) VALUES ($1, $2, 'vault-password', 'git_key', $3)`,
		credID, orgID, []byte("sealed")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(db.WithOrgScope(ctx, orgID),
		`INSERT INTO git_repo_vault_credentials (org_id, repo_id, credential_id, label) VALUES ($1, $2, $3, 'prod')`,
		orgID, repoID, credID); err != nil {
		t.Fatal(err)
	}
}

// projectionRow reads one organization's projection of def directly —
// store_sql.go's own methods are what is under test here, so assertions
// read the table underneath them rather than through the store.
func projectionRow(t *testing.T, d *db.DB, orgID, defID uuid.UUID) (id uuid.UUID, name, url, branch, subpath string) {
	t.Helper()
	err := d.Pool().QueryRow(ctx,
		`SELECT id, name, url, branch, subpath FROM git_repos WHERE org_id = $1 AND shared_repo_id = $2`,
		orgID, defID).Scan(&id, &name, &url, &branch, &subpath)
	if err != nil {
		t.Fatalf("read projection for org %s: %v", orgID, err)
	}
	return
}

func countProjections(t *testing.T, d *db.DB, defID uuid.UUID) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM git_repos WHERE shared_repo_id = $1`, defID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func projectionExists(t *testing.T, d *db.DB, orgID, defID uuid.UUID) bool {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM git_repos WHERE org_id = $1 AND shared_repo_id = $2`, orgID, defID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func containsString(haystack []string, want string) bool {
	for _, s := range haystack {
		if s == want {
			return true
		}
	}
	return false
}

func TestSQLStoreCreateWithProjectionsProjectsIntoEveryOrg(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	orgA := seedOrg(t, q, "shared-create-a")
	orgB := seedOrg(t, q, "shared-create-b")

	def, err := store.CreateWithProjections(ctx, shared.RepoDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "infra", URL: "https://git.example.com/infra.git",
		Branch: "main", Subpath: "ansible",
	})
	if err != nil {
		t.Fatalf("CreateWithProjections() error = %v, want nil", err)
	}

	for _, org := range []uuid.UUID{orgA, orgB} {
		_, name, url, branch, subpath := projectionRow(t, d, org, def.ID)
		if name != "infra" || url != def.URL || branch != "main" || subpath != "ansible" {
			t.Fatalf("org %s projection = (%q,%q,%q,%q), want the definition's own values", org, name, url, branch, subpath)
		}
	}
	if n := countProjections(t, d, def.ID); n != 2 {
		t.Fatalf("countProjections() = %d, want 2", n)
	}
}

// TestSQLStoreCreateWithProjectionsReadsTheOrganizationListLive proves the
// fix for the round-trip gap the review caught: CreateRepo used to read the
// organization list in a SEPARATE call from CreateWithProjections, leaving
// a window where an organization created in between received no
// projection. The organization list is now read from inside
// CreateWithProjections' own transaction — this test creates the
// organization immediately before the call, with nothing in between, and
// asserts it is included, the way any caller that queries organizations and
// then writes projections in two round trips could otherwise miss it.
func TestSQLStoreCreateWithProjectionsReadsTheOrganizationListLive(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	org := seedOrg(t, q, "shared-create-live")

	def, err := store.CreateWithProjections(ctx, shared.RepoDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "infra", URL: "https://git.example.com/infra.git", Branch: "main",
	})
	if err != nil {
		t.Fatalf("CreateWithProjections() error = %v, want nil", err)
	}
	if !projectionExists(t, d, org, def.ID) {
		t.Fatal("organization seeded before the call has no projection — CreateWithProjections must read the organization list itself, not rely on a stale list supplied earlier")
	}
}

func TestSQLStoreCreateWithProjectionsRefusesADuplicateDefinitionName(t *testing.T) {
	d := testdb.Open(t)
	store := shared.NewSQLStore(d)

	if _, err := store.CreateWithProjections(ctx, shared.RepoDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "dup-definition-name", URL: "https://git.example.com/a.git", Branch: "main",
	}); err != nil {
		t.Fatalf("seed CreateWithProjections: %v", err)
	}

	_, err := store.CreateWithProjections(ctx, shared.RepoDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "dup-definition-name", URL: "https://git.example.com/b.git", Branch: "main",
	})
	if !errors.Is(err, shared.ErrNameTaken) {
		t.Fatalf("CreateWithProjections() error = %v, want ErrNameTaken", err)
	}
}

func TestSQLStoreExistingNamesExcludesOwnProjections(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	orgA := seedOrg(t, q, "shared-existing-a")
	seedOrg(t, q, "shared-existing-b") // a second organization that also receives a projection; not asserted on individually
	orgC := seedOrg(t, q, "shared-existing-c")
	seedOtherRepo(t, d, orgC, "unrelated")

	def, err := store.CreateWithProjections(ctx, shared.RepoDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "infra", URL: "https://git.example.com/infra.git", Branch: "main",
	})
	if err != nil {
		t.Fatalf("seed CreateWithProjections: %v", err)
	}

	all, err := store.ExistingNames(ctx, uuid.Nil)
	if err != nil {
		t.Fatalf("ExistingNames(uuid.Nil) error = %v", err)
	}
	if !containsOrgName(all, orgA, "infra") || !containsOrgName(all, orgC, "unrelated") {
		t.Fatalf("ExistingNames(uuid.Nil) = %v, want every organization's own row, projections included", all)
	}

	excluded, err := store.ExistingNames(ctx, def.ID)
	if err != nil {
		t.Fatalf("ExistingNames(def.ID) error = %v", err)
	}
	if containsOrgName(excluded, orgA, "infra") {
		t.Fatalf("ExistingNames(def.ID) = %v, want def's own projections excluded", excluded)
	}
	if !containsOrgName(excluded, orgC, "unrelated") {
		t.Fatalf("ExistingNames(def.ID) = %v, want the unrelated row still present", excluded)
	}
}

func containsOrgName(rows []shared.OrgName, org uuid.UUID, name string) bool {
	for _, r := range rows {
		if r.OrgID == org && r.Name == name {
			return true
		}
	}
	return false
}

// TestUpdatePropagatesToEveryProjectionOrNone is Review Focus 2's own test:
// three organizations hold projections of one definition, and the second
// organization's own write fails (a real UNIQUE (org_id, name) violation,
// engineered the same deliberate way
// TestStoreReplaceRepoVaultIdentitiesRollsBackOnPartialFailure engineers a
// duplicate-label violation in internal/repos/vault_test.go — artificial as
// a cause, but a genuine constraint failure once UpdateProjectedRepo is
// asked to rename org2's projection to a name org2 already has on an
// unrelated repository of its own).
//
// Before UpdateAndPropagate wrapped its loop in one transaction, this test
// failed exactly the way the brief predicts: org1 (processed first, inside
// no transaction at all — each UpdateProjectedRepo call auto-committed on
// its own) ended up on the NEW branch while org2 and org3 stayed on the
// old one, a "one new, two old" state no caller of UpdateRepo could
// distinguish from success without re-reading every organization. Wrapped
// in a transaction (withTx), the same engineered failure instead leaves
// every organization — org1 included — on the value it had before this
// call.
func TestUpdatePropagatesToEveryProjectionOrNone(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	org1 := seedOrg(t, q, "propagate-org-1")
	org2 := seedOrg(t, q, "propagate-org-2")
	org3 := seedOrg(t, q, "propagate-org-3")

	def, err := store.CreateWithProjections(ctx, shared.RepoDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "infra", URL: "https://git.example.com/infra.git", Branch: "main",
	})
	if err != nil {
		t.Fatalf("seed CreateWithProjections: %v", err)
	}

	// org2 already has its OWN, unrelated repository named "infra-v2" — the
	// exact name this call is about to rename the definition to. Renaming
	// org2's projection to that name violates git_repos' own
	// UNIQUE (org_id, name), a real failure org1 and org3 do not share.
	seedOtherRepo(t, d, org2, "infra-v2")

	_, err = store.UpdateAndPropagate(ctx, shared.RepoDefinition{
		ID: def.ID, Name: "infra-v2", URL: def.URL, Branch: "develop", Subpath: def.Subpath,
	})
	if err == nil {
		t.Fatal("UpdateAndPropagate() error = nil, want org2's collision to fail the whole call")
	}
	if !errors.Is(err, shared.ErrNameTaken) {
		t.Fatalf("UpdateAndPropagate() error = %v, want errors.Is(_, ErrNameTaken)", err)
	}

	// The definition's own row must not have moved either — UpdateSharedRepo
	// ran in the SAME transaction as the propagation loop.
	got, err := store.GetDefinition(ctx, def.ID)
	if err != nil {
		t.Fatalf("GetDefinition() error = %v", err)
	}
	if got.Name != "infra" || got.Branch != "main" {
		t.Fatalf("GetDefinition() = %+v, want the PREVIOUS name %q and branch %q untouched", got, "infra", "main")
	}

	for _, org := range []uuid.UUID{org1, org2, org3} {
		_, name, _, branch, _ := projectionRow(t, d, org, def.ID)
		if name != "infra" || branch != "main" {
			t.Fatalf("organization %s projection = (name=%q, branch=%q), want the PREVIOUS values (name=%q, branch=%q) — a failed propagation must never leave some organizations on the new value and others on the old one",
				org, name, branch, "infra", "main")
		}
	}
	if n := countProjections(t, d, def.ID); n != 3 {
		t.Fatalf("countProjections() = %d, want 3 (no row lost or duplicated by the failed call)", n)
	}
}

// TestUpdateSkipsACascadedOrganization is Review Focus 1's own test: an
// organization holding a projection is deleted before UpdateAndPropagate
// ever runs. Its projection cascades away with it through git_repos.org_id's
// own ON DELETE CASCADE foreign key (migration 00018) — by the time
// UpdateAndPropagate's ListProjectedRepos reads the projection list, the
// dead organization is simply not in it, so the call must succeed and the
// survivors must end up correct.
func TestUpdateSkipsACascadedOrganization(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	org1 := seedOrg(t, q, "cascade-org-1")
	org2 := seedOrg(t, q, "cascade-org-2")
	org3 := seedOrg(t, q, "cascade-org-3")

	def, err := store.CreateWithProjections(ctx, shared.RepoDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "infra", URL: "https://git.example.com/infra.git", Branch: "main",
	})
	if err != nil {
		t.Fatalf("seed CreateWithProjections: %v", err)
	}

	if _, err := d.Pool().Exec(ctx, `DELETE FROM organizations WHERE id = $1`, org2); err != nil {
		t.Fatalf("delete organization org2: %v", err)
	}

	updated, err := store.UpdateAndPropagate(ctx, shared.RepoDefinition{
		ID: def.ID, Name: "infra", URL: def.URL, Branch: "develop", Subpath: def.Subpath,
	})
	if err != nil {
		t.Fatalf("UpdateAndPropagate() error = %v, want nil — a deleted organization must not break propagation", err)
	}
	if updated.Branch != "develop" {
		t.Fatalf("UpdateAndPropagate() branch = %q, want %q", updated.Branch, "develop")
	}

	for _, org := range []uuid.UUID{org1, org3} {
		_, _, _, branch, _ := projectionRow(t, d, org, def.ID)
		if branch != "develop" {
			t.Fatalf("surviving organization %s branch = %q, want %q", org, branch, "develop")
		}
	}
	if projectionExists(t, d, org2, def.ID) {
		t.Fatal("org2's projection exists after UpdateAndPropagate, want it to stay gone — the call must not resurrect a row for the dead organization")
	}
	if n := countProjections(t, d, def.ID); n != 2 {
		t.Fatalf("countProjections() = %d, want 2 (org2's cascade-deleted row never recreated)", n)
	}
}

// --- DeleteWithProjections ---
//
// What blocks and what does not is the Critical finding's own correction:
// job_triggers is the one table whose row is counted as a blocking
// dependant (customer configuration that would otherwise silently vanish —
// its own foreign key CASCADEs, migration 00026). playbooks and
// git_repo_vault_credentials are NOT counted — a vault binding is part of
// the projection and goes with it (git_repo_vault_credentials' own
// CASCADE, migration 00059), and a playbook is sync-discovered rather than
// customer-configured, so DeleteWithProjections clears it explicitly
// instead of either blocking on it or leaving it to a foreign key that
// (unlike the other two) is NO ACTION and would otherwise refuse the
// delete permanently. See DeleteWithProjections' own doc comment
// (store_sql.go) for the full inventory.

func TestDeleteWithProjectionsRefusesWhileAJobTriggerExists(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	org := seedOrg(t, q, "shared-delete-trigger")

	def, err := store.CreateWithProjections(ctx, shared.RepoDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "infra", URL: "https://git.example.com/infra.git", Branch: "main",
	})
	if err != nil {
		t.Fatalf("seed CreateWithProjections: %v", err)
	}
	projID, _, _, _, _ := projectionRow(t, d, org, def.ID)
	seedJobTrigger(t, d, org, projID)

	err = store.DeleteWithProjections(ctx, def.ID)
	var inUse *shared.InUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("DeleteWithProjections() error = %v, want *InUseError — job_triggers' own ON DELETE CASCADE must not let this through silently", err)
	}
	if inUse.OrgID != org {
		t.Fatalf("InUseError.OrgID = %v, want %v", inUse.OrgID, org)
	}
	if !containsString(inUse.Reasons, "a job trigger") {
		t.Fatalf("InUseError.Reasons = %v, want to include %q", inUse.Reasons, "a job trigger")
	}
	if _, gerr := store.GetDefinition(ctx, def.ID); gerr != nil {
		t.Fatalf("GetDefinition() after a refused delete = %v, want the definition still to exist", gerr)
	}
	if !projectionExists(t, d, org, def.ID) {
		t.Fatal("projection gone after a refused delete, want it rolled back along with the definition")
	}
}

// TestDeleteWithProjectionsRefusesWhileAJobTemplateUsesAPlaybook covers the
// one hop beyond git_repos the review's explicit inventory did not name
// directly: DeleteWithProjections clears a projection's discovered
// playbooks itself (they are not customer configuration), but if a job
// template still names one of them, job_templates' own (org_id,
// playbook_id) foreign key (migration 00018, NO ACTION) refuses THAT
// delete, and a job template choosing to run this playbook is customer
// configuration — the store maps the failure back to *InUseError rather
// than letting it surface as an unmapped database error.
func TestDeleteWithProjectionsRefusesWhileAJobTemplateUsesAPlaybook(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	org := seedOrg(t, q, "shared-delete-template-playbook")

	def, err := store.CreateWithProjections(ctx, shared.RepoDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "infra", URL: "https://git.example.com/infra.git", Branch: "main",
	})
	if err != nil {
		t.Fatalf("seed CreateWithProjections: %v", err)
	}
	projID, _, _, _, _ := projectionRow(t, d, org, def.ID)
	playbookID := seedPlaybook(t, d, org, projID)
	seedJobTemplateForPlaybook(t, d, org, playbookID)

	err = store.DeleteWithProjections(ctx, def.ID)
	var inUse *shared.InUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("DeleteWithProjections() error = %v, want *InUseError — a job template still using the playbook must block the delete", err)
	}
	if inUse.OrgID != org {
		t.Fatalf("InUseError.OrgID = %v, want %v", inUse.OrgID, org)
	}
	if !containsString(inUse.Reasons, "a job template") {
		t.Fatalf("InUseError.Reasons = %v, want to include %q", inUse.Reasons, "a job template")
	}
	if !projectionExists(t, d, org, def.ID) {
		t.Fatal("projection gone after a refused delete, want it rolled back along with the definition")
	}
}

// TestDeleteWithProjectionsSucceedsWithVaultIdentitiesAndDiscoveredPlaybooks
// is the test the review asked for specifically to catch the naive version
// of this fix: a projection with a vault credential binding AND a
// sync-discovered playbook — neither referenced by anything else — must
// still delete cleanly. A version of this fix that simply added
// job_triggers to the old three-table dependant count (rather than
// dropping playbooks and vault credentials from it and explicitly clearing
// playbooks) would fail this test, either by refusing the delete outright
// or by leaving the playbook behind to block git_repos' own DELETE through
// playbooks' NO ACTION foreign key.
func TestDeleteWithProjectionsSucceedsWithVaultIdentitiesAndDiscoveredPlaybooks(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	org := seedOrg(t, q, "shared-delete-clean-with-extras")

	def, err := store.CreateWithProjections(ctx, shared.RepoDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "infra", URL: "https://git.example.com/infra.git", Branch: "main",
	})
	if err != nil {
		t.Fatalf("seed CreateWithProjections: %v", err)
	}
	projID, _, _, _, _ := projectionRow(t, d, org, def.ID)
	seedPlaybook(t, d, org, projID)
	seedVaultCredentialBinding(t, d, org, projID)

	if err := store.DeleteWithProjections(ctx, def.ID); err != nil {
		t.Fatalf("DeleteWithProjections() error = %v, want nil — vault identities and discovered playbooks must not block a definition delete", err)
	}
	if _, gerr := store.GetDefinition(ctx, def.ID); !errors.Is(gerr, shared.ErrNotFound) {
		t.Fatalf("GetDefinition() after delete = %v, want ErrNotFound", gerr)
	}
	if n := countProjections(t, d, def.ID); n != 0 {
		t.Fatalf("countProjections() = %d, want 0", n)
	}
}

func TestDeleteWithProjectionsRemovesDefinitionAndProjectionsWhenUnused(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	seedOrg(t, q, "shared-delete-clean-a")
	seedOrg(t, q, "shared-delete-clean-b")

	def, err := store.CreateWithProjections(ctx, shared.RepoDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "infra", URL: "https://git.example.com/infra.git", Branch: "main",
	})
	if err != nil {
		t.Fatalf("seed CreateWithProjections: %v", err)
	}
	if n := countProjections(t, d, def.ID); n != 2 {
		t.Fatalf("countProjections() before delete = %d, want 2", n)
	}

	if err := store.DeleteWithProjections(ctx, def.ID); err != nil {
		t.Fatalf("DeleteWithProjections() error = %v, want nil", err)
	}

	if _, gerr := store.GetDefinition(ctx, def.ID); !errors.Is(gerr, shared.ErrNotFound) {
		t.Fatalf("GetDefinition() after delete = %v, want ErrNotFound", gerr)
	}
	if n := countProjections(t, d, def.ID); n != 0 {
		t.Fatalf("countProjections() = %d, want 0 — the projections must go with the definition", n)
	}
}

func TestSQLStoreProjectIntoANewOrganization(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)

	def, err := store.CreateWithProjections(ctx, shared.RepoDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "infra", URL: "https://git.example.com/infra.git", Branch: "main", Subpath: "ansible",
	})
	if err != nil {
		t.Fatalf("seed CreateWithProjections: %v", err)
	}

	newOrg := seedOrg(t, q, "shared-project-into-new")
	if err := store.ProjectInto(ctx, def, newOrg); err != nil {
		t.Fatalf("ProjectInto() error = %v, want nil", err)
	}

	_, name, url, branch, subpath := projectionRow(t, d, newOrg, def.ID)
	if name != "infra" || url != def.URL || branch != "main" || subpath != "ansible" {
		t.Fatalf("new organization's projection = (%q,%q,%q,%q), want the definition's own values", name, url, branch, subpath)
	}
}

func TestSQLStoreProjectIntoRefusesACollidingName(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)

	def, err := store.CreateWithProjections(ctx, shared.RepoDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "infra", URL: "https://git.example.com/infra.git", Branch: "main",
	})
	if err != nil {
		t.Fatalf("seed CreateWithProjections: %v", err)
	}
	org := seedOrg(t, q, "shared-project-collide")
	seedOtherRepo(t, d, org, "infra")

	if err := store.ProjectInto(ctx, def, org); !errors.Is(err, shared.ErrNameTaken) {
		t.Fatalf("ProjectInto() error = %v, want ErrNameTaken", err)
	}
}
