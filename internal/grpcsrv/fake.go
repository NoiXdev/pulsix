// In-memory implementations of the narrow ports, for tests in this package
// and (from Task 5 on) wherever a queue-backed implementation is not yet
// wired. They are mutex-guarded so a test may drive a handler while another
// goroutine flips state.
package grpcsrv

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// FakeClaimer serves claims from an in-memory queue: Claim pops the first
// enqueued claim or returns nil when the queue is empty. Every call is
// recorded so tests can assert the scope and version a worker presented.
type FakeClaimer struct {
	mu     sync.Mutex
	claims []*JobClaim
	calls  []FakeClaimCall
}

// FakeClaimCall is one recorded Claim invocation.
type FakeClaimCall struct {
	Scope           ClaimScope
	Version         string
	CancelStopsPlay bool
}

// Claim implements Claimer.
func (f *FakeClaimer) Claim(_ context.Context, scope ClaimScope, version string, cancelStopsPlay bool) (*JobClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, FakeClaimCall{Scope: scope, Version: version, CancelStopsPlay: cancelStopsPlay})
	if len(f.claims) == 0 {
		return nil, nil
	}
	c := f.claims[0]
	f.claims = f.claims[1:]
	return c, nil
}

// Enqueue queues a claim for the next Claim call.
func (f *FakeClaimer) Enqueue(c *JobClaim) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims = append(f.claims, c)
}

// Calls returns every Claim invocation so far.
func (f *FakeClaimer) Calls() []FakeClaimCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeClaimCall(nil), f.calls...)
}

// FakeEventRecorder keeps events in memory; Cancelled is answered from a
// settable map so a test can flip a job into 'cancelling' mid-stream.
type FakeEventRecorder struct {
	mu         sync.Mutex
	events     []Event
	cancelling map[uuid.UUID]bool
	appendErr  error
}

// Append implements EventRecorder.
func (f *FakeEventRecorder) Append(_ context.Context, ev Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.appendErr != nil {
		return f.appendErr
	}
	f.events = append(f.events, ev)
	return nil
}

// SetAppendError makes every later Append fail with err (tests the
// server's wrapped-Internal mapping).
func (f *FakeEventRecorder) SetAppendError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appendErr = err
}

// Cancelled implements EventRecorder.
func (f *FakeEventRecorder) Cancelled(_ context.Context, jobID uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancelling[jobID], nil
}

// Events returns every appended event in order.
func (f *FakeEventRecorder) Events() []Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Event(nil), f.events...)
}

// SetCancelling flips the cancel flag the Cancelled port reports.
func (f *FakeEventRecorder) SetCancelling(jobID uuid.UUID, cancelling bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancelling == nil {
		f.cancelling = map[uuid.UUID]bool{}
	}
	f.cancelling[jobID] = cancelling
}

// FakeResultRecorder records terminal results in a slice. Once Bind was
// called it enforces the claim binding like the real queue: a result for a
// job not bound to the reporting worker is refused with ErrJobNotClaimed
// and not recorded.
type FakeResultRecorder struct {
	mu       sync.Mutex
	results  []Result
	bindings map[string]WorkerRef
}

// Bind records that worker claimed jobID.
func (f *FakeResultRecorder) Bind(jobID string, worker WorkerRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bindings == nil {
		f.bindings = map[string]WorkerRef{}
	}
	f.bindings[jobID] = worker
}

// Finish implements ResultRecorder.
func (f *FakeResultRecorder) Finish(_ context.Context, res Result) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bindings != nil {
		if bound, ok := f.bindings[res.JobID]; !ok || bound != res.Worker {
			return ErrJobNotClaimed
		}
	}
	f.results = append(f.results, res)
	return nil
}

// Results returns every recorded result in order.
func (f *FakeResultRecorder) Results() []Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Result(nil), f.results...)
}

// FakeSystemAuditor records system audit events in a slice.
type FakeSystemAuditor struct {
	mu     sync.Mutex
	events []SystemEvent
}

// Record implements SystemAuditor.
func (f *FakeSystemAuditor) Record(_ context.Context, ev SystemEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return nil
}

// Events returns every recorded audit event in order.
func (f *FakeSystemAuditor) Events() []SystemEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SystemEvent(nil), f.events...)
}
