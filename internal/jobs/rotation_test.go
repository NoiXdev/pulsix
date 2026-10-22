// The rotation enqueue's unit tests (package-internal white-box fakes for
// Service.rotationTargets). The store-level rotation loop lives in
// rotation_store_test.go (package jobs_test), where the existing external
// helpers are.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/sshconf"
)

// fakeRotationTargets is the RotationTargets fake.
type fakeRotationTargets struct {
	hosts       []RotationHost
	bastions    []sshconf.Bastion
	orgBastions []sshconf.Bastion
}

func (f fakeRotationTargets) ListCredentialHosts(_ context.Context, _, _ uuid.UUID) ([]RotationHost, error) {
	return f.hosts, nil
}

func (f fakeRotationTargets) ListCredentialBastions(_ context.Context, _, _ uuid.UUID) ([]sshconf.Bastion, error) {
	return f.bastions, nil
}

// ListOrgBastions reports the full org set the chain walk needs (the
// referenced set when the test gives nothing else).
func (f fakeRotationTargets) ListOrgBastions(_ context.Context, _ uuid.UUID) ([]sshconf.Bastion, error) {
	if f.orgBastions != nil {
		return f.orgBastions, nil
	}
	return f.bastions, nil
}

// noQueue is the minimal Queue a rotation-enqueue-only Service needs.
type noQueue struct{}

func (noQueue) Claim(context.Context, uuid.UUID, []uuid.UUID, string) (*Claim, error) {
	return nil, nil
}
func (noQueue) BindStream(context.Context, uuid.UUID, string) (Stream, error) {
	return nil, errors.New("not wired")
}
func (noQueue) Finish(context.Context, uuid.UUID, Status, map[string]any, string) error { return nil }

// oneZone is the DefaultZoneSource stand-in for the bastion targets.
type oneZone struct{ id uuid.UUID }

func (z oneZone) ListZones(context.Context, uuid.UUID) ([]hosts.Zone, error) {
	return []hosts.Zone{{ID: z.id, Slug: hosts.DefaultZoneSlug}}, nil
}

// rotationOnlyStore is the Store stand-in the enqueue test needs: it
// records CreateJob calls and models the groupInserter capability with
// all-or-nothing semantics (staged rows commit only when the whole call
// succeeds).
type rotationOnlyStore struct {
	mu             sync.Mutex
	jobs           []Job
	payloads       map[uuid.UUID]json.RawMessage
	nextErr        error
	groupCalls     int
	groupID        uuid.UUID       // the group id the last group insert carried
	groupTargets   json.RawMessage // the targets snapshot the last group insert carried
	failGroupAt    int             // 1-based job index whose insert fails; 0 = no failure
	groupErr       error           // the failure the staged insert trips
	stagedPayloads map[uuid.UUID]json.RawMessage
}

func newRotationOnlyStore() *rotationOnlyStore {
	return &rotationOnlyStore{payloads: map[uuid.UUID]json.RawMessage{}, stagedPayloads: map[uuid.UUID]json.RawMessage{}}
}

func (s *rotationOnlyStore) CreateJob(_ context.Context, j Job, payload json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextErr != nil {
		return s.nextErr
	}
	s.jobs = append(s.jobs, j)
	s.payloads[j.ID] = payload
	return nil
}

// CreateJobGroup implements the groupInserter capability with the same
// all-or-nothing contract the real store's transaction gives: rows stage
// into the group's own slice and only become visible when the whole call
// succeeds, so a mid-group failure leaves nothing behind. The group id and
// targets snapshot are recorded with it — they ride the same transaction.
func (s *rotationOnlyStore) CreateRotationGroup(ctx context.Context, orgID, _, groupID uuid.UUID, targets json.RawMessage, jobs []Job, payloads map[uuid.UUID]json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.groupCalls++
	s.groupID, s.groupTargets = groupID, targets
	if s.nextErr != nil {
		return s.nextErr
	}
	if s.stagedPayloads == nil {
		s.stagedPayloads = map[uuid.UUID]json.RawMessage{}
	}
	staged := make([]Job, 0, len(jobs))
	for i, j := range jobs {
		if s.failGroupAt > 0 && i == s.failGroupAt {
			return s.groupErr
		}
		staged = append(staged, j)
		s.stagedPayloads[j.ID] = payloads[j.ID]
	}
	s.jobs = append(s.jobs, staged...)
	if s.payloads == nil {
		s.payloads = map[uuid.UUID]json.RawMessage{}
	}
	for id, p := range s.stagedPayloads {
		s.payloads[id] = p
	}
	return nil
}

