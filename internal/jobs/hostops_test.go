// White-box tests for the connection-test claim enrichment, the claim's
// host-key pins, the block-on-mismatch scope exclusion and the finish-path
// test recording. They need the unexported buildPayload/recordTest and the
// queue internals, so they live in package jobs.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/sshconf"
)

// fullProbeCreds opens every credential the probe fixture references.
type fullProbeCreds struct{}

func (fullProbeCreds) OpenSecret(_ context.Context, _, id uuid.UUID) (credentials.Secret, error) {
	return credentials.Secret{PrivateKey: "host-key"}, nil
}

func (fullProbeCreds) Get(_ context.Context, _, id uuid.UUID) (credentials.Credential, error) {
	return credentials.Credential{ID: id, Username: "deploy"}, nil
}

// fakePins is a KeyPinSource fake: pins by host id, with the pinned full
// public keys in pubByID (a pin without an entry is fingerprint-only).
// The bastion maps are the same shape for jump hosts (backlog 2.40).
type fakePins struct {
	byID            map[uuid.UUID]string
	pubByID         map[uuid.UUID]string
	bastionByID     map[uuid.UUID]string
	bastionPubByID  map[uuid.UUID]string
	changedBastions map[uuid.UUID]bool
	err             error
}

// PinnedPublicKeys implements KnownHostSource.
func (f fakePins) PinnedPublicKeys(_ context.Context, _ uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[uuid.UUID]string{}
	for _, id := range hostIDs {
		if pub, ok := f.pubByID[id]; ok {
			out[id] = pub
		}
	}
	return out, nil
}

// ChangedHosts reports no changed host: fakePins doubles as the claim's
// key guard (hosts.Service implements both).
func (fakePins) ChangedHosts(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]bool, error) {
	return map[uuid.UUID]bool{}, nil
}

// ChangedBastions reports the fake's changed set.
func (f fakePins) ChangedBastions(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]bool, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[uuid.UUID]bool{}
	for id := range f.changedBastions {
		out[id] = true
	}
	return out, nil
}

// PinnedBastionKeys implements BastionPinSource.
func (f fakePins) PinnedBastionKeys(_ context.Context, _ uuid.UUID, bastionIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[uuid.UUID]string{}
	for _, id := range bastionIDs {
		if pin, ok := f.bastionByID[id]; ok {
			out[id] = pin
		}
	}
	return out, nil
}

// PinnedBastionPublicKeys implements BastionPinSource (full keys map to the
// fingerprint-only fake: no entry means not enforceable).
func (f fakePins) PinnedBastionPublicKeys(_ context.Context, _ uuid.UUID, bastionIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[uuid.UUID]string{}
	for _, id := range bastionIDs {
		if pub, ok := f.bastionPubByID[id]; ok {
			out[id] = pub
		}
	}
	return out, nil
}

func (f fakePins) PinnedKeys(_ context.Context, _ uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[uuid.UUID]string{}
	for _, id := range hostIDs {
		if pin, ok := f.byID[id]; ok {
			out[id] = pin
		}
	}
	return out, nil
}

// fakeProbeRenderer renders exactly one host ("web1") with the effective
// connection values a connection-test claim reads: a non-default port and
// a user, one bastion in the chain.
// fakeProbeRenderer is a SnapshotSource fake.
type fakeProbeRenderer struct {
	inv render.Inventory
	err error
}

func (f fakeProbeRenderer) Snapshot(context.Context, uuid.UUID, render.Scope) (render.Inventory, error) {
	return f.inv, f.err
}

func probeSnapshot(hostID, credID, bastionID uuid.UUID) render.Inventory {
	return render.Inventory{
		Doc: map[string]any{"all": map[string]any{}, "_meta": map[string]any{"hostvars": map[string]any{
			"web1": map[string]any{"ansible_host": "192.0.2.10", "ansible_port": 2222, "ansible_user": "deploy"},
		}}},
		Connections: map[string]render.Connection{
			"web1": {HostID: hostID, CredentialID: &credID, Bastions: []uuid.UUID{bastionID}, BastionSource: "host"},
		},
	}
}

