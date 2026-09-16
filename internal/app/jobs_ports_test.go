// Tests for the gRPC port adapters: they run the real queue against the
// test database, because the adapters are the wiring this package exists
// to verify.
package app

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/grpcsrv"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/sshconf"
)

type stubAuditStore struct{ events []audit.Event }

func (s *stubAuditStore) Insert(_ context.Context, e audit.Event) error {
	s.events = append(s.events, e)
	return nil
}

func (s *stubAuditStore) List(context.Context, uuid.UUID, audit.Filter, string, int) ([]audit.Event, string, error) {
	return nil, "", nil
}

func (s *stubAuditStore) ListForActor(context.Context, uuid.UUID, audit.Filter, string, int) ([]audit.Event, string, error) {
	return nil, "", nil
}

type stubSnapshot struct{}

func (stubSnapshot) Snapshot(context.Context, uuid.UUID, render.Scope) (render.Inventory, error) {
	return render.Inventory{Doc: map[string]any{}, Connections: map[string]render.Connection{}}, nil
}

type stubTemplates struct{}

func (stubTemplates) TemplateLookup(context.Context, uuid.UUID, uuid.UUID) (jobs.TemplateRow, error) {
	return jobs.TemplateRow{}, jobs.ErrNotFound
}

type stubCreds struct{}

func (stubCreds) OpenSecret(context.Context, uuid.UUID, uuid.UUID) (credentials.Secret, error) {
	return credentials.Secret{}, credentials.ErrNotFound
}

func (stubCreds) Get(context.Context, uuid.UUID, uuid.UUID) (credentials.Credential, error) {
	return credentials.Credential{}, credentials.ErrNotFound
}

// stubPins pins nothing and reports no changed host key: a host-bound
// claim needs the key guard wired, or it fails closed.
type stubPins struct{}

func (stubPins) PinnedKeys(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]string, error) {
	return map[uuid.UUID]string{}, nil
}

func (stubPins) ChangedHosts(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]bool, error) {
	return map[uuid.UUID]bool{}, nil
}

type stubBastions struct{}

func (stubBastions) GetBastion(context.Context, uuid.UUID, uuid.UUID) (sshconf.Bastion, error) {
	return sshconf.Bastion{}, sshconf.ErrNotFound
}