func (s *rotationOnlyStore) GetJob(context.Context, uuid.UUID, uuid.UUID) (Job, error) {
	return Job{}, ErrNotFound
}

func (s *rotationOnlyStore) ListJobs(context.Context, uuid.UUID, Filter, string, int) ([]Job, string, error) {
	return nil, "", nil
}

func (s *rotationOnlyStore) CancelJob(context.Context, uuid.UUID, uuid.UUID) (Status, error) {
	return "", ErrNotRunning
}

func (s *rotationOnlyStore) ListJobEvents(context.Context, uuid.UUID, uuid.UUID, int64) ([]Event, error) {
	return nil, nil
}

func (s *rotationOnlyStore) RecordSyncResult(context.Context, uuid.UUID, uuid.UUID, time.Time, string, string, string) error {
	return nil
}

func (s *rotationOnlyStore) HasActiveTemplateJob(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}

func (s *rotationOnlyStore) HasActiveRepoSync(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}

// ReserveRunGroup is unused by every test built on this fake: rotation,
// bastion-key-claim and host-key-enforcement enqueues never set
// RunRequest.IdempotencyKey, so EnqueueRunWith never calls it.
func (s *rotationOnlyStore) ReserveRunGroup(context.Context, uuid.UUID, string, uuid.UUID) (uuid.UUID, bool, error) {
	return uuid.Nil, false, errors.New("ReserveRunGroup not implemented by rotationOnlyStore")
}

func (s *rotationOnlyStore) GroupSummaries(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]GroupSummary, error) {
	return nil, nil
}

