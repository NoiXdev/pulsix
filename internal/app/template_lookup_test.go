// The template lookup opens sealed extra vars for the claim (final review
// triage): templates keep secret-typed values sealed at rest, and the claim
// payload — the one place they are revealed, inside the mTLS claim — gets
// them in the clear.
package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/playbooks"
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
