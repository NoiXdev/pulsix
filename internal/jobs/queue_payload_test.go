// White-box tests for the claim payload build and the stream registry:
// these need the unexported buildPayload and the registry internals, so
// they live in package jobs while every other test in this package is
// external.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/sshconf"
)

// fakeBox seals with a marker prefix so a round trip is observable.
type fakeBox struct{}

func (fakeBox) SealAAD(plain, _ []byte) ([]byte, error) {
	return append([]byte("sealed:"), plain...), nil
}

func (fakeBox) OpenAAD(sealed, _ []byte) ([]byte, error) {
	rest, ok := strings.CutPrefix(string(sealed), "sealed:")
	if !ok {
		return nil, errors.New("not sealed")
	}
	return []byte(rest), nil
}

type fakeRenderer struct {
	inv render.Inventory
	err error
}

func (f fakeRenderer) Snapshot(context.Context, uuid.UUID, render.Scope) (render.Inventory, error) {
	return f.inv, f.err
}

type fakeCreds struct {
	secrets map[uuid.UUID]credentials.Secret
	metas   map[uuid.UUID]credentials.Credential
	err     error
}

func (f fakeCreds) OpenSecret(_ context.Context, _, id uuid.UUID) (credentials.Secret, error) {
	if f.err != nil {
		return credentials.Secret{}, f.err
	}
	s, ok := f.secrets[id]
	if !ok {
		return credentials.Secret{}, credentials.ErrNotFound
	}
	return s, nil
}

func (f fakeCreds) Get(_ context.Context, _, id uuid.UUID) (credentials.Credential, error) {
	c, ok := f.metas[id]
	if !ok {
		return credentials.Credential{}, credentials.ErrNotFound
	}
	return c, nil
}

type fakeBastions struct{ byID map[uuid.UUID]sshconf.Bastion }

func (f fakeBastions) GetBastion(_ context.Context, _, id uuid.UUID) (sshconf.Bastion, error) {
	b, ok := f.byID[id]
	if !ok {
		return sshconf.Bastion{}, sshconf.ErrNotFound
	}
	return b, nil
}

type fakeTemplates struct{ byID map[uuid.UUID]TemplateRow }

func (f fakeTemplates) TemplateLookup(_ context.Context, _, id uuid.UUID) (TemplateRow, error) {
	t, ok := f.byID[id]
	if !ok {
		return TemplateRow{}, ErrNotFound
	}
	return t, nil
}

type fakeRepos struct {
	byID map[uuid.UUID]RepoRef
	err  error
}

func (f fakeRepos) Get(_ context.Context, _, id uuid.UUID) (RepoRef, error) {
	if f.err != nil {
		return RepoRef{}, f.err
	}
	r, ok := f.byID[id]
	if !ok {
		return RepoRef{}, ErrNotFound
	}
	return r, nil
}

// fakeVaultResolver mirrors repos.ResolveVaultIdentities' contract (union,
// the template wins a label collision, sorted by label) without importing
// internal/repos — this package cannot (see jobs.VaultIdentity's own
// comment). Good enough to drive a unit test's sibling-isolation and
// collision-resolution assertions; parity with the real algorithm is
// internal/repos' own test file's job (vault_test.go), not this fake's.
type fakeVaultResolver struct{}

