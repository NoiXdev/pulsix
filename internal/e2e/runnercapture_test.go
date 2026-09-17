// The runner output capture: the worker deliberately keeps ansible-runner's
// output off the job row (it can carry revealed secrets), so a run that
// errors reads only "ansible-runner exited with code N" — useless in a CI
// log. The e2e harness wraps the runner binary instead and prints what it
// captured when a run assertion fails.
package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runnerOutputTail bounds how much captured output a failure message
// carries: the end of the log is where ansible reports why it stopped.
const runnerOutputTail = 16 << 10

// captureRunnerOutput writes an executable wrapper around bin that passes
// the runner's stdout (the JSONL event stream) through unchanged, appends
// both stdout and stderr to a log file, and exits with the runner's own
// exit code. It returns the wrapper's path and the log's path.
func captureRunnerOutput(t *testing.T, bin string) (wrapper, log string) {
	t.Helper()
	dir := t.TempDir()
	wrapper = filepath.Join(dir, "ansible-runner-capture")
	log = filepath.Join(dir, "ansible-runner.log")
	script := fmt.Sprintf(`#!/usr/bin/env bash
# e2e test fixture: tee ansible-runner's output into a log for failure reports.
set -o pipefail
log=%s
printf '=== ansible-runner %%s\n' "$*" >>"$log"
%s "$@" 2>>"$log" | tee -a "$log"
`, shellQuote(log), shellQuote(bin))
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return wrapper, log
}

// runnerOutput is the failure-message suffix with the tail of the captured
// runner output, or "" when the scenario captures nothing.
func (s *scenario) runnerOutput() string {
	if s.runnerLog == "" {
		return ""
	}
	raw, err := os.ReadFile(s.runnerLog)
	if err != nil || len(raw) == 0 {
		return fmt.Sprintf("\n(no ansible-runner output captured in %s: %v)", s.runnerLog, err)
	}
	truncated := ""
	if len(raw) > runnerOutputTail {
		raw = raw[len(raw)-runnerOutputTail:]
		truncated = fmt.Sprintf(", last %d bytes", runnerOutputTail)
	}
	return fmt.Sprintf("\n--- ansible-runner output (stdout+stderr%s) ---\n%s", truncated, strings.TrimRight(string(raw), "\n"))
}

// shellQuote single-quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
