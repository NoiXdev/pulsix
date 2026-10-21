package shared_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

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

// seedPlaybook inserts a playbook referencing repoID — a registered
// playbook (playbooks.Service.CreateRepo's own path, POST
// /orgs/{org}/playbooks under playbooks.write) is customer-owned
// configuration, not something sync discovers, so it blocks a definition
// delete exactly like a job trigger does.
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

// countVaultCredentialBindings reads git_repo_vault_credentials directly —
// the table the Critical finding's regression test must prove actually
// loses its rows on a successful delete, not merely that the call returned
// nil.
func countVaultCredentialBindings(t *testing.T, d *db.DB, repoID uuid.UUID) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM git_repo_vault_credentials WHERE repo_id = $1`, repoID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
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
// What blocks is job_triggers and playbooks, both checked explicitly in
// the same pre-write loop. git_repo_vault_credentials is the one NOT
// counted — a vault binding is part of the projection and goes with it
// (its own ON DELETE CASCADE, migration 00059). Playbooks were, for one
// review round, wrongly treated as sync-discovered and therefore safe to
// delete explicitly; they are registered by an organization itself
// (playbooks.Service.CreateRepo, POST /orgs/{org}/playbooks under
// playbooks.write) and block exactly like job triggers do — see
// DeleteWithProjections' own doc comment (store_sql.go) for the corrected,
// verified inventory.

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

// TestDeleteWithProjectionsRefusesWhileAPlaybookExists is the test that
// replaces the review's second, false premise: a playbook is registered
// deliberately (playbooks.Service.CreateRepo), never written by sync, so
// it is customer-owned configuration exactly like a job trigger and must
// block a definition delete rather than being cleared silently.
func TestDeleteWithProjectionsRefusesWhileAPlaybookExists(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	org := seedOrg(t, q, "shared-delete-playbook")

	def, err := store.CreateWithProjections(ctx, shared.RepoDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "infra", URL: "https://git.example.com/infra.git", Branch: "main",
	})
	if err != nil {
		t.Fatalf("seed CreateWithProjections: %v", err)
	}
	projID, _, _, _, _ := projectionRow(t, d, org, def.ID)
	seedPlaybook(t, d, org, projID)

	err = store.DeleteWithProjections(ctx, def.ID)
	var inUse *shared.InUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("DeleteWithProjections() error = %v, want *InUseError — a registered playbook is customer configuration and must not be cleared silently", err)
	}
	if inUse.OrgID != org {
		t.Fatalf("InUseError.OrgID = %v, want %v", inUse.OrgID, org)
	}
	if !containsString(inUse.Reasons, "a playbook") {
		t.Fatalf("InUseError.Reasons = %v, want to include %q", inUse.Reasons, "a playbook")
	}
	if _, gerr := store.GetDefinition(ctx, def.ID); gerr != nil {
		t.Fatalf("GetDefinition() after a refused delete = %v, want the definition still to exist", gerr)
	}
	if !projectionExists(t, d, org, def.ID) {
		t.Fatal("projection gone after a refused delete, want it rolled back along with the definition")
	}
}

// TestDeleteWithProjectionsSucceedsWithAVaultCredentialBinding is the
// regression test for the Critical finding, reduced to vault credentials
// alone now that playbooks block: a vault password binding, referenced by
// nothing else, must not prevent a definition delete, and — the assertion
// the review's own first version of this test was missing — the binding
// itself must actually be gone afterwards, not merely have let the call
// return nil. "It returned nil" is exactly what let the false playbooks
// premise through undetected the first time.
func TestDeleteWithProjectionsSucceedsWithAVaultCredentialBinding(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	org := seedOrg(t, q, "shared-delete-clean-with-vault")

	def, err := store.CreateWithProjections(ctx, shared.RepoDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "infra", URL: "https://git.example.com/infra.git", Branch: "main",
	})
	if err != nil {
		t.Fatalf("seed CreateWithProjections: %v", err)
	}
	projID, _, _, _, _ := projectionRow(t, d, org, def.ID)
	seedVaultCredentialBinding(t, d, org, projID)
	if n := countVaultCredentialBindings(t, d, projID); n != 1 {
		t.Fatalf("countVaultCredentialBindings() before delete = %d, want 1", n)
	}

	if err := store.DeleteWithProjections(ctx, def.ID); err != nil {
		t.Fatalf("DeleteWithProjections() error = %v, want nil — a vault credential binding must not block a definition delete", err)
	}
	if _, gerr := store.GetDefinition(ctx, def.ID); !errors.Is(gerr, shared.ErrNotFound) {
		t.Fatalf("GetDefinition() after delete = %v, want ErrNotFound", gerr)
	}
	if n := countProjections(t, d, def.ID); n != 0 {
		t.Fatalf("countProjections() = %d, want 0", n)
	}
	if n := countVaultCredentialBindings(t, d, projID); n != 0 {
		t.Fatalf("countVaultCredentialBindings() after delete = %d, want 0 — the binding must actually be gone, not just the call having returned nil", n)
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

// --- job templates (Task 6) ---
//
// Mirrors the repository fixtures above, table for table: seedSharedRepoDef
// gives every job-template test a real, already-projected repository
// definition to reference (shared_repo_id is NOT NULL on
// shared_job_templates — migration 00061's own reason), and the delete
// tests below seed exactly one dependant each — schedules, then job
// triggers — the shape the brief calls out as what actually catches a
// too-short block list: a test seeding both at once would still pass a
// store that only checks one of them.

// seedSharedRepoDef creates a repository definition and projects it into
// every organization that exists so far (shared.SQLStore.CreateWithProjections,
// the real path) — the raw material a job template definition's
// SharedRepoID must reference.
func seedSharedRepoDef(t *testing.T, d *db.DB, name string) shared.RepoDefinition {
	t.Helper()
	def, err := shared.NewSQLStore(d).CreateWithProjections(ctx, shared.RepoDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: name, URL: "https://git.example.com/" + name + ".git", Branch: "main",
	})
	if err != nil {
		t.Fatalf("seed shared repo definition %q: %v", name, err)
	}
	return def
}

// seedOtherTemplate inserts an ordinary, non-projection job_templates row
// directly — the raw material decision 7's collision check must see and a
// dependant must be able to point at. playbook_id is left NULL (migration
// 00061 made it nullable); nothing here exercises resolution.
func seedOtherTemplate(t *testing.T, d *db.DB, orgID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(db.WithOrgScope(ctx, orgID),
		`INSERT INTO job_templates (id, org_id, name, scope) VALUES ($1, $2, $3, '{}'::jsonb)`,
		id, orgID, name); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedSchedule inserts a schedule referencing templateID — migration
// 00025's own ON DELETE CASCADE off job_templates, the silent-loss shape
// DeleteTemplateWithProjections' JobTemplateHasSchedule check exists to
// catch.
func seedSchedule(t *testing.T, d *db.DB, orgID, templateID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(db.WithOrgScope(ctx, orgID),
		`INSERT INTO schedules (id, org_id, template_id, name, mode, cron, timezone, guards)
		 VALUES ($1, $2, $3, 'nightly', 'cron', '0 2 * * *', 'UTC', '{}'::jsonb)`,
		id, orgID, templateID); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedJobTriggerForTemplate inserts a template_run webhook trigger
// referencing templateID — migration 00026's own ON DELETE CASCADE off
// job_templates, the same silent-loss shape seedJobTrigger exercises for
// git_repos.
func seedJobTriggerForTemplate(t *testing.T, d *db.DB, orgID, templateID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(db.WithOrgScope(ctx, orgID),
		`INSERT INTO job_triggers (id, org_id, kind, template_id, name, preset, auth, signature_header, secret, rate_limit)
		 VALUES ($1, $2, 'template_run', $3, 'run', 'pulsix', 'hmac', 'X-Signature', $4, '{}'::jsonb)`,
		id, orgID, templateID, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedVaultCredentialBindingForTemplate inserts a labelled vault credential
// binding for templateID — migration 00059's own ON DELETE CASCADE off
// job_templates, left to cascade ON PURPOSE (the brief's own instruction:
// a vault binding is part of the projection, not a dependant on it).
func seedVaultCredentialBindingForTemplate(t *testing.T, d *db.DB, orgID, templateID uuid.UUID) {
	t.Helper()
	credID := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(db.WithOrgScope(ctx, orgID),
		`INSERT INTO credentials (id, org_id, name, kind, secret) VALUES ($1, $2, 'vault-password', 'git_key', $3)`,
		credID, orgID, []byte("sealed")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(db.WithOrgScope(ctx, orgID),
		`INSERT INTO job_template_vault_credentials (org_id, template_id, credential_id, label) VALUES ($1, $2, $3, 'prod')`,
		orgID, templateID, credID); err != nil {
		t.Fatal(err)
	}
}

// templateProjectionRow reads one organization's projection of def directly.
func templateProjectionRow(t *testing.T, d *db.DB, orgID, defID uuid.UUID) (id uuid.UUID, name, limit string, playbookID uuid.UUID) {
	t.Helper()
	var pb pgtype.UUID
	err := d.Pool().QueryRow(ctx,
		`SELECT id, name, "limit", playbook_id FROM job_templates WHERE org_id = $1 AND shared_template_id = $2`,
		orgID, defID).Scan(&id, &name, &limit, &pb)
	if err != nil {
		t.Fatalf("read template projection for org %s: %v", orgID, err)
	}
	if p := db.UUIDPtr(pb); p != nil {
		playbookID = *p
	}
	return
}

func countTemplateProjections(t *testing.T, d *db.DB, defID uuid.UUID) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM job_templates WHERE shared_template_id = $1`, defID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func templateProjectionExists(t *testing.T, d *db.DB, orgID, defID uuid.UUID) bool {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM job_templates WHERE org_id = $1 AND shared_template_id = $2`, orgID, defID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func countTemplateVaultCredentialBindings(t *testing.T, d *db.DB, templateID uuid.UUID) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM job_template_vault_credentials WHERE template_id = $1`, templateID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSQLStoreCreateTemplateWithProjectionsProjectsIntoEveryOrg(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	orgA := seedOrg(t, q, "shared-tpl-create-a")
	orgB := seedOrg(t, q, "shared-tpl-create-b")
	repoDef := seedSharedRepoDef(t, d, "infra-for-tpl-create")

	def, err := store.CreateTemplateWithProjections(ctx, shared.TemplateDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "deploy-site", SharedRepoID: repoDef.ID, PlaybookPath: "site.yml", Limit: "web*",
	})
	if err != nil {
		t.Fatalf("CreateTemplateWithProjections() error = %v, want nil", err)
	}

	for _, org := range []uuid.UUID{orgA, orgB} {
		_, name, limit, playbookID := templateProjectionRow(t, d, org, def.ID)
		if name != "deploy-site" || limit != "web*" {
			t.Fatalf("org %s projection = (name=%q, limit=%q), want the definition's own values", org, name, limit)
		}
		// Neither organization has synced a playbook at "site.yml" yet, so
		// playbook_id must resolve to NULL — migration 00061's own reason
		// the column had to become nullable in the first place.
		if playbookID != uuid.Nil {
			t.Fatalf("org %s projection playbook_id = %v, want uuid.Nil (no matching playbook synced yet)", org, playbookID)
		}
	}
	if n := countTemplateProjections(t, d, def.ID); n != 2 {
		t.Fatalf("countTemplateProjections() = %d, want 2", n)
	}
}

