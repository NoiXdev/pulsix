//go:build linux

// The runner under the real play supervisor (backlog 2.107), end to end:
// the worker binary is built once and set as Config.PlaySupervisor, exactly
// what worker.Load does on Linux, and the fake ansible-runner plays the
// shapes that used to survive a cancel. Everything here needs a child
// subreaper and /proc, so it is Linux only.
package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
)

var (
	supervisorBuild    sync.Once
	supervisorBin      string
	supervisorBuildErr error
)

// playSupervisor builds ./cmd/worker once per test binary and returns its
// path: that binary dispatches the supervisor's reserved argument before it
// reads any configuration. It lands in a temp dir of its own rather than a
// test's, because every supervised test shares it and the package has no
// TestMain to clean up after the last one.
func playSupervisor(t *testing.T) string {
	t.Helper()
	supervisorBuild.Do(func() {
		dir, err := os.MkdirTemp("", "pulsix-play-supervisor-")
		if err != nil {
			supervisorBuildErr = err
			return
		}
		bin := filepath.Join(dir, "pulsix-worker")
		out, err := exec.Command("go", "build", "-o", bin, "github.com/noixdev/pulsix/cmd/worker").CombinedOutput()
		if err != nil {
			supervisorBuildErr = errors.New("go build ./cmd/worker: " + err.Error() + ": " + string(out))
			return
		}
		supervisorBin = bin
	})
	if supervisorBuildErr != nil {
		t.Fatalf("build the play supervisor: %v", supervisorBuildErr)
	}
	return supervisorBin
}

// supervisedFake writes the fake runner and sets the control variables
// every supervised test needs. It returns the fake's path and the file its
// own pid lands in.
func supervisedFake(t *testing.T) (fake, pidFile string) {
	t.Helper()
	fake = writeFakeRunner(t)
	pidFile = filepath.Join(t.TempDir(), "runner.pid")
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvArgs, filepath.Join(t.TempDir(), "args"))
	t.Setenv(fakeEnvPid, pidFile)
	t.Setenv(fakeEnvEvents, "")
	t.Setenv(fakeEnvExit, "0")
	return fake, pidFile
}

// supervisedEnv is runEnv with the supervisor configured.
func supervisedEnv(t *testing.T) worker.RunEnv {
	t.Helper()
	env := runEnv(t)
	env.Config.PlaySupervisor = playSupervisor(t)
	return env
}

// runResult is one Run's return values.
type runResult struct {
	status worker.Status
	stats  map[string]any
	err    error
}

// startRun runs the job in the background, forwarding its events to c.
// started closes on the first event the fake itself produced (the
// run_start bookend is the runner's own and comes before the spawn).
func startRun(ctx context.Context, r *Runner, job *workerpb.Job, env worker.RunEnv, c *collector) (done <-chan runResult, started <-chan struct{}) {
	doneCh := make(chan runResult, 1)
	startedCh := make(chan struct{})
	var once sync.Once
	sink := func(ev *workerpb.RunEvent) error {
		if ev.GetState() != evStateRunStart {
			once.Do(func() { close(startedCh) })
		}
		return c.sink(ev)
	}
	go func() {
		status, stats, err := r.Run(ctx, job, env, sink)
		doneCh <- runResult{status, stats, err}
	}()
	return doneCh, startedCh
}

// readPid waits for a pid file the fake writes and returns its pid.
func readPid(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pid in %s after 30s (last error: %v)", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// procPresent reports whether pid is still the process named comm, live or
// a zombie: /proc/<pid> stays until the process is reaped, and a zombie
// keeps its comm (its cmdline does not, which is why this reads stat). A
// pid that now names another program has been recycled, so ours is gone.
func procPresent(t *testing.T, pid int, comm string) bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		// ENOENT once it is reaped, ESRCH for a read racing the reap.
		return false
	}
	stat := string(raw)
	open, closing := strings.IndexByte(stat, '('), strings.LastIndexByte(stat, ')')
	if open < 0 || closing < open {
		t.Fatalf("unparseable /proc/%d/stat: %q", pid, stat)
	}
	return stat[open+1:closing] == comm
}