func TestBuildPayloadConnectionTestShipsSelfContainedProbe(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hostID, credID, bastionID, bastionCredID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	pinned := "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	q := newTestQueue(fakeProbeRenderer{inv: probeSnapshot(hostID, credID, bastionID)}, fakeCreds{
		secrets: map[uuid.UUID]credentials.Secret{
			credID:        {PrivateKey: "host-key"},
			bastionCredID: {Password: "hop-pw"},
		},
	}, fakeTemplates{}, fakeBastions{byID: map[uuid.UUID]sshconf.Bastion{
		bastionID: {ID: bastionID, Address: "10.0.0.1", Port: 2223, Username: "jumper", CredentialID: &bastionCredID},
	}})
	q.SetKeyPins(fakePins{byID: map[uuid.UUID]string{hostID: pinned}})

	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindConnectionTest,
		HostIDs: []uuid.UUID{hostID}, Payload: json.RawMessage(`{"note":"ignored"}`)}
	out := &workerpb.Job{}
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), job, out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if out.Kind != "connection_test" || out.OrgId != org.String() || out.ZoneId != zone.String() {
		t.Fatalf("identity = %+v", out)
	}
	// The probe consumes the kind payload only: no snapshot, no connections,
	// no credentials map.
	if out.InventoryDoc != "{}" || len(out.Connections) != 0 || len(out.Credentials) != 0 {
		t.Fatalf("snapshot must not ship on a probe claim: %+v", out)
	}
	if out.HostKeys["web1"] != pinned {
		t.Fatalf("host keys = %v", out.HostKeys)
	}
	var payload struct {
		Host struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Address  string `json:"address"`
			Port     int    `json:"port"`
			Username string `json:"username"`
		} `json:"host"`
		Credential string `json:"credential"`
		Bastions   []struct {
			ID         string `json:"id"`
			Address    string `json:"address"`
			Port       int    `json:"port"`
			Username   string `json:"username"`
			Credential string `json:"credential"`
		} `json:"bastions"`
	}
	if err := json.Unmarshal([]byte(out.KindPayload), &payload); err != nil {
		t.Fatalf("kind payload = %q: %v", out.KindPayload, err)
	}
	if payload.Host.ID != hostID.String() || payload.Host.Name != "web1" || payload.Host.Address != "192.0.2.10" ||
		payload.Host.Port != 2222 || payload.Host.Username != "deploy" {
		t.Fatalf("host = %+v", payload.Host)
	}
	if payload.Credential != `{"private_key":"host-key"}` {
		t.Fatalf("credential = %q", payload.Credential)
	}
	if len(payload.Bastions) != 1 {
		t.Fatalf("bastions = %+v", payload.Bastions)
	}
	hop := payload.Bastions[0]
	if hop.ID != bastionID.String() || hop.Address != "10.0.0.1" || hop.Port != 2223 || hop.Username != "jumper" {
		t.Fatalf("hop = %+v", hop)
	}
	if hop.Credential != `{"password":"hop-pw"}` {
		t.Fatalf("hop credential = %q", hop.Credential)
	}
}