// TestSQLStoreCreateTemplateWithProjectionsResolvesPlaybookIDWhenSynced
// proves the per-organization resolution actually joins: an organization
// whose own projection of the shared repository already has a matching
// playbooks row gets playbook_id populated; one that doesn't stays NULL.
func TestSQLStoreCreateTemplateWithProjectionsResolvesPlaybookIDWhenSynced(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	orgSynced := seedOrg(t, q, "shared-tpl-resolve-synced")
	orgUnsynced := seedOrg(t, q, "shared-tpl-resolve-unsynced")
	repoDef := seedSharedRepoDef(t, d, "infra-for-tpl-resolve")

	// orgSynced's own projection of the shared repository already has a
	// playbook registered at the path the template definition is about to
	// use — standing in for playbooks.Service.CreateRepo having run after
	// a real sync.
	projectedRepoID, _, _, _, _ := projectionRow(t, d, orgSynced, repoDef.ID)
	wantPlaybook := seedPlaybook(t, d, orgSynced, projectedRepoID)

	def, err := store.CreateTemplateWithProjections(ctx, shared.TemplateDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "deploy-site-2", SharedRepoID: repoDef.ID, PlaybookPath: "site.yml",
	})
	if err != nil {
		t.Fatalf("CreateTemplateWithProjections() error = %v, want nil", err)
	}

	_, _, _, gotPlaybook := templateProjectionRow(t, d, orgSynced, def.ID)
	if gotPlaybook != wantPlaybook {
		t.Fatalf("orgSynced projection playbook_id = %v, want %v (the playbook seedPlaybook registered)", gotPlaybook, wantPlaybook)
	}
	_, _, _, gotUnsynced := templateProjectionRow(t, d, orgUnsynced, def.ID)
	if gotUnsynced != uuid.Nil {
		t.Fatalf("orgUnsynced projection playbook_id = %v, want uuid.Nil", gotUnsynced)
	}
}

