package repos_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/repos"
)

var ctx = context.Background()

// --- fakes ---

type fakeStore struct {
	mu         sync.Mutex
	byID       map[uuid.UUID]repos.Repo
	names      map[string]string // name -> id string, for uniqueness bookkeeping
	identities map[uuid.UUID][]repos.VaultIdentity
	nextErr    error
	cleared    []uuid.UUID
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		byID: map[uuid.UUID]repos.Repo{}, names: map[string]string{},
		identities: map[uuid.UUID][]repos.VaultIdentity{},
	}
}

// ReplaceRepoVaultIdentities and ListRepoVaultIdentities stand in for the
// real store's two statements run inside one transaction (store_sql.go):
// the fake has no partial-failure mode to reproduce, so a plain map
// assignment is the whole "replace".
func (f *fakeStore) ReplaceRepoVaultIdentities(_ context.Context, _, repoID uuid.UUID, identities []repos.VaultIdentity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	stored := make([]repos.VaultIdentity, len(identities))
	copy(stored, identities)
	f.identities[repoID] = stored
	return nil
}

func (f *fakeStore) ListRepoVaultIdentities(_ context.Context, _, repoID uuid.UUID) ([]repos.VaultIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]repos.VaultIdentity, len(f.identities[repoID]))
	copy(out, f.identities[repoID])
	return out, nil
}

func (f *fakeStore) Create(_ context.Context, r repos.Repo) (repos.Repo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nextErr != nil {
		return repos.Repo{}, f.nextErr
	}
	if f.names[r.Name] != "" {
		return repos.Repo{}, repos.ErrNameTaken
	}
	f.names[r.Name] = r.ID.String()
	f.byID[r.ID] = r
	return r, nil
}

func (f *fakeStore) Get(_ context.Context, orgID, id uuid.UUID) (repos.Repo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.byID[id]
	if !ok || r.OrgID != orgID {
		return repos.Repo{}, repos.ErrNotFound
	}
	return r, nil
}

func (f *fakeStore) List(_ context.Context, orgID uuid.UUID) ([]repos.Repo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []repos.Repo
	for _, r := range f.byID {
		if r.OrgID == orgID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) Update(_ context.Context, r repos.Repo) (repos.Repo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.byID[r.ID]
	if !ok || cur.OrgID != r.OrgID {
		return repos.Repo{}, repos.ErrNotFound
	}
	if other, taken := f.names[r.Name]; taken && other != r.ID.String() {
		return repos.Repo{}, repos.ErrNameTaken
	}
	delete(f.names, cur.Name)
	f.names[r.Name] = r.ID.String()
	f.byID[r.ID] = r
	return r, nil
}

func (f *fakeStore) Delete(_ context.Context, orgID, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.byID[id]
	if !ok || r.OrgID != orgID {
		return repos.ErrNotFound
	}
	if r.Name == "referenced" {
		return repos.ErrInUse
	}
	delete(f.byID, id)
	return nil
}

func (f *fakeStore) ClearHostKey(_ context.Context, orgID, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.byID[id]
	if !ok || r.OrgID != orgID {
		return repos.ErrNotFound
	}
	r.SSHHostKey, r.SSHHostPublicKey = "", ""
	f.byID[id] = r
	f.cleared = append(f.cleared, id)
	return nil
}

type fakeCreds struct {
	byID map[uuid.UUID]credentials.Credential
}

func (f *fakeCreds) Get(_ context.Context, _, id uuid.UUID) (credentials.Credential, error) {
	c, ok := f.byID[id]
	if !ok {
		return credentials.Credential{}, credentials.ErrNotFound
	}
	return c, nil
}

type fakeEnqueuer struct {
	mu    sync.Mutex
	calls []fakeSyncCall
	err   error
}

type fakeSyncCall struct {
	orgID, repoID uuid.UUID
	actor         *uuid.UUID
}

func (f *fakeEnqueuer) EnqueueSync(_ context.Context, orgID, repoID uuid.UUID, actor *uuid.UUID) (jobs.Job, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeSyncCall{orgID, repoID, actor})
	f.mu.Unlock()
	if f.err != nil {
		return jobs.Job{}, f.err
	}
	return jobs.Job{ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Kind: jobs.KindSyncRepo}, nil
}