func (fakeVaultResolver) ResolveVaultIdentities(repo, template []VaultIdentity) []VaultIdentity {
	byLabel := make(map[string]VaultIdentity, len(repo)+len(template))
	for _, id := range repo {
		id.FromTemplate = false
		byLabel[id.Label] = id
	}
	for _, id := range template {
		id.FromTemplate = true
		byLabel[id.Label] = id
	}
	out := make([]VaultIdentity, 0, len(byLabel))
	for _, id := range byLabel {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// testHosts is the target list the white-box payload tests give their
// host-bound jobs: a claim renders exactly job.HostIDs, and an empty list
// fails closed.
var testHosts = []uuid.UUID{uuid.MustParse("01a0ac45-0000-7000-8000-000000000001")}

func newTestQueue(renderer SnapshotSource, creds CredentialSource, templates TemplateSource, bastions BastionSource) *SQLQueue {
	return &SQLQueue{
		box: fakeBox{}, renderer: renderer, creds: creds, templates: templates, bastions: bastions,
		// The key guards default to "nothing changed" (both hosts and jump
		// hosts); the pins source stays unwired so the fail-closed probe
		// paths remain testable.
		keyGuard: fakePins{}, bastionGuard: fakePins{},
		now: time.Now, rand: rand.Reader, streams: newStreamRegistry(), cancels: newCancelRegistry(),
	}
}

func TestBuildPayloadRunShipsSnapshotConnectionsAndSecrets(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hostID, credID, bastionID, bastionCredID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	templateID := uuid.Must(uuid.NewV7())
	repoID := uuid.Must(uuid.NewV7())
	gitCredID := uuid.Must(uuid.NewV7())

	renderer := fakeRenderer{inv: render.Inventory{
		Doc: map[string]any{"all": map[string]any{}},
		Connections: map[string]render.Connection{
			"web1": {HostID: hostID, CredentialID: &credID, Bastions: []uuid.UUID{bastionID}, BastionSource: "host"},
		},
	}}
	creds := fakeCreds{
		secrets: map[uuid.UUID]credentials.Secret{
			credID:        {PrivateKey: "host-key"},
			bastionCredID: {Password: "bastion-pw"},
			gitCredID:     {Token: "git-token-material"},
		},
		metas: map[uuid.UUID]credentials.Credential{
			credID: {ID: credID, Username: "ansible"},
		},
	}
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {
			ID: templateID, OrgID: org, PlaybookID: uuid.Must(uuid.NewV7()),
			PlaybookRepo: &repoID, PlaybookPath: "site.yml",
			Scope:        json.RawMessage(`{"host_ids":["` + hostID.String() + `"]}`),
			Limit:        "web*",
			ExtraVars:    json.RawMessage(`{"run":"smoke","count":2}`),
			CredentialID: &credID,
		},
	}}
	q := newTestQueue(renderer, creds, templates, fakeBastions{byID: map[uuid.UUID]sshconf.Bastion{
		bastionID: {ID: bastionID, CredentialID: &bastionCredID},
	}})
	q.SetRepoSource(fakeRepos{byID: map[uuid.UUID]RepoRef{
		repoID: {ID: repoID, URL: "ssh://git@example.org/ansible.git", Branch: "main", Subpath: "ansible", CredentialID: &gitCredID},
	}})
	q.SetKeyPins(fakePins{})

	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}
	out := &workerpb.Job{}
	if err := q.buildPayload(context.Background(), job, out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	j := out
	if j.Id != job.ID.String() || j.Kind != "run" || j.OrgId != org.String() || j.ZoneId != zone.String() {
		t.Fatalf("identity fields = %+v", j)
	}
	if j.TemplateId != templateID.String() || j.Playbook != "repo:"+repoID.String()+":site.yml" || j.Limit != "web*" {
		t.Fatalf("template fields = %+v", j)
	}
	if j.SshUsername != "ansible" {
		t.Fatalf("ssh username = %q", j.SshUsername)
	}
	if j.ExtraVars["run"] != `"smoke"` || j.ExtraVars["count"] != "2" {
		t.Fatalf("extra vars = %v", j.ExtraVars)
	}
	if !strings.Contains(j.InventoryDoc, `"all"`) {
		t.Fatalf("inventory doc = %q", j.InventoryDoc)
	}
	var conn map[string]any
	if err := json.Unmarshal([]byte(j.Connections["web1"]), &conn); err != nil {
		t.Fatalf("connection web1 = %q, %v", j.Connections["web1"], err)
	}
	if conn["host_id"] != hostID.String() {
		t.Fatalf("connection host id = %v", conn["host_id"])
	}
	// The claim adds the bastions' login credentials, aligned with the
	// bastion list — the worker cannot hop without them.
	if ids, ok := conn["bastion_credential_ids"].([]any); !ok || len(ids) != 1 || ids[0] != bastionCredID.String() {
		t.Fatalf("bastion credential ids = %v", conn["bastion_credential_ids"])
	}
	if len(j.Credentials) != 3 {
		t.Fatalf("credentials = %v, want host, bastion and the git credential", j.Credentials)
	}
	if j.Credentials[credID.String()] != `{"private_key":"host-key"}` {
		t.Fatalf("host credential = %q", j.Credentials[credID.String()])
	}
	if j.Credentials[bastionCredID.String()] != `{"password":"bastion-pw"}` {
		t.Fatalf("bastion credential = %q", j.Credentials[bastionCredID.String()])
	}
	// A repo playbook claims its repository: url, branch and the file
	// (repo subpath + playbook path), plus the git credential as
	// `git:<repo-id>`.
	if j.Credentials["git:"+repoID.String()] != `{"token":"git-token-material"}` {
		t.Fatalf("git credential = %q", j.Credentials["git:"+repoID.String()])
	}
	var kindPayload struct {
		Repo struct {
			ID      string `json:"id"`
			URL     string `json:"url"`
			Branch  string `json:"branch"`
			Subpath string `json:"subpath"`
		} `json:"repo"`
	}
	if err := json.Unmarshal([]byte(j.KindPayload), &kindPayload); err != nil {
		t.Fatalf("kind payload = %q: %v", j.KindPayload, err)
	}
	if kindPayload.Repo.ID != repoID.String() || kindPayload.Repo.URL != "ssh://git@example.org/ansible.git" ||
		kindPayload.Repo.Branch != "main" || kindPayload.Repo.Subpath != "ansible/site.yml" {
		t.Fatalf("repo payload = %+v", kindPayload.Repo)
	}
	if len(j.HostKeys) != 0 {
		t.Fatalf("host keys must be empty until Task 10: %v", j.HostKeys)
	}
}

// TestBuildPayloadRepoPlaybookShipsTheGitServerPin proves a pinned
// repository's claim carries the pin (backlog 2.41): the run's repo fetch
// demands the pinned key exactly like a sync job's, and an unpinned repo's
// payload carries neither field.
func TestBuildPayloadRepoPlaybookShipsTheGitServerPin(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hostID := uuid.Must(uuid.NewV7())
	templateID := uuid.Must(uuid.NewV7())
	repoID := uuid.Must(uuid.NewV7())
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {
			ID: templateID, OrgID: org, PlaybookID: uuid.Must(uuid.NewV7()),
			PlaybookRepo: &repoID, PlaybookPath: "site.yml",
			Scope: json.RawMessage(`{"host_ids":["` + hostID.String() + `"]}`),
		},
	}}
	pinned := fakeRepos{byID: map[uuid.UUID]RepoRef{
		repoID: {ID: repoID, URL: "ssh://git@example.org/ansible.git", Branch: "main",
			SSHHostKey:       "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
			SSHHostPublicKey: "ssh-ed25519 AAAA pinned"},
	}}
	q := newTestQueue(fakeRenderer{inv: render.Inventory{
		Doc:         map[string]any{"all": map[string]any{}},
		Connections: map[string]render.Connection{"web1": {HostID: hostID}},
	}}, fakeCreds{}, templates, fakeBastions{})
	q.SetRepoSource(pinned)
	q.SetKeyPins(fakePins{})

	out := &workerpb.Job{}
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts,
	}, out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	var payload struct {
		Repo struct {
			ID            string `json:"id"`
			HostKey       string `json:"host_key"`
			HostPublicKey string `json:"host_public_key"`
		} `json:"repo"`
	}
	if err := json.Unmarshal([]byte(out.KindPayload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Repo.HostKey != "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB" ||
		payload.Repo.HostPublicKey != "ssh-ed25519 AAAA pinned" {
		t.Fatalf("pinned repo payload = %+v", payload.Repo)
	}

	// Unpinned: neither field rides.
	unpinned := fakeRepos{byID: map[uuid.UUID]RepoRef{
		repoID: {ID: repoID, URL: "https://git.example.org/ansible.git", Branch: "main"},
	}}
	q = newTestQueue(fakeRenderer{inv: render.Inventory{
		Doc:         map[string]any{"all": map[string]any{}},
		Connections: map[string]render.Connection{"web1": {HostID: hostID}},
	}}, fakeCreds{}, templates, fakeBastions{})
	q.SetRepoSource(unpinned)
	q.SetKeyPins(fakePins{})
	out = &workerpb.Job{}
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts,
	}, out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	var unpinnedPayload struct {
		Repo struct {
			HostKey       string `json:"host_key"`
			HostPublicKey string `json:"host_public_key"`
		} `json:"repo"`
	}
	if err := json.Unmarshal([]byte(out.KindPayload), &unpinnedPayload); err != nil {
		t.Fatal(err)
	}
	if unpinnedPayload.Repo.HostKey != "" || unpinnedPayload.Repo.HostPublicKey != "" {
		t.Fatalf("an unpinned repo's payload must carry no pin: %+v", unpinnedPayload.Repo)
	}
}