func TestBuildPayloadConnectionTestMinimal(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hostID := uuid.Must(uuid.NewV7())
	// No credential, no bastions, no pin: the payload carries the empty
	// shapes, the default port, and an empty host-keys map.
	q := newTestQueue(fakeProbeRenderer{inv: render.Inventory{
		Doc: map[string]any{"all": map[string]any{}, "_meta": map[string]any{"hostvars": map[string]any{
			"web1": map[string]any{"ansible_host": "web1.example.test"},
		}}},
		Connections: map[string]render.Connection{"web1": {HostID: hostID, BastionSource: "none"}},
	}}, fakeCreds{}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})

	out := &workerpb.Job{}
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindConnectionTest, HostIDs: []uuid.UUID{hostID},
	}, out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if _, pinned := out.HostKeys["web1"]; pinned {
		t.Fatalf("unpinned host must ship no key: %v", out.HostKeys)
	}
	var payload struct {
		Host struct {
			Address string `json:"address"`
			Port    int    `json:"port"`
		} `json:"host"`
		Credential string `json:"credential"`
		Bastions   []any  `json:"bastions"`
	}
	if err := json.Unmarshal([]byte(out.KindPayload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Host.Address != "web1.example.test" || payload.Host.Port != sshconf.DefaultPort {
		t.Fatalf("host = %+v", payload.Host)
	}
	if payload.Credential != "" || len(payload.Bastions) != 0 {
		t.Fatalf("empty credential and chain: %+v", payload)
	}
}

func TestBuildPayloadConnectionTestFailsClosed(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hostID, credID, bastionID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	target := func(q *SQLQueue, id uuid.UUID) error {
		return q.buildPayload(db.WithOrgScope(context.Background(), org), Job{
			ID: id, OrgID: org, ZoneID: zone, Kind: KindConnectionTest, HostIDs: []uuid.UUID{hostID},
		}, &workerpb.Job{})
	}
	// No pins source wired: the probe could not run TOFU.
	q := newTestQueue(fakeProbeRenderer{inv: probeSnapshot(hostID, credID, bastionID)}, fullProbeCreds{}, fakeTemplates{}, fakeBastions{byID: map[uuid.UUID]sshconf.Bastion{
		bastionID: {ID: bastionID, Address: "10.0.0.1", Port: 2223, Username: "jumper"},
	}})
	if err := target(q, uuid.Must(uuid.NewV7())); err == nil || !strings.Contains(err.Error(), "no host key pin source wired") {
		t.Fatalf("missing pins source: %v", err)
	}

	// A pins source that fails — reached only when every other resolution
	// (credential, bastion) succeeds first.
	q = newTestQueue(fakeProbeRenderer{inv: probeSnapshot(hostID, credID, bastionID)}, fullProbeCreds{}, fakeTemplates{}, fakeBastions{byID: map[uuid.UUID]sshconf.Bastion{
		bastionID: {ID: bastionID, Address: "10.0.0.1", Port: 2223, Username: "jumper"},
	}})
	q.SetKeyPins(fakePins{err: errors.New("locked")})
	if err := target(q, uuid.Must(uuid.NewV7())); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("failing pins source: %v", err)
	}

	// A job that carries no single host id.
	q = newTestQueue(fakeProbeRenderer{}, fakeCreds{}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindConnectionTest,
	}, &workerpb.Job{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero-host job: %v", err)
	}

	// A host the snapshot does not render.
	q = newTestQueue(fakeProbeRenderer{}, fakeCreds{}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	if err := target(q, uuid.Must(uuid.NewV7())); err == nil {
		t.Fatal("unrenderable host must fail the claim")
	}

	// An unopenable credential.
	q = newTestQueue(fakeProbeRenderer{inv: probeSnapshot(hostID, credID, bastionID)}, fakeCreds{err: errors.New("locked")}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	if err := target(q, uuid.Must(uuid.NewV7())); err == nil {
		t.Fatal("unopenable credential must fail the claim")
	}

	// An unknown bastion in the rendered chain.
	q = newTestQueue(fakeProbeRenderer{inv: probeSnapshot(hostID, credID, bastionID)}, fakeCreds{}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	if err := target(q, uuid.Must(uuid.NewV7())); err == nil {
		t.Fatal("unknown bastion must fail the claim")
	}
}

// TestBuildPayloadRunShipsPinnedHostKeys pins the run claim's host_keys:
// the pinned fingerprint rides by host name; unpinned hosts stay out.
func TestBuildPayloadRunShipsPinnedHostKeys(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hostID := uuid.Must(uuid.NewV7())
	templateID := uuid.Must(uuid.NewV7())
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {ID: templateID, OrgID: org, PlaybookID: uuid.Must(uuid.NewV7()), PlaybookInline: "- hosts: all",
			Scope: json.RawMessage(`{}`)},
	}}
	renderer := fakeRenderer{inv: render.Inventory{
		Doc:         map[string]any{"all": map[string]any{}},
		Connections: map[string]render.Connection{"web1": {HostID: hostID}},
	}}
	q := newTestQueue(renderer, fakeCreds{}, templates, fakeBastions{})
	q.SetKeyPins(fakePins{byID: map[uuid.UUID]string{hostID: "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"}, pubByID: map[uuid.UUID]string{hostID: "ssh-ed25519 AAAA pinned"}})
	out := &workerpb.Job{}
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRun, TemplateID: &templateID, HostIDs: []uuid.UUID{hostID},
	}, out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if len(out.HostKeys) != 1 || out.HostKeys["web1"] != "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB" || out.KnownHosts["web1"] != "ssh-ed25519 AAAA pinned" {
		t.Fatalf("host keys = %v, want exactly the claimed host's pin", out.HostKeys)
	}
}