// TestResolveTemplatePlaybooksForNewPathResolvesAnOrganizationThatSyncsLate
// is the organization-side half of playbook_id resolution — the review
// finding on Task 6's first draft that
// TestSQLStoreCreateTemplateWithProjectionsResolvesPlaybookIDWhenSynced
// above never exercised: a definition created BEFORE an organization has
// registered the matching playbook (the normal order — configure
// centrally, then customers catch up) must not stay unresolved forever.
// playbooks.Service.CreateRepo calls this exact method right after storing
// a new playbook; this test calls it directly to prove the store's own
// half, the same split store_sql_test.go draws throughout.
func TestResolveTemplatePlaybooksForNewPathResolvesAnOrganizationThatSyncsLate(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	org := seedOrg(t, q, "shared-tpl-resolve-late")
	repoDef := seedSharedRepoDef(t, d, "infra-for-tpl-resolve-late")

	def, err := store.CreateTemplateWithProjections(ctx, shared.TemplateDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "deploy-site-late", SharedRepoID: repoDef.ID, PlaybookPath: "site.yml",
	})
	if err != nil {
		t.Fatalf("seed CreateTemplateWithProjections: %v", err)
	}
	_, _, _, unresolved := templateProjectionRow(t, d, org, def.ID)
	if unresolved != uuid.Nil {
		t.Fatalf("projection playbook_id before any sync = %v, want uuid.Nil", unresolved)
	}

	// The organization now registers "site.yml" against its own projection
	// of the shared repository — playbooks.Service.CreateRepo's own path,
	// standing in here for the store call it makes afterward.
	projectedRepoID, _, _, _, _ := projectionRow(t, d, org, repoDef.ID)
	newPlaybookID := seedPlaybook(t, d, org, projectedRepoID)

	if err := store.ResolveTemplatePlaybooksForNewPath(ctx, org, projectedRepoID, newPlaybookID, "site.yml"); err != nil {
		t.Fatalf("ResolveTemplatePlaybooksForNewPath() error = %v, want nil", err)
	}

	_, _, _, resolved := templateProjectionRow(t, d, org, def.ID)
	if resolved != newPlaybookID {
		t.Fatalf("projection playbook_id after late sync = %v, want %v", resolved, newPlaybookID)
	}
}

