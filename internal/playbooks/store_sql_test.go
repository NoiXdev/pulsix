package playbooks_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/playbooks"
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

func seedRepo(t *testing.T, d *db.DB, orgID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := repos.NewSQLStore(d).Create(db.WithOrgScope(ctx, orgID), repos.Repo{
		ID: id, OrgID: orgID, Name: name, URL: "https://git.example.com/acme/" + name + ".git",
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedTemplatePlaybook inserts one job template referencing a playbook —
// standing in for the templates service's stored rows (Task 7).
func seedTemplatePlaybook(t *testing.T, d *db.DB, orgID, playbookID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(db.WithOrgScope(ctx, orgID),
		`INSERT INTO job_templates (id, org_id, name, playbook_id, scope)
		 VALUES ($1, $2, 'template', $3, '{"group_id": null, "host_ids": []}')`,
		id, orgID, playbookID); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestSQLStoreRoundTrip covers both sources end to end against the real
// CHECK constraints: an editor playbook round-trips its inline text, a repo
// playbook its repository and path.
func TestSQLStoreRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := playbooks.NewSQLStore(d)
	org := seedOrg(t, q, "pb-org")
	other := seedOrg(t, q, "pb-other")
	scoped := db.WithOrgScope(ctx, org)
	repoID := seedRepo(t, d, org, "infra")

	editor, err := store.Create(scoped, playbooks.Playbook{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Source: playbooks.SourceEditor,
		Inline: ptr("- name: deploy\n  hosts: all\n"),
	})
	if err != nil {
		t.Fatalf("editor Create: %v", err)
	}
	if editor.Path != nil || editor.RepoID != nil || editor.Inline == nil || editor.CreatedAt.IsZero() {
		t.Fatalf("editor created = %+v", editor)
	}
	repoPb, err := store.Create(scoped, playbooks.Playbook{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Source: playbooks.SourceRepo,
		RepoID: &repoID, Path: ptr("playbooks/site.yml"),
	})
	if err != nil {
		t.Fatalf("repo Create: %v", err)
	}
	if repoPb.Inline != nil || repoPb.RepoID == nil || *repoPb.RepoID != repoID || repoPb.Path == nil {
		t.Fatalf("repo created = %+v", repoPb)
	}

	got, err := store.Get(scoped, org, editor.ID)
	if err != nil || got.Inline == nil || *got.Inline != "- name: deploy\n  hosts: all\n" {
		t.Fatalf("editor Get = %+v, %v", got, err)
	}
	if _, err := store.Get(db.WithOrgScope(ctx, other), other, editor.ID); !errors.Is(err, playbooks.ErrNotFound) {
		t.Fatalf("cross-org Get = %v, want ErrNotFound", err)
	}

	list, err := store.List(scoped, org)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %v, %v", list, err)
	}

	updated, err := store.UpdateInline(scoped, org, editor.ID, "- hosts: db\n", nil)
	if err != nil || updated.Inline == nil || *updated.Inline != "- hosts: db\n" {
		t.Fatalf("UpdateInline = %+v, %v", updated, err)
	}
	if err := store.Delete(scoped, org, editor.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(scoped, org, editor.ID); !errors.Is(err, playbooks.ErrNotFound) {
		t.Fatalf("second Delete = %v, want ErrNotFound", err)
	}
	if err := store.Delete(db.WithOrgScope(ctx, other), other, repoPb.ID); !errors.Is(err, playbooks.ErrNotFound) {
		t.Fatalf("cross-org Delete = %v, want ErrNotFound", err)
	}
}

// TestSQLStoreCheckConstraintsRejectHybrids is the CHECK-constraint
// round-trip the brief asks for: a repo playbook carrying inline text and an
// editor playbook carrying a path are rejected by the database itself —
// surfacing as wrapped store errors, not domain sentinels, because only a
// caller that bypassed the service can produce them.
func TestSQLStoreCheckConstraintsRejectHybrids(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := playbooks.NewSQLStore(d)
	org := seedOrg(t, q, "pb-check-org")
	scoped := db.WithOrgScope(ctx, org)
	repoID := seedRepo(t, d, org, "infra")

	cases := []struct {
		name string
		p    playbooks.Playbook
	}{
		{
			"repo source with inline",
			playbooks.Playbook{ID: uuid.Must(uuid.NewV7()), OrgID: org, Source: playbooks.SourceRepo, Inline: ptr("- hosts: all\n")},
		},
		{
			"editor source with path",
			playbooks.Playbook{ID: uuid.Must(uuid.NewV7()), OrgID: org, Source: playbooks.SourceEditor, RepoID: &repoID, Path: ptr("site.yml")},
		},
		{
			"repo source without repo",
			playbooks.Playbook{ID: uuid.Must(uuid.NewV7()), OrgID: org, Source: playbooks.SourceRepo, Path: ptr("site.yml")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.Create(scoped, tc.p)
			if err == nil || errors.Is(err, playbooks.ErrInvalidInput) || errors.Is(err, playbooks.ErrNotFound) {
				t.Fatalf("hybrid row = %v, want the wrapped CHECK error", err)
			}
		})
	}
}

// TestSQLStoreUpdateInlineRefusesRepoPlaybook proves the UPDATE's source
// guard: a repo playbook has no inline column to update, so the statement
// finds no row and the store reports ErrNotFound.
func TestSQLStoreUpdateInlineRefusesRepoPlaybook(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := playbooks.NewSQLStore(d)
	org := seedOrg(t, q, "pb-update-org")
	scoped := db.WithOrgScope(ctx, org)
	repoID := seedRepo(t, d, org, "infra")

	repoPb, err := store.Create(scoped, playbooks.Playbook{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Source: playbooks.SourceRepo,
		RepoID: &repoID, Path: ptr("site.yml"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateInline(scoped, org, repoPb.ID, "- hosts: db\n", nil); !errors.Is(err, playbooks.ErrNotFound) {
		t.Fatalf("UpdateInline on a repo playbook = %v, want ErrNotFound", err)
	}
}

// TestSQLStoreDeleteReferencedPlaybookIsInUse proves the FK guard: a job
// template still referencing the playbook makes Delete ErrInUse, and the
// playbook survives.
func TestSQLStoreDeleteReferencedPlaybookIsInUse(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := playbooks.NewSQLStore(d)
	org := seedOrg(t, q, "pb-inuse-org")
	scoped := db.WithOrgScope(ctx, org)

	pb, err := store.Create(scoped, playbooks.Playbook{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Source: playbooks.SourceEditor,
		Inline: ptr("- hosts: all\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	seedTemplatePlaybook(t, d, org, pb.ID)

	if err := store.Delete(scoped, org, pb.ID); !errors.Is(err, playbooks.ErrInUse) {
		t.Fatalf("Delete = %v, want ErrInUse", err)
	}
	if _, err := store.Get(scoped, org, pb.ID); err != nil {
		t.Fatalf("referenced playbook must survive: %v", err)
	}
	if _, err := d.Exec(db.WithOrgScope(ctx, org), `DELETE FROM job_templates`); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(scoped, org, pb.ID); err != nil {
		t.Fatalf("Delete after unreferencing: %v", err)
	}
}

func ptr(s string) *string { return &s }

// TestSQLStoreListLeavesOutTheVarSource (backlog 7.114): the list query does
// not select the stored variable sources, which can be megabytes per row;
// Get still returns them.
func TestSQLStoreListLeavesOutTheVarSource(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := playbooks.NewSQLStore(d)
	org := seedOrg(t, q, "pb-list-org")
	scoped := db.WithOrgScope(ctx, org)
	repoID := seedRepo(t, d, org, "infra")

	editor, err := store.Create(scoped, playbooks.Playbook{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Source: playbooks.SourceEditor,
		Inline: ptr("- hosts: all\n"), InlineSource: []byte(`{"path":"inline"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(scoped, playbooks.Playbook{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Source: playbooks.SourceRepo,
		RepoID: &repoID, Path: ptr("site.yml"),
	}); err != nil {
		t.Fatal(err)
	}

	list, err := store.List(scoped, org)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %v, %v", list, err)
	}
	for _, p := range list {
		if p.InlineSource != nil {
			t.Fatalf("listed playbook %s carries its variable sources: %s", p.ID, p.InlineSource)
		}
	}
	if list[0].ID != editor.ID || list[0].Inline == nil || list[0].Source != playbooks.SourceEditor {
		t.Fatalf("listed editor playbook = %+v, want its other columns", list[0])
	}
	got, err := store.Get(scoped, org, editor.ID)
	if err != nil || len(got.InlineSource) == 0 {
		t.Fatalf("Get = %+v, %v; want the stored variable sources", got, err)
	}
}