func TestBuildPayloadRunWithoutKeyGuardFailsClosed(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hostID := uuid.Must(uuid.NewV7())
	templateID := uuid.Must(uuid.NewV7())
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {ID: templateID, OrgID: org, PlaybookID: uuid.Must(uuid.NewV7()), PlaybookInline: "- hosts: all",
			Scope: json.RawMessage(`{}`)},
	}}
	renderer := fakeRenderer{inv: render.Inventory{
		Doc:         map[string]any{"all": map[string]any{}},
		Connections: map[string]render.Connection{"web1": {HostID: hostID}},
	}}
	// No pins source and no key guard: a host-bound claim could neither
	// exclude changed keys nor ship pins, so it fails closed.
	q := newTestQueue(renderer, fakeCreds{}, templates, fakeBastions{})
	q.keyGuard = nil
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRun, TemplateID: &templateID, HostIDs: []uuid.UUID{hostID},
	}, &workerpb.Job{}); err == nil {
		t.Fatal("a run claim without a key guard must fail closed")
	}
}

// fakeTestSink records RecordConnectionTest calls.
type fakeTestSink struct {
	mu    sync.Mutex
	calls []testRecord
}

type testRecord struct {
	orgID, hostID uuid.UUID
	at            time.Time
	ok            bool
	hostKey       string
	hostPublicKey string
	hostKeyState  string
	checks        []hosts.HostTestCheck
}

func (f *fakeTestSink) RecordConnectionTest(_ context.Context, orgID, hostID uuid.UUID, at time.Time, ok bool, hostKey, hostPublicKey, hostKeyState string, checks []hosts.HostTestCheck, hops []hosts.HopKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, testRecord{orgID: orgID, hostID: hostID, at: at, ok: ok, hostKey: hostKey, hostPublicKey: hostPublicKey, hostKeyState: hostKeyState, checks: checks})
	return nil
}

func (f *fakeTestSink) all() []testRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// seedTestOrg creates the org a connection_test job needs and returns its id.
func seedTestSinkOrg(t *testing.T, q *gen.Queries, slug string) uuid.UUID {
	t.Helper()
	org, err := q.CreateOrganization(context.Background(), gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

func TestFinishRecordsConnectionTest(t *testing.T) {
	d := testdb.Open(t)
	org := seedTestSinkOrg(t, gen.New(d.Pool()), "jobs-test-sink")
	zone := testZone(t, d, org)
	hostID, credID, bastionID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	q := NewSQLQueue(d, fakeBox{}, fakeProbeRenderer{inv: probeSnapshot(hostID, credID, bastionID)}, fullProbeCreds{}, fakeTemplates{}, fakeBastions{byID: map[uuid.UUID]sshconf.Bastion{
		bastionID: {ID: bastionID, Address: "10.0.0.1", Port: 2223, Username: "jumper"},
	}}, time.Now, rand.Reader)
	q.SetKeyPins(fakePins{})
	sink := &fakeTestSink{}
	q.SetTestSink(sink)
	store := NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	// A connection_test job claimed, its single result event streamed, then
	// finished failed — the outcome records with the EVENT's timestamp.
	j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindConnectionTest,
		HostIDs: []uuid.UUID{hostID}, Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), CreatedAt: time.Now()}
	if err := store.CreateJob(scoped, j, nil); err != nil {
		t.Fatal(err)
	}
	claim, err := q.Claim(context.Background(), org, []uuid.UUID{zone}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("Claim = %v, %v", claim, err)
	}
	stream, err := q.BindStream(context.Background(), j.ID, claim.Token)
	if err != nil {
		t.Fatal(err)
	}
	// Postgres stores timestamps at microsecond precision; time.Now() on
	// Linux carries nanoseconds, so the round-tripped time would never be
	// Equal to an untruncated one (macOS clocks hide this: they tick in µs).
	at := time.Now().Add(-time.Minute).Truncate(time.Microsecond)
	// A failed test: reachable and observed, but authentication refused.
	result := `{"reachable":true,"host_key":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","host_key_state":"unpinned","auth":false,"sudo":false,"python":false,"checks":[{"name":"reachability","detail":"connected","ok":true},{"name":"auth","detail":"unable to authenticate","ok":false}]}`
	if err := stream.Append(context.Background(), Event{Seq: 1, Host: "web1", Task: "connection_test", State: "failed", Payload: json.RawMessage(result), At: at}); err != nil {
		t.Fatal(err)
	}
	if err := q.Finish(context.Background(), j.ID, StatusFailed, nil, "ssh authentication failed"); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	calls := sink.all()
	if len(calls) != 1 {
		t.Fatalf("sink calls = %+v, want exactly one", calls)
	}
	rec := calls[0]
	if rec.orgID != org || rec.hostID != hostID || !rec.at.Equal(at) {
		t.Fatalf("record = %+v", rec)
	}
	if rec.ok || rec.hostKeyState != "unpinned" || rec.hostKey != "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" {
		t.Fatalf("record fields = %+v", rec)
	}
	if len(rec.checks) != 2 || rec.checks[0].Name != "reachability" || rec.checks[1].Name != "auth" || rec.checks[1].OK {
		t.Fatalf("checks = %+v", rec.checks)
	}
}