func newFakeCreds(byID map[uuid.UUID]credentials.Credential) *fakeCreds {
	if byID == nil {
		byID = map[uuid.UUID]credentials.Credential{}
	}
	return &fakeCreds{byID: byID}
}

func gitKeyCred() credentials.Credential {
	return credentials.Credential{ID: uuid.Must(uuid.NewV7()), Kind: credentials.KindGitKey}
}

func newService(store *fakeStore, creds *fakeCreds, enq *fakeEnqueuer) *repos.Service {
	return repos.NewService(store, creds, enq)
}

// --- tests ---

func TestCreateValidation(t *testing.T) {
	org := uuid.Must(uuid.NewV7())

	cases := []struct {
		name    string
		in      repos.CreateInput
		wantErr string
	}{
		{"valid minimum", repos.CreateInput{Name: "infra", URL: "https://git.example.com/acme/infra.git"}, ""},
		{"valid ssh url", repos.CreateInput{Name: "infra", URL: "ssh://git@git.example.com:2222/acme/infra.git"}, ""},
		{"valid scp-like url", repos.CreateInput{Name: "infra", URL: "git@git.example.com:acme/infra.git"}, ""},
		{"bad name", repos.CreateInput{Name: "Bad Name", URL: "https://git.example.com/x"}, "invalid name"},
		{"empty name", repos.CreateInput{Name: "", URL: "https://git.example.com/x"}, "invalid name"},
		{"empty url", repos.CreateInput{Name: "infra", URL: ""}, "invalid url"},
		{"url with whitespace", repos.CreateInput{Name: "infra", URL: "https://git.example.com/a b"}, "invalid url"},
		{"overlong url", repos.CreateInput{Name: "infra", URL: "https://" + strings.Repeat("a", 493)}, "invalid url"},
		{"plain http url", repos.CreateInput{Name: "infra", URL: "http://git.example.com/x.git"}, "invalid url"},
		{"relative url", repos.CreateInput{Name: "infra", URL: "../repo"}, "invalid url"},
		{"branch with dotdot", repos.CreateInput{Name: "infra", URL: "https://git.example.com/x", Branch: "a..b"}, "invalid branch"},
		{"branch with space", repos.CreateInput{Name: "infra", URL: "https://git.example.com/x", Branch: "a b"}, "invalid branch"},
		// A leading dash would reach git's argument parser as an option
		// (--upload-pack=…, -oProxyCommand=…).
		{"branch with leading dash", repos.CreateInput{Name: "infra", URL: "https://git.example.com/x", Branch: "--upload-pack=touch"}, "invalid branch"},
		{"scp-like url with leading dash", repos.CreateInput{Name: "infra", URL: "-oProxyCommand=touch@evil:x"}, "invalid url"},
		{"subpath with dotdot", repos.CreateInput{Name: "infra", URL: "https://git.example.com/x", Subpath: "playbooks/../site.yml"}, "invalid subpath"},
		{"subpath bad chars", repos.CreateInput{Name: "infra", URL: "https://git.example.com/x", Subpath: "pl*ys"}, "invalid subpath"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newService(newFakeStore(), newFakeCreds(nil), &fakeEnqueuer{})
			_, err := svc.Create(ctx, org, tc.in)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Create = %v, want valid", err)
				}
				return
			}
			if !errors.Is(err, repos.ErrInvalidInput) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Create = %v, want ErrInvalidInput naming %q", err, tc.wantErr)
			}
		})
	}
}