// TestResolveTemplatePlaybooksForNewPathTouchesNothingForAnUnrelatedPath
// proves the statement is scoped exactly to (org, repo, path): a playbook
// registered at a DIFFERENT path, or in a DIFFERENT organization, must not
// resolve a projection it has nothing to do with — the harmless-no-op case
// ResolveTemplatePlaybooksForNewPath's own query comment describes as "the
// common case for most playbook registrations".
func TestResolveTemplatePlaybooksForNewPathTouchesNothingForAnUnrelatedPath(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	org := seedOrg(t, q, "shared-tpl-resolve-unrelated")
	otherOrg := seedOrg(t, q, "shared-tpl-resolve-unrelated-other")
	repoDef := seedSharedRepoDef(t, d, "infra-for-tpl-resolve-unrelated")

	def, err := store.CreateTemplateWithProjections(ctx, shared.TemplateDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "deploy-site-unrelated", SharedRepoID: repoDef.ID, PlaybookPath: "site.yml",
	})
	if err != nil {
		t.Fatalf("seed CreateTemplateWithProjections: %v", err)
	}

	projectedRepoID, _, _, _, _ := projectionRow(t, d, org, repoDef.ID)
	// A playbook at a DIFFERENT path, same org and repo: must not resolve.
	wrongPathID := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(db.WithOrgScope(ctx, org),
		`INSERT INTO playbooks (id, org_id, repo_id, path, source) VALUES ($1, $2, $3, 'other.yml', 'repo')`,
		wrongPathID, org, projectedRepoID); err != nil {
		t.Fatal(err)
	}
	if err := store.ResolveTemplatePlaybooksForNewPath(ctx, org, projectedRepoID, wrongPathID, "other.yml"); err != nil {
		t.Fatalf("ResolveTemplatePlaybooksForNewPath() error = %v, want nil", err)
	}
	_, _, _, stillUnresolved := templateProjectionRow(t, d, org, def.ID)
	if stillUnresolved != uuid.Nil {
		t.Fatalf("projection playbook_id after an unrelated path's resolve = %v, want uuid.Nil still", stillUnresolved)
	}

	// The right path, but a DIFFERENT organization's own repo projection
	// and playbook: must not resolve org's own projection.
	otherProjectedRepoID, _, _, _, _ := projectionRow(t, d, otherOrg, repoDef.ID)
	otherPlaybookID := seedPlaybook(t, d, otherOrg, otherProjectedRepoID)
	if err := store.ResolveTemplatePlaybooksForNewPath(ctx, otherOrg, otherProjectedRepoID, otherPlaybookID, "site.yml"); err != nil {
		t.Fatalf("ResolveTemplatePlaybooksForNewPath() error = %v, want nil", err)
	}
	_, _, _, orgStillUnresolved := templateProjectionRow(t, d, org, def.ID)
	if orgStillUnresolved != uuid.Nil {
		t.Fatalf("org's own projection playbook_id after OTHER organization's resolve = %v, want uuid.Nil still", orgStillUnresolved)
	}
	_, _, _, otherResolved := templateProjectionRow(t, d, otherOrg, def.ID)
	if otherResolved != otherPlaybookID {
		t.Fatalf("otherOrg's own projection playbook_id = %v, want %v (its own resolve must still work)", otherResolved, otherPlaybookID)
	}
}

