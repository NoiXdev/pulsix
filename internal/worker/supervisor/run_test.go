package supervisor

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

const runnerPid = 10

// fakeSystem is a system over fakeTable's scripted process table. The
// runner is one of the table's pids; once it is gone from the table (it
// exited, or a signal removed it) the next Reap reports it, exactly once,
// with the status it died with.
type fakeSystem struct {
	*fakeTable
	calls        []string
	subreaperErr error
	startErr     error
	dropErr      error
	started      []string

	runnerStatus syscall.WaitStatus
	runnerReaped bool
	// orphans are reported by successive Reap calls, one batch per call,
	// before the runner is looked at.
	orphans [][]reaped
	// beforeReap runs at the start of the nth Reap call (1-based), so a test
	// can let the runner exit between two SIGCHLDs.
	beforeReap func(n int)
	reaps      int
}

func newFakeSystem(clk *fakeClock, react map[int]reaction) *fakeSystem {
	return &fakeSystem{fakeTable: newFakeTable(clk, react)}
}

// exit lets pid end on its own with the given exit code.
func (fs *fakeSystem) exit(pid, code int) {
	delete(fs.live, pid)
	if pid == runnerPid {
		fs.runnerStatus = syscall.WaitStatus(code << 8)
	}
}

func (fs *fakeSystem) SetSubreaper() error {
	fs.calls = append(fs.calls, "SetSubreaper")
	return fs.subreaperErr
}

func (fs *fakeSystem) Start(argv []string) (int, error) {
	fs.calls = append(fs.calls, "Start")
	if fs.startErr != nil {
		return 0, fs.startErr
	}
	fs.started = argv
	return runnerPid, nil
}

func (fs *fakeSystem) DropStdout() error {
	fs.calls = append(fs.calls, "DropStdout")
	return fs.dropErr
}

func (fs *fakeSystem) Signal(pid int, sig syscall.Signal) error {
	wasLive := fs.live[pid]
	err := fs.fakeTable.Signal(pid, sig)
	if pid == runnerPid && wasLive && !fs.live[pid] {
		fs.runnerStatus = syscall.WaitStatus(sig)
	}
	return err
}

func (fs *fakeSystem) Reap() ([]reaped, error) {
	fs.reaps++
	if fs.beforeReap != nil {
		fs.beforeReap(fs.reaps)
	}
	var out []reaped
	if len(fs.orphans) > 0 {
		out = append(out, fs.orphans[0]...)
		fs.orphans = fs.orphans[1:]
	}
	if !fs.live[runnerPid] && !fs.runnerReaped {
		fs.runnerReaped = true
		out = append(out, reaped{Pid: runnerPid, Status: fs.runnerStatus})
	}
	return out, nil
}

// queued returns an events channel already holding sigs, with room to
// spare so nothing the test queues is dropped.
func queued(sigs ...os.Signal) chan os.Signal {
	ch := make(chan os.Signal, len(sigs)+8)
	for _, s := range sigs {
		ch <- s
	}
	return ch
}

var testArgv = []string{"/usr/bin/ansible-runner", "run", "/w"}

type superviseResult struct {
	ws  syscall.WaitStatus
	err error
}

// runSupervise runs supervise and fails the test if it has not returned
// within a few real seconds: every scenario here ends on its own, so a
// timeout means the loop hung waiting for an event that will never come.
func runSupervise(t *testing.T, sys system, clk *fakeClock, events chan os.Signal) superviseResult {
	t.Helper()
	done := make(chan superviseResult, 1)
	go func() {
		ws, err := supervise(sys, clk, events, testGrace, testArgv)
		done <- superviseResult{ws, err}
	}()
	select {
	case r := <-done:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("supervise did not return: the loop is waiting for an event that never comes")
		return superviseResult{}
	}
}

func TestSuperviseSetsSubreaperBeforeStart(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	fs := newFakeSystem(clk, map[int]reaction{runnerPid: {}})
	fs.exit(runnerPid, 0)

	r := runSupervise(t, fs, clk, queued(syscall.SIGCHLD))
	if r.err != nil {
		t.Fatalf("supervise: %v", r.err)
	}
	want := []string{"SetSubreaper", "Start", "DropStdout"}
	if !reflect.DeepEqual(fs.calls, want) {
		t.Errorf("calls = %q, want %q", fs.calls, want)
	}
	if !reflect.DeepEqual(fs.started, testArgv) {
		t.Errorf("started %q, want %q", fs.started, testArgv)
	}
}

func TestSuperviseRefusesWithoutSubreaper(t *testing.T) {
	boom := errors.New("prctl: operation not permitted")
	clk := &fakeClock{now: time.Unix(1000, 0)}
	fs := newFakeSystem(clk, nil)
	fs.subreaperErr = boom

	r := runSupervise(t, fs, clk, queued())
	if !errors.Is(r.err, boom) || !errors.Is(r.err, errRefused) {
		t.Fatalf("err = %v, want a refusal wrapping %v", r.err, boom)
	}
	for _, c := range fs.calls {
		if c == "Start" {
			t.Fatal("Start was called although the supervisor could not become a subreaper")
		}
	}
}

