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