func TestSQLStoreCreateTemplateWithProjectionsRefusesADuplicateDefinitionName(t *testing.T) {
	d := testdb.Open(t)
	store := shared.NewSQLStore(d)
	repoDef := seedSharedRepoDef(t, d, "infra-for-tpl-dup")

	if _, err := store.CreateTemplateWithProjections(ctx, shared.TemplateDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "dup-template-name", SharedRepoID: repoDef.ID, PlaybookPath: "a.yml",
	}); err != nil {
		t.Fatalf("seed CreateTemplateWithProjections: %v", err)
	}

	_, err := store.CreateTemplateWithProjections(ctx, shared.TemplateDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "dup-template-name", SharedRepoID: repoDef.ID, PlaybookPath: "b.yml",
	})
	if !errors.Is(err, shared.ErrNameTaken) {
		t.Fatalf("CreateTemplateWithProjections() error = %v, want ErrNameTaken", err)
	}
}

func TestSQLStoreCreateTemplateWithProjectionsRefusesAMissingRepoDefinition(t *testing.T) {
	d := testdb.Open(t)
	store := shared.NewSQLStore(d)

	_, err := store.CreateTemplateWithProjections(ctx, shared.TemplateDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "orphan-template", SharedRepoID: uuid.Must(uuid.NewV7()), PlaybookPath: "a.yml",
	})
	if !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("CreateTemplateWithProjections() with an unknown repo definition error = %v, want ErrNotFound", err)
	}
}

func TestSQLStoreExistingTemplateNamesExcludesOwnProjections(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	orgA := seedOrg(t, q, "shared-tpl-existing-a")
	seedOrg(t, q, "shared-tpl-existing-b")
	orgC := seedOrg(t, q, "shared-tpl-existing-c")
	seedOtherTemplate(t, d, orgC, "unrelated-template")
	repoDef := seedSharedRepoDef(t, d, "infra-for-tpl-existing")

	def, err := store.CreateTemplateWithProjections(ctx, shared.TemplateDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "deploy-site", SharedRepoID: repoDef.ID, PlaybookPath: "site.yml",
	})
	if err != nil {
		t.Fatalf("seed CreateTemplateWithProjections: %v", err)
	}

	all, err := store.ExistingTemplateNames(ctx, uuid.Nil)
	if err != nil {
		t.Fatalf("ExistingTemplateNames(uuid.Nil) error = %v", err)
	}
	if !containsOrgName(all, orgA, "deploy-site") || !containsOrgName(all, orgC, "unrelated-template") {
		t.Fatalf("ExistingTemplateNames(uuid.Nil) = %v, want every organization's own row, projections included", all)
	}

	excluded, err := store.ExistingTemplateNames(ctx, def.ID)
	if err != nil {
		t.Fatalf("ExistingTemplateNames(def.ID) error = %v", err)
	}
	if containsOrgName(excluded, orgA, "deploy-site") {
		t.Fatalf("ExistingTemplateNames(def.ID) = %v, want def's own projections excluded", excluded)
	}
	if !containsOrgName(excluded, orgC, "unrelated-template") {
		t.Fatalf("ExistingTemplateNames(def.ID) = %v, want the unrelated row still present", excluded)
	}
}

