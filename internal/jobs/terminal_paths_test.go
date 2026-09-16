// Every terminal transition runs through one helper (final review I2): the
// payload-build failure at claim time, the reaper's unconfirmed-cancel
// failure and the worker's own finish all wipe the sealed payload and run
// the finish-path sinks — none of them can leave a one-time login or a
// pending rotation key at rest, or a rotation group unfinalized. A cancel
// wipes the payload only when the job actually became terminal.
package jobs

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
)

// insertRotateJob inserts one queued rotate_key job whose sealed payload
// names target as the rotation target (a target that is not the job's
// host makes the claim's payload build fail).
func (w *atomicWorld) insertRotateJob(t *testing.T, target uuid.UUID) Job {
	t.Helper()
	zone := uuid.Must(uuid.NewV7())
	j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ZoneID: zone, Kind: KindRotateKey, HostIDs: []uuid.UUID{w.host},
		Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), CreatedAt: time.Now()}
	payload, err := json.Marshal(rotationJobPayload{
		CredentialID: uuid.Must(uuid.NewV7()), Target: rotationTarget{Kind: rotationTargetHost, ID: target},
		NewPublicKey: "pub", NewFingerprint: "SHA256:pending", NewKeyType: "ed25519",
		NewPrivateKey: credentials.Secret{PrivateKey: "pem"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewSQLStore(w.d).CreateJob(db.WithOrgScope(context.Background(), w.org), j, mustSeal(w.q.box, w.org, j.ID, payload)); err != nil {
		t.Fatal(err)
	}
	return j
}

func TestTerminalPathsWipePayloadAndRunSinks(t *testing.T) {
	tests := []struct {
		name string
		// drive moves the job to its terminal state through one path and
		// returns the status it must end in.
		drive func(t *testing.T, w *atomicWorld) (uuid.UUID, Status)
	}{
		{
			name: "payload build failure at claim",
			drive: func(t *testing.T, w *atomicWorld) (uuid.UUID, Status) {
				j := w.insertRotateJob(t, uuid.Must(uuid.NewV7())) // target mismatch: unbuildable
				c, err := w.q.Claim(context.Background(), w.org, []uuid.UUID{j.ZoneID}, "v1")
				if err != nil || c != nil {
					t.Fatalf("claim of an unbuildable job = %v, %v; want no claim, no error", c, err)
				}
				return j.ID, StatusError
			},
		},
		{
			name: "reaper fails an unconfirmed cancel",
			drive: func(t *testing.T, w *atomicWorld) (uuid.UUID, Status) {
				j := w.claimedRotateJobs(t, 1)[0]
				if _, err := NewSQLStore(w.d).CancelJob(db.WithOrgScope(context.Background(), w.org), w.org, j.ID); err != nil {
					t.Fatal(err)
				}
				w.q.NoteCancelRequested(j.ID, time.Now().Add(-2*cancelConfirmAfter))
				if _, failed, err := NewReaper(w.q, 0, 0).Sweep(context.Background()); err != nil || failed != 1 {
					t.Fatalf("Sweep failed=%d, %v", failed, err)
				}
				return j.ID, StatusFailed
			},
		},
		{
			name: "worker finish",
			drive: func(t *testing.T, w *atomicWorld) (uuid.UUID, Status) {
				j := w.claimedRotateJobs(t, 1)[0]
				if err := w.q.Finish(context.Background(), j.ID, StatusFailed, nil, "boom"); err != nil {
					t.Fatal(err)
				}
				return j.ID, StatusFailed
			},
		},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newAtomicWorld(t, "jobs-terminal-paths-"+string(rune('a'+i)))
			id, want := tt.drive(t, w)
			status, payload := w.jobState(t, id)
			if status != want {
				t.Fatalf("status = %s, want %s", status, want)
			}
			if payload {
				t.Fatal("the terminal job's sealed payload is still at rest")
			}
			calls, _ := w.finisher.snapshot()
			if len(calls) != 1 || calls[0].allSucceeded {
				t.Fatalf("rotation finalize calls = %+v, want exactly one unsuccessful finalize", calls)
			}
			if _, bound := w.q.streams.get(id); bound {
				t.Fatal("the terminal job kept its stream binding")
			}
		})
	}
}

func TestCancelWipesPayloadOnlyWhenTerminal(t *testing.T) {
	w := newAtomicWorld(t, "jobs-cancel-wipe")
	svc := NewService(NewSQLStore(w.d), w.q, fakeTemplates{}, nil, nil, nil, fakeBox{}, time.Now, nil)
	scoped := db.WithOrgScope(context.Background(), w.org)

	// A running job enters cancelling: the worker still runs and needs
	// nothing from the row, but the job is not terminal — a rotation or
	// test that still finishes correctly must find its payload.
	running := w.claimedRotateJobs(t, 1)[0]
	if err := svc.Cancel(scoped, w.org, running.ID); err != nil {
		t.Fatal(err)
	}
	if status, payload := w.jobState(t, running.ID); status != StatusCancelling || !payload {
		t.Fatalf("running job after cancel: status=%s payload=%v, want cancelling with the payload kept", status, payload)
	}

	// A queued job is cancelled outright: terminal, payload wiped.
	queued := w.insertRotateJob(t, w.host)
	if err := svc.Cancel(scoped, w.org, queued.ID); err != nil {
		t.Fatal(err)
	}
	if status, payload := w.jobState(t, queued.ID); status != StatusCancelled || payload {
		t.Fatalf("queued job after cancel: status=%s payload=%v, want cancelled with the payload wiped", status, payload)
	}

	// The exported wipe refuses a non-terminal job no matter who calls it.
	w.q.ClearTerminalPayload(context.Background(), running.ID)
	if _, payload := w.jobState(t, running.ID); !payload {
		t.Fatal("ClearTerminalPayload wiped a non-terminal job's payload")
	}
}