func TestFinishConnectionTestCancelledRecordsNothing(t *testing.T) {
	d := testdb.Open(t)
	org := seedTestSinkOrg(t, gen.New(d.Pool()), "jobs-test-sink-cancel")
	zone := testZone(t, d, org)
	hostID := uuid.Must(uuid.NewV7())
	q := NewSQLQueue(d, fakeBox{}, fakeProbeRenderer{}, fakeCreds{}, fakeTemplates{}, fakeBastions{}, time.Now, rand.Reader)
	sink := &fakeTestSink{}
	q.SetTestSink(sink)
	store := NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindConnectionTest,
		HostIDs: []uuid.UUID{hostID}, Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), CreatedAt: time.Now()}
	if err := store.CreateJob(scoped, j, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(context.Background(), org, []uuid.UUID{zone}, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := q.Finish(context.Background(), j.ID, StatusCancelled, nil, ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if calls := sink.all(); len(calls) != 0 {
		t.Fatalf("a cancelled test must record nothing: %+v", calls)
	}
}

func TestFinishConnectionTestWithoutEventRecordsNothing(t *testing.T) {
	d := testdb.Open(t)
	org := seedTestSinkOrg(t, gen.New(d.Pool()), "jobs-test-sink-noevent")
	zone := testZone(t, d, org)
	hostID := uuid.Must(uuid.NewV7())
	q := NewSQLQueue(d, fakeBox{}, fakeProbeRenderer{}, fakeCreds{}, fakeTemplates{}, fakeBastions{}, time.Now, rand.Reader)
	sink := &fakeTestSink{}
	q.SetTestSink(sink)
	store := NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	// The job errored at claim (payload build failed) — no events exist.
	j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindConnectionTest,
		HostIDs: []uuid.UUID{hostID}, Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), CreatedAt: time.Now()}
	if err := store.CreateJob(scoped, j, nil); err != nil {
		t.Fatal(err)
	}
	if claim, err := q.Claim(context.Background(), org, []uuid.UUID{zone}, "v1"); err != nil || claim != nil {
		t.Fatalf("Claim = %v, %v — the unbuildable claim must silently fail the job", claim, err)
	}
	// The failed claim already marked the job terminal, so finishing it is
	// the idempotent no-op; no event means no record either way.
	if err := q.Finish(context.Background(), j.ID, StatusError, nil, "boom"); err != nil {
		t.Fatalf("Finish (idempotent): %v", err)
	}
	if calls := sink.all(); len(calls) != 0 {
		t.Fatalf("no event, no record: %+v", calls)
	}
}

