// StreamRegistry: tracking, gating and the cut — the unit-level contract
// agents.Service.Revoke relies on through the StreamCutter port.
package agentapi

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"

	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

func TestStreamRegistryTracksAndCuts(t *testing.T) {
	r := NewStreamRegistry()
	host := uuid.Must(uuid.NewV7())
	a, releaseA := r.track("serial-1", uuid.Nil, host, nil) // nil: this test never calls send
	_, releaseB := r.track("serial-2", uuid.Nil, host, nil)
	releaseB()

	var steps atomic.Int64
	done, err := a.store(func() error { steps.Add(1); return nil })
	if err != nil || !done || steps.Load() != 1 {
		t.Fatalf("store on an open stream = %v, %v, %d steps", done, err, steps.Load())
	}
	if n := r.CloseHost(host); n != 1 {
		t.Fatalf("CloseHost = %d, want the one open stream", n)
	}
	select {
	case <-a.revoked:
	default:
		t.Fatal("the open stream was not signalled")
	}
	// A cut stream drops the message and reports done=false.
	done, err = a.store(func() error { steps.Add(1); return nil })
	if err != nil || done || steps.Load() != 1 {
		t.Fatalf("store on a cut stream = %v, %v, %d steps, want it dropped", done, err, steps.Load())
	}
	releaseA() // after close: a no-op
	if n := r.CloseHost(host); n != 0 {
		t.Fatalf("second CloseHost = %d", n)
	}
}

// TestStreamRegistryCloseHostExcept: the re-enrollment cut ends the
// replaced serial's streams and keeps the fresh serial's — the same
// in-flight guarantee as CloseHost applies to the cut ones.
func TestStreamRegistryCloseHostExcept(t *testing.T) {
	r := NewStreamRegistry()
	host := uuid.Must(uuid.NewV7())
	stale, releaseStale := r.track("serial-old", uuid.Nil, host, nil) // nil: this test never calls send
	fresh, releaseFresh := r.track("serial-new", uuid.Nil, host, nil)
	defer releaseFresh()

	if n := r.CloseHostExcept(host, "serial-new"); n != 1 {
		t.Fatalf("CloseHostExcept = %d, want the one stale stream", n)
	}
	select {
	case <-stale.revoked:
	default:
		t.Fatal("the stale stream was not signalled")
	}
	var steps int
	if done, err := fresh.store(func() error { steps++; return nil }); err != nil || !done || steps != 1 {
		t.Fatalf("the fresh stream must keep processing: done=%v err=%v steps=%d", done, err, steps)
	}
	releaseStale() // after the cut: a no-op
	if n := r.CloseHostExcept(host, "serial-new"); n != 0 {
		t.Fatalf("second CloseHostExcept = %d", n)
	}
}

// TestStreamRegistryCloseHostWaitsForInFlightWork: CloseHost returns only
// once a processing step that started before the cut has finished — the
// same guarantee the worker stream registry gives the job requeue. The
// step is parked mid-flight BEFORE the cut goroutine is spawned, so "the
// cut lands while a step is in flight" is a construction, not a race:
// spawned the other way around, a slow schedule can run the cut first
// (the store sees the cut and never starts the step) and the test then
// waits for a step that never began — the -cover slowdown makes that
// window wide enough to hit.
func TestStreamRegistryCloseHostWaitsForInFlightWork(t *testing.T) {
	r := NewStreamRegistry()
	host := uuid.Must(uuid.NewV7())
	ts, release := r.track("serial-1", uuid.Nil, host, nil) // nil: this test never calls send
	defer release()

	started := make(chan struct{})
	finished := make(chan struct{})
	proceed := make(chan struct{})
	cutReturned := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = ts.store(func() error {
			close(started)
			<-proceed // the cut lands while this step is in flight
			return nil
		})
	}()
	<-started // the step is parked mid-flight, holding the stream's lock
	go func() {
		r.CloseHost(host)
		close(cutReturned)
	}()
	// The cut is now in flight BEHIND the step's lock: CloseHost can only
	// return once the step finishes, so cutReturned cannot be closed yet.
	// (If it ever is, the guarantee this test exists for is broken.)
	select {
	case <-cutReturned:
		t.Fatal("CloseHost returned while a step was still in flight")
	default:
	}
	close(proceed)
	<-finished    // the step finished...
	<-cutReturned // ...and only then did CloseHost return
	select {
	case <-ts.revoked:
	default:
		t.Fatal("the in-flight stream was not signalled")
	}
}

