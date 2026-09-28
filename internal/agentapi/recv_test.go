package agentapi

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// TestRecvWithinFailsInsteadOfBlocking: the whole reason the helper exists
// (backlog 3.20). A bare stream.Recv() in a test whose context is
// t.Context() has no deadline at all — that context is cancelled when the
// test ends, never on a timer — so a regression in pushResolved that
// simply never sends does not fail its own test: it blocks until the
// package's timeout and takes every other test down with a goroutine dump
// instead of naming the one that broke.
func TestRecvWithinFailsInsteadOfBlocking(t *testing.T) {
	var fake recordingTB
	done := make(chan struct{})
	go func() {
		defer close(done)
		// The real FailNow ends the calling goroutine; the fake only
		// records, so recvWithin returns normally here.
		recvWithin(&fake, blockingReceiver{}, 20*time.Millisecond) //nolint:errcheck // the failure is the subject
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("recvWithin blocked on a receiver that never answers")
	}
	if !fake.failed() {
		t.Fatal("recvWithin returned without failing the test")
	}
	if msg := fake.message(); !strings.Contains(msg, "20ms") {
		t.Fatalf("failure message = %q, want it to name how long it waited", msg)
	}
}

// TestRecvWithinReturnsTheStreamsOwnError: several tests pin a refusal —
// they read a status code off recvWithin's second return — so a receiver
// that answers with an error must reach the caller, not be turned into a
// test failure by the helper.
func TestRecvWithinReturnsTheStreamsOwnError(t *testing.T) {
	var fake recordingTB
	want := errors.New("stream refused")

	_, err := recvWithin(&fake, errorReceiver{err: want}, time.Second)

	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if fake.failed() {
		t.Fatalf("an answered Recv must not fail the test: %q", fake.message())
	}
}

// blockingReceiver never answers, which is what a manager that stopped
// sending looks like from the agent's end.
type blockingReceiver struct{}

func (blockingReceiver) Recv() (*agentpb.ManagerMessage, error) { select {} }

type errorReceiver struct{ err error }

func (r errorReceiver) Recv() (*agentpb.ManagerMessage, error) { return nil, r.err }

// recordingTB stands in for *testing.T so the helper's own failure path is
// observable. Only the two methods recvWithin uses are real.
type recordingTB struct {
	testing.TB
	mu   sync.Mutex
	msg  string
	fail bool
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msg = fmt.Sprintf(format, args...)
	r.fail = true
}

func (r *recordingTB) failed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fail
}

func (r *recordingTB) message() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.msg
}

// recvTimeout is what every test in this package waits for one message
// from the manager. Generous against a loaded CI machine — the resolves
// and pushes behind a message are milliseconds of work — and still short
// enough that a stream which never sends fails its own test in seconds.
const recvTimeout = 5 * time.Second

// receiver is the one method recvWithin needs. Naming it rather than the
// generated grpc.BidiStreamingClient keeps the helper's own tests free of
// a stub that would have to implement the whole grpc.ClientStream surface
// to say "this one never answers".
type receiver interface {
	Recv() (*agentpb.ManagerMessage, error)
}

// recvWithin reads one message, failing the test if none arrives within d
// (backlog 3.20).
//
// The plain stream.Recv() it replaces had no deadline: these streams run
// on t.Context(), which is cancelled when the test ends and never on a
// timer, so a manager that stopped sending did not fail the test that
// asserted on its message — it hung until `go test`'s package timeout and
// took every other test in the package down with a goroutine dump, naming
// none of them.
//
// A receiver's own error is returned, not reported: refusing a stream is
// the behaviour several tests here pin, and they read the status code off
// the second return.
//
// On the failure path the reading goroutine is left blocked in Recv. It
// unblocks when the stream's context is cancelled at the end of the test,
// which is the same moment the process stops caring.
func recvWithin(t testing.TB, s receiver, d time.Duration) (*agentpb.ManagerMessage, error) {
	t.Helper()
	type answer struct {
		msg *agentpb.ManagerMessage
		err error
	}
	ch := make(chan answer, 1)
	go func() {
		msg, err := s.Recv()
		ch <- answer{msg, err}
	}()
	select {
	case a := <-ch:
		return a.msg, a.err
	case <-time.After(d):
		t.Fatalf("no message within %s", d)
		return nil, nil
	}
}