// waitGone fails the test unless pid (named comm) is gone, reaped and not a
// zombie, by the deadline.
func waitGone(t *testing.T, what string, pid int, comm string, deadline time.Time) {
	t.Helper()
	for procPresent(t, pid, comm) {
		if time.Now().After(deadline) {
			t.Fatalf("%s (pid %d) is still there, live or as a zombie: the supervisor did not stop and reap it", what, pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestSupervisedCancelStopsAnEscapedGrandchild is 2.107 without Ansible: a
// play that left the runner's process group and session for one of its own
// (setsid) never saw the worker's TERM before. Under the supervisor it is
// still a descendant, and a cancel must leave nothing of it behind.
func TestSupervisedCancelStopsAnEscapedGrandchild(t *testing.T) {
	fake, _ := supervisedFake(t)
	escaped := filepath.Join(t.TempDir(), "escaped.pid")
	t.Setenv(fakeEnvEscape, escaped)
	t.Setenv(fakeEnvEvents, fixtureAbs(t))
	env := supervisedEnv(t)
	r := &Runner{RunnerBin: fake, KillGrace: 500 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done, started := startRun(ctx, r, runJob(t), env, &collector{})
	select {
	case <-started:
	case res := <-done:
		t.Fatalf("the run ended before its first event: %+v", res)
	case <-time.After(60 * time.Second):
		t.Fatal("no event from the fake after 60s")
	}
	pid := readPid(t, escaped)
	if !procPresent(t, pid, "sleep") {
		t.Fatalf("the escaped sleep (pid %d) is not running before the cancel", pid)
	}

	cancel()
	cancelled := time.Now()
	var res runResult
	select {
	case res = <-done:
	case <-time.After(r.KillGrace + defaultSupervisorBackstop + 5*time.Second):
		t.Fatal("the run did not end after the cancel")
	}
	if res.status != worker.StatusCancelled || !errors.Is(res.err, context.Canceled) {
		t.Errorf("status = %q err = %v, want cancelled + context.Canceled", res.status, res.err)
	}
	waitGone(t, "the escaped `sleep 300`", pid, "sleep", cancelled.Add(r.KillGrace+time.Second))
}

// TestSupervisedCancelKillsAStickyRunner: a runner that ignores TERM (and
// passes that on to its own children) is the supervisor's to KILL after
// the grace, well before the worker's backstop.
func TestSupervisedCancelKillsAStickyRunner(t *testing.T) {
	fake, pidFile := supervisedFake(t)
	t.Setenv(fakeEnvSticky, "1")
	env := supervisedEnv(t)
	r := &Runner{RunnerBin: fake, KillGrace: 200 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done, _ := startRun(ctx, r, runJob(t), env, &collector{})
	pid := readPid(t, pidFile)
	if !procPresent(t, pid, filepath.Base(fake)) {
		t.Fatalf("the fake runner (pid %d) is not running before the cancel", pid)
	}
	// The pid is written before the fake's TERM trap is set; give it time
	// to be set, so the cancel meets a runner that really ignores TERM.
	time.Sleep(300 * time.Millisecond)

	cancel()
	cancelled := time.Now()
	var res runResult
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the run did not end within 2s of the cancel: the supervisor did not KILL a runner that ignores TERM")
	}
	if res.status != worker.StatusCancelled || !errors.Is(res.err, context.Canceled) {
		t.Errorf("status = %q err = %v, want cancelled + context.Canceled", res.status, res.err)
	}
	waitGone(t, "the sticky fake runner", pid, filepath.Base(fake), cancelled.Add(2*time.Second))
}

// TestSupervisedNormalRunStopsItsDaemon: the supervisor drops its own copy
// of stdout, so the event stream ends when the run's processes do; a
// daemon the play left behind (still holding that stdout) is stopped once
// the runner exits, the stream completes, and the run reads exactly as the
// same fixture does without a supervisor.
func TestSupervisedNormalRunStopsItsDaemon(t *testing.T) {
	fake, _ := supervisedFake(t)
	t.Setenv(fakeEnvEvents, cleanFixtureAbs(t))

	plain := &collector{}
	wantStatus, wantStats, err := (&Runner{RunnerBin: fake}).Run(context.Background(), runJob(t), runEnv(t), plain.sink)
	if err != nil || wantStatus != worker.StatusSucceeded {
		t.Fatalf("unsupervised run: status = %q err = %v, want succeeded", wantStatus, err)
	}

	daemon := filepath.Join(t.TempDir(), "daemon.pid")
	t.Setenv(fakeEnvDaemon, daemon)
	supervised := &collector{}
	done, _ := startRun(context.Background(), &Runner{RunnerBin: fake}, runJob(t), supervisedEnv(t), supervised)
	var res runResult
	select {
	case res = <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("the supervised run did not finish: its event stream never ended")
	}
	if res.err != nil || res.status != worker.StatusSucceeded {
		t.Fatalf("supervised run: status = %q err = %v, want succeeded", res.status, res.err)
	}
	if !reflect.DeepEqual(res.stats, wantStats) {
		t.Errorf("supervised stats = %v, want the unsupervised run's %v", res.stats, wantStats)
	}
	if !reflect.DeepEqual(supervised.states(), plain.states()) {
		t.Errorf("supervised events = %v, want the unsupervised run's %v", supervised.states(), plain.states())
	}
	pid := readPid(t, daemon)
	waitGone(t, "the daemon's `sleep 301`", pid, "sleep", time.Now().Add(time.Second))
}

// TestSupervisedExitCodesPassThrough: summarize tells failed (2) and
// unreachable (4) runs apart by the runner's exit code, so the supervisor's
// own exit must carry it through unchanged.
func TestSupervisedExitCodesPassThrough(t *testing.T) {
	for _, rc := range []string{"2", "4"} {
		t.Run("exit "+rc, func(t *testing.T) {
			fake, _ := supervisedFake(t)
			t.Setenv(fakeEnvEvents, fixtureAbs(t))
			t.Setenv(fakeEnvExit, rc)
			nop := func(*workerpb.RunEvent) error { return nil }

			wantStatus, wantStats, wantErr := (&Runner{RunnerBin: fake}).Run(context.Background(), runJob(t), runEnv(t), nop)
			status, stats, err := (&Runner{RunnerBin: fake}).Run(context.Background(), runJob(t), supervisedEnv(t), nop)
			if wantStatus != worker.StatusFailed || wantErr != nil {
				t.Fatalf("unsupervised run: status = %q err = %v, want failed", wantStatus, wantErr)
			}
			if status != wantStatus || err != nil {
				t.Errorf("supervised run: status = %q err = %v, want %q as unsupervised", status, err, wantStatus)
			}
			if !reflect.DeepEqual(stats, wantStats) {
				t.Errorf("supervised stats = %v, want the unsupervised run's %v", stats, wantStats)
			}
		})
	}
}
