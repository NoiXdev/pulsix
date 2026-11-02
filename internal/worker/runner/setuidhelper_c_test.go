package runner_test

// This file compiles deploy/worker/setuid-exec.c and exercises the CLI
// contract of its signal mode (backlog 2.104):
//
//	pulsix-setuid-exec UID --signal SIGNAL PGID
//
// It proves the validation order from the design — argc, uid drift, mode
// detection, signal-name and pgid validation, THEN the geteuid() refusal —
// without needing root or a real setuid binary: every case here is turned
// away before the helper would ever call kill(2).

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

var (
	helperOnce sync.Once
	helperPath string
	helperSkip string
)

// compiledSetuidHelper compiles deploy/worker/setuid-exec.c once per test
// binary and returns the path to the resulting executable.
//
// It is compiled WITHOUT -static on purpose: static linking is a property
// the shipped image gives the binary (deploy/worker/Dockerfile's build
// stage), not a property of the argv contract this test exercises, and a
// developer machine (darwin) cannot link a static binary at all. The whole
// file is skipped, naming the reason, when "cc" is not on PATH or the
// compile itself fails on this platform; CI is Linux and will not skip.
func compiledSetuidHelper(t *testing.T) string {
	t.Helper()
	helperOnce.Do(func() {
		ccPath, err := exec.LookPath("cc")
		if err != nil {
			helperSkip = "no cc on PATH: " + err.Error()
			return
		}

		_, thisFile, _, ok := runtime.Caller(0)
		if !ok {
			helperSkip = "could not determine this test file's own path"
			return
		}
		// internal/worker/runner/setuidhelper_c_test.go -> repo root
		repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
		src := filepath.Join(repoRoot, "deploy", "worker", "setuid-exec.c")

		out := filepath.Join(t.TempDir(), "pulsix-setuid-exec")
		cmd := exec.Command(ccPath, "-Wall", "-Wextra", "-Werror", "-O2",
			"-DPLAY_UID=65533", "-DWORKER_GID=65532", "-o", out, src)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			helperSkip = "compiling setuid-exec.c without -static failed on this platform: " +
				err.Error() + ": " + stderr.String()
			return
		}
		helperPath = out
	})
	if helperSkip != "" {
		t.Skip(helperSkip)
	}
	return helperPath
}

