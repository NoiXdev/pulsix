package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestStartLoopStopWaitsForTheLoop is final review minor 5: Close stops
// the background loops (ticker, reaper, re-test) and then closes the
// pool, so stopping must wait for a pass that is still running — a batch
// mid-enqueue must not find its pool closed underneath it.
func TestStartLoopStopWaitsForTheLoop(t *testing.T) {
	var finished atomic.Bool
	started := make(chan struct{})
	stop := startLoop(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		// The in-flight pass still has work to write back after the
		// cancellation (the ticker's detached write-back).
		time.Sleep(50 * time.Millisecond)
		finished.Store(true)
	})
	<-started
	stop()
	if !finished.Load() {
		t.Fatal("stop returned before the loop did")
	}
	// A second stop is harmless (every early return in Build and Close
	// may reach it).
	stop()
}
