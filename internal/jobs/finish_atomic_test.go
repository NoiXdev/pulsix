// SQLQueue.Finish atomicity (fix round 1): the terminal status, every
// finish-path sink (sync state, connection-test row, rotation finalize)
// and the payload wipe commit in one transaction — a failing sink or a
// cancelled context leaves the job exactly as it was, and a retry
// completes everything.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/inventory/render"
)

// writingFinisher is a RotationFinisher that performs a real database
// write through the ctx it is handed (a zone row, standing in for the
// credential update the real finisher makes) and then runs its hook — the
// failure injection point.
type writingFinisher struct {
	d  *db.DB
	mu sync.Mutex
	// hook runs after the write; its error is the finisher's error.
	hook  func(ctx context.Context) error
	calls []finishedRotationCall
	zones []uuid.UUID
}

func (f *writingFinisher) FinishRotation(ctx context.Context, orgID, credentialID, groupID uuid.UUID, allSucceeded bool, _ credentials.Secret, _, _, _ string) error {
	zone := uuid.Must(uuid.NewV7())
	if _, err := f.d.Exec(ctx, `INSERT INTO zones (id, org_id, slug, name) VALUES ($1, $2, $3, 'finish')`,
		zone, orgID, "finish-"+zone.String()[24:]); err != nil {
		return err
	}
	f.mu.Lock()
	hook := f.hook
	f.mu.Unlock()
	if hook != nil {
		if err := hook(ctx); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.zones = append(f.zones, zone)
	f.calls = append(f.calls, finishedRotationCall{orgID: orgID, credentialID: credentialID, groupID: groupID, allSucceeded: allSucceeded})
	return nil
}

func (f *writingFinisher) setHook(h func(ctx context.Context) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hook = h
}

func (f *writingFinisher) snapshot() ([]finishedRotationCall, []uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]finishedRotationCall(nil), f.calls...), append([]uuid.UUID(nil), f.zones...)
}

// atomicWorld is one org with a queue whose rotation finisher writes.
type atomicWorld struct {
	d        *db.DB
	org      uuid.UUID
	host     uuid.UUID // the one rendered host every rotate_key job targets
	q        *SQLQueue
	finisher *writingFinisher
}

func newAtomicWorld(t *testing.T, slug string) *atomicWorld {
	t.Helper()
	d := testdb.Open(t)
	org := uuid.Must(uuid.NewV7())
	if _, err := gen.New(d.Pool()).CreateOrganization(context.Background(), gen.CreateOrganizationParams{ID: org, Slug: slug, Name: slug}); err != nil {
		t.Fatal(err)
	}
	host := uuid.Must(uuid.NewV7())
	rendered := fakeRenderer{inv: render.Inventory{
		Doc:         map[string]any{"all": map[string]any{}},
		Connections: map[string]render.Connection{"web1": {HostID: host}},
	}}
	q := NewSQLQueue(d, fakeBox{}, rendered, fullProbeCreds{}, fakeTemplates{}, fakeBastions{}, time.Now, rand.Reader)
	q.SetKeyPins(fakePins{})
	f := &writingFinisher{d: d}
	q.SetRotationFinisher(f)
	return &atomicWorld{d: d, org: org, host: host, q: q, finisher: f}
}

