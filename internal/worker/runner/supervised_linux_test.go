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
// supervisorBuildDir holds the built supervisor; TestMain removes it once
// every test of the package has run (backlog 2.114).
var supervisorBuildDir string

// cleanupSupervisorBuild removes the built supervisor, if one was built.
func cleanupSupervisorBuild() {
	if supervisorBuildDir != "" {
		_ = os.RemoveAll(supervisorBuildDir)
	}
}

func playSupervisor(t *testing.T) string {
	t.Helper()
	supervisorBuild.Do(func() {
		dir, err := os.MkdirTemp("", "pulsix-play-supervisor-")
		if err != nil {
			supervisorBuildErr = err
			return
		}
		supervisorBuildDir = dir
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
	if took, limit := time.Since(cancelled), r.KillGrace+time.Second; took >= limit {
		t.Errorf("the run took %v to end after the cancel, want under %v: the supervisor, not the worker's backstop, must stop it", took, limit)
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
	fake, _ := supervisedFake(t)
	trapped := filepath.Join(t.TempDir(), "trapped.pid")
	t.Setenv(fakeEnvSticky, "1")
	t.Setenv(fakeEnvTrapped, trapped)
	env := supervisedEnv(t)
	r := &Runner{RunnerBin: fake, KillGrace: 200 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done, _ := startRun(ctx, r, runJob(t), env, &collector{})
	// The fake writes this file only once its TERM trap is set, so the
	// cancel always meets a runner that really ignores TERM.
	pid := readPid(t, trapped)
	if !procPresent(t, pid, filepath.Base(fake)) {
		t.Fatalf("the fake runner (pid %d) is not running before the cancel", pid)
	}

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
// own exit must carry it through unchanged. The fixture's recap is clean,
// so only the exit code can make these runs failed; rc 0 is the control
// that the same recap reads as succeeded.
func TestSupervisedExitCodesPassThrough(t *testing.T) {
	cases := []struct {
		rc   string
		want worker.Status
	}{
		{"0", worker.StatusSucceeded},
		{"2", worker.StatusFailed},
		{"4", worker.StatusFailed},
	}
	for _, tc := range cases {
		t.Run("exit "+tc.rc, func(t *testing.T) {
			fake, _ := supervisedFake(t)
			t.Setenv(fakeEnvEvents, cleanFixtureAbs(t))
			t.Setenv(fakeEnvExit, tc.rc)
			nop := func(*workerpb.RunEvent) error { return nil }

			wantStatus, wantStats, wantErr := (&Runner{RunnerBin: fake}).Run(context.Background(), runJob(t), runEnv(t), nop)
			status, stats, err := (&Runner{RunnerBin: fake}).Run(context.Background(), runJob(t), supervisedEnv(t), nop)
			if wantStatus != tc.want || wantErr != nil {
				t.Fatalf("unsupervised run: status = %q err = %v, want %q", wantStatus, wantErr, tc.want)
			}
			if status != tc.want || err != nil {
				t.Errorf("supervised run: status = %q err = %v, want %q as unsupervised", status, err, tc.want)
			}
			if !reflect.DeepEqual(stats, wantStats) {
				t.Errorf("supervised stats = %v, want the unsupervised run's %v", stats, wantStats)
			}
		})
	}
}
