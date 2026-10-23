package notify_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/notify"
)

// panickingQueue panics from whichever of the queue's methods the test
// names. Claim and the three outcome writes are the paths the final
// review's Important #6 found unrecovered: Claim runs inside Tick, which
// Dispatcher.Run called directly rather than through a tickSafely like its
// two sibling loops, and the outcome writes run inside the per-delivery
// goroutine, where no parent recover could ever catch them.
type panickingQueue struct {
	mu         sync.Mutex
	pending    []notify.Claimed
	claims     atomic.Int64
	panicClaim bool
	panicWrite bool
	completed  atomic.Int64
}

func (q *panickingQueue) Claim(context.Context, time.Duration, int) ([]notify.Claimed, error) {
	if q.claims.Add(1); q.panicClaim {
		panic("claim exploded")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.pending
	q.pending = nil
	return out, nil
}

func (q *panickingQueue) Complete(context.Context, notify.Claimed, time.Time) error {
	if q.panicWrite {
		panic("complete exploded")
	}
	q.completed.Add(1)
	return nil
}

func (q *panickingQueue) Retry(context.Context, notify.Claimed, time.Duration, string, []string) error {
	return nil
}
func (q *panickingQueue) Fail(context.Context, notify.Claimed, string) error { return nil }

type okSender struct{}

func (okSender) Send(context.Context, notify.Claimed, notify.Event) error { return nil }

// TestDispatcherRunSurvivesAPanickingClaim is the first half of the final
// review's Important #6. DigestTicker.Run and schedules.Ticker.Run both
// wrap their Tick in a tickSafely; Dispatcher.Run called Tick directly, so
// a panic anywhere in the claim took the whole manager process down.
func TestDispatcherRunSurvivesAPanickingClaim(t *testing.T) {
	q := &panickingQueue{panicClaim: true}
	d := notify.NewDispatcher(q, okSender{}, time.Millisecond, time.Now, nil, quiet())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.Run(ctx)
	}()
	// The loop must survive the panic and keep ticking.
	deadline := time.After(5 * time.Second)
	for q.claims.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("the dispatch loop stopped after %d claim(s); a panicking tick must not end it", q.claims.Load())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// TestDispatcherDeliverSurvivesAPanickingOutcomeWrite is the second half:
// the recover in the dispatcher covered only the send, while Complete,
// Retry and Fail run *after* it inside the same spawned goroutine. A panic
// there is unrecoverable from any parent, so it killed the manager — and,
// combined with the queue's missing attempts cap, produced an unbounded
// crash-and-resend loop: crash, restart, re-claim the same row, re-send,
// crash.
func TestDispatcherDeliverSurvivesAPanickingOutcomeWrite(t *testing.T) {
	q := &panickingQueue{panicWrite: true, pending: []notify.Claimed{claimed(0, true, `{"kind":"test"}`)}}
	d := notify.NewDispatcher(q, okSender{}, time.Millisecond, time.Now, nil, quiet())
	n, err := d.Tick(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("Tick = %d, %v", n, err)
	}
	if q.completed.Load() != 0 {
		t.Fatal("the outcome write was supposed to panic")
	}
}

// FailStuck satisfies notify.DeliveryQueue; these tests seed their rows
// directly and never exercise the claim backstop.
func (q *panickingQueue) FailStuck(context.Context) (int64, error) { return 0, nil }