// claimedRotateJobs inserts n rotate_key jobs of one rotation group (all
// targeting the world's rendered host) and claims them all.
func (w *atomicWorld) claimedRotateJobs(t *testing.T, n int) []Job {
	t.Helper()
	zone, group, cred := testZone(t, w.d, w.org), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	scoped := db.WithOrgScope(context.Background(), w.org)
	var out []Job
	for i := 0; i < n; i++ {
		j := Job{ID: uuid.Must(uuid.NewV7()), Trigger: TriggerManual, OrgID: w.org, ZoneID: zone, Kind: KindRotateKey, HostIDs: []uuid.UUID{w.host},
			Status: StatusQueued, RunGroupID: group, CreatedAt: time.Now().Add(time.Duration(i) * time.Microsecond)}
		payload, err := json.Marshal(rotationJobPayload{
			CredentialID: cred, Target: rotationTarget{Kind: rotationTargetHost, ID: w.host},
			NewPublicKey: "pub", NewFingerprint: "SHA256:pending", NewKeyType: "ed25519",
			NewPrivateKey: credentials.Secret{PrivateKey: "pem"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := NewSQLStore(w.d).CreateJob(scoped, j, mustSeal(w.q.box, w.org, j.ID, payload)); err != nil {
			t.Fatal(err)
		}
		out = append(out, j)
	}
	for range out {
		if c, err := w.q.Claim(context.Background(), w.org, []uuid.UUID{zone}, "v1"); err != nil || c == nil {
			t.Fatalf("claim = %v, %v", c, err)
		}
	}
	return out
}

// jobState reads a job's status and whether its payload is still at rest,
// through the raw pool — what any other session sees committed.
func (w *atomicWorld) jobState(t *testing.T, id uuid.UUID) (Status, bool) {
	t.Helper()
	var status string
	var hasPayload bool
	if err := w.d.Pool().QueryRow(context.Background(),
		`SELECT status, payload IS NOT NULL FROM jobs WHERE id = $1`, id).Scan(&status, &hasPayload); err != nil {
		t.Fatal(err)
	}
	return Status(status), hasPayload
}

func (w *atomicWorld) zoneCount(t *testing.T, ids []uuid.UUID) int {
	t.Helper()
	var n int
	if err := w.d.Pool().QueryRow(context.Background(), `SELECT count(*) FROM zones WHERE id = ANY($1)`, ids).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestFinishIsAtomic: a sink that fails — by returning an error, or
// because the request context is cancelled mid-finish — commits nothing:
// the job stays running with its sealed payload at rest, the sink's own
// write is gone, the stream binding survives; the retry then commits the
// terminal status, the finalize and the payload wipe together.
func TestFinishIsAtomic(t *testing.T) {
	tests := []struct {
		name string
		// fail installs the failure for the first Finish and returns the
		// context that Finish runs with.
		fail func(w *atomicWorld) context.Context
	}{
		{
			name: "sink returns an error",
			fail: func(w *atomicWorld) context.Context {
				w.finisher.setHook(func(context.Context) error { return errors.New("disk full") })
				return context.Background()
			},
		},
		{
			name: "request context cancelled inside the finish",
			fail: func(w *atomicWorld) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				w.finisher.setHook(func(context.Context) error {
					cancel() // the worker's RPC went away mid-finish
					return nil
				})
				return ctx
			},
		},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newAtomicWorld(t, "jobs-finish-atomic-"+string(rune('a'+i)))
			job := w.claimedRotateJobs(t, 1)[0]

			ctx := tt.fail(w)
			if err := w.q.Finish(ctx, job.ID, StatusSucceeded, map[string]any{"ok": 1}, ""); err == nil {
				t.Fatal("Finish reported success although a sink failed")
			}
			if status, payload := w.jobState(t, job.ID); status != StatusRunning || !payload {
				t.Fatalf("after the failed finish: status=%s payload-at-rest=%v, want running with the payload kept", status, payload)
			}
			if _, bound := w.q.streams.latest(job.ID); !bound {
				t.Fatal("the failed finish dropped the job's stream binding")
			}
			var leaked int
			if err := w.d.Pool().QueryRow(context.Background(),
				`SELECT count(*) FROM zones WHERE org_id = $1 AND slug LIKE 'finish-%'`, w.org).Scan(&leaked); err != nil {
				t.Fatal(err)
			}
			if leaked != 0 {
				t.Fatalf("the failed finish committed %d sink write(s)", leaked)
			}

			// The retry completes everything, together.
			w.finisher.setHook(nil)
			if err := w.q.Finish(context.Background(), job.ID, StatusSucceeded, map[string]any{"ok": 1}, ""); err != nil {
				t.Fatalf("retried Finish: %v", err)
			}
			if status, payload := w.jobState(t, job.ID); status != StatusSucceeded || payload {
				t.Fatalf("after the retry: status=%s payload-at-rest=%v, want succeeded with the payload wiped", status, payload)
			}
			// Only the retry's finalize write is committed (the failed
			// attempt's write — whether or not its hook returned — rolled
			// back with everything else).
			calls, zones := w.finisher.snapshot()
			if len(calls) == 0 || !calls[len(calls)-1].allSucceeded || w.zoneCount(t, zones) != 1 {
				t.Fatalf("after the retry: finalize calls=%+v committed sink writes=%d, want exactly one", calls, w.zoneCount(t, zones))
			}
			if _, bound := w.q.streams.latest(job.ID); bound {
				t.Fatal("the completed finish kept the job's stream binding")
			}
			// A further retry is the idempotent no-op.
			if err := w.q.Finish(context.Background(), job.ID, StatusSucceeded, nil, ""); err != nil {
				t.Fatalf("Finish on a terminal job: %v", err)
			}
			if again, _ := w.finisher.snapshot(); len(again) != len(calls) {
				t.Fatalf("a repeated finish finalized again: %+v", again)
			}
		})
	}
}

// TestFinishConcurrentGroupMembersFinalizeOnce: with the status and the
// group finalize in one transaction, two members of one rotation group
// finishing at the same moment must still finalize the group — exactly
// once — rather than each counting the other as still running.
func TestFinishConcurrentGroupMembersFinalizeOnce(t *testing.T) {
	w := newAtomicWorld(t, "jobs-finish-concurrent")
	const rounds = 8
	for r := 0; r < rounds; r++ {
		group := w.claimedRotateJobs(t, 2)
		var wg sync.WaitGroup
		errs := make([]error, len(group))
		start := make(chan struct{})
		for i, j := range group {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = w.q.Finish(context.Background(), j.ID, StatusSucceeded, nil, "")
			}()
		}
		close(start)
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatalf("round %d: Finish: %v", r, err)
			}
		}
		calls, _ := w.finisher.snapshot()
		if len(calls) != r+1 {
			t.Fatalf("round %d: the group finalized %d time(s) in total, want %d (exactly once per group)", r, len(calls), r+1)
		}
		if !calls[r].allSucceeded || calls[r].groupID != group[0].RunGroupID {
			t.Fatalf("round %d: finalize = %+v", r, calls[r])
		}
	}
}
