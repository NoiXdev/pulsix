package supervisor

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// mainHarness collects what run writes and raises.
type mainHarness struct {
	stderr bytes.Buffer
	raised []syscall.Signal
	sys    *fakeSystem
	clk    *fakeClock
	sysErr error
	events chan os.Signal
}

func newMainHarness(react map[int]reaction, events ...os.Signal) *mainHarness {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	return &mainHarness{clk: clk, sys: newFakeSystem(clk, react), events: queued(events...)}
}

func (h *mainHarness) run(args []string) int {
	return run(args, mainDeps{
		stderr: &h.stderr,
		newSystem: func() (system, error) {
			if h.sysErr != nil {
				return nil, h.sysErr
			}
			return h.sys, nil
		},
		clock:  h.clk,
		notify: func() <-chan os.Signal { return h.events },
		raise: func(sig syscall.Signal) error {
			h.raised = append(h.raised, sig)
			return nil
		},
	})
}

// oneErrorLine asserts that stderr holds exactly one line, starting with
// ErrPrefix and containing want.
func (h *mainHarness) oneErrorLine(t *testing.T, want string) {
	t.Helper()
	out := h.stderr.String()
	if strings.Count(out, "\n") != 1 || !strings.HasPrefix(out, ErrPrefix) {
		t.Fatalf("stderr = %q, want exactly one line starting with %q", out, ErrPrefix)
	}
	if !strings.Contains(out, want) {
		t.Errorf("stderr = %q, want it to mention %q", out, want)
	}
}

var goodArgs = []string{"--grace", "5s", "--", "/usr/bin/ansible-runner", "run", "/w"}

func TestRunRefusesBadArguments(t *testing.T) {
	h := newMainHarness(nil)
	if code := h.run([]string{"--grace", "5s"}); code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	h.oneErrorLine(t, "missing --")
	if len(h.sys.calls) != 0 {
		t.Errorf("calls = %q, want none for an argv that does not parse", h.sys.calls)
	}
}

func TestRunRefusesWithoutASystem(t *testing.T) {
	h := newMainHarness(nil)
	h.sysErr = errors.New("the play supervisor needs Linux")
	if code := h.run(goodArgs); code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	h.oneErrorLine(t, "needs Linux")
}

func TestRunRefusesWithoutSubreaper(t *testing.T) {
	h := newMainHarness(nil)
	h.sys.subreaperErr = errors.New("operation not permitted")
	if code := h.run(goodArgs); code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	h.oneErrorLine(t, "operation not permitted")
}

func TestRunPassesExitCodeThrough(t *testing.T) {
	for _, code := range []int{0, 2, 4} {
		h := newMainHarness(map[int]reaction{runnerPid: {}}, syscall.SIGCHLD)
		h.sys.exit(runnerPid, code)
		if got := h.run(goodArgs); got != code {
			t.Errorf("runner exit %d: code = %d", code, got)
		}
		if h.stderr.Len() != 0 {
			t.Errorf("runner exit %d: stderr = %q, want nothing", code, h.stderr.String())
		}
		if len(h.raised) != 0 {
			t.Errorf("runner exit %d: raised %v, want nothing", code, h.raised)
		}
	}
}

func TestRunReraisesASignalDeath(t *testing.T) {
	h := newMainHarness(map[int]reaction{runnerPid: {diesOnTerm: true}}, syscall.SIGTERM)
	code := h.run(goodArgs)
	if !reflect.DeepEqual(h.raised, []syscall.Signal{syscall.SIGTERM}) {
		t.Fatalf("raised %v, want [SIGTERM]", h.raised)
	}
	// The fake raise returns, as a real one does only when the signal did
	// not end the process; run then falls back to the shell's convention.
	if want := 128 + int(syscall.SIGTERM); code != want {
		t.Errorf("code = %d, want %d", code, want)
	}
}

func TestRunReportsAStopFailureOnOneLineAndKeepsTheCode(t *testing.T) {
	h := newMainHarness(map[int]reaction{runnerPid: {}, 30: {immortal: true}}, syscall.SIGCHLD)
	h.sys.exit(runnerPid, 2)
	// Two failures at once: errors.Join puts them on separate lines.
	h.sys.dropErr = errors.New("dup3 failed")
	if code := h.run(goodArgs); code != 2 {
		t.Errorf("code = %d, want the runner's 2", code)
	}
	h.oneErrorLine(t, "survived")
}
