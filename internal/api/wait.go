package api

import (
	"context"
	"time"
)

// The long poll of GET …/jobs/{id} and GET …/run-requests/{id}: with
// ?wait=N the handler reads the row again every waitPollInterval until its
// status differs from the one the caller already knows (?unless=), the
// request context ends or N seconds are used up, then answers as it would
// without waiting.
const (
	maxWaitSeconds   = 25
	waitPollInterval = 500 * time.Millisecond
)

// sleepFor waits d or until ctx ends (ctx's error); Deps.WaitSleep replaces
// it in tests so a wait needs no real time.
func sleepFor(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// waitForChange reads once and, while wait > 0 and the status still equals
// the known one, reads again after each poll interval. The budget is the
// number of intervals that fit in wait, so a fake sleep drives it exactly.
// read returns what the handler answers and the status to compare. A read
// error ends the wait with that error; a finished context answers with the
// last read.
func waitForChange[T any](ctx context.Context, deps Deps, wait int, unless string, read func() (T, string, error)) (T, error) {
	out, status, err := read()
	if err != nil || wait <= 0 {
		return out, err
	}
	known := unless
	if known == "" {
		known = status
	}
	sleep := deps.WaitSleep
	if sleep == nil {
		sleep = sleepFor
	}
	budget := min(wait, maxWaitSeconds)
	for polls := int(time.Duration(budget) * time.Second / waitPollInterval); status == known && polls > 0; polls-- {
		if sleep(ctx, waitPollInterval) != nil {
			return out, nil
		}
		if out, status, err = read(); err != nil {
			return out, err
		}
	}
	return out, nil
}
