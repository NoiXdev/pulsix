package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureRunnerOutputKeepsStreamsAndExitCode(t *testing.T) {
	inner := filepath.Join(t.TempDir(), "inner")
	script := "#!/bin/sh\necho \"event $1 $2\"\necho 'Traceback: boom' >&2\nexit 3\n"
	if err := os.WriteFile(inner, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	wrapper, log := captureRunnerOutput(t, inner)

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(wrapper, "run", "with space")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if code := cmd.ProcessState.ExitCode(); code != 3 {
		t.Fatalf("wrapper exit code = %d (err %v), want the inner runner's 3", code, err)
	}
	if got := stdout.String(); got != "event run with space\n" {
		t.Fatalf("wrapper stdout = %q, want the inner runner's event stream verbatim", got)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"event run with space", "Traceback: boom"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("capture log %q lacks %q", raw, want)
		}
	}

	s := &scenario{t: t, runnerLog: log}
	if out := s.runnerOutput(); !strings.Contains(out, "Traceback: boom") {
		t.Fatalf("runnerOutput() = %q, want the captured stderr", out)
	}
}

func TestRunnerOutputTailsLongLogs(t *testing.T) {
	log := filepath.Join(t.TempDir(), "runner.log")
	body := "HEAD" + strings.Repeat("x", 2*runnerOutputTail) + "TAIL"
	if err := os.WriteFile(log, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out := (&scenario{t: t, runnerLog: log}).runnerOutput()
	if strings.Contains(out, "HEAD") || !strings.HasSuffix(strings.TrimSpace(out), "TAIL") {
		t.Fatalf("runnerOutput() does not keep only the tail: %.80q...", out)
	}
	if len(out) > runnerOutputTail+200 {
		t.Fatalf("runnerOutput() length = %d, want at most the tail plus a header", len(out))
	}
}

func TestRunnerOutputWithoutLog(t *testing.T) {
	if out := (&scenario{t: t}).runnerOutput(); out != "" {
		t.Fatalf("runnerOutput() without a log = %q, want empty", out)
	}
	missing := &scenario{t: t, runnerLog: filepath.Join(t.TempDir(), "absent")}
	if out := missing.runnerOutput(); !strings.Contains(out, "no ansible-runner output captured") {
		t.Fatalf("runnerOutput() for a missing log = %q, want a note saying so", out)
	}
}
