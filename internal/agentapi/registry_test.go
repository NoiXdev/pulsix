// StreamRegistry: tracking, gating and the cut — the unit-level contract
// agents.Service.Revoke relies on through the StreamCutter port.
package agentapi

import (
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

func TestStreamRegistryTracksAndCuts(t *testing.T) {
	r := NewStreamRegistry()
	host := uuid.Must(uuid.NewV7())
	a, releaseA := r.track("serial-1", host)
	_, releaseB := r.track("serial-2", host)
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

// TestStreamRegistryCloseHostWaitsForInFlightWork: CloseHost returns only
// once a processing step that started before the cut has finished — the
// same guarantee the worker stream registry gives the job requeue.
func TestStreamRegistryCloseHostWaitsForInFlightWork(t *testing.T) {
	r := NewStreamRegistry()
	host := uuid.Must(uuid.NewV7())
	ts, release := r.track("serial-1", host)
	defer release()

	var steps atomic.Int64
	proceed := make(chan struct{})
	cutReturned := make(chan struct{})
	go func() {
		_, _ = ts.store(func() error {
			steps.Add(1)
			<-proceed // the cut lands while this step is in flight
			return nil
		})
	}()
	go func() {
		r.CloseHost(host)
		close(cutReturned)
	}()
	for steps.Load() == 0 {
	}
	select {
	case <-cutReturned:
		t.Fatal("CloseHost returned while a step was still in flight")
	default:
	}
	close(proceed)
	<-cutReturned
	select {
	case <-ts.revoked:
	default:
		t.Fatal("the in-flight stream was not signalled")
	}
}
