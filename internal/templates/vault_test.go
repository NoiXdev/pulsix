package templates_test

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/templates"
)

// seedTemplateVaultCredential inserts one vault_password credential in org
// — store_sql_test.go's own seedSSHCredential is ssh_key-kind, which the
// vault link table's composite foreign key does not care about, but a real
// kind keeps this fixture honest with what a vault credential picker would
// actually offer.
func seedTemplateVaultCredential(t *testing.T, d *db.DB, orgID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	c, err := credentials.NewSQLStore(d).Create(db.WithOrgScope(ctx, orgID), credentials.Credential{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: name, Kind: credentials.KindVaultPassword,
	}, []byte("sealed-material"))
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// TestSQLStoreVaultCredentialsRoundTrip round-trips ReplaceVaultCredentials/
// ListVaultCredentials directly: write a set, read it back in label order,
// replace it down to a different set, replace it down to empty.
func TestSQLStoreVaultCredentialsRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedTemplatesOrg(t, q, "tmpl-vault-org")
	scoped := db.WithOrgScope(ctx, org)
	playbookID := seedEditorPlaybook(t, d, org)
	store := templates.NewSQLStore(d)

	tmpl, err := store.Create(scoped, templates.Template{ID: uuid.Must(uuid.NewV7()), OrgID: org, PlaybookID: playbookID, Name: "deploy"})
	if err != nil {
		t.Fatalf("Create template: %v", err)
	}
	credA, credB := seedTemplateVaultCredential(t, d, org, "team a"), seedTemplateVaultCredential(t, d, org, "team b")

	if err := store.ReplaceVaultCredentials(scoped, org, tmpl.ID,
		[]templates.VaultCredential{{CredentialID: credB, Label: "team-b"}, {CredentialID: credA, Label: "team-a"}}); err != nil {
		t.Fatalf("ReplaceVaultCredentials: %v", err)
	}
	got, err := store.ListVaultCredentials(scoped, org, tmpl.ID)
	if err != nil {
		t.Fatalf("ListVaultCredentials: %v", err)
	}
	want := []templates.VaultCredential{{CredentialID: credA, Label: "team-a"}, {CredentialID: credB, Label: "team-b"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v (label order)", got, want)
	}

	if err := store.ReplaceVaultCredentials(scoped, org, tmpl.ID, nil); err != nil {
		t.Fatalf("replace to empty: %v", err)
	}
	if got, err := store.ListVaultCredentials(scoped, org, tmpl.ID); err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v, want empty", got, err)
	}
}

// TestSQLStoreReplaceVaultCredentialsRollsBackOnPartialFailure mirrors
// internal/repos's own TestStoreReplaceRepoVaultIdentitiesRollsBackOnPartialFailure:
// ReplaceVaultCredentials' delete-then-insert pair must commit atomically.
// Without a transaction, the DELETE half would commit on its own and a
// failing INSERT half (here, a duplicate label — illegal under PRIMARY KEY
// (template_id, label), and deliberately not absorbed by an ON CONFLICT)
// would leave the template's vault credential set wiped to empty instead of
// the previous, still-valid set it had before the call.
func TestSQLStoreReplaceVaultCredentialsRollsBackOnPartialFailure(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedTemplatesOrg(t, q, "tmpl-vault-txn-org")
	scoped := db.WithOrgScope(ctx, org)
	playbookID := seedEditorPlaybook(t, d, org)
	store := templates.NewSQLStore(d)

	tmpl, err := store.Create(scoped, templates.Template{ID: uuid.Must(uuid.NewV7()), OrgID: org, PlaybookID: playbookID, Name: "deploy"})
	if err != nil {
		t.Fatalf("Create template: %v", err)
	}
	credTeamA := seedTemplateVaultCredential(t, d, org, "team a")
	if err := store.ReplaceVaultCredentials(scoped, org, tmpl.ID,
		[]templates.VaultCredential{{CredentialID: credTeamA, Label: "team-a"}}); err != nil {
		t.Fatalf("seed ReplaceVaultCredentials: %v", err)
	}

	credDup := seedTemplateVaultCredential(t, d, org, "duplicate-label vault")
	err = store.ReplaceVaultCredentials(scoped, org, tmpl.ID, []templates.VaultCredential{
		{CredentialID: credTeamA, Label: "team-a"},
		{CredentialID: credDup, Label: "team-a"}, // same label twice: a unique violation on insert
	})
	if err == nil {
		t.Fatal("want an error from the duplicate label, got nil")
	}

	got, err := store.ListVaultCredentials(scoped, org, tmpl.ID)
	if err != nil {
		t.Fatalf("ListVaultCredentials after the failed replace: %v", err)
	}
	want := []templates.VaultCredential{{CredentialID: credTeamA, Label: "team-a"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v after a failed replace, want the previous set %+v left untouched", got, want)
	}
}
