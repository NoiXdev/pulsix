// The play supervisor's proof in the image (backlog 2.107), for what
// cancel_test.go does not cover: a cancel without a play uid, a background
// process a play leaves behind on a run that succeeds, and a failed play's
// status passing through the supervisor unchanged.
//
// 2.107 was never about the uid boundary: ansible-runner and ansible-core
// each move the play into a process group of its own, out of the worker's
// reach, whichever uid the play runs under. So the proof has to hold for the
// worker's own uid too, not only across the boundary.
package uidboundary

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
	"github.com/noixdev/pulsix/internal/worker/runner"
)

// stopGrace mirrors internal/worker/runner's defaultKillGrace, the
// supervisor's --grace: how long the play has between TERM and KILL. A
// cancelled play must be gone, reaped and all, within this plus one second.
const stopGrace = 5 * time.Second

// cancelRun drives the real worker through one job the way runOneJob does,
// but cancels it: once started reports the play is running, the fake
// manager pushes a cancel down the run's stream. It returns the terminal
// report, the worker's log and the moment the cancel was sent.
func cancelRun(t *testing.T, cfg worker.Config, job *workerpb.Job, started func() bool) (*workerpb.ReportResultRequest, string, time.Time) {
	t.Helper()
	var logs bytes.Buffer
	client := &fakeManager{job: job, reported: make(chan struct{})}
	executor := runner.NewRunner("", "", "", 0, slog.New(slog.NewTextHandler(&logs, nil)))
	loop := worker.NewLoop(cfg, client, executor, worker.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))

	ctx, stop := context.WithTimeout(context.Background(), 5*time.Minute)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()

	// The play must actually be running before the cancel means anything:
	// a worker that cancelled before it spawned anything would otherwise
	// pass for the wrong reason.
	if !waitUntil(time.Now().Add(60*time.Second), started) {
		t.Fatalf("the play never started within 60s:\n%s", logs.String())
	}

	cancelledAt := time.Now()
	client.cancel()

	select {
	case <-client.reported:
	case <-time.After(30 * time.Second):
		t.Fatalf("the job was not reported within 30s of cancelling:\n%s", logs.String())
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("Loop.Run = %v", err)
	}
	t.Logf("worker log:\n%s", logs.String())
	return client.result(), logs.String(), cancelledAt
}

// TestCancellingAPlayWithoutAPlayUIDStopsIt is acceptance criterion 2 of
// the cancel-stops-the-play design: with no play uid the play runs as the
// worker's own uid, which may signal it directly — and before the play
// supervisor the cancel still left `sleep` running, because the play sat in
// process groups the worker never signalled. The sleep must be gone within
// the grace plus one second: not running, and not a zombie either, which
// would mean it escaped the supervisor to this binary (PID 1).
func TestCancellingAPlayWithoutAPlayUIDStopsIt(t *testing.T) {
	inImage(t)
	writeWorkerKey(t)

	job := localhostJob(t, `
- hosts: localhost
  gather_facts: false
  tasks:
    - name: sleep far longer than this test is willing to wait
      ansible.builtin.command: sleep 303
      changed_when: false
`)
	job.Id = "01900000-0000-7000-8000-00000000006a"

	cfg := workerConfig(t)
	cfg.PlayUID = 0

	var sleepPID int
	result, logs, cancelledAt := cancelRun(t, cfg, job, func() bool {
		pid, state, ok := processWithCmdline(t, "sleep 303")
		if ok && state != "Z" {
			sleepPID = pid
			return true
		}
		return false
	})

	if got := result.GetStatus(); got != "cancelled" {
		t.Errorf("status = %q, want cancelled (error: %q):\n%s", got, result.GetError(), logs)
	}
	// processWithCmdline cannot see a zombie (its cmdline is empty), so the
	// pid it saw while the sleep ran must be gone from /proc altogether.
	if !waitUntil(cancelledAt.Add(stopGrace+time.Second), func() bool {
		_, _, ok := processWithCmdline(t, "sleep 303")
		return !ok && processGone(sleepPID)
	}) {
		pid, state, _ := processWithCmdline(t, "sleep 303")
		t.Errorf("the play's sleep (pid %d) is still in /proc %v after the cancel (now: pid %d, state %q, pid %d gone: %v)",
			sleepPID, stopGrace+time.Second, pid, state, sleepPID, processGone(sleepPID))
	}
	if leftovers := workDirs(t); len(leftovers) != 0 {
		t.Errorf("work dirs survived the cancelled run: %v", leftovers)
	}
}