// blockingStream is a fake AgentStream server stream whose Send blocks
// until unblock is closed — a direct simulation of a black-holed agent
// whose flow-control window never opens. Unlike the real gRPC transport
// (which eventually errors a stuck Send once the RPC's connection dies),
// nothing here unblocks Send on its own; that is deliberate; the tests
// using it exist to prove release()/shut() return promptly regardless,
// never that the stuck Send itself resolves quickly.
type blockingStream struct {
	grpc.ServerStream
	ctx     context.Context
	unblock chan struct{}
}

func (s *blockingStream) Context() context.Context { return s.ctx }
func (s *blockingStream) Send(*agentpb.ManagerMessage) error {
	<-s.unblock
	return nil
}

// Recv is never exercised by these tests (pushWriter never calls it; only
// receiveMessages would) — it blocks on the stream's own context so a
// stray call cannot return a bogus zero value.
func (s *blockingStream) Recv() (*agentpb.AgentMessage, error) {
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}

// TestReleaseDoesNotBlockOnAWedgedSend is the deadlock this fix round
// exists for: before it, track's release closure called closeSend, which
// took the same sendMu a wedged Send holds for as long as the agent
// never drains — meaning the handler's own `defer release()` could never
// return, which meant AgentStream could never return to gRPC, which meant
// the transport that would otherwise have errored the wedged Send out
// never got torn down either. A circular deadlock, not merely a slow
// path. This test queues a push, lets the writer goroutine actually enter
// the wedged Send, then proves release() still returns well within a
// generous deadline.
func TestReleaseDoesNotBlockOnAWedgedSend(t *testing.T) {
	r := NewStreamRegistry()
	host := uuid.Must(uuid.NewV7())
	blocked := &blockingStream{ctx: context.Background(), unblock: make(chan struct{})}
	_, release := r.track("serial-1", uuid.Nil, host, blocked)

	if reached := r.PushToHost(uuid.Nil, host, &agentpb.ManagerMessage{CheckConfig: &agentpb.CheckConfig{Version: "wedge-me"}}); reached != 1 {
		t.Fatalf("PushToHost reached %d, want 1", reached)
	}
	// Give the writer goroutine a real chance to have entered the wedged
	// Send; the assertion below is the deadline, not this pause.
	time.Sleep(50 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		release()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("release() hung on a wedged send — the teardown/send deadlock is back")
	}
	close(blocked.unblock) // let the stuck goroutine finish so it doesn't linger past the test
}

// TestCloseHostDoesNotBlockOnAWedgedSend is the same deadlock from the
// revocation side: CloseHost is what agents.Service.Revoke calls,
// synchronously, from the HTTP revoke handler — before this fix, revoking
// a wedged agent would have hung that unrelated request indefinitely.
func TestCloseHostDoesNotBlockOnAWedgedSend(t *testing.T) {
	r := NewStreamRegistry()
	host := uuid.Must(uuid.NewV7())
	blocked := &blockingStream{ctx: context.Background(), unblock: make(chan struct{})}
	_, release := r.track("serial-1", uuid.Nil, host, blocked)
	defer release()

	if reached := r.PushToHost(uuid.Nil, host, &agentpb.ManagerMessage{CheckConfig: &agentpb.CheckConfig{Version: "wedge-me"}}); reached != 1 {
		t.Fatalf("PushToHost reached %d, want 1", reached)
	}
	time.Sleep(50 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		r.CloseHost(host)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("CloseHost hung on a wedged send — the teardown/send deadlock is back")
	}
	close(blocked.unblock)
}

