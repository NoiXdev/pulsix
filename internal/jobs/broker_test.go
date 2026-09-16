// The SSE broker tests (backlog 2.12): the per-job subscriber list the
// event-append path feeds — publish order, the after filter, a cancelled
// subscription unsubscribes (no leak), and a full buffer drops instead of
// stalling the worker's stream.
package jobs_test

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

var jobNil = uuid.Must(uuid.NewV7())

func TestBrokerFansEventsOutInOrder(t *testing.T) {
	b := jobs.NewBroker()
	ch, cancel := b.Subscribe(jobNil, 0)
	defer cancel()

	b.Publish(jobNil, jobs.Event{Seq: 1})
	b.Publish(jobNil, jobs.Event{Seq: 2})
	b.Publish(jobNil, jobs.Event{Seq: 3})

	for want := 1; want <= 3; want++ {
		select {
		case ev := <-ch:
			if ev.Seq != uint64(want) {
				t.Fatalf("got seq %d, want %d", ev.Seq, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("event %d never arrived", want)
		}
	}
}

func TestBrokerSubscribeAfterSkipsOlderSeqs(t *testing.T) {
	b := jobs.NewBroker()
	ch, cancel := b.Subscribe(jobNil, 2)
	defer cancel()

	b.Publish(jobNil, jobs.Event{Seq: 1})
	b.Publish(jobNil, jobs.Event{Seq: 2})
	b.Publish(jobNil, jobs.Event{Seq: 3})

	select {
	case ev := <-ch:
		if ev.Seq != 3 {
			t.Fatalf("got seq %d, want only 3 (after=2)", ev.Seq)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("seq 3 never arrived")
	}
	select {
	case ev := <-ch:
		t.Fatalf("unexpected extra event %d", ev.Seq)
	default:
	}
}

func TestBrokerCancelUnsubscribesAndCloses(t *testing.T) {
	b := jobs.NewBroker()
	ch, cancel := b.Subscribe(jobNil, 0)
	if got := b.Len(jobNil); got != 1 {
		t.Fatalf("Len = %d, want 1", got)
	}
	cancel()
	cancel() // idempotent
	if got := b.Len(jobNil); got != 0 {
		t.Fatalf("Len after cancel = %d, want 0 (no leak)", got)
	}
	// Publishing after the cancel must not panic and must not resurrect
	// the subscription.
	b.Publish(jobNil, jobs.Event{Seq: 1})
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("cancelled channel received an event")
		}
	default:
		t.Fatal("cancelled channel must be closed so range loops end")
	}
	// A second cancel after another subscription took the slot must not
	// close the new subscriber's channel.
	ch2, cancel2 := b.Subscribe(jobNil, 0)
	defer cancel2()
	cancel()
	select {
	case <-ch2:
		t.Fatal("second cancel must not close another subscriber's channel")
	default:
	}
}

func TestBrokerPublishWithoutSubscribersAndNilBroker(t *testing.T) {
	// Publishing to a job nobody subscribes to is a no-op.
	b := jobs.NewBroker()
	b.Publish(jobNil, jobs.Event{Seq: 1})
	if got := b.Len(jobNil); got != 0 {
		t.Fatalf("Len = %d, want 0", got)
	}
	// A nil broker must be safe to publish to (the queue publishes before
	// SetEventBroker ever runs in some wiring orders).
	var nilB *jobs.Broker
	nilB.Publish(jobNil, jobs.Event{Seq: 1})
}

func TestBrokerDropsOnFullBufferInsteadOfBlocking(t *testing.T) {
	b := jobs.NewBroker()
	ch, cancel := b.Subscribe(jobNil, 0)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	done := make(chan struct{})
	go func() {
		defer wg.Done()
		// More events than the buffer holds, from a goroutine that must
		// not block: the publisher (the worker's event stream) can never
		// be stalled by a slow SSE consumer.
		for i := 1; i <= 1024; i++ {
			b.Publish(jobNil, jobs.Event{Seq: uint64(i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish blocked on a full subscriber buffer")
	}
	wg.Wait()

	// Whatever was buffered arrived in order.
	last := 0
	for i := 0; i < 1024; i++ {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("channel closed")
			}
			if int(ev.Seq) <= last {
				t.Fatalf("out of order: %d after %d", ev.Seq, last)
			}
			last = int(ev.Seq)
		default:
			i = 1024
		}
	}
	if last == 0 {
		t.Fatal("nothing was buffered at all")
	}
}