func TestGRPCPortAdapters(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: "app-jobs-org", Name: "app jobs"})
	if err != nil {
		t.Fatal(err)
	}
	zone := uuid.Must(uuid.NewV7())
	queue := jobs.NewSQLQueue(d, stubBox{}, stubSnapshot{}, stubCreds{}, stubTemplates{}, stubBastions{}, time.Now, rand.Reader)
	queue.SetKeyPins(stubPins{})
	store := jobs.NewSQLStore(d)
	// The claim path under test is kind-agnostic; adhoc keeps the claim
	// from depending on a rendered probe target the way a (fail-closed)
	// connection_test claim would.
	j := jobs.Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, ZoneID: zone, Kind: jobs.KindAdhoc, HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())},
		Status: jobs.StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), Trigger: jobs.TriggerManual, CreatedAt: time.Now(),
	}
	if err := store.CreateJob(db.WithOrgScope(ctx, org.ID), j, nil); err != nil {
		t.Fatal(err)
	}

	// Claim: the adapter maps the queue's claim onto grpcsrv's and binds
	// the job to the claiming worker.
	worker := grpcsrv.WorkerRef{ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, ZoneID: zone}
	claim, err := jobsClaimer{queue}.Claim(ctx, grpcsrv.ClaimScope{WorkerID: worker.ID, OrgID: org.ID, ZoneIDs: []uuid.UUID{zone}}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("Claim = %v, %v", claim, err)
	}
	if claim.Token == "" || claim.Job.GetId() != j.ID.String() {
		t.Fatalf("claim = %+v", claim)
	}
	empty, err := jobsClaimer{queue}.Claim(ctx, grpcsrv.ClaimScope{WorkerID: worker.ID, OrgID: org.ID, ZoneIDs: []uuid.UUID{zone}}, "v1")
	if err != nil || empty != nil {
		t.Fatalf("drained claim = %v, %v", empty, err)
	}

	// Events: append, then the cancel poll, then the result.
	events := jobsEventRecorder{queue}
	if err := events.Append(ctx, grpcsrv.Event{JobID: j.ID.String(), Seq: 1, State: "ok", At: time.Now()}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := events.Append(ctx, grpcsrv.Event{JobID: "not-a-uuid", Seq: 2}); err == nil {
		t.Fatal("unparseable job id must fail")
	}
	if cancelled, err := events.Cancelled(ctx, j.ID); err != nil || cancelled {
		t.Fatalf("Cancelled = %v, %v", cancelled, err)
	}
	if err := jobs.NewService(store, queue, stubTemplates{}, nil, nil, nil, stubBox{}, time.Now, rand.Reader).
		Cancel(db.WithOrgScope(ctx, org.ID), org.ID, j.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if cancelled, err := events.Cancelled(ctx, j.ID); err != nil || !cancelled {
		t.Fatalf("Cancelled after cancel = %v, %v", cancelled, err)
	}

	results := jobsResultRecorder{queue}
	// Another worker of the same organization and zone is refused with
	// grpcsrv's sentinel.
	stranger := grpcsrv.WorkerRef{ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, ZoneID: zone}
	if err := results.Finish(ctx, grpcsrv.Result{JobID: j.ID.String(), Status: "succeeded", Worker: stranger}); !errors.Is(err, grpcsrv.ErrJobNotClaimed) {
		t.Fatalf("stranger's Finish = %v, want grpcsrv.ErrJobNotClaimed", err)
	}
	if err := results.Finish(ctx, grpcsrv.Result{JobID: j.ID.String(), Status: "cancelled", Stats: map[string]any{"ok": 1}, Worker: worker}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if err := results.Finish(ctx, grpcsrv.Result{JobID: j.ID.String(), Status: "cancelled", Worker: worker}); err != nil {
		t.Fatalf("idempotent Finish: %v", err)
	}
	if err := results.Finish(ctx, grpcsrv.Result{JobID: "not-a-uuid", Status: "failed", Worker: worker}); err == nil {
		t.Fatal("unparseable job id must fail")
	}
	got, err := store.GetJob(db.WithOrgScope(ctx, org.ID), org.ID, j.ID)
	if err != nil || got.Status != jobs.StatusCancelled {
		t.Fatalf("finished job = %+v, %v", got, err)
	}
}

// stubBox is a Sealer stand-in; the adapters' tests never seal.
type stubBox struct{}

func (stubBox) SealAAD(plain, _ []byte) ([]byte, error)  { return plain, nil }
func (stubBox) OpenAAD(sealed, _ []byte) ([]byte, error) { return sealed, nil }

func TestSystemAuditorMapsOrgID(t *testing.T) {
	ctx := context.Background()
	store := &stubAuditStore{}
	a := systemAuditor{audit.NewRecorder(store, time.Now)}
	org := uuid.Must(uuid.NewV7())
	if err := a.Record(ctx, grpcsrv.SystemEvent{Operation: "job.claim", ObjectType: "job", ObjectID: "x", OrgID: org.String(), Summary: "s"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(store.events) != 1 || store.events[0].OrgID == nil || *store.events[0].OrgID != org {
		t.Fatalf("events = %+v", store.events)
	}
	if err := a.Record(ctx, grpcsrv.SystemEvent{Operation: "x", ObjectType: "job", ObjectID: "y", Summary: "s"}); err != nil {
		t.Fatalf("Record without org: %v", err)
	}
	if err := a.Record(ctx, grpcsrv.SystemEvent{Operation: "x", ObjectType: "job", ObjectID: "y", OrgID: "nope", Summary: "s"}); err == nil {
		t.Fatal("unparseable org id must fail")
	}
}