// stepStream is a fake AgentStream server stream whose Send reports every
// call on entered (before it does anything else) and then blocks until a
// value is sent on gate — letting a test control exactly when one Send
// call is allowed to complete, and observe exactly when the next one
// begins, without any sleep-based guessing.
type stepStream struct {
	grpc.ServerStream
	ctx     context.Context
	entered chan *agentpb.ManagerMessage
	gate    chan struct{}
}

func (s *stepStream) Context() context.Context { return s.ctx }
func (s *stepStream) Send(msg *agentpb.ManagerMessage) error {
	s.entered <- msg
	<-s.gate
	return nil
}

// Recv is never exercised by these tests — see blockingStream's own
// comment.
func (s *stepStream) Recv() (*agentpb.AgentMessage, error) {
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}

// TestPushToHostCoalescesToTheNewestConfiguration: two pushes queued while
// an earlier one is still being sent must not both go out — only the
// newest configuration is ever worth delivering, so the mailbox replaces
// rather than queues. This is also what makes push delivery FIFO by
// construction rather than by luck: with a single slot there is nothing
// left to reorder.
func TestPushToHostCoalescesToTheNewestConfiguration(t *testing.T) {
	r := NewStreamRegistry()
	host := uuid.Must(uuid.NewV7())
	stream := &stepStream{ctx: context.Background(), entered: make(chan *agentpb.ManagerMessage, 4), gate: make(chan struct{})}
	_, release := r.track("serial-1", uuid.Nil, host, stream)
	defer release()

	push := func(version string) {
		t.Helper()
		if reached := r.PushToHost(uuid.Nil, host, &agentpb.ManagerMessage{CheckConfig: &agentpb.CheckConfig{Version: version}}); reached != 1 {
			t.Fatalf("PushToHost(%s) reached %d, want 1", version, reached)
		}
	}

	push("v1")
	first := <-stream.entered // deterministic: waits until the writer has actually entered Send for v1
	if first.GetCheckConfig().GetVersion() != "v1" {
		t.Fatalf("first send = %q, want v1", first.GetCheckConfig().GetVersion())
	}

	// v2 and v3 both arrive while v1's Send is still blocked on gate; v3
	// must coalesce over v2 in the mailbox — v2 is never worth sending
	// once v3 exists.
	push("v2")
	push("v3")

	stream.gate <- struct{}{} // let v1's send complete
	second := <-stream.entered
	if second.GetCheckConfig().GetVersion() != "v3" {
		t.Fatalf("second send = %q, want v3 (v2 must have been coalesced away)", second.GetCheckConfig().GetVersion())
	}
	stream.gate <- struct{}{} // let v3's send complete

	// Nothing else is ever sent — in particular, not v2.
	select {
	case extra := <-stream.entered:
		t.Fatalf("an unexpected third send happened: %+v", extra.GetCheckConfig())
	case <-time.After(150 * time.Millisecond):
	}
}

// TestPushWriterGoroutineExitsOnRelease and
// TestPushWriterGoroutineExitsOnShut pin "the writer goroutine goes away
// when the stream does" directly, via writerDone — pushWriter's own
// witness that it actually returned, rather than inferring it indirectly.
func TestPushWriterGoroutineExitsOnRelease(t *testing.T) {
	r := NewStreamRegistry()
	host := uuid.Must(uuid.NewV7())
	stream := &stepStream{ctx: context.Background(), entered: make(chan *agentpb.ManagerMessage, 1), gate: make(chan struct{}, 1)}
	ts, release := r.track("serial-1", uuid.Nil, host, stream)

	release()
	select {
	case <-ts.writerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the push writer goroutine never exited after release")
	}
}

func TestPushWriterGoroutineExitsOnShut(t *testing.T) {
	r := NewStreamRegistry()
	host := uuid.Must(uuid.NewV7())
	stream := &stepStream{ctx: context.Background(), entered: make(chan *agentpb.ManagerMessage, 1), gate: make(chan struct{}, 1)}
	ts, release := r.track("serial-1", uuid.Nil, host, stream)
	defer release()

	if n := r.CloseHost(host); n != 1 {
		t.Fatalf("CloseHost = %d, want 1", n)
	}
	select {
	case <-ts.writerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the push writer goroutine never exited after shut")
	}
}
