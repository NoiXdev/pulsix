// The SSE subscribe flow against the real wiring (backlog 2.12, Task
// 12's end-to-end rule): the gRPC append path (EventRecorder port →
// SQLQueue.AppendEvent) feeds the service's broker, so a run view
// subscribed through Service.Subscribe sees the worker's events; and
// the org check on Subscribe is Events' own rule — another
// organization's job id is exactly as invisible as one that does not
// exist, and cancelling a subscription leaves nothing behind.
package jobs_test

import (
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
)

// TestSubscribeReceivesAppendedEvents drives the whole in-process
// event flow: subscribe through the org-scoped service, append through
// the queue exactly like the gRPC RunEvents handler does, and receive
// the events on the channel, in order, only from the seq range the
// subscriber asked for.
func TestSubscribeReceivesAppendedEvents(t *testing.T) {
	d := testdb.Open(t)
	org, other := seedOrg(t, gen.New(d.Pool()), "jobs-subs-org"), seedOrg(t, gen.New(d.Pool()), "jobs-subs-other")
	zone := uuid.Must(uuid.NewV7())
	queue := newTestQueue(d)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, emptySources{}, nil, nil, nil, fakeSealer{}, time.Now, rand.Reader)
	queue.SetEventBroker(svc.EventsBroker())

	claim := claimOne(t, d, org, zone, queue)
	jobID := uuid.MustParse(claim.Job.GetId())

	// Subscribe with after=1 (the run view resuming after a poll read).
	sctx := db.WithOrgScope(ctx, org)
	events, cancel, err := svc.Subscribe(sctx, org, jobID, 1)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	if got := svc.EventsBroker().Len(jobID); got != 1 {
		t.Fatalf("subscriber count = %d, want 1", got)
	}

	// The gRPC append path: the worker's stream lands here event by
	// event. The first one is below the subscriber's floor, so only
	// seqs 2 and 3 come through, in order.
	for _, ev := range []jobs.Event{
		{Seq: 1, Host: "web1", State: "ok", At: time.Now()},
		{Seq: 2, Host: "web1", State: "ok", Payload: []byte(`{"line":1}`), At: time.Now()},
		{Seq: 3, Host: "web1", State: "changed", At: time.Now()},
	} {
		if err := queue.AppendEvent(ctx, jobID, ev); err != nil {
			t.Fatalf("AppendEvent %d: %v", ev.Seq, err)
		}
	}
	select {
	case ev := <-events:
		if ev.Seq != 2 {
			t.Fatalf("first delivered seq = %d, want 2", ev.Seq)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("seq 2 never arrived")
	}
	select {
	case ev := <-events:
		if ev.Seq != 3 {
			t.Fatalf("second delivered seq = %d, want 3", ev.Seq)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("seq 3 never arrived")
	}

	// Another organization's subscribe attempt is a 404-invisible
	// ErrNotFound, for the job id and its events alike.
	octx := db.WithOrgScope(ctx, other)
	if _, _, err := svc.Subscribe(octx, other, jobID, 0); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("other org's Subscribe: %v", err)
	}
	if _, err := svc.Events(octx, other, jobID, 0); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("other org's Events: %v", err)
	}
	if _, _, err := svc.Subscribe(sctx, org, uuid.Must(uuid.NewV7()), 0); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("unknown job's Subscribe: %v", err)
	}

	// Cancelling the subscription unsubscribes: the count returns to
	// zero and a later publish reaches nobody.
	cancel()
	if got := svc.EventsBroker().Len(jobID); got != 0 {
		t.Fatalf("subscriber count after cancel = %d, want 0", got)
	}
	select {
	case ev, ok := <-events:
		if ok {
			t.Fatalf("cancelled channel received %v", ev)
		}
	default:
		t.Fatal("cancelled channel must be closed so range loops end")
	}
}