func TestSuperviseRefusesWhenStartFails(t *testing.T) {
	boom := errors.New("exec: no such file")
	clk := &fakeClock{now: time.Unix(1000, 0)}
	fs := newFakeSystem(clk, nil)
	fs.startErr = boom

	r := runSupervise(t, fs, clk, queued())
	if !errors.Is(r.err, boom) || !errors.Is(r.err, errRefused) {
		t.Fatalf("err = %v, want a refusal wrapping %v", r.err, boom)
	}
	if !strings.Contains(r.err.Error(), testArgv[0]) {
		t.Errorf("err = %q, want it to name the program %q", r.err, testArgv[0])
	}
}

func TestSuperviseReturnsRunnerStatus(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	fs := newFakeSystem(clk, map[int]reaction{runnerPid: {}})
	fs.exit(runnerPid, 2)

	r := runSupervise(t, fs, clk, queued(syscall.SIGCHLD))
	if r.err != nil {
		t.Fatalf("supervise: %v", r.err)
	}
	if r.ws.Signaled() || r.ws.ExitStatus() != 2 {
		t.Errorf("status = %#x, want exit 2", uint32(r.ws))
	}
	if len(fs.sent) != 0 {
		t.Errorf("sent %v, want no signals for a runner that exited with nothing left behind", fs.sent)
	}
}

func TestSuperviseSweepsAfterNormalExit(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	fs := newFakeSystem(clk, map[int]reaction{runnerPid: {}, 30: {diesOnTerm: true}})
	fs.exit(runnerPid, 0)

	r := runSupervise(t, fs, clk, queued(syscall.SIGCHLD))
	if r.err != nil {
		t.Fatalf("supervise: %v", r.err)
	}
	if n := fs.count(30, syscall.SIGTERM); n != 1 {
		t.Errorf("leftover 30 got %d SIGTERMs, want 1", n)
	}
	if fs.live[30] {
		t.Error("supervise returned while the leftover 30 was still alive")
	}
	if r.ws.Signaled() || r.ws.ExitStatus() != 0 {
		t.Errorf("status = %#x, want the runner's exit 0", uint32(r.ws))
	}
}

func TestSuperviseStopsTreeOnTerm(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	fs := newFakeSystem(clk, map[int]reaction{
		runnerPid: {diesOnTerm: true}, 11: {diesOnTerm: true}, 12: {diesOnTerm: true},
	})

	r := runSupervise(t, fs, clk, queued(syscall.SIGTERM))
	if r.err != nil {
		t.Fatalf("supervise: %v", r.err)
	}
	for _, pid := range []int{runnerPid, 11, 12} {
		if n := fs.count(pid, syscall.SIGTERM); n != 1 {
			t.Errorf("pid %d got %d SIGTERMs, want 1", pid, n)
		}
	}
	if !r.ws.Signaled() || r.ws.Signal() != syscall.SIGTERM {
		t.Errorf("status = %#x, want the runner's reaped death by SIGTERM", uint32(r.ws))
	}
}

func TestSuperviseKillsARunnerThatIgnoresTerm(t *testing.T) {
	start := time.Unix(1000, 0)
	clk := &fakeClock{now: start}
	fs := newFakeSystem(clk, map[int]reaction{runnerPid: {}}) // ignores TERM

	r := runSupervise(t, fs, clk, queued(syscall.SIGTERM))
	if r.err != nil {
		t.Fatalf("supervise: %v", r.err)
	}
	if !r.ws.Signaled() || r.ws.Signal() != syscall.SIGKILL {
		t.Errorf("status = %#x, want death by SIGKILL", uint32(r.ws))
	}
	if elapsed := clk.now.Sub(start); elapsed < testGrace {
		t.Errorf("returned %v after the start, before the %v grace elapsed", elapsed, testGrace)
	}
}

func TestSuperviseTermBeforeAnythingElse(t *testing.T) {
	start := time.Unix(1000, 0)
	clk := &fakeClock{now: start}
	fs := newFakeSystem(clk, map[int]reaction{runnerPid: {diesOnTerm: true}})
	// The cancel is queued before the loop reads its first event, as when
	// the worker's TERM lands while the supervisor is still starting.
	r := runSupervise(t, fs, clk, queued(syscall.SIGTERM, syscall.SIGCHLD))
	if r.err != nil {
		t.Fatalf("supervise: %v", r.err)
	}
	if !r.ws.Signaled() || r.ws.Signal() != syscall.SIGTERM {
		t.Errorf("status = %#x, want the runner's death by SIGTERM", uint32(r.ws))
	}
	if elapsed := clk.now.Sub(start); elapsed > testGrace {
		t.Errorf("slept %v, want no more than one grace (%v)", elapsed, testGrace)
	}
}