// TestSetuidHelperSignalModeContract is Review Focus 5 for the signal mode:
// every case here must be refused before the helper ever drops privilege,
// so it is safe to run as an ordinary developer account. The last case is
// the load-bearing one — it proves the helper reaches the geteuid() refusal
// for a well-formed signal and pgid, i.e. that no kill(2) is reachable
// without the setuid bit actually having taken effect.
func TestSetuidHelperSignalModeContract(t *testing.T) {
	helper := compiledSetuidHelper(t)

	tests := []struct {
		name string
		argv []string
		want string
		// prefix is what stderr must start with; empty means the helper's
		// own "pulsix-setuid-exec: " error prefix. The argc cases print a
		// bare usage line instead, which is not an error about a value.
		prefix string
	}{
		// argc. Too few arguments for any mode at all is caught before the
		// uid is even looked at; the signal mode then demands exactly its
		// own four, so neither a missing PGID nor a stray extra argument
		// can be read as a different, well-formed invocation.
		{name: "a lone uid is refused", argv: []string{"65533"}, want: "usage: pulsix-setuid-exec UID PROGRAM", prefix: "usage: "},
		{name: "the signal mode without a pgid is refused", argv: []string{"65533", "--signal", "TERM"}, want: "usage: pulsix-setuid-exec UID --signal SIGNAL PGID", prefix: "usage: "},
		{name: "the signal mode with an extra argument is refused", argv: []string{"65533", "--signal", "TERM", "4242", "extra"}, want: "usage: pulsix-setuid-exec UID --signal SIGNAL PGID", prefix: "usage: "},

		{name: "pgid 1 is refused", argv: []string{"65533", "--signal", "TERM", "1"}, want: "not a process group"},
		{name: "pgid 0 is refused", argv: []string{"65533", "--signal", "TERM", "0"}, want: "not a process group"},
		{name: "a negative pgid is refused", argv: []string{"65533", "--signal", "TERM", "-5"}, want: "not a process group"},
		{name: "trailing junk is refused", argv: []string{"65533", "--signal", "TERM", "42x"}, want: "not a process group"},
		// Regression for the review finding on the whole-branch review:
		// parse_pgid guarded only s[0] == '-', but strtoul skips leading
		// whitespace BEFORE it reads the sign, so one space in front of a
		// negative walked straight past the guard and then wrapped —
		// " -18446744073709551614" is 2^64-2 unsigned, which strtoul
		// returns as 2, inside [2, INT_MAX] and accepted. The guard is now
		// "the first character is a digit", which covers the sign and the
		// whitespace in one condition.
		{name: "a leading space is refused", argv: []string{"65533", "--signal", "TERM", " 42"}, want: "not a process group"},
		{name: "a whitespace-prefixed negative that wraps is refused", argv: []string{"65533", "--signal", "TERM", " -18446744073709551614"}, want: "not a process group"},
		{name: "a leading tab is refused", argv: []string{"65533", "--signal", "TERM", "\t42"}, want: "not a process group"},
		{name: "a leading plus is refused", argv: []string{"65533", "--signal", "TERM", "+42"}, want: "not a process group"},
		// Regression for the review finding on this task: parse_pgid used
		// to validate the parsed value as unsigned long and cast to pid_t
		// (32-bit signed) only afterwards, so a value a multiple of 2^32
		// away from a small number passed the ">1" check on the wide type
		// and then truncated to that small number on the narrow one —
		// 2^32+1 truncated to 1, turning "refuse pgid 1" into "accept a
		// pgid that becomes 1", i.e. kill(-1, ...) after all.
		{name: "2^31 is refused (first value that truncates negative)", argv: []string{"65533", "--signal", "TERM", "2147483648"}, want: "not a process group"},
		{name: "2^32 is refused (truncates to the helper's own pgid, 0)", argv: []string{"65533", "--signal", "TERM", "4294967296"}, want: "not a process group"},
		{name: "2^32+1 is refused (truncates to pgid 1, the fleet-wide kill)", argv: []string{"65533", "--signal", "TERM", "4294967297"}, want: "not a process group"},
		{name: "an unsupported signal is refused", argv: []string{"65533", "--signal", "HUP", "4242"}, want: "unsupported signal"},
		{name: "the uid drift check comes first", argv: []string{"65534", "--signal", "TERM", "4242"}, want: "this helper is built for uid 65533"},
		{name: "no signal without the drop", argv: []string{"65533", "--signal", "TERM", "4242"}, want: "not setuid-root"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.want == "not setuid-root" && os.Geteuid() == 0 {
				// Root needs no drop to signal, so the helper rightly
				// does not refuse (backlog 2.109).
				t.Skip("running as root: the helper has nothing to refuse")
			}
			cmd := exec.Command(helper, tt.argv...)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()

			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("expected the helper to exit non-zero, got err=%v (stderr=%q)", err, stderr.String())
			}
			if exitErr.ExitCode() != 1 {
				t.Errorf("exit code = %d, want 1 (stderr=%q)", exitErr.ExitCode(), stderr.String())
			}
			prefix := tt.prefix
			if prefix == "" {
				prefix = "pulsix-setuid-exec: "
			}
			if !strings.HasPrefix(stderr.String(), prefix) {
				t.Errorf("stderr = %q, want prefix %q", stderr.String(), prefix)
			}
			if !strings.Contains(stderr.String(), tt.want) {
				t.Errorf("stderr = %q, want to contain %q", stderr.String(), tt.want)
			}
		})
	}
}