// TestEnqueueRotationCreatesOneJobPerTarget pins the group shape: one
// rotate_key job per host (its zone) plus its follow-up connection_test
// (same group and zone, created strictly later so it claims after the
// playbook run), one rotate_key per bastion (the default zone, no
// host-bound test), all payloads sealed with the job row's own AAD.
func TestEnqueueRotationCreatesOneJobPerTarget(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	zoneA, zoneB, zoneDefault := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	credID := uuid.Must(uuid.NewV7())
	host1, host2, bastion1 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	store := newRotationOnlyStore()
	svc := &Service{
		store: store, queue: noQueue{},
		zones: oneZone{zoneDefault},
		box:   fakeBox{}, now: time.Now, rand: rand.Reader,
	}
	svc.rotationTargets = fakeRotationTargets{
		hosts:    []RotationHost{{ID: host1, ZoneID: zoneA}, {ID: host2, ZoneID: zoneB}},
		bastions: []sshconf.Bastion{{ID: bastion1, Name: "jump-01", Address: "10.0.0.9", Port: 22}},
	}
	key := credentials.PendingKey{
		PrivateKey:  credentials.Secret{PrivateKey: "new-pem"},
		PublicKey:   "ssh-ed25519 AAAA new",
		Fingerprint: "SHA256:new",
		KeyType:     "ed25519",

		PreviousPublicKey: "ssh-ed25519 AAAA old",
	}
	actor := uuid.Must(uuid.NewV7())

	group, first, count, err := svc.EnqueueRotation(db.WithOrgScope(context.Background(), org), org, credID, key, &actor)
	if err != nil {
		t.Fatalf("EnqueueRotation: %v", err)
	}
	if count != 5 || first == uuid.Nil || group == uuid.Nil {
		t.Fatalf("group = %s, first = %s, count = %d, want 2 runs + 2 tests + 1 bastion run", group, first, count)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.jobs) != 5 {
		t.Fatalf("stored jobs = %d", len(store.jobs))
	}
	if store.groupCalls != 1 {
		t.Fatalf("group insert calls = %d, want exactly one transactional call", store.groupCalls)
	}
	// The verification jobs come strictly after the playbook runs in the
	// claim order: created_at advances one microsecond per job (the
	// monotonic clock readings stay comparable, so .After is exact).
	// The claim order (created_at) must put every HOST's verification test
	// after its playbook run — the created_at advances one microsecond per
	// job. The bastion run comes last and carries no host-bound test.
	var lastHostRunAt, firstTestAt time.Time
	for _, j := range store.jobs {
		if j.Kind == KindRotateKey && len(j.HostIDs) == 1 &&
			(lastHostRunAt.IsZero() || j.CreatedAt.After(lastHostRunAt)) {
			lastHostRunAt = j.CreatedAt
		}
		if j.Kind == KindConnectionTest && (firstTestAt.IsZero() || j.CreatedAt.Before(firstTestAt)) {
			firstTestAt = j.CreatedAt
		}
	}
	if firstTestAt.IsZero() || !firstTestAt.After(lastHostRunAt) {
		t.Fatalf("the follow-up tests must claim after the host playbook runs: last host run %v, first test %v", lastHostRunAt, firstTestAt)
	}
	for _, j := range store.jobs {
		if j.RunGroupID != group {
			t.Fatalf("job %s carries group %s, want %s", j.ID, j.RunGroupID, group)
		}
		if j.Status != StatusQueued || j.Trigger != TriggerManual {
			t.Fatalf("job = %+v", j)
		}
		var b64 string
		if err := json.Unmarshal(store.payloads[j.ID], &b64); err != nil {
			t.Fatalf("payload of %s is not the sealed string form: %v", j.ID, err)
		}
		sealed, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := fakeBox{}.OpenAAD(sealed, JobAAD(org, j.ID))
		if err != nil {
			t.Fatalf("open payload of %s: %v", j.ID, err)
		}
		var rp rotationJobPayload
		if err := json.Unmarshal(plain, &rp); err != nil {
			t.Fatal(err)
		}
		if rp.CredentialID != credID || rp.NewPrivateKey.PrivateKey != "new-pem" || rp.OldPublicKey != "ssh-ed25519 AAAA old" ||
			rp.NewPublicKey != "ssh-ed25519 AAAA new" || rp.NewFingerprint != "SHA256:new" || rp.NewKeyType != "ed25519" {
			t.Fatalf("rotation payload of %s = %+v", j.ID, rp)
		}
		// Every job belongs to the credential's rotation (the exclusivity
		// marker); only a verification depends on something — its host's
		// rotate_key run.
		if j.RotationCredentialID == nil || *j.RotationCredentialID != credID {
			t.Fatalf("job %s rotation credential = %v", j.ID, j.RotationCredentialID)
		}
		if j.Kind == KindConnectionTest {
			var dep *Job
			for i := range store.jobs {
				if j.DependsOn != nil && store.jobs[i].ID == *j.DependsOn {
					dep = &store.jobs[i]
				}
			}
			if dep == nil || dep.Kind != KindRotateKey || dep.HostIDs[0] != j.HostIDs[0] {
				t.Fatalf("verification %s depends on %v, want its host's rotate_key run", j.ID, j.DependsOn)
			}
		} else if j.DependsOn != nil {
			t.Fatalf("%s %s depends on %v, want nothing", j.Kind, j.ID, *j.DependsOn)
		}
		// Every job's payload carries the same pending key; the kind and
		// host scope differ. The follow-up test of a host target is the
		// group's verification: a connection_test in the host's zone.
		switch {
		case j.Kind == KindRotateKey && rp.Target.Kind == rotationTargetBastion:
			if rp.Target.ID != bastion1 || len(j.HostIDs) != 0 || j.ZoneID != zoneDefault {
				t.Fatalf("bastion job = %+v (target %+v)", j, rp.Target)
			}
		case j.Kind == KindRotateKey && rp.Target.Kind == rotationTargetHost:
			if len(j.HostIDs) != 1 || (j.ZoneID != zoneA && j.ZoneID != zoneB) || rp.Target.ID != j.HostIDs[0] {
				t.Fatalf("host run = %+v", j)
			}
		case j.Kind == KindConnectionTest:
			if rp.Target.Kind != rotationTargetHost || rp.Target.ID != j.HostIDs[0] {
				t.Fatalf("verification job = %+v (target %+v)", j, rp.Target)
			}
		default:
			t.Fatalf("unexpected job kind %q (target %+v)", j.Kind, rp.Target.Kind)
		}
	}
}

