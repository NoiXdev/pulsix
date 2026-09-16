// The SSE stream's error frame (final review triage): a failure inside a
// running stream is logged server side with its detail, and the client
// gets one generic error frame — internal error text (database messages,
// ids of other rows) never reaches the browser.
package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestStreamErrorFrameIsGenericAndLogged(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	var frames []struct {
		event string
		data  any
	}
	write := func(event string, data any) error {
		frames = append(frames, struct {
			event string
			data  any
		}{event, data})
		return nil
	}
	cause := errors.New(`pq: relation "jobs" does not exist (SQLSTATE 42P01) conn 10.0.0.5:5432`)
	streamError(context.Background(), write, "read job status", cause)

	if len(frames) != 1 || frames[0].event != "error" {
		t.Fatalf("frames = %+v, want one error frame", frames)
	}
	body, _ := frames[0].data.(map[string]any)
	if body["error"] != "the event stream failed; reload to continue" {
		t.Fatalf("error frame = %v, want the generic message", body)
	}
	if !strings.Contains(logs.String(), "SQLSTATE 42P01") || !strings.Contains(logs.String(), "read job status") {
		t.Fatalf("the detail was not logged server side: %s", logs.String())
	}
}
