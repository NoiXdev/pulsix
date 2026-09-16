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

// testHosts is the target list the white-box payload tests give their
// host-bound jobs: a claim renders exactly job.HostIDs, and an empty list
// fails closed.
var testHosts = []uuid.UUID{uuid.MustParse("01a0ac45-0000-7000-8000-000000000001")}

func newTestQueue(renderer SnapshotSource, creds CredentialSource, templates TemplateSource, bastions BastionSource) *SQLQueue {
	return &SQLQueue{
		box: fakeBox{}, renderer: renderer, creds: creds, templates: templates, bastions: bastions,
		// The key guard defaults to "nothing changed"; the pins source
		// stays unwired so the fail-closed probe paths remain testable.
		keyGuard: fakePins{},
		now:      time.Now, rand: rand.Reader, streams: newStreamRegistry(), cancels: newCancelRegistry(),
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
	r.register(jobID, orgID, "tok", now)

	e, ok := r.get(jobID)
	if !ok || e.token != "tok" || e.orgID != orgID {
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
	r.remove(jobID)
	if _, ok := r.get(jobID); ok {
		t.Fatal("removed entry must be gone")
	}
	if ids := r.aliveIDs(now.Add(-time.Second)); len(ids) != 0 {
		t.Fatalf("aliveIDs after removal = %v", ids)
	}
}

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
	zone := uuid.Must(uuid.NewV7())
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