// TestUpdateTemplatePropagatesToEveryProjectionOrNone is
// TestUpdatePropagatesToEveryProjectionOrNone's own test for job templates
// (Review Focus 2): three organizations hold projections of one
// definition, the second organization's own write fails on a real
// UNIQUE (org_id, name) violation, and the whole call — definition
// included — must roll back to the previous values.
func TestUpdateTemplatePropagatesToEveryProjectionOrNone(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	org1 := seedOrg(t, q, "tpl-propagate-org-1")
	org2 := seedOrg(t, q, "tpl-propagate-org-2")
	org3 := seedOrg(t, q, "tpl-propagate-org-3")
	repoDef := seedSharedRepoDef(t, d, "infra-for-tpl-propagate")

	def, err := store.CreateTemplateWithProjections(ctx, shared.TemplateDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "deploy-site", SharedRepoID: repoDef.ID, PlaybookPath: "site.yml", Limit: "web*",
	})
	if err != nil {
		t.Fatalf("seed CreateTemplateWithProjections: %v", err)
	}

	// org2 already has its OWN, unrelated template named "deploy-site-v2" —
	// the exact name this call is about to rename the definition to.
	seedOtherTemplate(t, d, org2, "deploy-site-v2")

	_, err = store.UpdateTemplateAndPropagate(ctx, shared.TemplateDefinition{
		ID: def.ID, Name: "deploy-site-v2", SharedRepoID: repoDef.ID, PlaybookPath: "site.yml", Limit: "db*",
	})
	if !errors.Is(err, shared.ErrNameTaken) {
		t.Fatalf("UpdateTemplateAndPropagate() error = %v, want errors.Is(_, ErrNameTaken)", err)
	}

	got, err := store.GetTemplateDefinition(ctx, def.ID)
	if err != nil {
		t.Fatalf("GetTemplateDefinition() error = %v", err)
	}
	if got.Name != "deploy-site" || got.Limit != "web*" {
		t.Fatalf("GetTemplateDefinition() = %+v, want the PREVIOUS name/limit untouched", got)
	}
	for _, org := range []uuid.UUID{org1, org2, org3} {
		_, name, limit, _ := templateProjectionRow(t, d, org, def.ID)
		if name != "deploy-site" || limit != "web*" {
			t.Fatalf("organization %s projection = (name=%q, limit=%q), want the PREVIOUS values — a failed propagation must never leave some organizations on the new value", org, name, limit)
		}
	}
	if n := countTemplateProjections(t, d, def.ID); n != 3 {
		t.Fatalf("countTemplateProjections() = %d, want 3", n)
	}
}

// --- DeleteTemplateWithProjections ---
//
// One test per blocking table, each seeding only that one dependant — the
// shape that caught the repository version's hole (a test seeding both
// job_triggers and playbooks at once would still have passed a store that
// only checked one of them). job_template_vault_credentials gets its own
// "survives" test instead: it is deliberately NOT one of the two blocking
// tables (DeleteTemplateWithProjections' own doc comment, store_sql.go,
// has the reasoning — a vault binding is part of the projection, not a
// dependant on it, the same call v0.2e made for git_repo_vault_credentials).

