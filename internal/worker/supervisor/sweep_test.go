package supervisor

import (
	"bytes"
	"strings"
	"syscall"
	"testing"
)

// TestSweepKillsEverythingItMaySignal pins what sweep mode asks the kernel
// for (backlog 7.21): SIGKILL to pid -1, every process the caller may
// signal. Nothing left to kill is success; any other refusal is a failure
// named on stderr.
func TestSweepKillsEverythingItMaySignal(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code int
	}{
		"killed":          {nil, 0},
		"nothing to kill": {syscall.ESRCH, 0},
		"refused":         {syscall.EPERM, 1},
	} {
		t.Run(name, func(t *testing.T) {
			var gotPid int
			var gotSig syscall.Signal
			var stderr bytes.Buffer
			code := sweep(&stderr, func(pid int, sig syscall.Signal) error {
				gotPid, gotSig = pid, sig
				return tc.err
			})
			if gotPid != -1 || gotSig != syscall.SIGKILL {
				t.Fatalf("kill(%d, %v), want kill(-1, SIGKILL)", gotPid, gotSig)
			}
			if code != tc.code {
				t.Fatalf("exit %d, want %d", code, tc.code)
			}
			if (code != 0) != strings.HasPrefix(stderr.String(), ErrPrefix) {
				t.Fatalf("stderr = %q for exit %d", stderr.String(), code)
			}
		})
	}
}
