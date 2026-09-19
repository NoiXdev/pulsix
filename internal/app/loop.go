package app

import (
	"context"
	"sync"
)

// startLoop runs a background loop (the reaper, the connection re-test,
// the schedule ticker) until the returned stop is called. stop cancels
// the loop's context and waits until run has returned, so Close never
// closes the database pool under a pass that is still writing (final
// review minor 5). stop may be called more than once.
func startLoop(run func(context.Context)) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(ctx)
	}()
	var once sync.Once
	return func() {
		once.Do(cancel)
		<-done
	}
}
