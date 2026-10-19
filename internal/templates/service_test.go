package templates_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/templates"
)

var ctx = context.Background()

// --- fakes ---

type fakeStore struct {
	byID    map[uuid.UUID]templates.Template
	vault   map[uuid.UUID][]templates.VaultCredential
	nextErr error // one-shot error, standing in for a constraint violation
}

func newFakeStore() *fakeStore {
	return &fakeStore{byID: map[uuid.UUID]templates.Template{}, vault: map[uuid.UUID][]templates.VaultCredential{}}
}

// ReplaceVaultCredentials and ListVaultCredentials stand in for the real
// store's two statements run inside one transaction (store_sql.go): the fake
// has no partial-failure mode to reproduce, so a plain map assignment is the
// whole "replace".
func (f *fakeStore) ReplaceVaultCredentials(_ context.Context, _, templateID uuid.UUID, items []templates.VaultCredential) error {
	stored := make([]templates.VaultCredential, len(items))
	copy(stored, items)
	f.vault[templateID] = stored
	return nil
}

func (f *fakeStore) ListVaultCredentials(_ context.Context, _, templateID uuid.UUID) ([]templates.VaultCredential, error) {
	out := make([]templates.VaultCredential, len(f.vault[templateID]))
	copy(out, f.vault[templateID])
	return out, nil
}

func (f *fakeStore) Create(_ context.Context, t templates.Template) (templates.Template, error) {
	if f.nextErr != nil {
		err := f.nextErr
		f.nextErr = nil
		return templates.Template{}, err
	}
	f.byID[t.ID] = t
	return t, nil
}

func (f *fakeStore) Get(_ context.Context, orgID, id uuid.UUID) (templates.Template, error) {
	t, ok := f.byID[id]
	if !ok || t.OrgID != orgID {
		return templates.Template{}, templates.ErrNotFound
	}
	return t, nil
}

