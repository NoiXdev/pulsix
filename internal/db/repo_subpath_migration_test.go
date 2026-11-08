package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// TestRepoSubpathCanonicalised (backlog 7.94): migration 00083 rewrites
// every stored subpath, of an organization's repositories and of the
// installation's definitions alike, into the form repos.CanonicalSubpath
// stores — no empty or "." segments — so a projection stays equal to its
// definition, and leaves anything else (a ".." the worker refuses anyway)
// as it was.
func TestRepoSubpathCanonicalised(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, testdb.FreshDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.MigrateTo(ctx, 82); err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"":                  "",
		"ansible":           "ansible",
		"/ansible":          "ansible",
		"ansible/":          "ansible",
		"//deploy//site//":  "deploy/site",
		"./deploy/./site":   "deploy/site",
		"/":                 "",
		".":                 "",
		"/./":               "",
		".hidden/x":         ".hidden/x",
		"../outside":        "../outside",
		"deploy/../outside": "deploy/../outside",
	}
	org := uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(ctx, `INSERT INTO organizations (id, slug, name) VALUES ($1, 'subpath', 'subpath')`, org); err != nil {
		t.Fatal(err)
	}
	repoOf, defOf := map[string]uuid.UUID{}, map[string]uuid.UUID{}
	i := 0
	for stored := range cases {
		i++
		def, repo := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
		defOf[stored], repoOf[stored] = def, repo
		if _, err := d.Pool().Exec(ctx,
			`INSERT INTO shared_repos (id, name, url, branch, subpath) VALUES ($1, $2, 'https://example.com/r.git', 'main', $3)`,
			def, "def-"+uuid.NewString(), stored); err != nil {
			t.Fatal(err)
		}
		// Every other repository is a projection of its definition, the
		// rest the organization's own.
		var shared *uuid.UUID
		if i%2 == 0 {
			shared = &def
		}
		if _, err := d.Pool().Exec(ctx,
			`INSERT INTO git_repos (id, org_id, name, url, branch, subpath, shared_repo_id) VALUES ($1, $2, $3, 'https://example.com/r.git', 'main', $4, $5)`,
			repo, org, "repo-"+uuid.NewString()[:8], stored, shared); err != nil {
			t.Fatal(err)
		}
	}

	if err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	for stored, want := range cases {
		var repoSub, defSub string
		if err := d.Pool().QueryRow(ctx, `SELECT subpath FROM git_repos WHERE id = $1`, repoOf[stored]).Scan(&repoSub); err != nil {
			t.Fatal(err)
		}
		if err := d.Pool().QueryRow(ctx, `SELECT subpath FROM shared_repos WHERE id = $1`, defOf[stored]).Scan(&defSub); err != nil {
			t.Fatal(err)
		}
		if repoSub != want || defSub != want {
			t.Errorf("stored %q: git_repos %q, shared_repos %q; want %q", stored, repoSub, defSub, want)
		}
	}
}
