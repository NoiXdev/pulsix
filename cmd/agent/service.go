package main

import (
	"context"
	"time"
)

// svcState is the lifecycle state serve reports to a service manager.
type svcState int

const (
	svcStartPending svcState = iota
	svcRunning
	svcStopPending
	svcStopped
)

// serve runs body under a service manager: it reports svcRunning once body
// has started, and on a stop request cancels body's ctx, reports
// svcStopPending and returns body's exit code once it returns, or 1 after
// stopWait if it does not. The last report is always svcStopped. report is
// only called from the goroutine that calls serve.
func serve(stop <-chan struct{}, report func(svcState), body func(context.Context) int, stopWait time.Duration) uint32 {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 1)
	go func() { done <- body(ctx) }()
	report(svcRunning)

	var code uint32
	select {
	case c := <-done:
		code = uint32(c)
	case <-stop:
		cancel()
		report(svcStopPending)
		timer := time.NewTimer(stopWait)
		defer timer.Stop()
		select {
		case c := <-done:
			code = uint32(c)
		case <-timer.C:
			code = 1
		}
	}
	report(svcStopped)
	return code
}
