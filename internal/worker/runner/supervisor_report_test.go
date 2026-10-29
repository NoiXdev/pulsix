package runner

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
)

// What a play supervisor reports after the runner started (a stop that did
// not drain, a failed signal or reap) comes beside the runner's status and
// never replaces it. The worker must still log it, naming the run, on every
// path out of Run: a cancel and a broken event stream as well as a run that
// ended on its own. These tests stand a shell script in for the supervisor,
// so they run on every OS.

// standInReport is the post-start line the stand-in supervisor writes.
const standInReport = "pulsix-play-supervisor: stop the play: descendants survived the kill passes: pids [42] still alive after 100 kill passes"

// fakeSupervisorReady names the file the stand-in writes once its TERM
// trap is in place, so a test cancels only a supervisor that will report.
const fakeSupervisorReady = "FAKE_SUPERVISOR_READY"

// writeReportingSupervisor writes a stand-in for the play supervisor. It
// takes the supervisor's argv (Arg --grace <d> -- <runner> <args…>), runs
// the runner in the background and passes its exit code through, writing
// standInReport to stderr on the way out; on TERM it writes the same line
// and exits 143 without waiting for the runner. With hang set it runs no
// runner at all and waits to be TERMed.
func writeReportingSupervisor(t *testing.T, hang bool) string {
	t.Helper()
	run := `"$@" &`
	if hang {
		run = `sleep 300 &`
	}
	script := "#!/bin/sh\n" +
		"while [ \"$1\" != \"--\" ]; do shift; done\n" +
		"shift\n" +
		"trap 'echo \"" + standInReport + "\" >&2; exit 143' TERM\n" +
		run + "\n" +
		"if [ -n \"$" + fakeSupervisorReady + "\" ]; then echo ready > \"$" + fakeSupervisorReady + "\"; fi\n" +
		"wait $!\n" +
		"rc=$?\n" +
		"echo \"" + standInReport + "\" >&2\n" +
		"exit $rc\n"
	path := filepath.Join(t.TempDir(), "pulsix-worker")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// lockedBuffer is a log sink that Run's goroutines may write concurrently.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// assertReportLogged checks that the log carries the stand-in's report at
// warn level, naming the job.
func assertReportLogged(t *testing.T, log, jobID string) {
	t.Helper()
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "job_id="+jobID) &&
			strings.Contains(line, standInReport) {
			return
		}
	}
	t.Errorf("log = %q, want a warn line with job_id=%s carrying %q", log, jobID, standInReport)
}

func TestRunLogsASupervisorReportAndKeepsTheRunnersStatus(t *testing.T) {
	fake := writeFakeRunner(t)
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvEvents, cleanFixtureAbs(t))
	t.Setenv(fakeEnvExit, "0")

	env := runEnv(t)
	env.Config.PlaySupervisor = writeReportingSupervisor(t, false)
	var logs lockedBuffer
	r := &Runner{RunnerBin: fake, Log: slog.New(slog.NewTextHandler(&logs, nil))}
	job := runJob(t)
	c := &collector{}
	status, _, err := r.Run(context.Background(), job, env, c.sink)
	if status != worker.StatusSucceeded || err != nil {
		t.Errorf("status = %q err = %v, want the runner's %q: a report never replaces the run's outcome", status, err, worker.StatusSucceeded)
	}
	assertReportLogged(t, logs.String(), job.GetId())
}

func TestRunLogsASupervisorReportOnACancel(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv(fakeSupervisorReady, ready)
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))

	env := runEnv(t)
	env.Config.PlaySupervisor = writeReportingSupervisor(t, true)
	var logs lockedBuffer
	r := &Runner{RunnerBin: writeFakeRunner(t), KillGrace: 150 * time.Millisecond, Log: slog.New(slog.NewTextHandler(&logs, nil))}
	job := runJob(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var status worker.Status
	go func() {
		defer close(done)
		status, _, _ = r.Run(ctx, job, env, func(*workerpb.RunEvent) error { return nil })
	}()
	waitForFile(t, ready)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not end after cancellation")
	}
	if status != worker.StatusCancelled {
		t.Errorf("status = %q, want %q", status, worker.StatusCancelled)
	}
	assertReportLogged(t, logs.String(), job.GetId())
}

func TestRunLogsASupervisorReportWhenTheStreamBreaks(t *testing.T) {
	fake := writeFakeRunner(t)
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvEvents, "")
	t.Setenv(fakeEnvExit, "0")
	t.Setenv(fakeEnvFlood, filepath.Join(t.TempDir(), "count"))

	env := runEnv(t)
	env.Config.PlaySupervisor = writeReportingSupervisor(t, false)
	var logs lockedBuffer
	r := &Runner{RunnerBin: fake, KillGrace: 150 * time.Millisecond, Log: slog.New(slog.NewTextHandler(&logs, nil))}
	job := runJob(t)
	// The run_start bookend goes through; the first event of the run
	// breaks the stream.
	sink := func(ev *workerpb.RunEvent) error {
		if ev.GetState() == evStateRunStart {
			return nil
		}
		return context.Canceled
	}
	_, _, err := r.Run(context.Background(), job, env, sink)
	if err == nil || !strings.Contains(err.Error(), "stream runner events") {
		t.Fatalf("err = %v, want the broken event stream", err)
	}
	assertReportLogged(t, logs.String(), job.GetId())
}

// waitForFile waits for path to have content, failing the test after 5 s.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was not written within 5s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