func TestRecordTestErrorPathsAreLoggedNotFatal(t *testing.T) {
	d := testdb.Open(t)
	org := seedTestSinkOrg(t, gen.New(d.Pool()), "jobs-test-sink-errors")
	zone := testZone(t, d, org)
	hostID := uuid.Must(uuid.NewV7())
	bastionID := uuid.Must(uuid.NewV7())
	credID := uuid.Must(uuid.NewV7())
	q := NewSQLQueue(d, fakeBox{}, fakeProbeRenderer{inv: probeSnapshot(hostID, credID, bastionID)}, fullProbeCreds{}, fakeTemplates{}, fakeBastions{byID: map[uuid.UUID]sshconf.Bastion{
		bastionID: {ID: bastionID, Address: "10.0.0.1", Port: 2223, Username: "jumper"},
	}}, time.Now, rand.Reader)
	q.SetKeyPins(fakePins{})
	sink := &fakeTestSink{}
	q.SetTestSink(sink)
	store := NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	// A job whose claim failed carries no single host: recordTest logs and
	// records nothing (the job is already terminal, no event, no record).
	j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindConnectionTest,
		HostIDs: nil, Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), CreatedAt: time.Now()}
	if err := store.CreateJob(scoped, j, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(context.Background(), org, []uuid.UUID{zone}, "v1"); err != nil || claimJobID(t, d, org, zone) != uuid.Nil {
		t.Fatal("the unbuildable claim must fail the job")
	}
	if _, err := q.Claim(context.Background(), org, []uuid.UUID{zone}, "v1"); err != nil {
		t.Fatalf("empty queue: %v", err)
	}
	// The job errored at claim; no events → recordTest (never called from
	// Finish on this path, but the lookup-with-no-event branch is covered
	// by the Finish path on a running job below).
	if err := q.Finish(context.Background(), j.ID, StatusError, nil, "boom"); err != nil {
		t.Fatalf("Finish (idempotent on the already-errored job): %v", err)
	}

	// A running connection_test job whose event payload is not a result:
	// recordTest logs the parse failure and records nothing.
	j2 := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindConnectionTest,
		HostIDs: []uuid.UUID{hostID}, Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), CreatedAt: time.Now()}
	if err := store.CreateJob(scoped, j2, nil); err != nil {
		t.Fatal(err)
	}
	claim, err := q.Claim(context.Background(), org, []uuid.UUID{zone}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("Claim = %v, %v", claim, err)
	}
	stream, err := q.BindStream(context.Background(), j2.ID, claim.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Append(context.Background(), Event{Seq: 1, State: "failed", Payload: json.RawMessage(`{oops`), At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := q.Finish(context.Background(), j2.ID, StatusFailed, nil, ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if calls := sink.all(); len(calls) != 0 {
		t.Fatalf("an unparseable result records nothing: %+v", calls)
	}
}

// claimJobID returns the id of the currently running (claimed) job, or
// uuid.Nil when none — a debug helper for claim-path assertions.
func claimJobID(t *testing.T, d *db.DB, org, zone uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := d.Pool().QueryRow(context.Background(),
		`SELECT id FROM jobs WHERE org_id = $1 AND zone_id = $2 AND status = 'running'`, org, zone).Scan(&id); err != nil {
		return uuid.Nil
	}
	return id
}

// failingTestSink always fails the record — the sink-error tradeoff: the
// job's terminal status already stands, the failure is logged, never fatal.
type failingTestSink struct{}

func (failingTestSink) RecordConnectionTest(context.Context, uuid.UUID, uuid.UUID, time.Time, bool, string, string, string, []hosts.HostTestCheck, []hosts.HopKey) error {
	return errors.New("disk full")
}

func TestFinishConnectionTestTwoHostsRecordsNothing(t *testing.T) {
	d := testdb.Open(t)
	org := seedTestSinkOrg(t, gen.New(d.Pool()), "jobs-test-sink-multi")
	zone := testZone(t, d, org)
	q := NewSQLQueue(d, fakeBox{}, fakeProbeRenderer{}, fullProbeCreds{}, fakeTemplates{}, fakeBastions{}, time.Now, rand.Reader)
	q.SetKeyPins(fakePins{})
	sink := &fakeTestSink{}
	q.SetTestSink(sink)
	store := NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	// A connection_test job that carries two hosts is a wiring bug: the
	// probe target is self-contained per host. It fails closed at claim
	// (a multi-host connection_test cannot build a payload), so the branch
	// is driven through recordTest directly with a seeded running job.
	j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindConnectionTest,
		HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())},
		Status:  StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), CreatedAt: time.Now()}
	if err := store.CreateJob(scoped, j, nil); err != nil {
		t.Fatal(err)
	}
	claim, err := q.Claim(context.Background(), org, []uuid.UUID{zone}, "v1")
	if err != nil || claim != nil {
		t.Fatalf("a multi-host connection_test claim must fail closed: %v, %v", claim, err)
	}
	// Seed the running job the claim would have produced, then recordTest.
	if _, err := d.Pool().Exec(context.Background(),
		`UPDATE jobs SET status = 'running', claimed_at = now(), host_ids = $2 WHERE id = $1`,
		j.ID, []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(context.Background(),
		`INSERT INTO job_events (job_id, seq, org_id, at, host, task, state, payload)
		 VALUES ($1, 1, $2, now(), 'web1', 'connection_test', 'ok', '{"reachable":true}')`, j.ID, org); err != nil {
		t.Fatal(err)
	}
	if err := q.recordTest(context.Background(), j.ID, StatusSucceeded); err != nil {
		t.Fatalf("recordTest: %v", err)
	}
	if calls := sink.all(); len(calls) != 0 {
		t.Fatalf("a multi-host connection_test records nothing: %+v", calls)
	}
}