func TestSuperviseTermAfterRunnerExited(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	fs := newFakeSystem(clk, map[int]reaction{runnerPid: {}})
	fs.exit(runnerPid, 4)

	r := runSupervise(t, fs, clk, queued(syscall.SIGCHLD, syscall.SIGTERM))
	if r.err != nil {
		t.Fatalf("supervise: %v", r.err)
	}
	if r.ws.ExitStatus() != 4 {
		t.Errorf("status = %#x, want the runner's exit 4", uint32(r.ws))
	}
	if len(fs.sent) != 0 {
		t.Errorf("sent %v after the runner had already exited with nothing left, want none", fs.sent)
	}
}

func TestSuperviseReapsOrphans(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	fs := newFakeSystem(clk, map[int]reaction{runnerPid: {}})
	fs.orphans = [][]reaped{{{Pid: 40, Status: 0}, {Pid: 41, Status: syscall.WaitStatus(syscall.SIGKILL)}}}
	fs.beforeReap = func(n int) {
		if n == 2 {
			fs.exit(runnerPid, 3)
		}
	}

	r := runSupervise(t, fs, clk, queued(syscall.SIGCHLD, syscall.SIGCHLD))
	if r.err != nil {
		t.Fatalf("supervise: %v", r.err)
	}
	if r.ws.ExitStatus() != 3 {
		t.Errorf("status = %#x, want the runner's exit 3, not an orphan's", uint32(r.ws))
	}
	if fs.reaps < 2 {
		t.Errorf("reaped %d times, want the orphans' reap not to end the loop", fs.reaps)
	}
	if len(fs.sent) != 0 {
		t.Errorf("sent %v, want none: reaped orphans are not a reason to stop the play", fs.sent)
	}
}

func TestSuperviseStopErrorKeepsStatus(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	fs := newFakeSystem(clk, map[int]reaction{runnerPid: {}, 30: {immortal: true}})
	fs.exit(runnerPid, 2)

	r := runSupervise(t, fs, clk, queued(syscall.SIGCHLD))
	if !errors.Is(r.err, ErrTreeDidNotDrain) {
		t.Fatalf("err = %v, want ErrTreeDidNotDrain", r.err)
	}
	if errors.Is(r.err, errRefused) {
		t.Errorf("err = %v: a started runner's stop failure must not read as a refusal", r.err)
	}
	if r.ws.ExitStatus() != 2 {
		t.Errorf("status = %#x, want the runner's exit 2 despite the stop failure", uint32(r.ws))
	}
}

func TestSuperviseDropStdoutFailureStillSupervises(t *testing.T) {
	boom := errors.New("dup3: bad file descriptor")
	clk := &fakeClock{now: time.Unix(1000, 0)}
	fs := newFakeSystem(clk, map[int]reaction{runnerPid: {}})
	fs.dropErr = boom
	fs.exit(runnerPid, 0)

	r := runSupervise(t, fs, clk, queued(syscall.SIGCHLD))
	if !errors.Is(r.err, boom) || errors.Is(r.err, errRefused) {
		t.Fatalf("err = %v, want %v reported, not a refusal", r.err, boom)
	}
	if r.ws.ExitStatus() != 0 || r.ws.Signaled() {
		t.Errorf("status = %#x, want the runner's exit 0", uint32(r.ws))
	}
}

func TestSuperviseReapErrorStopsThePlay(t *testing.T) {
	boom := errors.New("wait4: invalid argument")
	clk := &fakeClock{now: time.Unix(1000, 0)}
	fs := newFakeSystem(clk, map[int]reaction{runnerPid: {diesOnTerm: true}})
	rs := &failingReap{fakeSystem: fs, failures: 1, err: boom}

	r := runSupervise(t, rs, clk, queued(syscall.SIGCHLD))
	if !errors.Is(r.err, boom) {
		t.Fatalf("err = %v, want it to report %v", r.err, boom)
	}
	if n := fs.count(runnerPid, syscall.SIGTERM); n != 1 {
		t.Errorf("runner got %d SIGTERMs, want 1: a supervisor that cannot reap must stop the play", n)
	}
	if !r.ws.Signaled() || r.ws.Signal() != syscall.SIGTERM {
		t.Errorf("status = %#x, want the runner's death by SIGTERM once reaping worked again", uint32(r.ws))
	}
}

func TestSuperviseClosedEventsStopsThePlay(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	fs := newFakeSystem(clk, map[int]reaction{runnerPid: {diesOnTerm: true}})
	events := queued()
	close(events)

	r := runSupervise(t, fs, clk, events)
	if r.err != nil {
		t.Fatalf("supervise: %v", r.err)
	}
	if n := fs.count(runnerPid, syscall.SIGTERM); n != 1 {
		t.Errorf("runner got %d SIGTERMs, want 1: with no events left, the play must be stopped", n)
	}
	if !r.ws.Signaled() || r.ws.Signal() != syscall.SIGTERM {
		t.Errorf("status = %#x, want the runner's death by SIGTERM", uint32(r.ws))
	}
}

// failingReap fails the first `failures` Reap calls.
type failingReap struct {
	*fakeSystem
	failures int
	err      error
}

func (f *failingReap) Reap() ([]reaped, error) {
	if f.failures > 0 {
		f.failures--
		return nil, f.err
	}
	return f.fakeSystem.Reap()
}