func TestDeleteTemplateWithProjectionsRefusesWhileAScheduleExists(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	org := seedOrg(t, q, "shared-tpl-delete-schedule")
	repoDef := seedSharedRepoDef(t, d, "infra-for-tpl-delete-schedule")

	def, err := store.CreateTemplateWithProjections(ctx, shared.TemplateDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "deploy-site", SharedRepoID: repoDef.ID, PlaybookPath: "site.yml",
	})
	if err != nil {
		t.Fatalf("seed CreateTemplateWithProjections: %v", err)
	}
	projID, _, _, _ := templateProjectionRow(t, d, org, def.ID)
	seedSchedule(t, d, org, projID)

	err = store.DeleteTemplateWithProjections(ctx, def.ID)
	var inUse *shared.InUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("DeleteTemplateWithProjections() error = %v, want *InUseError — schedules' own ON DELETE CASCADE must not let this through silently", err)
	}
	if inUse.OrgID != org {
		t.Fatalf("InUseError.OrgID = %v, want %v", inUse.OrgID, org)
	}
	if !containsString(inUse.Reasons, "a schedule") {
		t.Fatalf("InUseError.Reasons = %v, want to include %q", inUse.Reasons, "a schedule")
	}
	if _, gerr := store.GetTemplateDefinition(ctx, def.ID); gerr != nil {
		t.Fatalf("GetTemplateDefinition() after a refused delete = %v, want the definition still to exist", gerr)
	}
	if !templateProjectionExists(t, d, org, def.ID) {
		t.Fatal("projection gone after a refused delete, want it rolled back along with the definition")
	}
}

func TestDeleteTemplateWithProjectionsRefusesWhileAJobTriggerExists(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	org := seedOrg(t, q, "shared-tpl-delete-trigger")
	repoDef := seedSharedRepoDef(t, d, "infra-for-tpl-delete-trigger")

	def, err := store.CreateTemplateWithProjections(ctx, shared.TemplateDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "deploy-site", SharedRepoID: repoDef.ID, PlaybookPath: "site.yml",
	})
	if err != nil {
		t.Fatalf("seed CreateTemplateWithProjections: %v", err)
	}
	projID, _, _, _ := templateProjectionRow(t, d, org, def.ID)
	seedJobTriggerForTemplate(t, d, org, projID)

	err = store.DeleteTemplateWithProjections(ctx, def.ID)
	var inUse *shared.InUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("DeleteTemplateWithProjections() error = %v, want *InUseError — job_triggers' own ON DELETE CASCADE must not let this through silently", err)
	}
	if inUse.OrgID != org {
		t.Fatalf("InUseError.OrgID = %v, want %v", inUse.OrgID, org)
	}
	if !containsString(inUse.Reasons, "a job trigger") {
		t.Fatalf("InUseError.Reasons = %v, want to include %q", inUse.Reasons, "a job trigger")
	}
	if _, gerr := store.GetTemplateDefinition(ctx, def.ID); gerr != nil {
		t.Fatalf("GetTemplateDefinition() after a refused delete = %v, want the definition still to exist", gerr)
	}
	if !templateProjectionExists(t, d, org, def.ID) {
		t.Fatal("projection gone after a refused delete, want it rolled back along with the definition")
	}
}

// TestDeleteTemplateWithProjectionsSucceedsWithAVaultCredentialBinding is
// the brief's own required proof: a projection WITH vault identities still
// deletes — job_template_vault_credentials is deliberately absent from the
// block list, and its own ON DELETE CASCADE (migration 00059) must
// actually run, not merely let the call return nil.
func TestDeleteTemplateWithProjectionsSucceedsWithAVaultCredentialBinding(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	org := seedOrg(t, q, "shared-tpl-delete-clean-with-vault")
	repoDef := seedSharedRepoDef(t, d, "infra-for-tpl-delete-clean-with-vault")

	def, err := store.CreateTemplateWithProjections(ctx, shared.TemplateDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "deploy-site", SharedRepoID: repoDef.ID, PlaybookPath: "site.yml",
	})
	if err != nil {
		t.Fatalf("seed CreateTemplateWithProjections: %v", err)
	}
	projID, _, _, _ := templateProjectionRow(t, d, org, def.ID)
	seedVaultCredentialBindingForTemplate(t, d, org, projID)
	if n := countTemplateVaultCredentialBindings(t, d, projID); n != 1 {
		t.Fatalf("countTemplateVaultCredentialBindings() before delete = %d, want 1", n)
	}

	if err := store.DeleteTemplateWithProjections(ctx, def.ID); err != nil {
		t.Fatalf("DeleteTemplateWithProjections() error = %v, want nil — a vault credential binding must not block a definition delete", err)
	}
	if _, gerr := store.GetTemplateDefinition(ctx, def.ID); !errors.Is(gerr, shared.ErrNotFound) {
		t.Fatalf("GetTemplateDefinition() after delete = %v, want ErrNotFound", gerr)
	}
	if n := countTemplateProjections(t, d, def.ID); n != 0 {
		t.Fatalf("countTemplateProjections() = %d, want 0", n)
	}
	if n := countTemplateVaultCredentialBindings(t, d, projID); n != 0 {
		t.Fatalf("countTemplateVaultCredentialBindings() after delete = %d, want 0 — the binding must actually be gone, not just the call having returned nil", n)
	}
}

