package repos_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/repos"
)

func seedOrg(t *testing.T, q *gen.Queries, slug string) uuid.UUID {
	t.Helper()
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

// seedGitCredential stores one git_key credential through the real
// credentials store, the way the org actually references one.
func seedGitCredential(t *testing.T, d *db.DB, orgID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	c, err := credentials.NewSQLStore(d).Create(db.WithOrgScope(ctx, orgID), credentials.Credential{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: name, Kind: credentials.KindGitKey,
	}, []byte("sealed-material"))
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// seedRepoPlaybook inserts one playbook row referencing a repository —
// standing in for the playbooks service's stored rows.
func seedRepoPlaybook(t *testing.T, d *db.DB, orgID, repoID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(db.WithOrgScope(ctx, orgID),
		`INSERT INTO playbooks (id, org_id, repo_id, path, source) VALUES ($1, $2, $3, 'site.yml', 'repo')`,
		id, orgID, repoID); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSQLStoreRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := repos.NewSQLStore(d)
	org := seedOrg(t, q, "repo-org")
	other := seedOrg(t, q, "repo-other")
	scoped := db.WithOrgScope(ctx, org)
	credID := seedGitCredential(t, d, org, "deploy-key")

	created, err := store.Create(scoped, repos.Repo{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "infra",
		URL: "https://git.example.com/acme/infra.git", Branch: "main", Subpath: "playbooks",
		CredentialID: &credID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Branch != "main" || created.CredentialID == nil || *created.CredentialID != credID ||
		created.CreatedAt.IsZero() || created.LastSync != nil || created.LastSyncError != "" {
		t.Fatalf("created = %+v", created)
	}
	// Same name, same org: ErrNameTaken. Same name, other org: fine — the
	// unique constraint is (org_id, name), not name.
	if _, err := store.Create(scoped, repos.Repo{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "infra", URL: "https://git.example.com/x"}); !errors.Is(err, repos.ErrNameTaken) {
		t.Fatalf("duplicate name = %v, want ErrNameTaken", err)
	}
	if _, err := store.Create(db.WithOrgScope(ctx, other), repos.Repo{ID: uuid.Must(uuid.NewV7()), OrgID: other, Name: "infra", URL: "https://git.example.com/y"}); err != nil {
		t.Fatalf("same name in another org = %v", err)
	}

	got, err := store.Get(scoped, org, created.ID)
	if err != nil || got.URL != created.URL || got.CredentialID == nil {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := store.Get(db.WithOrgScope(ctx, other), other, created.ID); !errors.Is(err, repos.ErrNotFound) {
		t.Fatalf("cross-org Get = %v, want ErrNotFound", err)
	}

	list, err := store.List(scoped, org)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}

	renamed := created
	renamed.Name = "infra-renamed"
	updated, err := store.Update(scoped, renamed)
	if err != nil || updated.Name != "infra-renamed" || !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("Update = %+v, %v", updated, err)
	}
	if _, err := store.Update(scoped, repos.Repo{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "x", URL: "u"}); !errors.Is(err, repos.ErrNotFound) {
		t.Fatalf("Update unknown = %v, want ErrNotFound", err)
	}

	if err := store.Delete(db.WithOrgScope(ctx, other), other, created.ID); !errors.Is(err, repos.ErrNotFound) {
		t.Fatalf("cross-org Delete = %v, want ErrNotFound", err)
	}
	if err := store.Delete(scoped, org, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(scoped, org, created.ID); !errors.Is(err, repos.ErrNotFound) {
		t.Fatalf("second Delete = %v, want ErrNotFound", err)
	}
}

// TestSQLStoreListOrdersByName proves the list the service hands to the API
// is name-ordered, so two-org and lifecycle tests see a stable order.
func TestSQLStoreListOrdersByName(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := repos.NewSQLStore(d)
	org := seedOrg(t, q, "repo-order-org")
	scoped := db.WithOrgScope(ctx, org)
	for _, name := range []string{"zeta", "alpha", "mid"} {
		if _, err := store.Create(scoped, repos.Repo{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: name, URL: "https://git.example.com/x"}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := store.List(scoped, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].Name != "alpha" || list[1].Name != "mid" || list[2].Name != "zeta" {
		t.Fatalf("list = %v, want [alpha mid zeta]", names(list))
	}
}

func names(list []repos.Repo) []string {
	out := make([]string, len(list))
	for i, r := range list {
		out[i] = r.Name
	}
	return out
}

// TestSQLStoreCredentialFKIsOrgScoped proves the composite (org_id,
// credential_id) foreign key: a credential from another organization cannot
// be referenced, surfacing as the wrapped store error — not a sentinel.
func TestSQLStoreCredentialFKIsOrgScoped(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := repos.NewSQLStore(d)
	org := seedOrg(t, q, "repo-fk-org")
	other := seedOrg(t, q, "repo-fk-other")
	otherCred := seedGitCredential(t, d, other, "other-key")

	_, err := store.Create(db.WithOrgScope(ctx, org), repos.Repo{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "infra",
		URL: "https://git.example.com/x", CredentialID: &otherCred,
	})
	if err == nil || errors.Is(err, repos.ErrNameTaken) || errors.Is(err, repos.ErrInUse) {
		t.Fatalf("cross-org credential reference = %v, want the wrapped FK error", err)
	}
}

// TestSQLStoreDeleteReferencedRepoIsInUse proves the FK guard: a playbook
// still referencing the repository makes Delete ErrInUse, and the
// repository survives.
func TestSQLStoreDeleteReferencedRepoIsInUse(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := repos.NewSQLStore(d)
	org := seedOrg(t, q, "repo-inuse-org")
	scoped := db.WithOrgScope(ctx, org)

	created, err := store.Create(scoped, repos.Repo{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "infra", URL: "https://git.example.com/x"})
	if err != nil {
		t.Fatal(err)
	}
	seedRepoPlaybook(t, d, org, created.ID)

	if err := store.Delete(scoped, org, created.ID); !errors.Is(err, repos.ErrInUse) {
		t.Fatalf("Delete = %v, want ErrInUse", err)
	}
	if _, err := store.Get(scoped, org, created.ID); err != nil {
		t.Fatalf("referenced repository must survive: %v", err)
	}
	// Once the referencing playbook is gone, deletion works.
	if _, err := d.Exec(db.WithOrgScope(ctx, org), `DELETE FROM playbooks`); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(scoped, org, created.ID); err != nil {
		t.Fatalf("Delete after unreferencing: %v", err)
	}
}

// TestSQLStoreSyncStateColumns proves last_sync/last_sync_error read back
// through the repository (the columns the jobs finish path writes).
func TestSQLStoreSyncStateColumns(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := repos.NewSQLStore(d)
	org := seedOrg(t, q, "repo-sync-org")
	scoped := db.WithOrgScope(ctx, org)

	created, err := store.Create(scoped, repos.Repo{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "infra", URL: "https://git.example.com/x"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 14, 14, 0, 0, 0, time.UTC)
	errText := "git pull: not a repository"
	if _, err := d.Exec(scoped,
		`UPDATE git_repos SET last_sync = $3, last_sync_error = $4 WHERE org_id = $1 AND id = $2`,
		org, created.ID, at, errText); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(scoped, org, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastSync == nil || !got.LastSync.Equal(at) || got.LastSyncError != errText {
		t.Fatalf("sync columns = %+v", got)
	}
}
