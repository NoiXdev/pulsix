// Fix round 1, Important 1: clearPayload used to filter on kind IN
// ('connection_test','bootstrap','rotate_key','revoke_key'), so a
// terminal run job's sealed per-run overrides (which can carry
// secret-typed extra vars from a schedule or webhook) and a terminal
// ad-hoc job's sealed command stayed at rest indefinitely. These tests
// pin the fix: both kinds' payloads are wiped on every terminal path,
// exercised here through terminate directly (the core Finish and the
// reaper both share) and through a queued Service.Cancel (the
// payloadCleaner hook).
package jobs

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
)

// insertRunJobWithOverrides inserts one queued run job whose payload is a
// sealed RunOverrides carrying a secret-typed value, the shape a schedule
// or webhook trigger would enqueue.
func (w *atomicWorld) insertRunJobWithOverrides(t *testing.T) Job {
	t.Helper()
	zone := testZone(t, w.d, w.org)
	// jobs.template_id carries no foreign key (like a schedule's or
	// webhook's own reference), so an unregistered id is fine: this job is
	// never claimed.
	templateID := uuid.Must(uuid.NewV7())
	j := Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ZoneID: zone, Kind: KindRun, TemplateID: &templateID,
		HostIDs: []uuid.UUID{w.host}, Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()),
		CreatedAt: time.Now(), Trigger: TriggerSchedule,
	}
	limit := "web*"
	payload, err := json.Marshal(RunOverrides{Limit: &limit, ExtraVars: map[string]any{"token": "wipe-me-9f2"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewSQLStore(w.d).CreateJob(db.WithOrgScope(context.Background(), w.org), j, mustSeal(w.q.box, w.org, j.ID, payload)); err != nil {
		t.Fatal(err)
	}
	return j
}

// insertAdhocJob inserts one queued ad-hoc job with a sealed kind payload.
func (w *atomicWorld) insertAdhocJob(t *testing.T) Job {
	t.Helper()
	zone := testZone(t, w.d, w.org)
	j := Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ZoneID: zone, Kind: KindAdhoc,
		HostIDs: []uuid.UUID{w.host}, Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()),
		CreatedAt: time.Now(), Trigger: TriggerManual,
	}
	payload, err := json.Marshal(adhocKindPayload{Module: AdhocModule, Argv: "uptime"})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewSQLStore(w.d).CreateJob(db.WithOrgScope(context.Background(), w.org), j, mustSeal(w.q.box, w.org, j.ID, payload)); err != nil {
		t.Fatal(err)
	}
	return j
}

// TestRunAndAdhocPayloadWipedOnTerminalTransition drives a run job (with
// sealed overrides) and an ad-hoc job (with a sealed command) through
// terminate directly — RED before the fix: clearPayload's kind filter
// excluded both kinds, so payload stayed non-NULL and this assertion
// failed.
func TestRunAndAdhocPayloadWipedOnTerminalTransition(t *testing.T) {
	w := newAtomicWorld(t, "jobs-run-payload-wipe")

	runJob := w.insertRunJobWithOverrides(t)
	if _, err := w.q.terminate(context.Background(), terminalTransition{
		jobID: runJob.ID, status: StatusSucceeded, from: []Status{StatusQueued},
	}); err != nil {
		t.Fatal(err)
	}
	if status, payload := w.jobState(t, runJob.ID); status != StatusSucceeded || payload {
		t.Fatalf("run job after terminate: status=%s payload=%v, want succeeded with the payload wiped", status, payload)
	}

	adhocJob := w.insertAdhocJob(t)
	if _, err := w.q.terminate(context.Background(), terminalTransition{
		jobID: adhocJob.ID, status: StatusFailed, from: []Status{StatusQueued},
	}); err != nil {
		t.Fatal(err)
	}
	if status, payload := w.jobState(t, adhocJob.ID); status != StatusFailed || payload {
		t.Fatalf("adhoc job after terminate: status=%s payload=%v, want failed with the payload wiped", status, payload)
	}
}

// TestRunPayloadWipedOnQueuedCancel is the other terminal entry point:
// Service.Cancel's payloadCleaner hook (ClearTerminalPayload), reached
// when a queued job is cancelled before it ever ships.
func TestRunPayloadWipedOnQueuedCancel(t *testing.T) {
	w := newAtomicWorld(t, "jobs-run-cancel-wipe")
	svc := NewService(NewSQLStore(w.d), w.q, fakeTemplates{}, nil, nil, nil, fakeBox{}, time.Now, nil)
	scoped := db.WithOrgScope(context.Background(), w.org)

	queuedRun := w.insertRunJobWithOverrides(t)
	if err := svc.Cancel(scoped, w.org, queuedRun.ID); err != nil {
		t.Fatal(err)
	}
	if status, payload := w.jobState(t, queuedRun.ID); status != StatusCancelled || payload {
		t.Fatalf("queued run job after cancel: status=%s payload=%v, want cancelled with the payload wiped", status, payload)
	}
}