// TestFinishConnectionTestSinkFailureFailsTheFinish (fix round 1): the
// connection-test row and the terminal status commit together, so a
// failing sink fails the finish and leaves the job running for the retry.
func TestFinishConnectionTestSinkFailureFailsTheFinish(t *testing.T) {
	d := testdb.Open(t)
	org := seedTestSinkOrg(t, gen.New(d.Pool()), "jobs-test-sink-fails")
	zone := testZone(t, d, org)
	hostID := uuid.Must(uuid.NewV7())
	bastionID := uuid.Must(uuid.NewV7())
	q := NewSQLQueue(d, fakeBox{}, fakeProbeRenderer{inv: probeSnapshot(hostID, uuid.Must(uuid.NewV7()), bastionID)}, fullProbeCreds{}, fakeTemplates{}, fakeBastions{byID: map[uuid.UUID]sshconf.Bastion{
		bastionID: {ID: bastionID, Address: "10.0.0.1", Port: 2223, Username: "jumper"},
	}}, time.Now, rand.Reader)
	q.SetKeyPins(fakePins{})
	q.SetTestSink(failingTestSink{})
	store := NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindConnectionTest,
		HostIDs: []uuid.UUID{hostID}, Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), CreatedAt: time.Now()}
	if err := store.CreateJob(scoped, j, nil); err != nil {
		t.Fatal(err)
	}
	claim, err := q.Claim(context.Background(), org, []uuid.UUID{zone}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("Claim = %v, %v", claim, err)
	}
	stream, err := q.BindStream(context.Background(), j.ID, claim.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Append(context.Background(), Event{Seq: 1, State: "ok", Payload: json.RawMessage(`{"reachable":true,"auth":true,"sudo":true,"python":true}`), At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := q.Finish(context.Background(), j.ID, StatusSucceeded, nil, ""); err == nil {
		t.Fatal("Finish succeeded although the test sink failed")
	}
	var status string
	if err := d.Pool().QueryRow(context.Background(), `SELECT status FROM jobs WHERE id = $1`, j.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if Status(status) != StatusRunning {
		t.Fatalf("status after the failed finish = %s, want running", status)
	}
	// A host deleted meanwhile is permanent: logged and skipped.
	q.SetTestSink(goneTestSink{})
	if err := q.Finish(context.Background(), j.ID, StatusSucceeded, nil, ""); err != nil {
		t.Fatalf("Finish with a vanished host: %v", err)
	}
}

type goneTestSink struct{}

func (goneTestSink) RecordConnectionTest(context.Context, uuid.UUID, uuid.UUID, time.Time, bool, string, string, string, []hosts.HostTestCheck, []hosts.HopKey) error {
	return hosts.ErrNotFound
}