func TestDeleteTemplateWithProjectionsRemovesDefinitionAndProjectionsWhenUnused(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	seedOrg(t, q, "shared-tpl-delete-clean-a")
	seedOrg(t, q, "shared-tpl-delete-clean-b")
	repoDef := seedSharedRepoDef(t, d, "infra-for-tpl-delete-clean")

	def, err := store.CreateTemplateWithProjections(ctx, shared.TemplateDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "deploy-site", SharedRepoID: repoDef.ID, PlaybookPath: "site.yml",
	})
	if err != nil {
		t.Fatalf("seed CreateTemplateWithProjections: %v", err)
	}
	if n := countTemplateProjections(t, d, def.ID); n != 2 {
		t.Fatalf("countTemplateProjections() before delete = %d, want 2", n)
	}

	if err := store.DeleteTemplateWithProjections(ctx, def.ID); err != nil {
		t.Fatalf("DeleteTemplateWithProjections() error = %v, want nil", err)
	}
	if _, gerr := store.GetTemplateDefinition(ctx, def.ID); !errors.Is(gerr, shared.ErrNotFound) {
		t.Fatalf("GetTemplateDefinition() after delete = %v, want ErrNotFound", gerr)
	}
	if n := countTemplateProjections(t, d, def.ID); n != 0 {
		t.Fatalf("countTemplateProjections() = %d, want 0 — the projections must go with the definition", n)
	}
}

func TestSQLStoreProjectTemplateIntoANewOrganization(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	repoDef := seedSharedRepoDef(t, d, "infra-for-tpl-project-into")

	def, err := store.CreateTemplateWithProjections(ctx, shared.TemplateDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "deploy-site", SharedRepoID: repoDef.ID, PlaybookPath: "site.yml", Limit: "web*",
	})
	if err != nil {
		t.Fatalf("seed CreateTemplateWithProjections: %v", err)
	}

	newOrg := seedOrg(t, q, "shared-tpl-project-into-new")
	// A brand-new organization has no projection of repoDef at all yet in
	// this test (ProjectInto for the repository is not called here), so
	// the template's own playbook_id resolution must still produce NULL
	// rather than erroring — the LEFT JOIN in InsertProjectedTemplate's own
	// query comment.
	if err := store.ProjectTemplateInto(ctx, def, newOrg); err != nil {
		t.Fatalf("ProjectTemplateInto() error = %v, want nil", err)
	}

	_, name, limit, playbookID := templateProjectionRow(t, d, newOrg, def.ID)
	if name != "deploy-site" || limit != "web*" {
		t.Fatalf("new organization's projection = (name=%q, limit=%q), want the definition's own values", name, limit)
	}
	if playbookID != uuid.Nil {
		t.Fatalf("new organization's projection playbook_id = %v, want uuid.Nil", playbookID)
	}
}

func TestSQLStoreProjectTemplateIntoRefusesACollidingName(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := shared.NewSQLStore(d)
	repoDef := seedSharedRepoDef(t, d, "infra-for-tpl-project-collide")

	def, err := store.CreateTemplateWithProjections(ctx, shared.TemplateDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "deploy-site", SharedRepoID: repoDef.ID, PlaybookPath: "site.yml",
	})
	if err != nil {
		t.Fatalf("seed CreateTemplateWithProjections: %v", err)
	}
	org := seedOrg(t, q, "shared-tpl-project-collide")
	seedOtherTemplate(t, d, org, "deploy-site")

	if err := store.ProjectTemplateInto(ctx, def, org); !errors.Is(err, shared.ErrNameTaken) {
		t.Fatalf("ProjectTemplateInto() error = %v, want ErrNameTaken", err)
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