func TestBuildPayloadHostJobOpensSealedKindPayload(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	q := newTestQueue(fakeRenderer{}, fakeCreds{}, fakeTemplates{}, fakeBastions{})

	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindAdhoc, HostIDs: testHosts}
	sealed, err := q.box.SealAAD([]byte(`{"login":"ansible"}`), JobAAD(org, job.ID))
	if err != nil {
		t.Fatal(err)
	}
	job.Payload = json.RawMessage(`"` + base64.StdEncoding.EncodeToString(sealed) + `"`)
	out := &workerpb.Job{}
	if err := q.buildPayload(context.Background(), job, out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if out.KindPayload != `{"login":"ansible"}` {
		t.Fatalf("kind payload = %q", out.KindPayload)
	}
	if out.Playbook != "" || out.TemplateId != "" {
		t.Fatalf("host jobs carry no playbook/template: %+v", out)
	}

	// A payload that is not a sealed string passes through as plain JSON.
	job.ID = uuid.Must(uuid.NewV7())
	job.Payload = json.RawMessage(`{"argv":["ls"]}`)
	out2 := &workerpb.Job{}
	if err := q.buildPayload(context.Background(), job, out2); err != nil {
		t.Fatalf("buildPayload plain: %v", err)
	}
	if out2.KindPayload != `{"argv":["ls"]}` {
		t.Fatalf("plain kind payload = %q", out2.KindPayload)
	}

	// A sealed payload that does not open under the job's AAD fails. The
	// fake box ignores AAD, so this case uses a real one.
	job.ID = uuid.Must(uuid.NewV7())
	var key [32]byte
	copy(key[:], "jobs-wrong-aad-test-key-012345678")
	realBox := crypto.NewBox(key)
	otherSealed, err := realBox.SealAAD([]byte(`{}`), JobAAD(org, uuid.Must(uuid.NewV7())))
	if err != nil {
		t.Fatal(err)
	}
	job.Payload = json.RawMessage(`"` + base64.StdEncoding.EncodeToString(otherSealed) + `"`)
	wrongAAD := *q
	wrongAAD.box = realBox
	if err := wrongAAD.buildPayload(context.Background(), job, &workerpb.Job{}); err == nil {
		t.Fatal("payload sealed for another job must not open")
	}
}

func TestBuildPayloadFailsClosed(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	templateID := uuid.Must(uuid.NewV7())

	// A run job without a template id is a programming error.
	q := newTestQueue(fakeRenderer{}, fakeCreds{}, fakeTemplates{}, fakeBastions{})
	if err := q.buildPayload(context.Background(), Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, Kind: KindRun}, &workerpb.Job{}); err == nil {
		t.Fatal("run job without template must fail")
	}

	// An unknown template.
	if err := q.buildPayload(context.Background(), Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}, &workerpb.Job{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown template: %v", err)
	}

	// A broken template scope no longer matters at claim time: the claim
	// renders the job's own hosts (the enqueue parsed the scope).
	q = newTestQueue(fakeRenderer{}, fakeCreds{}, fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {ID: templateID, Scope: json.RawMessage(`{oops`), PlaybookInline: "- hosts: all"},
	}}, fakeBastions{})
	if err := q.buildPayload(context.Background(), Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}, &workerpb.Job{}); err != nil {
		t.Fatalf("a broken stored scope must not affect the claim: %v", err)
	}

	// A failing renderer.
	q = newTestQueue(fakeRenderer{err: errors.New("boom")}, fakeCreds{}, fakeTemplates{}, fakeBastions{})
	if err := q.buildPayload(context.Background(), Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, Kind: KindAdhoc, HostIDs: testHosts}, &workerpb.Job{}); err == nil {
		t.Fatal("failing renderer must fail the payload")
	}

	// An unopenable credential.
	hostID, credID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	q = newTestQueue(fakeRenderer{inv: render.Inventory{Connections: map[string]render.Connection{
		"web1": {HostID: hostID, CredentialID: &credID},
	}}}, fakeCreds{err: errors.New("locked")}, fakeTemplates{}, fakeBastions{})
	if err := q.buildPayload(context.Background(), Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, Kind: KindAdhoc, HostIDs: testHosts}, &workerpb.Job{}); err == nil {
		t.Fatal("unopenable credential must fail the payload")
	}

	// An unknown bastion in a rendered chain.
	q = newTestQueue(fakeRenderer{inv: render.Inventory{Connections: map[string]render.Connection{
		"web1": {HostID: hostID, Bastions: []uuid.UUID{uuid.Must(uuid.NewV7())}},
	}}}, fakeCreds{}, fakeTemplates{}, fakeBastions{})
	if err := q.buildPayload(context.Background(), Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, Kind: KindAdhoc, HostIDs: testHosts}, &workerpb.Job{}); err == nil {
		t.Fatal("unknown bastion must fail the payload")
	}
}

func TestStringMapJSONEncodesEveryValue(t *testing.T) {
	m, err := stringMap(json.RawMessage(`{"a":"x","b":2,"c":{"d":null},"e":[1]}`))
	if err != nil {
		t.Fatal(err)
	}
	if m["a"] != `"x"` || m["b"] != "2" || m["c"] != `{"d":null}` || m["e"] != "[1]" {
		t.Fatalf("stringMap = %v", m)
	}
	if _, err := stringMap(json.RawMessage(`[1]`)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("non-object extra vars: %v", err)
	}
	if m, err := stringMap(nil); err != nil || m != nil {
		t.Fatalf("nil extra vars = %v, %v", m, err)
	}
}