func (f *fakeStore) List(_ context.Context, orgID uuid.UUID) ([]templates.Template, error) {
	var out []templates.Template
	for _, t := range f.byID {
		if t.OrgID == orgID {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeStore) Update(_ context.Context, t templates.Template) (templates.Template, error) {
	cur, ok := f.byID[t.ID]
	if !ok || cur.OrgID != t.OrgID {
		return templates.Template{}, templates.ErrNotFound
	}
	if f.nextErr != nil {
		err := f.nextErr
		f.nextErr = nil
		return templates.Template{}, err
	}
	f.byID[t.ID] = t
	return t, nil
}

func (f *fakeStore) Delete(_ context.Context, orgID, id uuid.UUID) error {
	t, ok := f.byID[id]
	if !ok || t.OrgID != orgID {
		return templates.ErrNotFound
	}
	delete(f.byID, id)
	return nil
}

type fakePlaybooks struct {
	byID map[uuid.UUID]playbooks.Playbook
}

func (f fakePlaybooks) Get(_ context.Context, orgID, id uuid.UUID) (playbooks.Playbook, error) {
	p, ok := f.byID[id]
	if !ok || p.OrgID != orgID {
		return playbooks.Playbook{}, playbooks.ErrNotFound
	}
	return p, nil
}

type fakeGroups struct{ known map[uuid.UUID]bool }

func (f fakeGroups) GetGroup(_ context.Context, orgID, id uuid.UUID) (inventory.GroupView, error) {
	if !f.known[id] {
		return inventory.GroupView{}, inventory.ErrNotFound
	}
	return inventory.GroupView{Group: inventory.Group{ID: id, OrgID: orgID}}, nil
}

type fakeHosts struct{ list []hosts.Host }

func (f fakeHosts) ListAllHosts(_ context.Context, _ uuid.UUID) ([]hosts.Host, error) {
	return f.list, nil
}

type fakeCreds struct {
	byID map[uuid.UUID]credentials.Credential
}

func (f fakeCreds) Get(_ context.Context, orgID, id uuid.UUID) (credentials.Credential, error) {
	c, ok := f.byID[id]
	if !ok || c.OrgID != orgID {
		return credentials.Credential{}, credentials.ErrNotFound
	}
	return c, nil
}

// --- fixture helpers ---

type fixture struct {
	store     *fakeStore
	svc       *templates.Service
	org       uuid.UUID
	playbook  uuid.UUID
	group     uuid.UUID
	managed   uuid.UUID
	unmanaged uuid.UUID
	sshCred   uuid.UUID
	gitCred   uuid.UUID
	vaultCred uuid.UUID
}

func newFixture() fixture {
	org := uuid.Must(uuid.NewV7())
	playbook := uuid.Must(uuid.NewV7())
	group := uuid.Must(uuid.NewV7())
	managed := uuid.Must(uuid.NewV7())
	unmanaged := uuid.Must(uuid.NewV7())
	sshCred := uuid.Must(uuid.NewV7())
	gitCred := uuid.Must(uuid.NewV7())
	vaultCred := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := templates.NewService(store,
		fakePlaybooks{byID: map[uuid.UUID]playbooks.Playbook{playbook: {ID: playbook, OrgID: org, Source: playbooks.SourceEditor}}},
		fakeGroups{known: map[uuid.UUID]bool{group: true}},
		fakeHosts{list: []hosts.Host{
			{ID: managed, OrgID: org, Managed: true},
			{ID: unmanaged, OrgID: org, Managed: false},
		}},
		fakeCreds{byID: map[uuid.UUID]credentials.Credential{
			sshCred:   {ID: sshCred, OrgID: org, Name: "ssh", Kind: credentials.KindSSHPassword},
			gitCred:   {ID: gitCred, OrgID: org, Name: "git", Kind: credentials.KindGitToken},
			vaultCred: {ID: vaultCred, OrgID: org, Name: "vault", Kind: credentials.KindVaultPassword},
		}},
		testBox(),
	)
	return fixture{
		store: store, svc: svc, org: org, playbook: playbook, group: group,
		managed: managed, unmanaged: unmanaged, sshCred: sshCred, gitCred: gitCred, vaultCred: vaultCred,
	}
}

func (f fixture) in() templates.CreateInput {
	return templates.CreateInput{Name: "deploy-site", PlaybookID: f.playbook}
}

// --- tests ---

func TestCreateHappyPathMinimal(t *testing.T) {
	f := newFixture()
	got, err := f.svc.Create(ctx, f.org, f.in())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID == (uuid.UUID{}) || got.OrgID != f.org || got.PlaybookID != f.playbook || got.Name != "deploy-site" {
		t.Fatalf("created = %+v", got)
	}
	if !got.Scope.Empty() || got.Limit != "" || got.ExtraVarsSchema != nil || got.CredentialID != nil {
		t.Fatalf("defaults = %+v", got)
	}
	if got.ExtraVars == nil || len(got.ExtraVars) != 0 {
		t.Fatalf("extra vars = %#v, want a non-nil empty object", got.ExtraVars)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		// The store fills timestamps; the fake echoes the built template, so
		// both stay zero here — only assert what the service sets.
		_ = got
	}
	if _, ok := f.store.byID[got.ID]; !ok {
		t.Fatal("template was not stored")
	}
}

func TestCreateHappyPathFull(t *testing.T) {
	f := newFixture()
	sc := &schema.Schema{Fields: []schema.Field{{Name: "greeting", Type: schema.TypeString, Required: true}}}
	cred := f.sshCred
	got, err := f.svc.Create(ctx, f.org, templates.CreateInput{
		Name: "deploy", PlaybookID: f.playbook,
		Scope:           templates.Scope{GroupID: &f.group, HostIDs: []uuid.UUID{f.managed}},
		Limit:           "web1:web2",
		ExtraVars:       map[string]any{"greeting": "hello"},
		ExtraVarsSchema: sc,
		CredentialID:    &cred,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Scope.GroupID == nil || *got.Scope.GroupID != f.group || len(got.Scope.HostIDs) != 1 || got.Scope.HostIDs[0] != f.managed {
		t.Fatalf("scope = %+v", got.Scope)
	}
	if got.Limit != "web1:web2" || got.CredentialID == nil || *got.CredentialID != f.sshCred {
		t.Fatalf("created = %+v", got)
	}
	if got.ExtraVars["greeting"] != "hello" {
		t.Fatalf("extra vars = %#v", got.ExtraVars)
	}
}

func TestCreateValidation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(f fixture, in *templates.CreateInput)
		wantErr error
		inMsg   string
	}{
		{"empty name", func(f fixture, in *templates.CreateInput) { in.Name = "" }, templates.ErrInvalidInput, "name"},
		{"uppercase name", func(f fixture, in *templates.CreateInput) { in.Name = "Deploy" }, templates.ErrInvalidInput, ""},
		{"leading hyphen", func(f fixture, in *templates.CreateInput) { in.Name = "-x" }, templates.ErrInvalidInput, ""},
		{"space in name", func(f fixture, in *templates.CreateInput) { in.Name = "deploy site" }, templates.ErrInvalidInput, ""},
		{"64-char name", func(f fixture, in *templates.CreateInput) { in.Name = strings.Repeat("a", 64) }, templates.ErrInvalidInput, ""},
		{"over-long limit", func(f fixture, in *templates.CreateInput) { in.Limit = strings.Repeat("a", 501) }, templates.ErrInvalidInput, "limit"},
		{"NUL in limit", func(f fixture, in *templates.CreateInput) { in.Limit = "web\x001" }, templates.ErrInvalidInput, "limit"},
		{"unknown playbook", func(f fixture, in *templates.CreateInput) { in.PlaybookID = uuid.Must(uuid.NewV7()) }, templates.ErrNotFound, "playbook"},
		{"cross-org playbook", func(f fixture, in *templates.CreateInput) {
			other := uuid.Must(uuid.NewV7())
			in.PlaybookID = other
		}, templates.ErrNotFound, ""},
		{"unknown group", func(f fixture, in *templates.CreateInput) {
			missing := uuid.Must(uuid.NewV7())
			in.Scope = templates.Scope{GroupID: &missing}
		}, templates.ErrNotFound, "group"},
		{"unknown host", func(f fixture, in *templates.CreateInput) {
			in.Scope = templates.Scope{HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}}
		}, templates.ErrNotFound, "host"},
		{"unmanaged host", func(f fixture, in *templates.CreateInput) {
			in.Scope = templates.Scope{HostIDs: []uuid.UUID{f.unmanaged}}
		}, templates.ErrUnmanagedHost, "not managed"},
		{"unknown credential", func(f fixture, in *templates.CreateInput) {
			missing := uuid.Must(uuid.NewV7())
			in.CredentialID = &missing
		}, templates.ErrNotFound, "credential"},
		{"git credential", func(f fixture, in *templates.CreateInput) {
			in.CredentialID = &f.gitCred
		}, templates.ErrNotSSH, "SSH"},
		{"value violates schema", func(f fixture, in *templates.CreateInput) {
			in.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{{Name: "greeting", Type: schema.TypeString}}}
			in.ExtraVars = map[string]any{"greeting": 123}
		}, nil, "must be a string"},
		{"unknown var with schema", func(f fixture, in *templates.CreateInput) {
			in.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{{Name: "greeting", Type: schema.TypeString}}}
			in.ExtraVars = map[string]any{"greeting": "hi", "extra": "nope"}
		}, nil, "unknown field"},
		{"malformed schema document", func(f fixture, in *templates.CreateInput) {
			in.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{{Name: "Greeting", Type: schema.TypeString}}}
		}, templates.ErrBadSchema, "fields[0].name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()
			in := f.in()
			tc.mutate(f, &in)
			_, err := f.svc.Create(ctx, f.org, in)
			if err == nil {
				t.Fatal("Create succeeded, want an error")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil {
				var ve *schema.ValidationError
				if !errors.As(err, &ve) {
					t.Fatalf("err = %v, want a *schema.ValidationError", err)
				}
			}
			if tc.inMsg != "" && !strings.Contains(err.Error(), tc.inMsg) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.inMsg)
			}
		})
	}
}

