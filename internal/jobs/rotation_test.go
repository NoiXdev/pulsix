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
	hosts    []RotationHost
	bastions []sshconf.Bastion
}

func (f fakeRotationTargets) ListCredentialHosts(_ context.Context, _, _ uuid.UUID) ([]RotationHost, error) {
	return f.hosts, nil
}

func (f fakeRotationTargets) ListCredentialBastions(_ context.Context, _, _ uuid.UUID) ([]sshconf.Bastion, error) {
	return f.bastions, nil
}

// noQueue is the minimal Queue a rotation-enqueue-only Service needs.
type noQueue struct{}

func (noQueue) Claim(context.Context, []uuid.UUID, string) (*Claim, error) { return nil, nil }
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
// records CreateJob calls.
type rotationOnlyStore struct {
	mu       sync.Mutex
	jobs     []Job
	payloads map[uuid.UUID]json.RawMessage
	nextErr  error
}

func newRotationOnlyStore() *rotationOnlyStore {
	return &rotationOnlyStore{payloads: map[uuid.UUID]json.RawMessage{}}
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

func (s *rotationOnlyStore) RecordSyncResult(context.Context, uuid.UUID, uuid.UUID, time.Time, string) error {
	return nil
}

// TestEnqueueRotationCreatesOneJobPerTarget pins the group shape: one job
// per host (its zone) and per bastion (the default zone), all sharing the
// group, each payload sealed with the job row's own AAD.
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
	}
	actor := uuid.Must(uuid.NewV7())

	group, first, count, err := svc.EnqueueRotation(db.WithOrgScope(context.Background(), org), org, credID, key, &actor)
	if err != nil {
		t.Fatalf("EnqueueRotation: %v", err)
	}
	if count != 3 || first == uuid.Nil || group == uuid.Nil {
		t.Fatalf("group = %s, first = %s, count = %d", group, first, count)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.jobs) != 3 {
		t.Fatalf("stored jobs = %d", len(store.jobs))
	}
	for _, j := range store.jobs {
		if j.RunGroupID != group {
			t.Fatalf("job %s carries group %s, want %s", j.ID, j.RunGroupID, group)
		}
		if j.Kind != KindRotateKey || j.Status != StatusQueued || j.Trigger != TriggerManual {
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
		if rp.CredentialID != credID || rp.NewPrivateKey.PrivateKey != "new-pem" ||
			rp.NewPublicKey != "ssh-ed25519 AAAA new" || rp.NewFingerprint != "SHA256:new" || rp.NewKeyType != "ed25519" {
			t.Fatalf("rotation payload of %s = %+v", j.ID, rp)
		}
		switch rp.Target.Kind {
		case rotationTargetHost:
			if len(j.HostIDs) != 1 || (j.ZoneID != zoneA && j.ZoneID != zoneB) {
				t.Fatalf("host job = %+v", j)
			}
		case rotationTargetBastion:
			if rp.Target.ID != bastion1 || len(j.HostIDs) != 0 || j.ZoneID != zoneDefault {
				t.Fatalf("bastion job = %+v (target %+v)", j, rp.Target)
			}
		default:
			t.Fatalf("unknown target kind %q", rp.Target.Kind)
		}
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