func TestPlaybookRef(t *testing.T) {
	repo := uuid.Must(uuid.NewV7())
	if got := playbookRef(TemplateRow{PlaybookRepo: &repo, PlaybookPath: "site.yml"}); got != "repo:"+repo.String()+":site.yml" {
		t.Fatalf("repo playbook ref = %q", got)
	}
	if got := playbookRef(TemplateRow{PlaybookInline: "- hosts: all"}); got != "- hosts: all" {
		t.Fatalf("editor playbook ref = %q", got)
	}
}

func TestNewTokenIsHex128(t *testing.T) {
	q := newTestQueue(fakeRenderer{}, fakeCreds{}, fakeTemplates{}, fakeBastions{})
	tok, err := q.newToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 32 {
		t.Fatalf("token = %q", tok)
	}
	if _, err := hex.DecodeString(tok); err != nil {
		t.Fatalf("token is not hex: %v", err)
	}
}

func TestStreamRegistryLifecycle(t *testing.T) {
	r := newStreamRegistry()
	jobID, orgID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	now := time.Now()
	r.register(jobID, orgID, "tok", 1, time.Time{}, now)

	e, ok := r.get(jobID, "tok")
	if !ok || e.token != "tok" || e.orgID != orgID || e.attempt != 1 {
		t.Fatalf("entry = %+v, %v", e, ok)
	}
	if e.alive(now.Add(-time.Second)) {
		t.Fatal("a fresh entry is registered but not yet bound")
	}
	e.touch(now)
	if !e.alive(now.Add(-time.Second)) {
		t.Fatal("a touched entry is alive")
	}
	if e.alive(now.Add(time.Second)) {
		t.Fatal("an entry older than the cutoff is not alive")
	}
	if ids := r.aliveIDs(now.Add(-time.Second)); len(ids) != 1 || ids[0] != jobID {
		t.Fatalf("aliveIDs = %v", ids)
	}
	e.markClosed()
	if e.alive(now.Add(-time.Second)) {
		t.Fatal("a closed entry is not alive")
	}
	r.removeAll(jobID)
	if _, ok := r.get(jobID, "tok"); ok {
		t.Fatal("removed entry must be gone")
	}
	if ids := r.aliveIDs(now.Add(-time.Second)); len(ids) != 0 {
		t.Fatalf("aliveIDs after removal = %v", ids)
	}
}

// TestStreamRegistryKeepsOneBindingPerAttempt (backlog 2.49): a job that
// was re-claimed while its old attempt's stream was still open has TWO
// live bindings — the registry keys on (job id, token), so a re-claim
// must not overwrite the old attempt's binding. The reaper's liveness set
// consults ANY live binding of a job: a single old-attempt stream keeps
// the job out of the requeue (and out of the exhausted set), and a job
// only vanishes from it once every binding is dead.
func TestStreamRegistryKeepsOneBindingPerAttempt(t *testing.T) {
	r := newStreamRegistry()
	jobID, orgID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	now := time.Now()
	r.register(jobID, orgID, "tok-attempt-1", 1, time.Time{}, now)
	r.register(jobID, orgID, "tok-attempt-2", 2, time.Time{}, now)

	first, ok := r.get(jobID, "tok-attempt-1")
	if !ok || first.attempt != 1 || first.token != "tok-attempt-1" {
		t.Fatalf("attempt-1 binding = %+v ok=%v; the re-claim must not have evicted it", first, ok)
	}
	second, ok := r.get(jobID, "tok-attempt-2")
	if !ok || second.attempt != 2 {
		t.Fatalf("attempt-2 binding = %+v ok=%v", second, ok)
	}
	if first == second {
		t.Fatal("both attempts resolved to the same entry")
	}
	if _, ok := r.get(jobID, "unknown"); ok {
		t.Fatal("unknown token must have no binding")
	}
	if _, ok := r.get(uuid.Must(uuid.NewV7()), "tok-attempt-1"); ok {
		t.Fatal("another job must have no binding")
	}

	// Only the old attempt's stream is alive: the job counts as live.
	first.touch(now)
	if ids := r.aliveIDs(now.Add(-time.Second)); len(ids) != 1 || ids[0] != jobID {
		t.Fatalf("aliveIDs with one live old-attempt binding = %v", ids)
	}
	// Both dead: the job drops out of the liveness set.
	first.markClosed()
	if ids := r.aliveIDs(now.Add(-time.Second)); len(ids) != 0 {
		t.Fatalf("aliveIDs after both bindings closed = %v", ids)
	}

	// The terminal transition drops every binding of the job.
	r.register(jobID, orgID, "tok-attempt-3", 3, time.Time{}, now)
	r.removeAll(jobID)
	for _, tok := range []string{"tok-attempt-1", "tok-attempt-2", "tok-attempt-3"} {
		if _, ok := r.get(jobID, tok); ok {
			t.Fatalf("binding %s survived removeAll", tok)
		}
	}
}