// TestCreateCredentialKindChecks pins the credential rules: the referenced
// credential must exist and be a git_key or git_token credential — an SSH
// credential is ErrInvalidInput, not a store error.
func TestCreateCredentialKindChecks(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	git := gitKeyCred()
	ssh := credentials.Credential{ID: uuid.Must(uuid.NewV7()), Kind: credentials.KindSSHKey}

	cases := []struct {
		name    string
		creds   *fakeCreds
		credID  uuid.UUID
		wantErr string
	}{
		{"git key accepted", newFakeCreds(map[uuid.UUID]credentials.Credential{git.ID: git}), git.ID, ""},
		{"git token accepted", newFakeCreds(map[uuid.UUID]credentials.Credential{git.ID: {ID: git.ID, Kind: credentials.KindGitToken}}), git.ID, ""},
		{"ssh credential rejected", newFakeCreds(map[uuid.UUID]credentials.Credential{ssh.ID: ssh}), ssh.ID, "git_key or git_token"},
		{"unknown credential", newFakeCreds(nil), uuid.Must(uuid.NewV7()), "credential not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newService(newFakeStore(), tc.creds, &fakeEnqueuer{})
			_, err := svc.Create(ctx, org, repos.CreateInput{Name: "infra", URL: "https://git.example.com/x", CredentialID: &tc.credID})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Create = %v, want valid", err)
				}
				return
			}
			if !errors.Is(err, repos.ErrInvalidInput) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Create = %v, want ErrInvalidInput naming %q", err, tc.wantErr)
			}
		})
	}
}