// TestEnqueueRotationSnapshotsItsTargets pins backlog 2.46: the enqueue
// writes the referencing host and jump host ids into a rotation_groups row
// keyed by the group id, inside the SAME transactional insert call the
// group's jobs ride — the snapshot the revocation phase later reads, so a
// target that stops referencing the credential mid-rotation is still
// revoked (the old key sits in its authorized_keys and must be removed).
func TestEnqueueRotationSnapshotsItsTargets(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	zoneA, zoneDefault := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	credID := uuid.Must(uuid.NewV7())
	host1, bastion1 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	store := newRotationOnlyStore()
	svc := &Service{
		store: store, queue: noQueue{},
		zones: oneZone{zoneDefault},
		box:   fakeBox{}, now: time.Now, rand: rand.Reader,
	}
	svc.rotationTargets = fakeRotationTargets{
		hosts:    []RotationHost{{ID: host1, ZoneID: zoneA}},
		bastions: []sshconf.Bastion{{ID: bastion1, Name: "jump-01", Address: "10.0.0.9", Port: 22}},
	}
	key := credentials.PendingKey{
		PrivateKey: credentials.Secret{PrivateKey: "new-pem"}, PublicKey: "ssh-ed25519 AAAA new",
		Fingerprint: "SHA256:new", KeyType: "ed25519",
		PreviousPublicKey: "ssh-ed25519 AAAA old",
	}

	group, _, _, err := svc.EnqueueRotation(db.WithOrgScope(context.Background(), org), org, credID, key, nil)
	if err != nil {
		t.Fatalf("EnqueueRotation: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.groupID != group {
		t.Fatalf("snapshot keyed by group %s, want %s", store.groupID, group)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(store.groupTargets, &raw); err != nil {
		t.Fatalf("snapshot is not a JSON object: %v (%s)", err, store.groupTargets)
	}
	if len(raw) != 2 || raw["host_ids"] == nil || raw["bastion_ids"] == nil {
		t.Fatalf("snapshot keys = %v, want exactly host_ids and bastion_ids", raw)
	}
	var snap rotationTargetSnapshot
	if err := json.Unmarshal(store.groupTargets, &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.HostIDs) != 1 || snap.HostIDs[0] != host1 {
		t.Fatalf("snapshot hosts = %v, want [%s]", snap.HostIDs, host1)
	}
	if len(snap.BastionIDs) != 1 || snap.BastionIDs[0] != bastion1 {
		t.Fatalf("snapshot bastions = %v, want [%s]", snap.BastionIDs, bastion1)
	}
}

// TestEnqueueRotationEmptyGroupIsCountZero pins the immediate-rekey path.
func TestEnqueueRotationEmptyGroupIsCountZero(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := newRotationOnlyStore()
	svc := &Service{store: store, queue: noQueue{}, box: fakeBox{}, now: time.Now, rand: rand.Reader}
	svc.rotationTargets = fakeRotationTargets{}

	group, first, count, err := svc.EnqueueRotation(db.WithOrgScope(context.Background(), org), org, uuid.Must(uuid.NewV7()), credentials.PendingKey{}, nil)
	if err != nil {
		t.Fatalf("EnqueueRotation: %v", err)
	}
	if count != 0 || group != uuid.Nil || first != uuid.Nil {
		t.Fatalf("group = %s, first = %s, count = %d, want the zero group", group, first, count)
	}
	if len(store.jobs) != 0 {
		t.Fatalf("no job may exist: %+v", store.jobs)
	}
	if store.groupID != uuid.Nil || store.groupTargets != nil {
		t.Fatalf("a rotation without references must not snapshot targets: %s %s", store.groupID, store.groupTargets)
	}
}

// TestEnqueueRotationUnwiredTargetsFailsClosed pins the wiring posture.
func TestEnqueueRotationUnwiredTargetsFailsClosed(t *testing.T) {
	svc := &Service{store: newRotationOnlyStore(), queue: noQueue{}, box: fakeBox{}, now: time.Now, rand: rand.Reader}
	_, _, _, err := svc.EnqueueRotation(db.WithOrgScope(context.Background(), uuid.Must(uuid.NewV7())), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), credentials.PendingKey{}, nil)
	if err == nil || !strings.Contains(err.Error(), "no rotation target source wired") {
		t.Fatalf("err = %v", err)
	}
}

var _ Store = (*rotationOnlyStore)(nil) // the queue's own Store shape, satisfied

// TestEnqueueRotationErrorPaths covers the enqueue's failure branches: a
// failing target source and an unrecoverable insert both fail the whole
// enqueue (no partial group), and an absent default zone fails the
// bastion half.
func TestEnqueueRotationErrorPaths(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	credID := uuid.Must(uuid.NewV7())
	bastionID := uuid.Must(uuid.NewV7())

	// A failing target source fails closed.
	svc := &Service{store: newRotationOnlyStore(), queue: noQueue{}, box: fakeBox{}, now: time.Now, rand: rand.Reader}
	svc.rotationTargets = errTargets{err: errors.New("locked")}
	_, _, _, err := svc.EnqueueRotation(db.WithOrgScope(context.Background(), org), org, credID, credentials.PendingKey{}, nil)
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("failing targets: %v", err)
	}

	// A bastion target without a resolvable default zone fails.
	broken := &brokenZones{}
	svc = &Service{store: newRotationOnlyStore(), queue: noQueue{}, zones: broken, box: fakeBox{}, now: time.Now, rand: rand.Reader}
	svc.rotationTargets = fakeRotationTargets{
		bastions: []sshconf.Bastion{{ID: bastionID, Name: "jump-01", Address: "10.0.0.9", Port: 22}},
	}
	_, _, _, err = svc.EnqueueRotation(db.WithOrgScope(context.Background(), org), org, credID, credentials.PendingKey{}, nil)
	if err == nil {
		t.Fatal("a missing default zone must fail the bastion half")
	}

	// A store insert failure fails the whole enqueue.
	failing := newRotationOnlyStore()
	failing.nextErr = errors.New("disk full")
	svc = &Service{store: failing, queue: noQueue{}, box: fakeBox{}, now: time.Now, rand: rand.Reader}
	svc.rotationTargets = fakeRotationTargets{
		hosts: []RotationHost{{ID: uuid.Must(uuid.NewV7()), ZoneID: uuid.Must(uuid.NewV7())}},
	}
	_, _, _, err = svc.EnqueueRotation(db.WithOrgScope(context.Background(), org), org, credID, credentials.PendingKey{}, nil)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("insert failure: %v", err)
	}
}