// TestLiveBindingCarriesJobCreatedAtIntoAppend (fix round 1 on backlog
// 2.50): the created_at bound on the max(seq) subquery is only effective
// if EVERY append path supplies it — and the normal steady state is a
// live stream appending, i.e. the BOUND path. The binding must carry the
// job's created_at from the claim row, eventBinding's adoption branch
// must return it, and the bound stream pins exactly that entry. Asserted
// per hop: the claim wiring against the row, the adoption branch against
// a db-less queue (a fall-through to the row read would nil-panic), and
// the bound stream's pinned entry with a real append through it.
func TestLiveBindingCarriesJobCreatedAtIntoAppend(t *testing.T) {
	d := testdb.Open(t)
	org, err := gen.New(d.Pool()).CreateOrganization(context.Background(), gen.CreateOrganizationParams{
		ID: uuid.Must(uuid.NewV7()), Slug: "jobs-binding-createdat", Name: "binding createdAt",
	})
	if err != nil {
		t.Fatal(err)
	}
	zone := testZone(t, d, org.ID)
	q := NewSQLQueue(d, fakeBox{}, fakeRenderer{}, fakeCreds{}, fakeTemplates{}, fakeBastions{}, time.Now, rand.Reader)
	q.SetKeyPins(fakePins{})
	j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, ZoneID: zone, Kind: KindAdhoc, HostIDs: testHosts,
		Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), Trigger: TriggerManual, CreatedAt: time.Now()}
	if err := NewSQLStore(d).CreateJob(db.WithOrgScope(context.Background(), org.ID), j, nil); err != nil {
		t.Fatal(err)
	}
	claim, err := q.Claim(context.Background(), org.ID, []uuid.UUID{zone}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("Claim = %v, %v", claim, err)
	}
	jobID := uuid.MustParse(claim.Job.GetId())

	// Hop 1: the claim registered the binding with the job row's
	// created_at.
	var rowCreatedAt time.Time
	if err := d.Pool().QueryRow(context.Background(),
		`SELECT created_at FROM jobs WHERE id = $1`, jobID).Scan(&rowCreatedAt); err != nil {
		t.Fatal(err)
	}
	e, ok := q.streams.get(jobID, claim.Token)
	if !ok {
		t.Fatal("no binding after the claim")
	}
	if !e.createdAt.Equal(rowCreatedAt) {
		t.Fatalf("binding createdAt = %v, want the job row's %v", e.createdAt, rowCreatedAt)
	}

	// Hop 2: eventBinding's adoption branch returns the binding's
	// created_at. The queue carries no db, so a fall-through to the row
	// read would panic instead of silently passing on the row's value.
	touchQueue := &SQLQueue{streams: q.streams, cancels: q.cancels, now: q.now}
	e.touch(time.Now())
	gotOrg, gotAttempt, gotCreatedAt, err := touchQueue.eventBinding(context.Background(), jobID)
	if err != nil {
		t.Fatalf("eventBinding: %v", err)
	}
	if gotOrg != org.ID || gotAttempt != 1 || !gotCreatedAt.Equal(rowCreatedAt) {
		t.Fatalf("eventBinding adoption = org %s attempt %d createdAt %v; want org %s attempt 1 createdAt %v",
			gotOrg, gotAttempt, gotCreatedAt, org.ID, rowCreatedAt)
	}

	// Hop 3: the bound stream pins the same entry, so its Append passes
	// the job's created_at into the INSERT — verified by appending for
	// real through the live binding.
	stream, err := q.BindStream(context.Background(), jobID, claim.Token)
	if err != nil {
		t.Fatal(err)
	}
	bs, ok := stream.(boundStream)
	if !ok {
		t.Fatalf("BindStream returned %T, want boundStream", stream)
	}
	if !bs.entry.createdAt.Equal(rowCreatedAt) {
		t.Fatalf("bound stream pins createdAt %v, want %v", bs.entry.createdAt, rowCreatedAt)
	}
	if err := stream.Append(context.Background(), Event{Seq: 1, State: "ok", At: time.Now()}); err != nil {
		t.Fatalf("append through the live binding: %v", err)
	}
}

// TestStreamEntryCancelClosesExactlyOnce is unchanged by the per-attempt
// keying: one binding's cancel signal closes exactly once.
func TestStreamEntryCancelClosesExactlyOnce(t *testing.T) {
	e := &streamEntry{cancel: make(chan struct{})}
	e.requestCancel()
	e.requestCancel() // second call must not panic
	select {
	case <-e.cancel:
	default:
		t.Fatal("cancel channel must be closed")
	}
}