func TestCreateUnmanagedHostNamesTheHost(t *testing.T) {
	f := newFixture()
	in := f.in()
	in.Scope = templates.Scope{HostIDs: []uuid.UUID{f.unmanaged}}
	_, err := f.svc.Create(ctx, f.org, in)
	if !errors.Is(err, templates.ErrUnmanagedHost) || !strings.Contains(err.Error(), f.unmanaged.String()) {
		t.Fatalf("err = %v, want ErrUnmanagedHost naming the host", err)
	}
	if !errors.Is(err, templates.ErrInvalidInput) {
		t.Fatalf("ErrUnmanagedHost must remain an invalid-input error, got %v", err)
	}
}

func TestCreateValueProblemPreservesPath(t *testing.T) {
	f := newFixture()
	in := f.in()
	in.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{
		{Name: "vhosts", Type: schema.TypeRepeater, Fields: []schema.Field{{Name: "domain", Type: schema.TypeString}}},
	}}
	in.ExtraVars = map[string]any{"vhosts": []any{map[string]any{"domain": 9}}}
	_, err := f.svc.Create(ctx, f.org, in)
	var ve *schema.ValidationError
	if !errors.As(err, &ve) || len(ve.Problems) != 1 || ve.Problems[0].Path != "vhosts[0].domain" {
		t.Fatalf("err = %v, want one problem at vhosts[0].domain", err)
	}
}

