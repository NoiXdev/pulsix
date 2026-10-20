package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/shared"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// TestBuildProjectsSharedRepoDefinitionsIntoNewOrganizations is v0.2f Task
// 3's own Step 2 test: a repository definition created before an
// organization exists must already be there the moment the organization is
// created — spec decision 4, the same seam rules.SeedForOrg already uses
// (TestBuildSeedsRuleSetOnOrgCreation, app_test.go), now carrying
// sharedSvc.ProjectAllInto too (app.go's own ChainOrgCreatedHooks call).
// Access to a.db is why this lives in package app, not app_test: Build does
// not expose the pool it wires sharedSvc onto, and reusing the SAME pool
// (rather than a second admin connection, TestBuildReconcilesZonelessOrganizations's
// own pattern) also means this test reads through exactly the row
// production code would.
func TestBuildProjectsSharedRepoDefinitionsIntoNewOrganizations(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	a, err := Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	def, err := shared.NewService(shared.NewSQLStore(a.db)).CreateRepo(ctx, shared.RepoDefinitionInput{
		Name: "infra-base", URL: "https://git.example.com/infra-base.git", Branch: "main", Subpath: "playbooks",
	})
	if err != nil {
		t.Fatalf("CreateRepo (definition): %v", err)
	}

	body := `{"admin_email":"shared-admin@example.test","admin_password":"correct horse battery","admin_name":"Root","org_slug":"shared-org","org_name":"Shared Org"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/setup/complete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("complete setup: status %d, body %s", rec.Code, rec.Body.String())
	}

	q := gen.New(a.db.Pool())
	org, err := q.GetOrganizationBySlug(ctx, "shared-org")
	if err != nil {
		t.Fatal(err)
	}

	var name, url, branch, subpath string
	var credentialID *uuid.UUID
	err = a.db.Pool().QueryRow(ctx,
		`SELECT name, url, branch, subpath, credential_id FROM git_repos WHERE org_id = $1 AND shared_repo_id = $2`,
		org.ID, def.ID).Scan(&name, &url, &branch, &subpath, &credentialID)
	if err != nil {
		t.Fatalf("read the new organization's projection: %v", err)
	}
	if name != def.Name || url != def.URL || branch != def.Branch || subpath != def.Subpath {
		t.Fatalf("projection = (name=%q, url=%q, branch=%q, subpath=%q), want the definition's own (%q, %q, %q, %q)",
			name, url, branch, subpath, def.Name, def.URL, def.Branch, def.Subpath)
	}
	if credentialID != nil {
		t.Fatalf("a brand-new projection must have no credential of its own, got %v", *credentialID)
	}
}

// seedTenancyUser creates a user row directly, standing in for an account
// that already exists — tenancy.Service.CreateOrg needs an owner id, and
// this test is not exercising signup.
func seedTenancyUser(t *testing.T, q *gen.Queries, email string) uuid.UUID {
	t.Helper()
	u, err := q.CreateUser(context.Background(), gen.CreateUserParams{ID: uuid.Must(uuid.NewV7()), Email: email, DisplayName: email})
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// noopMailer never sends anything — CreateOrg itself never calls Send
// (only Invite does), so this exists only to satisfy tenancy.NewService's
// signature.
type noopMailer struct{}

func (noopMailer) Send(context.Context, string, string, string) error { return nil }

// TestOrgCreatedWhileDefinitionBeingEditedGetsOldOrNewNeverMixed is Review
// Focus 5's own test. It does not call Build: the property under test —
// that a read of a repository definition sees either every shared column
// from before an edit or every one from after it, never some of each — is
// a property of shared.Service and shared.SQLStore, driven here through the
// exact hook shape app.go wires (tenancySvc.SetOrgCreatedHook(sharedSvc.
// ProjectAllInto)), on the same real Postgres testdb.Open gives every other
// real-store test in this codebase.
//
// shared.SQLStore.UpdateAndPropagate cannot be paused mid-flight from
// outside the package (it begins, writes and commits its own transaction
// in one call — tx.go's withTx), so the update here is driven by hand: a
// transaction this test holds open performs the same single-row UPDATE
// UpdateAndPropagate would, the organization created while it is open sees
// the value Postgres actually has committed (the row's previous one — an
// uncommitted UPDATE is invisible outside its own transaction, same as any
// other write under READ COMMITTED), and the organization created after
// the held-open transaction commits sees the new one. Nothing about this
// needs application-level locking or a transaction around CreateOrg itself
// (which this sub-project's brief explicitly ruled out for CreateOrg): the
// guarantee comes from ordinary Postgres row visibility, which is exactly
// the point the brief's own correction makes — shared.Service.UpdateRepo's
// propagation is what is atomic, not the caller.
func TestOrgCreatedWhileDefinitionBeingEditedGetsOldOrNewNeverMixed(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())

	sharedSvc := shared.NewService(shared.NewSQLStore(d))
	tenancySvc := tenancy.NewService(tenancy.NewSQLStore(d), noopMailer{}, "http://localhost:8080", time.Now)
	tenancySvc.SetOrgCreatedHook(sharedSvc.ProjectAllInto)
	owner := seedTenancyUser(t, q, "mid-edit-owner@example.test")

	def, err := sharedSvc.CreateRepo(ctx, shared.RepoDefinitionInput{
		Name: "infra", URL: "https://git.example.com/infra-v1.git", Branch: "main", Subpath: "v1",
	})
	if err != nil {
		t.Fatalf("CreateRepo (definition): %v", err)
	}

	// Hold the definition's own row update open in a transaction this test
	// controls — the same statement shared.SQLStore.UpdateAndPropagate would
	// run on shared_repos, just not committed yet.
	tx, err := d.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE shared_repos SET url = $1, branch = $2, subpath = $3, updated_at = now() WHERE id = $4`,
		"https://git.example.com/infra-v2.git", "develop", "v2", def.ID); err != nil {
		t.Fatal(err)
	}

	orgDuringEdit, err := tenancySvc.CreateOrg(ctx, owner, "mid-edit-org", "Mid Edit Org")
	if err != nil {
		t.Fatalf("CreateOrg while the definition's update transaction is open: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	orgAfterEdit, err := tenancySvc.CreateOrg(ctx, owner, "post-edit-org", "Post Edit Org")
	if err != nil {
		t.Fatalf("CreateOrg after the definition's update committed: %v", err)
	}

	duringURL, duringBranch, duringSubpath := projectedColumns(t, d, orgDuringEdit.ID, def.ID)
	if duringURL != "https://git.example.com/infra-v1.git" || duringBranch != "main" || duringSubpath != "v1" {
		t.Fatalf("org created mid-edit = (url=%q, branch=%q, subpath=%q), want every OLD value together, never a mixture",
			duringURL, duringBranch, duringSubpath)
	}

	afterURL, afterBranch, afterSubpath := projectedColumns(t, d, orgAfterEdit.ID, def.ID)
	if afterURL != "https://git.example.com/infra-v2.git" || afterBranch != "develop" || afterSubpath != "v2" {
		t.Fatalf("org created after the edit committed = (url=%q, branch=%q, subpath=%q), want every NEW value together, never a mixture",
			afterURL, afterBranch, afterSubpath)
	}
}

// projectedColumns reads one organization's projection of defID directly.
func projectedColumns(t *testing.T, d *db.DB, orgID, defID uuid.UUID) (url, branch, subpath string) {
	t.Helper()
	err := d.Pool().QueryRow(context.Background(),
		`SELECT url, branch, subpath FROM git_repos WHERE org_id = $1 AND shared_repo_id = $2`,
		orgID, defID).Scan(&url, &branch, &subpath)
	if err != nil {
		t.Fatalf("read projection for org %s: %v", orgID, err)
	}
	return
}
