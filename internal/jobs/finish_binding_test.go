// Result binding (fix round 2): a worker's terminal report acts only on a
// job of its own organization and zone that it claimed itself (jobs.worker_id,
// set at claim). Anything else is ErrNotClaimedByWorker and changes nothing.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// claimRotateJobFor inserts one rotate_key job in zone and claims it as
// worker (ClaimFor), returning the job.
func (w *atomicWorld) claimRotateJobFor(t *testing.T, worker WorkerRef) Job {
	t.Helper()
	j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ZoneID: worker.ZoneID, Kind: KindRotateKey, HostIDs: []uuid.UUID{w.host},
		Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), CreatedAt: time.Now()}
	payload, err := json.Marshal(rotationJobPayload{
		CredentialID: uuid.Must(uuid.NewV7()), Target: rotationTarget{Kind: rotationTargetHost, ID: w.host},
		NewPublicKey: "pub", NewPrivateKey: credentials.Secret{PrivateKey: "pem"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewSQLStore(w.d).CreateJob(db.WithOrgScope(context.Background(), w.org), j, mustSeal(w.q.box, w.org, j.ID, payload)); err != nil {
		t.Fatal(err)
	}
	if c, err := w.q.ClaimFor(context.Background(), worker.ID, worker.OrgID, []uuid.UUID{worker.ZoneID}, "v1"); err != nil || c == nil || c.Job.GetId() != j.ID.String() {
		t.Fatalf("ClaimFor = %+v, %v", c, err)
	}
	return j
}

// rowSnapshot is everything a forged finish could change on the job row.
type rowSnapshot struct {
	Status, Stats, Error string
	HasPayload           bool
}

func (w *atomicWorld) row(t *testing.T, id uuid.UUID) rowSnapshot {
	t.Helper()
	var r rowSnapshot
	if err := w.d.Pool().QueryRow(context.Background(),
		`SELECT status, stats::text, error, payload IS NOT NULL FROM jobs WHERE id = $1`, id,
	).Scan(&r.Status, &r.Stats, &r.Error, &r.HasPayload); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFinishForIsBoundToTheClaimingWorker(t *testing.T) {
	w := newAtomicWorld(t, "jobs-finish-binding")
	orgB := uuid.Must(uuid.NewV7())
	if _, err := gen.New(w.d.Pool()).CreateOrganization(context.Background(), gen.CreateOrganizationParams{ID: orgB, Slug: "jobs-finish-binding-b", Name: "b"}); err != nil {
		t.Fatal(err)
	}
	zone := uuid.Must(uuid.NewV7())
	owner := WorkerRef{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ZoneID: zone}

	tests := []struct {
		name   string
		caller func(job Job) WorkerRef
	}{
		{name: "worker of another organization", caller: func(Job) WorkerRef {
			return WorkerRef{ID: uuid.Must(uuid.NewV7()), OrgID: orgB, ZoneID: uuid.Must(uuid.NewV7())}
		}},
		{name: "another worker of the same organization and zone", caller: func(Job) WorkerRef {
			return WorkerRef{ID: uuid.Must(uuid.NewV7()), OrgID: owner.OrgID, ZoneID: owner.ZoneID}
		}},
		{name: "the claiming worker id under another zone", caller: func(Job) WorkerRef {
			return WorkerRef{ID: owner.ID, OrgID: owner.OrgID, ZoneID: uuid.Must(uuid.NewV7())}
		}},
		{name: "the claiming worker id under another organization", caller: func(Job) WorkerRef {
			return WorkerRef{ID: owner.ID, OrgID: orgB, ZoneID: owner.ZoneID}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := w.claimRotateJobFor(t, owner)
			before := w.row(t, job.ID)
			callsBefore, _ := w.finisher.snapshot()
			err := w.q.FinishFor(context.Background(), tt.caller(job), job.ID, StatusSucceeded, map[string]any{"forged": 1}, "forged")
			if !errors.Is(err, ErrNotClaimedByWorker) {
				t.Fatalf("FinishFor = %v, want ErrNotClaimedByWorker", err)
			}
			if after := w.row(t, job.ID); after != before {
				t.Fatalf("refused finish changed the row: %+v → %+v", before, after)
			}
			if calls, _ := w.finisher.snapshot(); len(calls) != len(callsBefore) {
				t.Fatalf("refused finish finalized a rotation: %+v", calls)
			}
			// The claiming worker still finishes it, and its retry is the
			// idempotent no-op — while the stranger's retry stays refused.
			if err := w.q.FinishFor(context.Background(), owner, job.ID, StatusFailed, nil, "real"); err != nil {
				t.Fatalf("owner's FinishFor: %v", err)
			}
			if got := w.row(t, job.ID); got.Status != string(StatusFailed) || got.HasPayload {
				t.Fatalf("owner's finish = %+v", got)
			}
			if err := w.q.FinishFor(context.Background(), owner, job.ID, StatusFailed, nil, "real"); err != nil {
				t.Fatalf("owner's retry: %v", err)
			}
			if err := w.q.FinishFor(context.Background(), tt.caller(job), job.ID, StatusSucceeded, nil, ""); !errors.Is(err, ErrNotClaimedByWorker) {
				t.Fatalf("stranger's finish of a terminal job = %v, want ErrNotClaimedByWorker", err)
			}
		})
	}

	t.Run("unknown job id", func(t *testing.T) {
		if err := w.q.FinishFor(context.Background(), owner, uuid.Must(uuid.NewV7()), StatusSucceeded, nil, ""); !errors.Is(err, ErrNotClaimedByWorker) {
			t.Fatalf("FinishFor(unknown) = %v, want ErrNotClaimedByWorker", err)
		}
	})
	t.Run("job claimed without a worker binding", func(t *testing.T) {
		job := w.claimedRotateJobs(t, 1)[0]
		before := w.row(t, job.ID)
		self := WorkerRef{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ZoneID: job.ZoneID}
		if err := w.q.FinishFor(context.Background(), self, job.ID, StatusSucceeded, nil, ""); !errors.Is(err, ErrNotClaimedByWorker) {
			t.Fatalf("FinishFor(unbound) = %v, want ErrNotClaimedByWorker", err)
		}
		if after := w.row(t, job.ID); after != before {
			t.Fatalf("refused finish changed the row: %+v → %+v", before, after)
		}
	})
}
