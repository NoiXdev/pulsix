package runner

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
)

// TestMalformedRunnerLineIsNamedRedacted is backlog 2.115's first step: a
// stdout line of ansible-runner that is not an event was logged with the
// decode error alone, so nobody could tell what printed it. The warning
// now carries a bounded prefix of the line, redacted like everything else
// the worker shows.
func TestMalformedRunnerLineIsNamedRedacted(t *testing.T) {
	var logs bytes.Buffer
	r := &Runner{Log: slog.New(slog.NewTextHandler(&logs, nil))}
	red := newRedactor(&workerpb.Job{Redact: []string{"hunter2-secret"}})
	line := "WARNING: something printed hunter2-secret " + strings.Repeat("x", 500) + "\n"

	if _, err := r.streamEvents(context.Background(), strings.NewReader(line), func(*workerpb.RunEvent) error { return nil }, red); err != nil {
		t.Fatal(err)
	}
	got := logs.String()
	if !strings.Contains(got, "WARNING: something printed "+redactedMarker) {
		t.Fatalf("the warning does not name the line: %s", got)
	}
	if strings.Contains(got, "hunter2-secret") {
		t.Fatalf("the warning leaked a secret: %s", got)
	}
	if strings.Contains(got, strings.Repeat("x", malformedPrefixLen)) {
		t.Fatalf("the warning carries more than a prefix: %s", got)
	}
}

// TestRunnerConfigCreatesTheRunsHome is backlog 2.115's cause: the run's
// HOME (<work dir>/home) existed only for repository-backed runs, so for
// every other run ansible-core printed "Failed to create the directory
// …/home/.ansible" to stdout, where the worker took it for a broken event.
// The home exists, owner-only like the rest of the work dir until
// ShareWorkDir opens it to the play's group.
func TestRunnerConfigCreatesTheRunsHome(t *testing.T) {
	env := worker.RunEnv{WorkDir: t.TempDir()}
	if err := (&Runner{}).materializeRunnerConfig(&workerpb.Job{Id: "j1"}, env); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(env.WorkDir, homeDirName))
	if err != nil || !info.IsDir() {
		t.Fatalf("the run's home is missing: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("the run's home = %v, want 0700 until the work dir is shared", perm)
	}
}