// TestCreateAppliesDefaults proves an empty branch becomes "main" and the
// validated input reaches the store verbatim otherwise.
func TestCreateAppliesDefaults(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store, creds, enq := newFakeStore(), &fakeCreds{}, &fakeEnqueuer{}
	svc := newService(store, creds, enq)
	created, err := svc.Create(ctx, org, repos.CreateInput{Name: "infra", URL: "https://git.example.com/acme/infra.git", Subpath: "playbooks/site.yml"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Branch != repos.DefaultBranch {
		t.Fatalf("branch = %q, want default %q", created.Branch, repos.DefaultBranch)
	}
	if created.Subpath != "playbooks/site.yml" || created.URL != "https://git.example.com/acme/infra.git" {
		t.Fatalf("created = %+v", created)
	}
	if created.ID == uuid.Nil || created.OrgID != org {
		t.Fatalf("created = %+v", created)
	}
}

// TestCreateNameTaken proves a name clash inside the organization is
// ErrNameTaken; the same name in another organization is fine (store's job,
// this only checks propagation).
func TestCreateNameTaken(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := newService(store, &fakeCreds{}, &fakeEnqueuer{})
	if _, err := svc.Create(ctx, org, repos.CreateInput{Name: "infra", URL: "https://git.example.com/x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, org, repos.CreateInput{Name: "infra", URL: "https://git.example.com/y"}); !errors.Is(err, repos.ErrNameTaken) {
		t.Fatalf("duplicate name = %v, want ErrNameTaken", err)
	}
}

// TestUpdateReplacesEverything proves Update replaces every editable field
// (including the name), validates like Create, and maps store outcomes.
func TestUpdateReplacesEverything(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	cred := gitKeyCred()
	creds := newFakeCreds(map[uuid.UUID]credentials.Credential{cred.ID: cred})
	enq := &fakeEnqueuer{}
	svc := newService(store, creds, enq)
	created, err := svc.Create(ctx, org, repos.CreateInput{Name: "infra", URL: "https://git.example.com/acme/infra.git"})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := svc.Update(ctx, org, created.ID, repos.UpdateInput{
		Name: "infra-2", URL: "ssh://git@git.example.com/acme/infra.git",
		Branch: "release/1", Subpath: "site.yml", CredentialID: &cred.ID,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "infra-2" || updated.Branch != "release/1" || updated.Subpath != "site.yml" ||
		updated.CredentialID == nil || *updated.CredentialID != cred.ID ||
		updated.URL != "ssh://git@git.example.com/acme/infra.git" {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := svc.Update(ctx, org, created.ID, repos.UpdateInput{Name: "bad name", URL: created.URL}); !errors.Is(err, repos.ErrInvalidInput) {
		t.Fatalf("update with bad name = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.Create(ctx, org, repos.CreateInput{Name: "other", URL: "https://git.example.com/x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, org, created.ID, repos.UpdateInput{Name: "other", URL: "https://git.example.com/x"}); !errors.Is(err, repos.ErrNameTaken) {
		t.Fatalf("update to a taken name = %v, want ErrNameTaken", err)
	}
	if _, err := svc.Update(ctx, org, uuid.Must(uuid.NewV7()), repos.UpdateInput{Name: "x", URL: "https://git.example.com/x"}); !errors.Is(err, repos.ErrNotFound) {
		t.Fatalf("update unknown = %v, want ErrNotFound", err)
	}
}

// TestDeletePropagatesStoreOutcomes proves ErrNotFound and ErrInUse flow
// through unchanged (the FK mapping itself is the store's integration test).
func TestDeletePropagatesStoreOutcomes(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := newService(store, &fakeCreds{}, &fakeEnqueuer{})
	if err := svc.Delete(ctx, org, uuid.Must(uuid.NewV7())); !errors.Is(err, repos.ErrNotFound) {
		t.Fatalf("delete unknown = %v, want ErrNotFound", err)
	}
	// The fake store flags a repo named "referenced" as still in use —
	// standing in for the FK violation the SQL store maps.
	created, err := svc.Create(ctx, org, repos.CreateInput{Name: "referenced", URL: "https://git.example.com/x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, org, created.ID); !errors.Is(err, repos.ErrInUse) {
		t.Fatalf("delete referenced = %v, want ErrInUse", err)
	}
}

// TestUpdateDropsThePinOnURLChange proves the pin's one service-side rule
// (backlog 2.41): the pin pins the server the URL names, so an edit that
// changes the URL drops it (the next sync re-pins whatever the new server
// presents); an edit that keeps the URL keeps the pin.
func TestUpdateDropsThePinOnURLChange(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := newService(store, &fakeCreds{}, &fakeEnqueuer{})
	created, err := svc.Create(ctx, org, repos.CreateInput{Name: "infra", URL: "ssh://git@git.example.com/acme/infra.git"})
	if err != nil {
		t.Fatal(err)
	}
	created.SSHHostKey = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	created.SSHHostPublicKey = "ssh-ed25519 AAAA pinned"
	if _, err := store.Update(ctx, created); err != nil {
		t.Fatal(err)
	}

	// An edit keeping the URL keeps the pin.
	same, err := svc.Update(ctx, org, created.ID, repos.UpdateInput{Name: "infra", URL: created.URL, Branch: "release/1"})
	if err != nil {
		t.Fatal(err)
	}
	if same.SSHHostKey != created.SSHHostKey || same.SSHHostPublicKey != created.SSHHostPublicKey {
		t.Fatalf("pin lost on a same-URL edit: %+v", same)
	}

	// An edit changing the URL drops it.
	moved, err := svc.Update(ctx, org, created.ID, repos.UpdateInput{Name: "infra", URL: "https://git.example.com/acme/infra.git"})
	if err != nil {
		t.Fatal(err)
	}
	if moved.SSHHostKey != "" || moved.SSHHostPublicKey != "" {
		t.Fatalf("pin survived a URL change: %+v", moved)
	}
}

// TestPinState pins the pin-state shape the read APIs expose (backlog 2.41,
// the hosts 2.43 mirror): a pin always carries the full key for repos, so
// the state is full or nothing.
func TestPinState(t *testing.T) {
	cases := []struct {
		name string
		repo repos.Repo
		want string
	}{
		{"pinned", repos.Repo{SSHHostKey: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", SSHHostPublicKey: "ssh-ed25519 AAAA"}, repos.PinStateFull},
		{"unpinned", repos.Repo{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.repo.PinState(); got != tc.want {
				t.Fatalf("PinState = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestClearHostKey proves the unpin action: the pin is gone after the call,
// an unknown repository is ErrNotFound and never reaches the store.
func TestClearHostKey(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := newService(store, &fakeCreds{}, &fakeEnqueuer{})
	created, err := svc.Create(ctx, org, repos.CreateInput{Name: "infra", URL: "ssh://git@git.example.com/x.git"})
	if err != nil {
		t.Fatal(err)
	}
	created.SSHHostKey = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	created.SSHHostPublicKey = "ssh-ed25519 AAAA pinned"
	if _, err := store.Update(ctx, created); err != nil {
		t.Fatal(err)
	}

	if err := svc.ClearHostKey(ctx, org, created.ID); err != nil {
		t.Fatalf("ClearHostKey: %v", err)
	}
	got, err := store.Get(ctx, org, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SSHHostKey != "" || got.SSHHostPublicKey != "" {
		t.Fatalf("pin survived the unpin: %+v", got)
	}
	if err := svc.ClearHostKey(ctx, org, uuid.Must(uuid.NewV7())); !errors.Is(err, repos.ErrNotFound) {
		t.Fatalf("clear unknown = %v, want ErrNotFound", err)
	}
	if len(store.cleared) != 1 {
		t.Fatalf("an unknown repository must not reach the store: %+v", store.cleared)
	}
}

// TestSyncEnqueuesThroughPort proves Sync resolves the repo first (an
// unknown repo is ErrNotFound and never reaches the enqueuer) and then
// delegates to the local JobEnqueuer port with the actor verbatim.
func TestSyncEnqueuesThroughPort(t *testing.T) {
	org, actor := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	store := newFakeStore()
	created, err := newService(store, &fakeCreds{}, &fakeEnqueuer{}).Create(ctx, org, repos.CreateInput{Name: "infra", URL: "https://git.example.com/x"})
	if err != nil {
		t.Fatal(err)
	}
	enq := &fakeEnqueuer{}
	svc := newService(store, &fakeCreds{}, enq)

	job, err := svc.Sync(ctx, org, created.ID, &actor)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(enq.calls) != 1 || enq.calls[0].orgID != org || enq.calls[0].repoID != created.ID ||
		enq.calls[0].actor == nil || *enq.calls[0].actor != actor {
		t.Fatalf("enqueuer calls = %+v", enq.calls)
	}
	if job.Kind != jobs.KindSyncRepo {
		t.Fatalf("job = %+v", job)
	}
	if _, err := svc.Sync(ctx, org, uuid.Must(uuid.NewV7()), &actor); !errors.Is(err, repos.ErrNotFound) {
		t.Fatalf("sync unknown = %v, want ErrNotFound", err)
	}
	if len(enq.calls) != 1 {
		t.Fatalf("a failed lookup must not enqueue: %d calls", len(enq.calls))
	}
	enq.err = repos.ErrNotFound // any error surfaces wrapped
	if _, err := svc.Sync(ctx, org, created.ID, &actor); !errors.Is(err, repos.ErrNotFound) {
		t.Fatalf("enqueuer failure = %v, want wrapped", err)
	}
}

func vaultPasswordCred() credentials.Credential {
	return credentials.Credential{ID: uuid.Must(uuid.NewV7()), Kind: credentials.KindVaultPassword}
}

// TestCreateAndUpdateVaultIdentitiesRoundTrip is the brief's Step 3 round
// trip: create with two labelled identities, read back and compare values
// (not only the count — an earlier sub-project here shipped a round-trip
// test that counted and never compared, and the mapping bug it would have
// caught was found only by a later review), update down to one, and assert
// the dropped label is actually gone.
func TestCreateAndUpdateVaultIdentitiesRoundTrip(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	prod, staging := vaultPasswordCred(), vaultPasswordCred()
	creds := newFakeCreds(map[uuid.UUID]credentials.Credential{prod.ID: prod, staging.ID: staging})
	store := newFakeStore()
	svc := newService(store, creds, &fakeEnqueuer{})

	created, err := svc.Create(ctx, org, repos.CreateInput{
		Name: "infra", URL: "https://git.example.com/x",
		VaultIdentities: []repos.VaultIdentity{
			{CredentialID: prod.ID, Label: "prod"},
			{CredentialID: staging.ID, Label: "staging"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantCreated := []repos.VaultIdentity{
		{CredentialID: prod.ID, Label: "prod"},
		{CredentialID: staging.ID, Label: "staging"},
	}
	if !reflect.DeepEqual(created.VaultIdentities, wantCreated) {
		t.Fatalf("created.VaultIdentities = %+v, want %+v", created.VaultIdentities, wantCreated)
	}

	got, err := svc.Get(ctx, org, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got.VaultIdentities, wantCreated) {
		t.Fatalf("Get.VaultIdentities = %+v, want %+v", got.VaultIdentities, wantCreated)
	}

	updated, err := svc.Update(ctx, org, created.ID, repos.UpdateInput{
		Name: "infra", URL: created.URL,
		VaultIdentities: []repos.VaultIdentity{{CredentialID: prod.ID, Label: "prod"}},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	wantUpdated := []repos.VaultIdentity{{CredentialID: prod.ID, Label: "prod"}}
	if !reflect.DeepEqual(updated.VaultIdentities, wantUpdated) {
		t.Fatalf("updated.VaultIdentities = %+v, want %+v (staging must be dropped)", updated.VaultIdentities, wantUpdated)
	}
	got, err = svc.Get(ctx, org, created.ID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if !reflect.DeepEqual(got.VaultIdentities, wantUpdated) {
		t.Fatalf("Get after update = %+v, want %+v", got.VaultIdentities, wantUpdated)
	}
	for _, id := range got.VaultIdentities {
		if id.Label == "staging" {
			t.Fatalf("staging must be gone, got %+v", got.VaultIdentities)
		}
	}
}

// TestCreateBridgesLegacyVaultCredentialIntoTheDefaultLabel is the write-
// time bridge (the review that followed the claim path's own task): a
// repository configured only through the old single VaultCredentialID slot
// must still produce a usable link row, labelled "default" — the same
// label migration 00059's backfill uses — or its runs silently lose their
// vault password now that the claim path reads only the link table. The
// two inputs are never merged: identities, when given at all, are the
// whole truth and the legacy field is stored but inert beside them.
func TestCreateBridgesLegacyVaultCredentialIntoTheDefaultLabel(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	legacyCred, explicitCred := vaultPasswordCred(), vaultPasswordCred()
	creds := newFakeCreds(map[uuid.UUID]credentials.Credential{legacyCred.ID: legacyCred, explicitCred.ID: explicitCred})

	cases := []struct {
		name              string
		repoName          string
		vaultCredentialID *uuid.UUID
		identities        []repos.VaultIdentity
		want              []repos.VaultIdentity
	}{
		{
			name:              "legacy field alone synthesizes the default label",
			repoName:          "infra-legacy-only",
			vaultCredentialID: &legacyCred.ID,
			identities:        nil,
			want:              []repos.VaultIdentity{{CredentialID: legacyCred.ID, Label: "default"}},
		},
		{
			name:              "identities alongside the legacy field win outright",
			repoName:          "infra-both",
			vaultCredentialID: &legacyCred.ID,
			identities:        []repos.VaultIdentity{{CredentialID: explicitCred.ID, Label: "prod"}},
			want:              []repos.VaultIdentity{{CredentialID: explicitCred.ID, Label: "prod"}},
		},
		{
			name:              "identities alone, no legacy field: unchanged",
			repoName:          "infra-identities-only",
			vaultCredentialID: nil,
			identities:        []repos.VaultIdentity{{CredentialID: explicitCred.ID, Label: "prod"}},
			want:              []repos.VaultIdentity{{CredentialID: explicitCred.ID, Label: "prod"}},
		},
		{
			name:              "neither given: no vault identities",
			repoName:          "infra-neither",
			vaultCredentialID: nil,
			identities:        nil,
			want:              nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			svc := newService(store, creds, &fakeEnqueuer{})
			created, err := svc.Create(ctx, org, repos.CreateInput{
				Name: tc.repoName, URL: "https://git.example.com/x",
				VaultCredentialID: tc.vaultCredentialID, VaultIdentities: tc.identities,
			})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if !sameVaultIdentities(created.VaultIdentities, tc.want) {
				t.Fatalf("created.VaultIdentities = %+v, want %+v", created.VaultIdentities, tc.want)
			}
			got, err := svc.Get(ctx, org, created.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if !sameVaultIdentities(got.VaultIdentities, tc.want) {
				t.Fatalf("Get.VaultIdentities = %+v, want %+v", got.VaultIdentities, tc.want)
			}
		})
	}
}

// sameVaultIdentities compares ignoring the nil-vs-empty-slice distinction:
// repos.Service.Create/Update echo the input slice as-is (possibly nil),
// while a round trip through the store normalizes to a non-nil empty slice
// (fakeStore.ListRepoVaultIdentities' own make([]repos.VaultIdentity, 0)) —
// a storage-layer detail unrelated to what this test is pinning.
func sameVaultIdentities(a, b []repos.VaultIdentity) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// TestUpdateClearingIdentitiesButKeepingTheLegacyFieldReactivatesTheBridge
// is the brief's own warning: Update is where this is easiest to get
// wrong. A repository created with both an explicit identity set and a
// (then inert) legacy VaultCredentialID, then edited down to no explicit
// identities while the legacy field is left untouched, must not end up
// stranded with zero vault identities — the legacy field "wakes up" and
// synthesizes the default row, exactly as it would have on a fresh Create.
func TestUpdateClearingIdentitiesButKeepingTheLegacyFieldReactivatesTheBridge(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	legacyCred, explicitCred := vaultPasswordCred(), vaultPasswordCred()
	creds := newFakeCreds(map[uuid.UUID]credentials.Credential{legacyCred.ID: legacyCred, explicitCred.ID: explicitCred})
	store := newFakeStore()
	svc := newService(store, creds, &fakeEnqueuer{})

	created, err := svc.Create(ctx, org, repos.CreateInput{
		Name: "infra", URL: "https://git.example.com/x",
		VaultCredentialID: &legacyCred.ID,
		VaultIdentities:   []repos.VaultIdentity{{CredentialID: explicitCred.ID, Label: "prod"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantAfterCreate := []repos.VaultIdentity{{CredentialID: explicitCred.ID, Label: "prod"}}
	if !reflect.DeepEqual(created.VaultIdentities, wantAfterCreate) {
		t.Fatalf("after create, VaultIdentities = %+v, want %+v (the legacy field must stay inert)", created.VaultIdentities, wantAfterCreate)
	}

	// The PUT clears the explicit identities but leaves VaultCredentialID
	// exactly as it was — a plausible "I only meant to remove the extra
	// password" edit, not a deliberate opt-out of vault entirely.
	updated, err := svc.Update(ctx, org, created.ID, repos.UpdateInput{
		Name: "infra", URL: created.URL,
		VaultCredentialID: &legacyCred.ID,
		VaultIdentities:   nil,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	wantAfterUpdate := []repos.VaultIdentity{{CredentialID: legacyCred.ID, Label: "default"}}
	if !reflect.DeepEqual(updated.VaultIdentities, wantAfterUpdate) {
		t.Fatalf("after update, VaultIdentities = %+v, want %+v (the repository must not be stranded with none)", updated.VaultIdentities, wantAfterUpdate)
	}
	got, err := svc.Get(ctx, org, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got.VaultIdentities, wantAfterUpdate) {
		t.Fatalf("Get after update = %+v, want %+v", got.VaultIdentities, wantAfterUpdate)
	}
}

// TestVaultIdentityLabelValidation is the brief's Step 2/3: one case per
// character class internal/worker/runner's invalidVaultLabel rejects
// (matched exactly, see repos.InvalidVaultLabel's own comment), plus
// duplicate labels (the table's PRIMARY KEY would otherwise turn this into a
// 500) and a wrong-kind credential.
func TestVaultIdentityLabelValidation(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	vault := vaultPasswordCred()
	ssh := credentials.Credential{ID: uuid.Must(uuid.NewV7()), Kind: credentials.KindSSHKey}
	creds := newFakeCreds(map[uuid.UUID]credentials.Credential{vault.ID: vault, ssh.ID: ssh})

	cases := []struct {
		name       string
		identities []repos.VaultIdentity
		wantErr    string
	}{
		{"empty label", []repos.VaultIdentity{{CredentialID: vault.ID, Label: ""}}, "label is empty"},
		{"leading whitespace", []repos.VaultIdentity{{CredentialID: vault.ID, Label: " prod"}}, "leading or trailing whitespace"},
		{"trailing whitespace", []repos.VaultIdentity{{CredentialID: vault.ID, Label: "prod "}}, "leading or trailing whitespace"},
		// unicode.IsSpace whitespace (a non-breaking space), not only ASCII —
		// strings.TrimSpace (and so this gate and the runner's) treats it as
		// whitespace too.
		{"non-breaking space", []repos.VaultIdentity{{CredentialID: vault.ID, Label: "prod "}}, "leading or trailing whitespace"},
		{"newline", []repos.VaultIdentity{{CredentialID: vault.ID, Label: "prod\nhost_key_checking = False"}}, "control or format"},
		{"carriage return", []repos.VaultIdentity{{CredentialID: vault.ID, Label: "prod\rx"}}, "control or format"},
		{"comma", []repos.VaultIdentity{{CredentialID: vault.ID, Label: "prod,staging"}}, "comma"},
		{"at sign", []repos.VaultIdentity{{CredentialID: vault.ID, Label: "prod@file"}}, "'@'"},
		// internal/vaultlabel's rule widened from a fixed character list to
		// a Unicode category (Cc/Cf) after a reviewer found the client and
		// the server disagreed about a leading BOM: JavaScript's trim()
		// strips it, Go's strings.TrimSpace does not. Both cases now fail
		// on the shared rule itself rather than on either trim's accident.
		{"byte-order mark", []repos.VaultIdentity{{CredentialID: vault.ID, Label: "\ufeffprod"}}, "control or format"},
		{"bidirectional override", []repos.VaultIdentity{{CredentialID: vault.ID, Label: "prod\u202e"}}, "control or format"},
		{"overlong", []repos.VaultIdentity{{CredentialID: vault.ID, Label: strings.Repeat("a", repos.MaxVaultLabelLength+1)}}, "exceeds"},
		// The bound is runes, not bytes, matching huma's own maxLength check
		// (utf8.RuneCountInString): a 100-rune label of umlauts is over 100
		// bytes but must still be accepted, or the server would refuse what
		// the published maxLength: 100 promised was fine.
		{"100 runes with umlauts", []repos.VaultIdentity{{CredentialID: vault.ID, Label: strings.Repeat("ä", 100)}}, ""},
		{"101 runes with umlauts", []repos.VaultIdentity{{CredentialID: vault.ID, Label: strings.Repeat("ä", 101)}}, "exceeds"},
		{"duplicate label", []repos.VaultIdentity{
			{CredentialID: vault.ID, Label: "prod"},
			{CredentialID: vault.ID, Label: "prod"},
		}, "duplicate vault identity label"},
		{"wrong credential kind", []repos.VaultIdentity{{CredentialID: ssh.ID, Label: "prod"}}, "vault_password credential"},
		{"unknown credential", []repos.VaultIdentity{{CredentialID: uuid.Must(uuid.NewV7()), Label: "prod"}}, "vault credential not found"},
		{"valid", []repos.VaultIdentity{{CredentialID: vault.ID, Label: "prod"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newService(newFakeStore(), creds, &fakeEnqueuer{})
			_, err := svc.Create(ctx, org, repos.CreateInput{
				Name: "infra", URL: "https://git.example.com/x", VaultIdentities: tc.identities,
			})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Create = %v, want valid", err)
				}
				return
			}
			if !errors.Is(err, repos.ErrInvalidInput) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Create = %v, want ErrInvalidInput naming %q", err, tc.wantErr)
			}
		})
	}
}
