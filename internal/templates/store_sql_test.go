package templates_test

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/templates"
)

// testBoxKey builds a 32-byte key from a label, the way the jobs tests do.
// playbookStoreLookup is the playbooks store as the service's
// PlaybookLookup, for tests that read no variable sources.
type playbookStoreLookup struct{ *playbooks.SQLStore }

func (playbookStoreLookup) VarSource(context.Context, uuid.UUID, uuid.UUID) (playbooks.VarSource, error) {
	return playbooks.VarSource{}, nil
}

func testBoxKey(label string) [32]byte {
	var key [32]byte
	copy(key[:], label)
	return key
}

func seedTemplatesOrg(t *testing.T, q *gen.Queries, slug string) uuid.UUID {
	t.Helper()
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

func seedEditorPlaybook(t *testing.T, d *db.DB, orgID uuid.UUID) uuid.UUID {
	t.Helper()
	p, err := playbooks.NewSQLStore(d).Create(db.WithOrgScope(ctx, orgID), playbooks.Playbook{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Source: playbooks.SourceEditor,
		Inline: ptr("- hosts: all\n  tasks: []\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func seedSSHCredential(t *testing.T, d *db.DB, orgID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	svc := credentials.NewService(credentials.NewSQLStore(d), crypto.NewBox(testBoxKey("templates-store-key-0123456789")), time.Now, rand.Reader)
	c, err := svc.Create(db.WithOrgScope(ctx, orgID), orgID, credentials.CreateInput{
		Name: name, Kind: credentials.KindSSHKey, Username: "ansible",
		Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func ptr(s string) *string { return &s }

// TestSQLStoreRoundTrip covers a fully populated template end to end: scope
// with group and hosts, limit, extra vars, schema and credential.
func TestSQLStoreRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedTemplatesOrg(t, q, "tmpl-org")
	other := seedTemplatesOrg(t, q, "tmpl-other")
	scoped := db.WithOrgScope(ctx, org)
	playbookID := seedEditorPlaybook(t, d, org)
	credID := seedSSHCredential(t, d, org, "login")
	store := templates.NewSQLStore(d)

	groupID, hostID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	created, err := store.Create(scoped, templates.Template{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, PlaybookID: playbookID, Name: "deploy",
		Scope: templates.Scope{GroupID: &groupID, HostIDs: []uuid.UUID{hostID}},
		Limit: "web1", ExtraVars: map[string]any{"greeting": "hello", "retries": float64(3), "opts": map[string]any{"a": true}},
		ExtraVarsSchema: &schema.Schema{Fields: []schema.Field{{Name: "greeting", Type: schema.TypeString}}},
		CredentialID:    &credID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("timestamps = %+v", created)
	}

	got, err := store.Get(scoped, org, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != created.ID || got.OrgID != org || got.PlaybookID != playbookID || got.Name != "deploy" ||
		got.Limit != "web1" || got.Scope.GroupID == nil || *got.Scope.GroupID != groupID ||
		len(got.Scope.HostIDs) != 1 || got.Scope.HostIDs[0] != hostID {
		t.Fatalf("got = %+v", got)
	}
	if got.ExtraVars["greeting"] != "hello" || got.ExtraVars["retries"] != float64(3) {
		t.Fatalf("extra vars = %#v", got.ExtraVars)
	}
	if got.ExtraVarsSchema == nil || len(got.ExtraVarsSchema.Fields) != 1 || got.ExtraVarsSchema.Fields[0].Name != "greeting" {
		t.Fatalf("schema = %+v", got.ExtraVarsSchema)
	}
	if got.CredentialID == nil || *got.CredentialID != credID {
		t.Fatalf("credential = %+v", got.CredentialID)
	}

	// Cross-org reads and deletes are exactly "not found".
	if _, err := store.Get(db.WithOrgScope(ctx, other), other, created.ID); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("cross-org Get = %v, want ErrNotFound", err)
	}

	updated, err := store.Update(scoped, templates.Template{
		ID: created.ID, OrgID: org, PlaybookID: playbookID, Name: "deploy-2",
		ExtraVars: map[string]any{"greeting": "again"},
	}, nil)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "deploy-2" || updated.Scope.GroupID != nil || len(updated.Scope.HostIDs) != 0 ||
		updated.Limit != "" || updated.ExtraVarsSchema != nil || updated.CredentialID != nil {
		t.Fatalf("updated = %+v, want a full replacement", updated)
	}
	if updated.ExtraVars["greeting"] != "again" {
		t.Fatalf("extra vars = %#v", updated.ExtraVars)
	}
	if _, err := store.Update(scoped, templates.Template{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "x"}, nil); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("unknown Update = %v, want ErrNotFound", err)
	}

	if err := store.Delete(scoped, org, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(scoped, org, created.ID); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("second Delete = %v, want ErrNotFound", err)
	}
	if err := store.Delete(db.WithOrgScope(ctx, other), other, created.ID); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("cross-org Delete = %v, want ErrNotFound", err)
	}
}

// TestSQLStoreEmptyScopeAndSchema decodes the whole-org defaults: an empty
// scope round-trips to the zero Scope (group_id null, host_ids empty), a
// missing schema to nil, empty extra vars to a non-nil empty object.
func TestSQLStoreEmptyScopeAndSchema(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedTemplatesOrg(t, q, "tmpl-empty-org")
	scoped := db.WithOrgScope(ctx, org)
	playbookID := seedEditorPlaybook(t, d, org)
	store := templates.NewSQLStore(d)

	created, err := store.Create(scoped, templates.Template{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, PlaybookID: playbookID, Name: "whole-org",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := store.Get(scoped, org, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Scope.Empty() || got.Scope.GroupID != nil || len(got.Scope.HostIDs) != 0 {
		t.Fatalf("scope = %+v, want the zero (whole-org) scope", got.Scope)
	}
	if got.ExtraVarsSchema != nil {
		t.Fatalf("schema = %+v, want nil", got.ExtraVarsSchema)
	}
	if got.ExtraVars == nil || len(got.ExtraVars) != 0 {
		t.Fatalf("extra vars = %#v, want an empty object", got.ExtraVars)
	}

	// A stored {} scope (the shape earlier seeds used) decodes the same.
	if _, err := d.Exec(scoped, `UPDATE job_templates SET scope = '{}' WHERE id = $1`, created.ID); err != nil {
		t.Fatal(err)
	}
	if got, err = store.Get(scoped, org, created.ID); err != nil || !got.Scope.Empty() {
		t.Fatalf("{} scope = %+v, %v, want the zero scope", got.Scope, err)
	}
}

// TestSQLStoreNameUniquePerOrg proves the (org_id, name) uniqueness: two
// organizations may share a name, one may not.
func TestSQLStoreNameUniquePerOrg(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedTemplatesOrg(t, q, "tmpl-uniq-org")
	other := seedTemplatesOrg(t, q, "tmpl-uniq-other")
	scoped := db.WithOrgScope(ctx, org)
	playbookID := seedEditorPlaybook(t, d, org)
	store := templates.NewSQLStore(d)

	first := templates.Template{ID: uuid.Must(uuid.NewV7()), OrgID: org, PlaybookID: playbookID, Name: "deploy"}
	if _, err := store.Create(scoped, first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(scoped, templates.Template{ID: uuid.Must(uuid.NewV7()), OrgID: org, PlaybookID: playbookID, Name: "deploy"}); !errors.Is(err, templates.ErrNameTaken) {
		t.Fatalf("same-org clash = %v, want ErrNameTaken", err)
	}
	if _, err := store.Update(scoped, templates.Template{ID: first.ID, OrgID: org, PlaybookID: playbookID, Name: "deploy", ExtraVars: map[string]any{}}, nil); err != nil {
		t.Fatalf("Update keeping its own name: %v", err)
	}
	if _, err := store.Create(db.WithOrgScope(ctx, other), templates.Template{
		ID: uuid.Must(uuid.NewV7()), OrgID: other, PlaybookID: playbookID, Name: "deploy",
	}); !errors.Is(err, templates.ErrNotFound) {
		// The playbook belongs to org, so this cross-org insert fails on the
		// foreign key first — the unique index is per org, covered above.
		t.Fatalf("cross-org create = %v, want the FK's ErrNotFound", err)
	}
}

// TestSQLStoreForeignKeysRejectCrossOrgReferences proves the composite FKs:
// a playbook or credential belonging to another organization cannot be
// referenced — surfacing as the mapped ErrNotFound.
func TestSQLStoreForeignKeysRejectCrossOrgReferences(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedTemplatesOrg(t, q, "tmpl-fk-org")
	scoped := db.WithOrgScope(ctx, org)
	otherPlaybook := seedEditorPlaybook(t, d, seedTemplatesOrg(t, q, "tmpl-fk-other"))
	otherCred := seedSSHCredential(t, d, seedTemplatesOrg(t, q, "tmpl-fk-other2"), "foreign-login")
	store := templates.NewSQLStore(d)

	if _, err := store.Create(scoped, templates.Template{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, PlaybookID: otherPlaybook, Name: "pb-foreign",
	}); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("foreign playbook = %v, want ErrNotFound", err)
	}
	localPlaybook := seedEditorPlaybook(t, d, org)
	if _, err := store.Create(scoped, templates.Template{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, PlaybookID: localPlaybook, Name: "cred-foreign", CredentialID: &otherCred,
	}); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("foreign credential = %v, want ErrNotFound", err)
	}
}

// failingVaultStore is the real SQL store with one write made to fail:
// everything, including InTx, goes to Postgres, so a rollback here is a
// real rollback and not a fake's bookkeeping.
type failingVaultStore struct {
	*templates.SQLStore
	err error
}

func (s failingVaultStore) ReplaceVaultCredentials(context.Context, uuid.UUID, uuid.UUID, []templates.VaultCredential) error {
	return s.err
}

// TestATemplateAndItsVaultCredentialsLandTogetherOrNotAtAll is backlog
// 2.95 for templates: the row names repositories, but templates.Service
// had the identical split from the identical v0.2e work — Create wrote
// the template row and then its labelled vault credential set as two
// separate statements. A failure in between left a template stored with
// none of the vault passwords its request named, which shows up not at
// save time but as a decryption failure on the next run.
func TestATemplateAndItsVaultCredentialsLandTogetherOrNotAtAll(t *testing.T) {
	d := testdb.Open(t)
	org := seedTemplatesOrg(t, gen.New(d.Pool()), "tmpl-atomic")
	playbookID := seedEditorPlaybook(t, d, org)
	cred := seedTemplateVaultCredential(t, d, org, "prod vault")
	real := templates.NewSQLStore(d)
	store := failingVaultStore{SQLStore: real, err: errors.New("the connection went away")}
	// The real credential store, so the service's own validation of the
	// named vault credential runs for real and the failure under test is
	// the store write, not a short-circuit before it.
	svc := templates.NewService(store, playbookStoreLookup{playbooks.NewSQLStore(d)}, nil, nil, credentials.NewSQLStore(d), crypto.NewBox(testBoxKey("tmpl-atomic-key-1234")))

	scoped := db.WithOrgScope(ctx, org)
	if _, err := svc.Create(scoped, org, templates.CreateInput{
		Name: "deploy", PlaybookID: playbookID,
		VaultCredentials: []templates.VaultCredential{{CredentialID: cred, Label: "prod"}},
	}); err == nil {
		t.Fatal("want the vault credential write's failure")
	}

	list, err := real.List(scoped, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("%d templates survived a failed vault credential write: the row and its vault passwords must land together or not at all", len(list))
	}
}

func revPtr(n int64) *int64 { return &n }

// TestUpdateTemplateHonoursRevision: a template write names the revision
// it is based on, or nil to skip the check (API clients without If-Match).
// A stale revision is ErrRevisionMismatch, an unknown id ErrNotFound; each
// write bumps the revision. UpdateVariables follows the same guard and
// writes only the variables columns.
func TestUpdateTemplateHonoursRevision(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedTemplatesOrg(t, q, "tmpl-revision")
	scoped := db.WithOrgScope(ctx, org)
	playbookID := seedEditorPlaybook(t, d, org)
	store := templates.NewSQLStore(d)

	created, err := store.Create(scoped, templates.Template{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, PlaybookID: playbookID, Name: "deploy", ExtraVars: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Revision != 1 || created.VarsMode != templates.ModeOff || created.VarsSourceRev != "" || created.VarsAppliedAt != nil {
		t.Fatalf("created = rev %d mode %q source rev %q applied %v, want 1, off, empty, nil",
			created.Revision, created.VarsMode, created.VarsSourceRev, created.VarsAppliedAt)
	}

	next := created
	next.Name = "deploy-2"
	got, err := store.Update(scoped, next, revPtr(1))
	if err != nil || got.Revision != 2 || got.Name != "deploy-2" {
		t.Fatalf("Update(if 1) = rev %d name %q, %v; want 2, deploy-2", got.Revision, got.Name, err)
	}
	if _, err := store.Update(scoped, next, revPtr(1)); !errors.Is(err, templates.ErrRevisionMismatch) {
		t.Fatalf("Update(stale 1) = %v, want ErrRevisionMismatch", err)
	}
	if got, err = store.Update(scoped, next, nil); err != nil || got.Revision != 3 {
		t.Fatalf("Update(nil) = rev %d, %v; want 3", got.Revision, err)
	}
	unknown := templates.Template{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "x", ExtraVars: map[string]any{}}
	if _, err := store.Update(scoped, unknown, revPtr(1)); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("Update(unknown, if 1) = %v, want ErrNotFound", err)
	}
	if _, err := store.Update(scoped, unknown, nil); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("Update(unknown, nil) = %v, want ErrNotFound", err)
	}

	applied := time.Now().UTC().Truncate(time.Microsecond)
	vars := got
	vars.Name = "ignored-by-update-variables"
	vars.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{{Name: "region", Type: schema.TypeString}}}
	vars.ExtraVars = map[string]any{"region": "eu"}
	vars.VarsMode, vars.VarsSourceRev, vars.VarsAppliedAt = templates.ModeManual, "c1", &applied
	got, err = store.UpdateVariables(scoped, vars, revPtr(3))
	if err != nil {
		t.Fatalf("UpdateVariables(if 3): %v", err)
	}
	if got.Revision != 4 || got.Name != "deploy-2" || got.ExtraVars["region"] != "eu" || got.ExtraVarsSchema == nil ||
		got.VarsMode != templates.ModeManual || got.VarsSourceRev != "c1" || got.VarsAppliedAt == nil || !got.VarsAppliedAt.Equal(applied) {
		t.Fatalf("UpdateVariables = %+v", got)
	}
	if _, err := store.UpdateVariables(scoped, vars, revPtr(3)); !errors.Is(err, templates.ErrRevisionMismatch) {
		t.Fatalf("UpdateVariables(stale 3) = %v, want ErrRevisionMismatch", err)
	}
	if _, err := store.UpdateVariables(scoped, unknown, nil); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("UpdateVariables(unknown) = %v, want ErrNotFound", err)
	}
	read, err := store.Get(scoped, org, created.ID)
	if err != nil || read.Revision != 4 || read.VarsMode != templates.ModeManual || read.VarsSourceRev != "c1" {
		t.Fatalf("Get = %+v, %v", read, err)
	}
}
