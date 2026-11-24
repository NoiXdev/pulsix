package testdb

import (
	"strings"
	"time"
)

const (
	startAttempts = 3
	startBackoff  = 2 * time.Second
)

// isReaperRemoving reports the testcontainers reaper's transient state: the
// shared reaper container was being torn down as another package started
// (backlog 3.73).
func isReaperRemoving(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "wait for reaper") && strings.Contains(msg, `unexpected container status "removing"`)
}

// startWithRetry runs start up to startAttempts times while it fails with the
// reaper's transient state, sleeping and logging between attempts. Any other
// error is returned at once.
func startWithRetry(start func() error, sleep func(time.Duration), logf func(string, ...any)) error {
	var err error
	for attempt := 1; attempt <= startAttempts; attempt++ {
		if err = start(); err == nil || !isReaperRemoving(err) {
			return err
		}
		if attempt < startAttempts {
			logf("testdb: reaper is being removed (attempt %d of %d), retrying: %v", attempt, startAttempts, err)
			sleep(startBackoff * time.Duration(attempt))
		}
	}
	return err
}