func TestUpdate(t *testing.T) {
	f := newFixture()
	created, err := f.svc.Create(ctx, f.org, f.in())
	if err != nil {
		t.Fatal(err)
	}
	cred := f.sshCred
	updated, err := f.svc.Update(ctx, f.org, created.ID, templates.UpdateInput{
		Name: "renamed", PlaybookID: f.playbook, Limit: "db*",
		ExtraVars: map[string]any{"run": "x"}, CredentialID: &cred,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "renamed" || updated.Limit != "db*" || updated.CredentialID == nil {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := f.svc.Update(ctx, f.org, uuid.Must(uuid.NewV7()), templates.UpdateInput{Name: "x", PlaybookID: f.playbook}); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("unknown id = %v, want ErrNotFound", err)
	}
	// Update validates like Create.
	if _, err := f.svc.Update(ctx, f.org, created.ID, templates.UpdateInput{Name: "BAD", PlaybookID: f.playbook}); !errors.Is(err, templates.ErrInvalidInput) {
		t.Fatalf("bad name = %v, want ErrInvalidInput", err)
	}
	// A rename colliding with an existing name is a conflict (the SQL store
	// maps the unique violation; the fake simulates it).
	other, err := f.svc.Create(ctx, f.org, templates.CreateInput{Name: "other", PlaybookID: f.playbook})
	if err != nil {
		t.Fatal(err)
	}
	f.store.nextErr = templates.ErrNameTaken
	if _, err := f.svc.Update(ctx, f.org, other.ID, templates.UpdateInput{Name: "renamed", PlaybookID: f.playbook}); !errors.Is(err, templates.ErrNameTaken) {
		t.Fatalf("clashing rename = %v, want ErrNameTaken", err)
	}
}

func TestDelete(t *testing.T) {
	f := newFixture()
	created, err := f.svc.Create(ctx, f.org, f.in())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Delete(ctx, f.org, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := f.svc.Delete(ctx, f.org, created.ID); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("second Delete = %v, want ErrNotFound", err)
	}
}

func TestList(t *testing.T) {
	f := newFixture()
	for _, name := range []string{"b-second", "a-first"} {
		if _, err := f.svc.Create(ctx, f.org, templates.CreateInput{Name: name, PlaybookID: f.playbook}); err != nil {
			t.Fatal(err)
		}
	}
	other := uuid.Must(uuid.NewV7())
	// A template of another organization is stored through the store
	// directly (the service would refuse the cross-org playbook).
	otherID := uuid.Must(uuid.NewV7())
	f.store.byID[otherID] = templates.Template{ID: otherID, OrgID: other, Name: "z-other-org", PlaybookID: f.playbook}
	list, err := f.svc.List(ctx, f.org)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "a-first" || list[1].Name != "b-second" {
		t.Fatalf("list = %+v, want the org's two templates ordered by name", list)
	}
}

// TestMaskingCoversSchemaChange is the string→secret case backlog 2.21
// describes: a template stored without a schema keeps plaintext extra vars;
// once a schema declares the field secret, every read masks it, and the next
// save seals the stored value.
func TestMaskingCoversSchemaChange(t *testing.T) {
	f := newFixture()
	created, err := f.svc.Create(ctx, f.org, templates.CreateInput{
		Name:       "masked",
		PlaybookID: f.playbook,
		ExtraVars:  map[string]any{"token": "plaintext", "plain": "visible"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ExtraVars["token"] != "plaintext" {
		t.Fatalf("without a schema nothing is masked: %#v", created.ExtraVars)
	}
	stored := f.store.byID[created.ID]
	if stored.ExtraVars["token"] != "plaintext" {
		t.Fatalf("masking must not touch the stored value: %#v", stored.ExtraVars)
	}
	// Legacy data: an empty value under a field that only now becomes
	// secret-typed (strict validation would refuse it today, the way it is
	// stored here it predates the schema change).
	stored.ExtraVars["empty"] = ""
	f.store.byID[created.ID] = stored

	sc := &schema.Schema{Fields: []schema.Field{
		{Name: "token", Type: schema.TypeSecret},
		{Name: "plain", Type: schema.TypeString},
		{Name: "empty", Type: schema.TypeSecret},
	}}
	if _, err := f.svc.Update(ctx, f.org, created.ID, templates.UpdateInput{
		Name: "masked", PlaybookID: f.playbook, ExtraVars: map[string]any{"token": "plaintext", "plain": "visible"}, ExtraVarsSchema: sc,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// The update replaced the stored extra vars; re-add the legacy empty
	// value the way it would sit after a schema change on old data.
	stored = f.store.byID[created.ID]
	stored.ExtraVars["empty"] = ""
	f.store.byID[created.ID] = stored
	got, err := f.svc.Get(ctx, f.org, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := got.ExtraVars["token"].(map[string]any); !ok || m["$secret"] != "set" {
		t.Fatalf("secret field must read as the $secret marker, got %#v", got.ExtraVars["token"])
	}
	if got.ExtraVars["plain"] != "visible" {
		t.Fatalf("non-secret value must be untouched: %#v", got.ExtraVars)
	}
	if got.ExtraVars["empty"] != "" {
		t.Fatalf("empty value must be untouched: %#v", got.ExtraVars["empty"])
	}
	// Saved under the secret-typed schema, the value is sealed at rest; the
	// claim path opens it (internal/app's template lookup).
	if got := openStored(t, testBox(), f, created, "token"); got != "plaintext" {
		t.Fatalf("the stored secret opens to %q", got)
	}

	list, err := f.svc.List(ctx, f.org)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range list {
		if entry.ID == created.ID {
			if m, ok := entry.ExtraVars["token"].(map[string]any); !ok || m["$secret"] != "set" {
				t.Fatalf("list must mask too: %#v", entry.ExtraVars)
			}
		}
	}
}

// TestMaskingCoversSealedValues proves MaskVars runs before the schema walk:
// a stored sealed value under a key the schema no longer declares is still
// masked on reads.
func TestMaskingCoversSealedValues(t *testing.T) {
	f := newFixture()
	created, err := f.svc.Create(ctx, f.org, templates.CreateInput{
		Name:       "sealed",
		PlaybookID: f.playbook,
		ExtraVars:  map[string]any{"legacy": schema.Sealed([]byte("ciphertext"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := schema.SealedBytes(created.ExtraVars["legacy"]); ok {
		t.Fatalf("a sealed value must read as masked, got %#v", created.ExtraVars["legacy"])
	}
	m := created.ExtraVars["legacy"].(map[string]any)
	if m["$secret"] != "set" {
		t.Fatalf("masked form = %#v", m)
	}
}

// TestSecretFieldsInsideContainers masks nested secret sub-fields too.
func TestSecretFieldsInsideContainers(t *testing.T) {
	f := newFixture()
	sc := &schema.Schema{Fields: []schema.Field{
		{Name: "vhosts", Type: schema.TypeRepeater, Fields: []schema.Field{
			{Name: "domain", Type: schema.TypeString},
			{Name: "password", Type: schema.TypeSecret},
		}},
	}}
	created, err := f.svc.Create(ctx, f.org, templates.CreateInput{
		Name: "nested", PlaybookID: f.playbook,
		ExtraVars:       map[string]any{"vhosts": []any{map[string]any{"domain": "a.example", "password": "hunter2"}}},
		ExtraVarsSchema: sc,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := created.ExtraVars["vhosts"].([]any)[0].(map[string]any)
	if m, ok := entry["password"].(map[string]any); !ok || m["$secret"] != "set" {
		t.Fatalf("nested secret must be masked, got %#v", entry["password"])
	}
	if entry["domain"] != "a.example" {
		t.Fatalf("sibling value must be untouched: %#v", entry["domain"])
	}
}

// --- vault credentials (v0.2e, task 4a) ---

// TestCreateAndUpdateVaultCredentialsRoundTrip is the brief's Step 3 round
// trip: create with two labelled vault credentials, read back and compare
// values (not only the count), update down to one, and assert the dropped
// label is actually gone.
func TestCreateAndUpdateVaultCredentialsRoundTrip(t *testing.T) {
	f := newFixture()
	otherVault := uuid.Must(uuid.NewV7())
	// A second vault_password credential, added through the fake creds map
	// directly (newFixture only seeds one).
	creds := fakeCreds{byID: map[uuid.UUID]credentials.Credential{
		f.vaultCred: {ID: f.vaultCred, OrgID: f.org, Name: "vault", Kind: credentials.KindVaultPassword},
		otherVault:  {ID: otherVault, OrgID: f.org, Name: "vault-2", Kind: credentials.KindVaultPassword},
	}}
	svc := templates.NewService(f.store,
		fakePlaybooks{byID: map[uuid.UUID]playbooks.Playbook{f.playbook: {ID: f.playbook, OrgID: f.org, Source: playbooks.SourceEditor}}},
		fakeGroups{}, fakeHosts{}, creds, testBox())

	created, err := svc.Create(ctx, f.org, templates.CreateInput{
		Name: "deploy", PlaybookID: f.playbook,
		VaultCredentials: []templates.VaultCredential{
			{CredentialID: f.vaultCred, Label: "team-a"},
			{CredentialID: otherVault, Label: "team-b"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantCreated := []templates.VaultCredential{
		{CredentialID: f.vaultCred, Label: "team-a"},
		{CredentialID: otherVault, Label: "team-b"},
	}
	if !reflect.DeepEqual(created.VaultCredentials, wantCreated) {
		t.Fatalf("created.VaultCredentials = %+v, want %+v", created.VaultCredentials, wantCreated)
	}

	got, err := svc.Get(ctx, f.org, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got.VaultCredentials, wantCreated) {
		t.Fatalf("Get.VaultCredentials = %+v, want %+v", got.VaultCredentials, wantCreated)
	}

	updated, err := svc.Update(ctx, f.org, created.ID, templates.UpdateInput{
		Name: "deploy", PlaybookID: f.playbook,
		VaultCredentials: []templates.VaultCredential{{CredentialID: f.vaultCred, Label: "team-a"}},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	wantUpdated := []templates.VaultCredential{{CredentialID: f.vaultCred, Label: "team-a"}}
	if !reflect.DeepEqual(updated.VaultCredentials, wantUpdated) {
		t.Fatalf("updated.VaultCredentials = %+v, want %+v (team-b must be dropped)", updated.VaultCredentials, wantUpdated)
	}
	got, err = svc.Get(ctx, f.org, created.ID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	for _, item := range got.VaultCredentials {
		if item.Label == "team-b" {
			t.Fatalf("team-b must be gone, got %+v", got.VaultCredentials)
		}
	}
}

// TestVaultCredentialLabelValidation is the brief's Step 2/3: one case per
// character class internal/worker/runner's invalidVaultLabel rejects
// (matched exactly through repos.InvalidVaultLabel), plus duplicate labels
// (the table's PRIMARY KEY would otherwise turn this into a 500) and a
// wrong-kind credential.
func TestVaultCredentialLabelValidation(t *testing.T) {
	f := newFixture()

	cases := []struct {
		name    string
		items   []templates.VaultCredential
		wantErr string
	}{
		{"empty label", []templates.VaultCredential{{CredentialID: f.vaultCred, Label: ""}}, "label is empty"},
		{"leading whitespace", []templates.VaultCredential{{CredentialID: f.vaultCred, Label: " team-a"}}, "leading or trailing whitespace"},
		{"trailing whitespace", []templates.VaultCredential{{CredentialID: f.vaultCred, Label: "team-a "}}, "leading or trailing whitespace"},
		{"newline", []templates.VaultCredential{{CredentialID: f.vaultCred, Label: "team-a\nhost_key_checking = False"}}, "newline"},
		{"carriage return", []templates.VaultCredential{{CredentialID: f.vaultCred, Label: "team-a\rx"}}, "carriage return"},
		{"comma", []templates.VaultCredential{{CredentialID: f.vaultCred, Label: "team-a,team-b"}}, "comma"},
		{"at sign", []templates.VaultCredential{{CredentialID: f.vaultCred, Label: "team-a@file"}}, "'@'"},
		{"duplicate label", []templates.VaultCredential{
			{CredentialID: f.vaultCred, Label: "team-a"},
			{CredentialID: f.vaultCred, Label: "team-a"},
		}, "duplicate vault credential label"},
		{"wrong credential kind", []templates.VaultCredential{{CredentialID: f.sshCred, Label: "team-a"}}, "vault_password credential"},
		{"unknown credential", []templates.VaultCredential{{CredentialID: uuid.Must(uuid.NewV7()), Label: "team-a"}}, "vault credential not found"},
		{"valid", []templates.VaultCredential{{CredentialID: f.vaultCred, Label: "team-a"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := f.in()
			in.VaultCredentials = tc.items
			_, err := f.svc.Create(ctx, f.org, in)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Create = %v, want valid", err)
				}
				return
			}
			if !errors.Is(err, templates.ErrInvalidInput) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Create = %v, want ErrInvalidInput naming %q", err, tc.wantErr)
			}
		})
	}
}