// errTargets is the RotationTargets fake that always fails.
type errTargets struct{ err error }

func (f errTargets) ListCredentialHosts(context.Context, uuid.UUID, uuid.UUID) ([]RotationHost, error) {
	return nil, f.err
}

func (f errTargets) ListCredentialBastions(context.Context, uuid.UUID, uuid.UUID) ([]sshconf.Bastion, error) {
	return nil, f.err
}

func (f errTargets) ListOrgBastions(context.Context, uuid.UUID) ([]sshconf.Bastion, error) {
	return nil, f.err
}

// brokenZones is the DefaultZoneSource that never resolves.
type brokenZones struct{}

func (brokenZones) ListZones(context.Context, uuid.UUID) ([]hosts.Zone, error) {
	return nil, errors.New("no zone")
}

// TestEnqueueRotationBastionListError isolates the bastion half's failure
// branch: hosts resolve, bastions fail.
func TestEnqueueRotationBastionListError(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	svc := &Service{store: newRotationOnlyStore(), queue: noQueue{}, box: fakeBox{}, now: time.Now, rand: rand.Reader}
	svc.rotationTargets = hostOnlyTargets{bastionErr: errors.New("locked")}
	_, _, _, err := svc.EnqueueRotation(db.WithOrgScope(context.Background(), org), org, uuid.Must(uuid.NewV7()), credentials.PendingKey{}, nil)
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("bastion list failure: %v", err)
	}
}

// hostOnlyTargets lists hosts but fails the bastion half.
type hostOnlyTargets struct{ bastionErr error }

func (f hostOnlyTargets) ListCredentialHosts(context.Context, uuid.UUID, uuid.UUID) ([]RotationHost, error) {
	return []RotationHost{}, nil
}