// TestABackgroundProcessDoesNotOutliveItsRun is acceptance criterion 3: a
// task that starts a process in the background and returns leaves it
// running for the rest of the play — the second task proves it is still
// there — and the run then succeeds. When the run ends, that process must
// end with it: nothing of a finished run may outlive it, and the run's own
// status must not change because the supervisor cleaned up after it.
func TestABackgroundProcessDoesNotOutliveItsRun(t *testing.T) {
	inImage(t)
	writeWorkerKey(t)
	// A survivor is the regression this test catches; it must not also be
	// the starting state of every test after it.
	t.Cleanup(func() { reapPlayUIDLeftovers(t) })

	job := localhostJob(t, `
- hosts: localhost
  gather_facts: false
  tasks:
    - name: start a process that outlives its task
      ansible.builtin.shell: nohup sleep 304 >/dev/null 2>&1 & echo $!
      register: background
      changed_when: false
    - name: the process is still running after its task ended
      ansible.builtin.shell: kill -0 {{ background.stdout }}
      changed_when: false
`)
	job.Id = "01900000-0000-7000-8000-00000000007a"

	result, events, logs := runOneJob(t, job)

	if got := result.GetStatus(); got != "succeeded" {
		t.Fatalf("status = %q, want succeeded (error: %q):\n%s\n%s", got, result.GetError(), eventDump(events), logs)
	}
	// The worker reports only after the supervisor has exited, and the
	// supervisor exits only once it has stopped and reaped the whole tree:
	// by now there is nothing left to wait for.
	if pid, state, ok := processWithCmdline(t, "sleep 304"); ok {
		t.Errorf("the background sleep outlived its run: pid %d, state %q\n%s", pid, state, debugPlayUIDProcesses(t))
	}
	if playUIDEntryExists(t) {
		t.Errorf("a /proc entry of uid %d outlived the run, live or zombie:\n%s", playUID, debugPlayUIDProcesses(t))
	}
	if leftovers := workDirs(t); len(leftovers) != 0 {
		t.Errorf("work dirs survived the run: %v", leftovers)
	}
}

// TestAFailingPlayStillReportsFailed is part of acceptance criterion 4: the
// supervisor stands between the worker and ansible-runner, and passes the
// runner's exit status through. A failed play (ansible-runner's exit 2)
// must still read as failed, not as succeeded or as an error.
func TestAFailingPlayStillReportsFailed(t *testing.T) {
	inImage(t)
	writeWorkerKey(t)

	job := localhostJob(t, `
- hosts: localhost
  gather_facts: false
  tasks:
    - name: fail on purpose
      ansible.builtin.fail:
        msg: x
`)
	job.Id = "01900000-0000-7000-8000-00000000008a"

	result, events, logs := runOneJob(t, job)

	if got := result.GetStatus(); got != "failed" {
		t.Errorf("status = %q, want failed (error: %q):\n%s\n%s", got, result.GetError(), eventDump(events), logs)
	}
	if leftovers := workDirs(t); len(leftovers) != 0 {
		t.Errorf("work dirs survived the run: %v", leftovers)
	}
	if playUIDEntryExists(t) {
		t.Errorf("a /proc entry of uid %d outlived the run, live or zombie:\n%s", playUID, debugPlayUIDProcesses(t))
	}
}
