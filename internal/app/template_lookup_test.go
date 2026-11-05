// The template lookup opens sealed extra vars for the claim (final review
// triage): templates keep secret-typed values sealed at rest, and the claim
// payload — the one place they are revealed, inside the mTLS claim — gets
// them in the clear.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/shared"
	"github.com/noixdev/pulsix/internal/templates"
)

func TestTemplateLookupOpensSealedExtraVars(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	org, err := gen.New(d.Pool()).CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: "app-template-lookup", Name: "lookup"})
	if err != nil {
		t.Fatal(err)
	}
	scoped := db.WithOrgScope(ctx, org.ID)
	box := crypto.NewBox([32]byte{7, 7, 7})
	inline := "- hosts: all\n  tasks: []"
	pb, err := playbooks.NewSQLStore(d).Create(scoped, playbooks.Playbook{ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, Inline: &inline, Source: playbooks.SourceEditor})
	if err != nil {
		t.Fatal(err)
	}
	tmplID := uuid.Must(uuid.NewV7())
	ct, err := box.SealAAD([]byte("hunter2-lookup"), templates.ExtraVarsAAD(org.ID, tmplID))
	if err != nil {
		t.Fatal(err)
	}
	store := templates.NewSQLStore(d)
	if _, err := store.Create(scoped, templates.Template{
		ID: tmplID, OrgID: org.ID, PlaybookID: pb.ID, Name: "sealed",
		ExtraVars:       map[string]any{"db_pass": schema.Sealed(ct), "region": "eu"},
		ExtraVarsSchema: &schema.Schema{Fields: []schema.Field{{Name: "db_pass", Type: schema.TypeSecret}, {Name: "region", Type: schema.TypeString}}},
	}); err != nil {
		t.Fatal(err)
	}

	row, err := NewTemplateLookup(store, playbooks.NewSQLStore(d), box).TemplateLookup(scoped, org.ID, tmplID)
	if err != nil {
		t.Fatalf("TemplateLookup: %v", err)
	}
	var vars map[string]any
	if err := json.Unmarshal(row.ExtraVars, &vars); err != nil {
		t.Fatal(err)
	}
	if vars["db_pass"] != "hunter2-lookup" || vars["region"] != "eu" {
		t.Fatalf("claim extra vars = %v, want the sealed secret opened", vars)
	}

	// A template saved before sealing shipped (backlog 2.47) holds the
	// plaintext in the clear; the claim payload carries it as the value
	// until the template's next save seals it — the lookup opens only what
	// is sealed and passes the rest through unchanged.
	legacy := uuid.Must(uuid.NewV7())
	if _, err := store.Create(scoped, templates.Template{
		ID: legacy, OrgID: org.ID, PlaybookID: pb.ID, Name: "legacy",
		ExtraVars:       map[string]any{"db_pass": "hunter2-legacy", "region": "eu"},
		ExtraVarsSchema: &schema.Schema{Fields: []schema.Field{{Name: "db_pass", Type: schema.TypeSecret}, {Name: "region", Type: schema.TypeString}}},
	}); err != nil {
		t.Fatal(err)
	}
	lrow, err := NewTemplateLookup(store, playbooks.NewSQLStore(d), box).TemplateLookup(scoped, org.ID, legacy)
	if err != nil {
		t.Fatalf("TemplateLookup over legacy plaintext: %v", err)
	}
	var lvars map[string]any
	if err := json.Unmarshal(lrow.ExtraVars, &lvars); err != nil {
		t.Fatal(err)
	}
	if lvars["db_pass"] != "hunter2-legacy" || lvars["region"] != "eu" {
		t.Fatalf("claim extra vars over a legacy plaintext row = %v, want the plaintext as the value", lvars)
	}

	// A ciphertext that does not open under this template's AAD fails the
	// lookup (and so the claim) closed.
	other, err := box.SealAAD([]byte("x"), templates.ExtraVarsAAD(org.ID, uuid.Must(uuid.NewV7())))
	if err != nil {
		t.Fatal(err)
	}
	moved := uuid.Must(uuid.NewV7())
	if _, err := store.Create(scoped, templates.Template{
		ID: moved, OrgID: org.ID, PlaybookID: pb.ID, Name: "moved", ExtraVars: map[string]any{"db_pass": schema.Sealed(other)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTemplateLookup(store, playbooks.NewSQLStore(d), box).TemplateLookup(scoped, org.ID, moved); err == nil {
		t.Fatal("a ciphertext of another template must not open")
	}
}

// TestTemplateLookupRefusesAnUnresolvedSharedTemplateDistinctly is finding
// 2 of the Task 6 review: a projection of an installation-owned definition
// (spec v0.2f) whose playbook has not been registered in this organization
// yet must not report jobs.ErrNotFound when a run is attempted — that is
// indistinguishable from a template that was deleted or never existed,
// while the row is visibly sitting in this organization's own list. It
// gets its own sentinel, jobs.ErrTemplateNotReady, instead.
func TestTemplateLookupRefusesAnUnresolvedSharedTemplateDistinctly(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	org, err := gen.New(d.Pool()).CreateOrganization(ctx, gen.CreateOrganizationParams{
		ID: uuid.Must(uuid.NewV7()), Slug: "app-template-lookup-unresolved", Name: "lookup",
	})
	if err != nil {
		t.Fatal(err)
	}

	// A shared repository and job template definition, created through the
	// real installation-admin path — CreateWithProjections/
	// CreateTemplateWithProjections project into every existing
	// organization immediately (spec decision 4), but nothing has
	// registered a matching playbook in THIS organization yet, so the
	// projection's own playbook_id stays NULL (migration 00061's own
	// reason the column became nullable).
	sharedStore := shared.NewSQLStore(d)
	repoDef, err := sharedStore.CreateWithProjections(ctx, shared.RepoDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "infra-for-lookup-unresolved", URL: "https://git.example.com/x.git", Branch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	tmplDef, err := sharedStore.CreateTemplateWithProjections(ctx, shared.TemplateDefinition{
		ID: uuid.Must(uuid.NewV7()), Name: "deploy-site-unresolved", SharedRepoID: repoDef.ID, PlaybookPath: "site.yml",
	})
	if err != nil {
		t.Fatal(err)
	}

	scoped := db.WithOrgScope(ctx, org.ID)
	templateStore := templates.NewSQLStore(d)
	list, err := templateStore.List(scoped, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	var projID uuid.UUID
	for _, tt := range list {
		if tt.SharedTemplateID != nil && *tt.SharedTemplateID == tmplDef.ID {
			projID = tt.ID
		}
	}
	if projID == uuid.Nil {
		t.Fatal("no projection of the shared template definition found in the organization")
	}

	box := crypto.NewBox([32]byte{7, 7, 7})
	_, err = NewTemplateLookup(templateStore, playbooks.NewSQLStore(d), box).TemplateLookup(scoped, org.ID, projID)
	if !errors.Is(err, jobs.ErrTemplateNotReady) {
		t.Fatalf("TemplateLookup() on an unresolved projection error = %v, want errors.Is(_, jobs.ErrTemplateNotReady)", err)
	}
	if errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("TemplateLookup() error = %v, must not also be jobs.ErrNotFound — indistinguishable from a deleted template", err)
	}
}

// TestTemplateLookupOpensActiveFieldsOnly (spec 7.75 goal 3): the lookup
// leaves out a value under an inactive or undeclared field before it opens
// anything, so such a value is never decrypted at claim time and an
// envelope that would not open there (here: sealed for another template)
// does not fail the run. Active values are opened as before.
func TestTemplateLookupOpensActiveFieldsOnly(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	org, err := gen.New(d.Pool()).CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: "app-template-lookup-active", Name: "lookup"})
	if err != nil {
		t.Fatal(err)
	}
	scoped := db.WithOrgScope(ctx, org.ID)
	box := crypto.NewBox([32]byte{7, 7, 7})
	inline := "- hosts: all\n  tasks: []"
	pb, err := playbooks.NewSQLStore(d).Create(scoped, playbooks.Playbook{ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, Inline: &inline, Source: playbooks.SourceEditor})
	if err != nil {
		t.Fatal(err)
	}
	tmplID := uuid.Must(uuid.NewV7())
	own, err := box.SealAAD([]byte("active-secret"), templates.ExtraVarsAAD(org.ID, tmplID))
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := box.SealAAD([]byte("x"), templates.ExtraVarsAAD(org.ID, uuid.Must(uuid.NewV7())))
	if err != nil {
		t.Fatal(err)
	}
	off := false
	store := templates.NewSQLStore(d)
	if _, err := store.Create(scoped, templates.Template{
		ID: tmplID, OrgID: org.ID, PlaybookID: pb.ID, Name: "inactive",
		ExtraVars: map[string]any{
			"db_pass": schema.Sealed(own), "old_pass": schema.Sealed(foreign), "gone_pass": schema.Sealed(foreign), "region": "eu",
		},
		ExtraVarsSchema: &schema.Schema{Fields: []schema.Field{
			{Name: "db_pass", Type: schema.TypeSecret},
			{Name: "old_pass", Type: schema.TypeSecret, Active: &off},
			{Name: "region", Type: schema.TypeString},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	row, err := NewTemplateLookup(store, playbooks.NewSQLStore(d), box).TemplateLookup(scoped, org.ID, tmplID)
	if err != nil {
		t.Fatalf("TemplateLookup with an unopenable value under an inactive and an undeclared field: %v", err)
	}
	var vars map[string]any
	if err := json.Unmarshal(row.ExtraVars, &vars); err != nil {
		t.Fatal(err)
	}
	if len(vars) != 2 || vars["db_pass"] != "active-secret" || vars["region"] != "eu" {
		t.Fatalf("claim extra vars = %v, want exactly the active values, opened", vars)
	}
}
