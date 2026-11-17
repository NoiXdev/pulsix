package app

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestBuildAndCloseOnASmallPool (backlog 3.92): the advisory lockers hold
// their sessions on a pool of their own, so the background loops cannot
// starve the shared pool. Before, each locker took its session from the
// shared pool and then needed a second connection for the work it locked:
// once as many loops held a lock as the pool had connections, every one of
// them waited for a connection none would give back, and so did Build and
// every request. CI hit it with the default pool of four (a four-core
// runner) on 2026-11-17; with the shared pool capped at two it hung every
// time. Build must return and Close must stop every loop, repeatedly.
func TestBuildAndCloseOnASmallPool(t *testing.T) {
	cfg := testConfig(t)
	sep := "?"
	if strings.Contains(cfg.DatabaseURL, "?") {
		sep = "&"
	}
	cfg.DatabaseURL += sep + "pool_max_conns=2"

	for i := range 5 {
		done := make(chan error, 1)
		go func() {
			a, err := Build(context.Background(), cfg, WithAuthReaperInterval(5*time.Millisecond))
			if err != nil {
				done <- err
				return
			}
			// Several reaper passes, each taking and releasing its lock.
			time.Sleep(100 * time.Millisecond)
			a.Close()
			done <- nil
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("round %d: Build: %v", i, err)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("round %d: Build or Close did not return within 30s on a two-connection pool", i)
		}
	}
}
