// On-save sealing of legacy plaintext extra vars (backlog 2.47, 2.52),
// against the real store: a template row saved before sealing shipped holds
// plaintext under a secret-typed field; a save that keeps it — the mask
// marker a masked read returned — resolves to the stored plaintext, seals
// it with the row's AAD and stores the envelope, so the legacy row is
// sealed from that save on. A background or migration-time re-seal is
// impossible (sealing needs the master-key box at app time); a save is the
// migration.
package templates_test

import (
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

func TestSQLServiceSealsLegacyPlaintextOnKeepSave(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedTemplatesOrg(t, q, "tmpl-legacy-seal-org")
	scoped := db.WithOrgScope(ctx, org)
	playbookID := seedEditorPlaybook(t, d, org)
	box := crypto.NewBox(testBoxKey("templates-legacy-seal-key-1234"))
	store := templates.NewSQLStore(d)

	// The legacy row, stored exactly as a pre-sealing template has it: the
	// plaintext in the clear under the secret field.
	legacy := templates.Template{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, PlaybookID: playbookID, Name: "legacy",
		ExtraVars:       map[string]any{"db_pass": "hunter2-legacy", "region": "eu-central"},
		ExtraVarsSchema: secretSchema(),
	}
	if _, err := store.Create(scoped, legacy); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	// The service reads the raw row (the store, not a masked read), so the
	// marker resolves against the plaintext and the save seals it.
	svc := templates.NewService(store, playbookStoreLookup{playbooks.NewSQLStore(d)}, nil, nil, nil, box)
	updated, err := svc.Update(scoped, org, legacy.ID, templates.UpdateInput{
		Name: "legacy", PlaybookID: playbookID,
		ExtraVarsSchema: secretSchema(),
		ExtraVars:       map[string]any{"db_pass": schema.Mask(), "region": "eu-central"},
	})
	if err != nil {
		t.Fatalf("Update over legacy plaintext: %v", err)
	}
	if !equalJSON(updated.ExtraVars["db_pass"], schema.Mask()) {
		t.Fatalf("read-back extra vars = %#v, want the mask marker", updated.ExtraVars["db_pass"])
	}

	// The stored row now holds a sealed envelope of the legacy value under
	// the row's own AAD; the plain string field is stored as is.
	stored, err := store.Get(scoped, org, legacy.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	ct, ok := schema.SealedBytes(stored.ExtraVars["db_pass"])
	if !ok {
		t.Fatalf("stored db_pass = %#v, want a sealed envelope", stored.ExtraVars["db_pass"])
	}
	plain, err := box.OpenAAD(ct, templates.ExtraVarsAAD(org, legacy.ID))
	if err != nil {
		t.Fatalf("open stored db_pass: %v", err)
	}
	if string(plain) != "hunter2-legacy" {
		t.Fatalf("sealed value opens to %q, want the legacy plaintext", string(plain))
	}
	if stored.ExtraVars["region"] != "eu-central" {
		t.Fatalf("a string field must be stored as is: %#v", stored.ExtraVars)
	}
}