func (f hostOnlyTargets) ListCredentialBastions(context.Context, uuid.UUID, uuid.UUID) ([]sshconf.Bastion, error) {
	return nil, f.bastionErr
}

func (f hostOnlyTargets) ListOrgBastions(context.Context, uuid.UUID) ([]sshconf.Bastion, error) {
	return nil, f.bastionErr
}

// TestEnqueueRotationAtomicGroupInsert pins the finding's fix: the whole
// group rides ONE transactional insert call — a mid-group failure leaves
// nothing behind (the staged rows never commit), no per-row CreateJob
// calls happen, and the error propagates.
func TestEnqueueRotationAtomicGroupInsert(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	credID := uuid.Must(uuid.NewV7())
	host1, host2 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	key := credentials.PendingKey{
		PrivateKey:  credentials.Secret{PrivateKey: "new-pem"},
		PublicKey:   "ssh-ed25519 AAAA new",
		Fingerprint: "SHA256:new",
		KeyType:     "ed25519",
	}

	// A store whose second staged insert fails: the group's rows stage,
	// the failure aborts, nothing commits.
	store := newRotationOnlyStore()
	store.failGroupAt = 2
	store.groupErr = errors.New("disk full")
	svc := &Service{store: store, queue: noQueue{}, box: fakeBox{}, now: time.Now, rand: rand.Reader}
	svc.rotationTargets = fakeRotationTargets{
		hosts: []RotationHost{{ID: host1, ZoneID: uuid.Must(uuid.NewV7())}, {ID: host2, ZoneID: uuid.Must(uuid.NewV7())}},
	}
	_, _, _, err := svc.EnqueueRotation(db.WithOrgScope(context.Background(), org), org, credID, key, nil)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("mid-group failure: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.jobs) != 0 {
		t.Fatalf("a failed group insert must leave nothing behind: %+v", store.jobs)
	}
	if store.groupCalls != 1 {
		t.Fatalf("group insert calls = %d, want exactly one", store.groupCalls)
	}

	// A store without the atomic capability fails the enqueue closed.
	svc = &Service{store: &plainRotationStore{}, queue: noQueue{}, box: fakeBox{}, now: time.Now, rand: rand.Reader}
	svc.rotationTargets = fakeRotationTargets{
		hosts: []RotationHost{{ID: host1, ZoneID: uuid.Must(uuid.NewV7())}},
	}
	_, _, _, err = svc.EnqueueRotation(db.WithOrgScope(context.Background(), org), org, credID, key, nil)
	if err == nil || !strings.Contains(err.Error(), "cannot insert the group atomically") {
		t.Fatalf("store without the group capability: %v", err)
	}
}

// plainRotationStore implements Store WITHOUT the groupInserter
// capability: the rotation enqueue must refuse it rather than fall back to
// per-row inserts.
type plainRotationStore struct{}

func (plainRotationStore) CreateJob(context.Context, Job, json.RawMessage) error { return nil }
func (plainRotationStore) GetJob(context.Context, uuid.UUID, uuid.UUID) (Job, error) {
	return Job{}, ErrNotFound
}
func (plainRotationStore) ListJobs(context.Context, uuid.UUID, Filter, string, int) ([]Job, string, error) {
	return nil, "", nil
}
func (plainRotationStore) CancelJob(context.Context, uuid.UUID, uuid.UUID) (Status, error) {
	return "", ErrNotRunning
}
func (plainRotationStore) ListJobEvents(context.Context, uuid.UUID, uuid.UUID, int64) ([]Event, error) {
	return nil, nil
}
func (plainRotationStore) RecordSyncResult(context.Context, uuid.UUID, uuid.UUID, time.Time, string, string, string) error {
	return nil
}

func (plainRotationStore) HasActiveTemplateJob(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}

func (plainRotationStore) HasActiveRepoSync(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}

func (plainRotationStore) GroupSummaries(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]GroupSummary, error) {
	return nil, nil
}

func (plainRotationStore) ReserveRunGroup(context.Context, uuid.UUID, string, uuid.UUID) (uuid.UUID, bool, error) {
	return uuid.Nil, false, errors.New("ReserveRunGroup not implemented by plainRotationStore")
}
