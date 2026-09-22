// The run view's error mapping, unit-tested directly (it maps the jobs
// package's sentinels one by one): every sentinel lands on its HTTP
// status, and an unmapped error is logged and replaced with a generic
// 500 rather than ever reaching the client.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/jobs"
)

func TestMapJobsErr(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"invalid input", jobs.ErrInvalidInput, http.StatusBadRequest},
		{"wrapped invalid input", fmt.Errorf("claim job: %w", jobs.ErrInvalidInput), http.StatusBadRequest},
		{"invalid cursor", jobs.ErrInvalidCursor, http.StatusBadRequest},
		{"unknown job", jobs.ErrNotFound, http.StatusNotFound},
		{"not running", jobs.ErrNotRunning, http.StatusConflict},
		{"nothing to retry", jobs.ErrNothingToRetry, http.StatusUnprocessableEntity},
		{"empty scope", jobs.ErrEmptyScope, http.StatusUnprocessableEntity},
		{"host key changed", jobs.ErrHostKeyChanged, http.StatusConflict},
		{"unpinned hosts", jobs.ErrUnpinnedHosts, http.StatusUnprocessableEntity},
		{"unmapped error", errors.New("database exploded"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := mapJobsErr(context.Background(), c.err)
			se, ok := err.(huma.StatusError)
			if !ok {
				t.Fatalf("err = %v, want a huma status error", err)
			}
			if se.GetStatus() != c.status {
				t.Fatalf("status = %d, want %d", se.GetStatus(), c.status)
			}
		})
	}
}