func TestNormalizePayload(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"ok":1}`, `{"ok":1}`},
		{`plain stdout line`, `"plain stdout line"`},
		{``, `{}`},
	}
	for _, c := range cases {
		if got := string(normalizePayload(json.RawMessage(c.in))); got != c.want {
			t.Errorf("normalizePayload(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCancelRegistry(t *testing.T) {
	r := newCancelRegistry()
	id := uuid.Must(uuid.NewV7())
	if _, ok := r.get(id); ok {
		t.Fatal("unknown job must have no cancel request")
	}
	now := time.Now()
	r.note(id, now)
	at, ok := r.get(id)
	if !ok || !at.Equal(now) {
		t.Fatalf("get = %v, %v", at, ok)
	}
	r.remove(id)
	if _, ok := r.get(id); ok {
		t.Fatal("removed cancel request must be gone")
	}
}

// TestRealBoxRoundTripsJobPayload pins the sealing shape the service and
// the queue share: SealAAD with the job AAD, base64 JSON string at rest,
// OpenAAD at claim time.
func TestRealBoxRoundTripsJobPayload(t *testing.T) {
	var key [32]byte
	copy(key[:], "jobs-payload-test-key-012345678")
	box := crypto.NewBox(key)
	org, jobID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	sealed, err := box.SealAAD([]byte(`{"secret":"x"}`), JobAAD(org, jobID))
	if err != nil {
		t.Fatal(err)
	}
	stored := json.RawMessage(`"` + base64.StdEncoding.EncodeToString(sealed) + `"`)
	var b64 string
	if err := json.Unmarshal(stored, &b64); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := box.OpenAAD(raw, JobAAD(org, jobID))
	if err != nil || string(plain) != `{"secret":"x"}` {
		t.Fatalf("round trip = %q, %v", plain, err)
	}
	if _, err := box.OpenAAD(raw, JobAAD(org, uuid.Must(uuid.NewV7()))); err == nil {
		t.Fatal("payload must not open under another job's AAD")
	}
}

// TestReaperFallbackAnchorsOnClaimedAt covers the fallback a manager
// restart needs: a cancelling job with no recorded cancel request (the
// in-process registry was wiped) anchors the confirmation deadline on the
// claim time instead. Internal because the test must simulate the wiped
// registry by cancelling without Service.Cancel's note.
func TestReaperFallbackAnchorsOnClaimedAt(t *testing.T) {
	d := testdb.Open(t)
	org, err := gen.New(d.Pool()).CreateOrganization(context.Background(), gen.CreateOrganizationParams{
		ID: uuid.Must(uuid.NewV7()), Slug: "jobs-reaper-fallback", Name: "fallback",
	})
	if err != nil {
		t.Fatal(err)
	}
	zone := testZone(t, d, org.ID)
	q := NewSQLQueue(d, fakeBox{}, fakeRenderer{}, fakeCreds{}, fakeTemplates{}, fakeBastions{}, time.Now, rand.Reader)
	store := NewSQLStore(d)
	// Kind-agnostic reaper bookkeeping; adhoc claims do not depend on a
	// rendered probe target the way a (fail-closed) connection_test
	// claim would.
	q.SetKeyPins(fakePins{})
	j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, ZoneID: zone, Kind: KindAdhoc, HostIDs: testHosts,
		Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), Trigger: TriggerManual, CreatedAt: time.Now()}
	if err := store.CreateJob(db.WithOrgScope(context.Background(), org.ID), j, nil); err != nil {
		t.Fatal(err)
	}
	claim, err := q.Claim(context.Background(), org.ID, []uuid.UUID{zone}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("Claim = %v, %v", claim, err)
	}
	jobID := uuid.MustParse(claim.Job.GetId())
	// Backdate the claim, then cancel without a note (the restart case).
	if _, err := d.Pool().Exec(context.Background(),
		`UPDATE jobs SET claimed_at = now() - ($2::bigint * interval '1 microsecond') WHERE id = $1`,
		jobID, (61 * time.Second).Microseconds()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelJob(db.WithOrgScope(context.Background(), org.ID), org.ID, jobID); err != nil {
		t.Fatal(err)
	}
	requeued, failed, err := NewReaper(q, 0, 0).Sweep(context.Background())
	if err != nil || requeued != 0 || failed != 1 {
		t.Fatalf("Sweep = %d/%d, %v", requeued, failed, err)
	}
}

func TestBuildPayloadEditorPlaybookCarriesNoRepoPayload(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hostID := uuid.Must(uuid.NewV7())
	templateID := uuid.Must(uuid.NewV7())
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {ID: templateID, OrgID: org, PlaybookID: uuid.Must(uuid.NewV7()), PlaybookInline: "- hosts: all",
			Scope: json.RawMessage(`{"host_ids":["` + hostID.String() + `"]}`)},
	}}
	q := newTestQueue(fakeRenderer{}, fakeCreds{}, templates, fakeBastions{})
	q.SetRepoSource(fakeRepos{byID: map[uuid.UUID]RepoRef{}})

	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}
	out := &workerpb.Job{}
	if err := q.buildPayload(context.Background(), job, out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if out.Playbook != "- hosts: all" {
		t.Fatalf("playbook = %q", out.Playbook)
	}
	if out.KindPayload != "" {
		t.Fatalf("an editor playbook carries no repo payload: %q", out.KindPayload)
	}
}

func TestBuildPayloadRepoPlaybookFailsClosed(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	templateID := uuid.Must(uuid.NewV7())
	repoID := uuid.Must(uuid.NewV7())
	gitCredID := uuid.Must(uuid.NewV7())

	// A repo playbook claim with no repo source wired is a wiring bug.
	q := newTestQueue(fakeRenderer{}, fakeCreds{}, fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {ID: templateID, PlaybookRepo: &repoID, PlaybookPath: "site.yml", Scope: json.RawMessage(`{}`)},
	}}, fakeBastions{})
	if err := q.buildPayload(context.Background(), Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}, &workerpb.Job{}); err == nil {
		t.Fatal("a repo playbook without a repo source must fail")
	}

	// An unknown repo.
	q = newTestQueue(fakeRenderer{}, fakeCreds{}, fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {ID: templateID, PlaybookRepo: &repoID, PlaybookPath: "site.yml", Scope: json.RawMessage(`{}`)},
	}}, fakeBastions{})
	q.SetRepoSource(fakeRepos{byID: map[uuid.UUID]RepoRef{}})
	if err := q.buildPayload(context.Background(), Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}, &workerpb.Job{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown repo: %v", err)
	}

	// An unopenable git credential.
	creds := fakeCreds{err: errors.New("locked")}
	q = newTestQueue(fakeRenderer{}, creds, fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {ID: templateID, PlaybookRepo: &repoID, PlaybookPath: "site.yml", Scope: json.RawMessage(`{}`)},
	}}, fakeBastions{})
	q.SetRepoSource(fakeRepos{byID: map[uuid.UUID]RepoRef{
		repoID: {ID: repoID, URL: "ssh://git@example.org/x.git", Branch: "main", CredentialID: &gitCredID},
	}})
	if err := q.buildPayload(context.Background(), Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}, &workerpb.Job{}); err == nil {
		t.Fatal("an unopenable git credential must fail")
	}
}

func TestBuildPayloadShipsEffectiveCredentialForTemplateDefault(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hostID, credID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	templateID := uuid.Must(uuid.NewV7())

	// The host has no SSH credential of its own; the template's default
	// is the login the worker uses — the claim names it per host, so the
	// worker can map the key file.
	renderer := fakeRenderer{inv: render.Inventory{
		Doc: map[string]any{"all": map[string]any{}, "_meta": map[string]any{"hostvars": map[string]any{
			"web1": map[string]any{"ansible_host": "192.0.2.10"},
		}}},
		Connections: map[string]render.Connection{
			"web1": {HostID: hostID, BastionSource: "none"},
		},
	}}
	creds := fakeCreds{
		secrets: map[uuid.UUID]credentials.Secret{credID: {PrivateKey: "default-key"}},
		metas:   map[uuid.UUID]credentials.Credential{credID: {ID: credID, Username: "ansible"}},
	}
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {ID: templateID, PlaybookInline: "- hosts: all", Scope: json.RawMessage(`{"host_ids":["` + hostID.String() + `"]}`), CredentialID: &credID},
	}}
	q := newTestQueue(renderer, creds, templates, fakeBastions{})
	q.SetKeyPins(fakePins{})

	out := &workerpb.Job{}
	if err := q.buildPayload(context.Background(), Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}, out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	var conn map[string]any
	if err := json.Unmarshal([]byte(out.Connections["web1"]), &conn); err != nil {
		t.Fatalf("connection web1 = %q, %v", out.Connections["web1"], err)
	}
	if conn["credential_id"] != credID.String() {
		t.Fatalf("effective credential = %v, want the template default", conn["credential_id"])
	}
	if out.Credentials[credID.String()] != `{"private_key":"default-key"}` {
		t.Fatalf("default credential = %q", out.Credentials[credID.String()])
	}
	// The rendered hostvars' bastion-less args stay out.
	if _, has := conn["bastions"]; has {
		t.Fatalf("no bastions, no chain: %v", conn)
	}
}

// TestBuildPayloadRunShipsBastionPins: a run whose route dials a pinned
// jump host ships its fingerprint and full public key, so the worker's
// ssh_config demands the key per hop alias (backlog 2.40).
func TestBuildPayloadRunShipsBastionPins(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hostID, credID, bastionID, bastionCredID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	templateID := uuid.Must(uuid.NewV7())

	renderer := fakeRenderer{inv: render.Inventory{
		Doc: map[string]any{"all": map[string]any{}},
		Connections: map[string]render.Connection{
			"web1": {HostID: hostID, CredentialID: &credID, Bastions: []uuid.UUID{bastionID}, BastionSource: "host"},
		},
	}}
	creds := fakeCreds{
		secrets: map[uuid.UUID]credentials.Secret{
			credID:        {PrivateKey: "host-key"},
			bastionCredID: {PrivateKey: "hop-key"},
		},
		metas: map[uuid.UUID]credentials.Credential{
			credID: {ID: credID, Username: "ansible"},
		},
	}
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {
			ID: templateID, OrgID: org, PlaybookID: uuid.Must(uuid.NewV7()),
			PlaybookInline: "- hosts: all\n  tasks: []\n",
			Scope:          json.RawMessage(`{"host_ids":["` + hostID.String() + `"]}`),
			CredentialID:   &credID,
		},
	}}
	q := newTestQueue(renderer, creds, templates, fakeBastions{byID: map[uuid.UUID]sshconf.Bastion{
		bastionID: {ID: bastionID, CredentialID: &bastionCredID},
	}})
	q.SetKeyPins(fakePins{
		byID:           map[uuid.UUID]string{hostID: "SHA256:TARGET"},
		pubByID:        map[uuid.UUID]string{hostID: "ssh-ed25519 T"},
		bastionByID:    map[uuid.UUID]string{bastionID: "SHA256:AAAA"},
		bastionPubByID: map[uuid.UUID]string{bastionID: "ssh-ed25519 A"},
	})

	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}
	out := &workerpb.Job{}
	if err := q.buildPayload(context.Background(), job, out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if out.BastionHostKeys[bastionID.String()] != "SHA256:AAAA" {
		t.Fatalf("bastion_host_keys = %v", out.BastionHostKeys)
	}
	if out.BastionKnownHosts[bastionID.String()] != "ssh-ed25519 A" {
		t.Fatalf("bastion_known_hosts = %v", out.BastionKnownHosts)
	}

	// A pin without its full key cannot be enforced: the claim fails closed
	// with the connection-test remedy.
	q2 := newTestQueue(renderer, creds, templates, fakeBastions{byID: map[uuid.UUID]sshconf.Bastion{
		bastionID: {ID: bastionID, CredentialID: &bastionCredID},
	}})
	q2.SetKeyPins(fakePins{bastionByID: map[uuid.UUID]string{bastionID: "SHA256:AAAA"}})
	if err := q2.buildPayload(context.Background(), job, &workerpb.Job{}); err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("fingerprint-only bastion pin: %v, want the connection-test remedy", err)
	}
}

// TestBuildPayloadRunRefusesChangedBastion: a run whose route dials a
// changed jump host is refused at claim time (backlog 2.40) — the host
// rides a connection the worker cannot trust.
func TestBuildPayloadRunRefusesChangedBastion(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	// The rendered connection names the JOB's own host id (testHosts), so
	// the scope drop matches the route.
	hostID, credID, bastionID, bastionCredID := testHosts[0], uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	templateID := uuid.Must(uuid.NewV7())

	renderer := fakeRenderer{inv: render.Inventory{
		Doc: map[string]any{"all": map[string]any{}, "_meta": map[string]any{"hostvars": map[string]any{
			"web1": map[string]any{"ansible_host": "192.0.2.10", "ansible_user": "ansible"},
		}}},
		Connections: map[string]render.Connection{
			"web1": {HostID: hostID, CredentialID: &credID, Bastions: []uuid.UUID{bastionID}, BastionSource: "host"},
		},
	}}
	creds := fakeCreds{
		secrets: map[uuid.UUID]credentials.Secret{
			credID:        {PrivateKey: "host-key"},
			bastionCredID: {Password: "hop-pw"},
		},
		metas: map[uuid.UUID]credentials.Credential{credID: {ID: credID, Username: "ansible"}},
	}
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {
			ID: templateID, OrgID: org, PlaybookID: uuid.Must(uuid.NewV7()),
			PlaybookInline: "- hosts: all\n  tasks: []\n",
			Scope:          json.RawMessage(`{"host_ids":["` + hostID.String() + `"]}`),
			CredentialID:   &credID,
		},
	}}
	q := newTestQueue(renderer, creds, templates, fakeBastions{byID: map[uuid.UUID]sshconf.Bastion{
		bastionID: {ID: bastionID, CredentialID: &bastionCredID},
	}})
	q.SetKeyPins(fakePins{changedBastions: map[uuid.UUID]bool{bastionID: true}})

	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}
	err := q.buildPayload(context.Background(), job, &workerpb.Job{})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "host key changed") {
		t.Fatalf("a run through a changed bastion must be refused: %v", err)
	}
}

// TestClaimSendsActiveFieldsOnly (spec 7.75 goal 3, Review Focus 2): a run
// receives the values of active fields only. A value stored under an
// inactive field or under a name the schema no longer declares stays
// stored and is not sent — not by the template, not by a schedule's or a
// webhook's override — and a secret that is not sent is not handed to the
// worker in the redact list either. A field marked gone but still active is
// sent until a person removes it. A template without a schema sends every
// stored key, as before.
func TestClaimSendsActiveFieldsOnly(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	withSchema, withoutSchema := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	schemaRaw := json.RawMessage(`{"fields":[` +
		`{"name":"a","type":"string"},` +
		`{"name":"b","type":"secret","active":false},` +
		`{"name":"c","type":"string","active":true,"origin":"gone","source":{"kind":"vars_prompt","file":"site.yml","line":3}},` +
		`{"name":"e","type":"secret"}]}`)
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		withSchema: {
			ID: withSchema, OrgID: org, PlaybookInline: "- hosts: all\n", ExtraVarsSchema: schemaRaw,
			ExtraVars: json.RawMessage(`{"a":"A","b":"b-stored-secret-71c","c":"C","d":"D"}`),
		},
		withoutSchema: {
			ID: withoutSchema, OrgID: org, PlaybookInline: "- hosts: all\n",
			ExtraVars: json.RawMessage(`{"a":"A","b":"B","d":"D"}`),
		},
	}}
	q := newTestQueue(fakeRenderer{inv: render.Inventory{Doc: map[string]any{}}}, fakeCreds{}, templates, fakeBastions{})
	q.SetKeyPins(fakePins{})

	keys := func(m map[string]string) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	cases := []struct {
		name     string
		template uuid.UUID
		ov       *RunOverrides
		want     []string
	}{
		{"template run", withSchema, nil, []string{"a", "c"}},
		{"schedule override of an inactive field", withSchema, &RunOverrides{ExtraVars: map[string]any{"b": "b-override-secret-3e9"}}, []string{"a", "c"}},
		{"webhook override of an inactive and an undeclared field", withSchema, &RunOverrides{Untrusted: true, ExtraVars: map[string]any{"b": "b-override-secret-3e9", "d": "x"}}, []string{"a", "c"}},
		{"override of an active field", withSchema, &RunOverrides{ExtraVars: map[string]any{"a": "A2", "b": "b-override-secret-3e9"}}, []string{"a", "c"}},
		{"no schema", withoutSchema, nil, []string{"a", "b", "d"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRun, TemplateID: &c.template, HostIDs: testHosts}
			if c.ov != nil {
				job.Payload = sealedOverride(t, q, job, *c.ov)
			}
			out := &workerpb.Job{}
			if err := q.buildPayload(context.Background(), job, out); err != nil {
				t.Fatalf("buildPayload: %v", err)
			}
			if got := keys(out.ExtraVars); strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("extra var keys = %v, want %v (extra vars %v)", got, c.want, out.ExtraVars)
			}
			for _, secret := range []string{"b-stored-secret-71c", "b-override-secret-3e9"} {
				if strings.Contains(strings.Join(out.Redact, "\n"), secret) {
					t.Fatalf("a value that is not sent must not reach the worker in the redact list: %v", out.Redact)
				}
			}
		})
	}
}

// TestClaimRedactsEveryOpenedSecret: every value the template lookup
// opened from a sealed envelope is in the claim's redact list, whatever
// the schema says about its field (here a plain string) and without a
// schema at all.
func TestClaimRedactsEveryOpenedSecret(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	withSchema, withoutSchema := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		withSchema: {
			ID: withSchema, OrgID: org, PlaybookInline: "- hosts: all\n",
			ExtraVarsSchema: json.RawMessage(`{"fields":[{"name":"token","type":"string"}]}`),
			ExtraVars:       json.RawMessage(`{"token":"opened-under-a-string-5e1"}`),
			OpenedSecrets:   []string{"opened-under-a-string-5e1"},
		},
		withoutSchema: {
			ID: withoutSchema, OrgID: org, PlaybookInline: "- hosts: all\n",
			ExtraVars:     json.RawMessage(`{"legacy":"opened-without-a-schema-8a4"}`),
			OpenedSecrets: []string{"opened-without-a-schema-8a4"},
		},
	}}
	q := newTestQueue(fakeRenderer{inv: render.Inventory{Doc: map[string]any{}}}, fakeCreds{}, templates, fakeBastions{})
	q.SetKeyPins(fakePins{})
	for id, secret := range map[uuid.UUID]string{withSchema: "opened-under-a-string-5e1", withoutSchema: "opened-without-a-schema-8a4"} {
		job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRun, TemplateID: &id, HostIDs: testHosts}
		out := &workerpb.Job{}
		if err := q.buildPayload(context.Background(), job, out); err != nil {
			t.Fatalf("buildPayload: %v", err)
		}
		if !slices.Contains(out.Redact, secret) {
			t.Errorf("redact = %v, want %s", out.Redact, secret)
		}
	}
}

// TestSendableVarsKeepsRawValues: the filter keeps each sent value's JSON
// as stored (an integer past 2^53 is not rounded through float64), and a
// template without a schema — absent or JSON null — is passed through.
func TestSendableVarsKeepsRawValues(t *testing.T) {
	sc := json.RawMessage(`{"fields":[{"name":"a","type":"int"},{"name":"b","type":"int","active":false}]}`)
	got, err := sendableVars(sc, json.RawMessage(`{"a":12345678901234567891,"b":1,"z":2}`))
	if err != nil || string(got) != `{"a":12345678901234567891}` {
		t.Fatalf("sendableVars = %s, %v", got, err)
	}
	if got, err := sendableVars(sc, json.RawMessage(`null`)); err != nil || string(got) != `{}` {
		t.Fatalf("sendableVars(null vars) = %s, %v", got, err)
	}
	for _, none := range []json.RawMessage{nil, json.RawMessage(`null`)} {
		if got, err := sendableVars(none, json.RawMessage(`{"z":2}`)); err != nil || string(got) != `{"z":2}` {
			t.Fatalf("sendableVars(no schema %q) = %s, %v", none, got, err)
		}
	}
	if _, err := sendableVars(sc, json.RawMessage(`[1]`)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("sendableVars(non-object) = %v, want ErrInvalidInput", err)
	}
}
