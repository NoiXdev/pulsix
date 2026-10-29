// The cancel-across-the-uid-boundary proof (backlog 2.104): the worker may
// not signal another uid's processes directly — kill(2) against the play
// uid's process group returns EPERM once PULSIX_WORKER_PLAY_UID is set — so
// a cancelled run used to leave its play running forever. The fix (Tasks 1
// and 2: the setuid helper's --signal mode, and runner.Runner.stopGroup
// routing through it) can only be proven inside the real image: every unit
// test exercises a stand-in helper that execs WITHOUT dropping uid, which
// can signal its own child just fine and is therefore structurally blind to
// the half that actually fails.
//
// This file holds more than one test because investigating the first one
// surfaced a SECOND, separate defect: backlog 2.107, the play surviving the
// cancel in process groups the worker never signalled.
// TestCancellingAPlayUnderThePlayUIDReportsCancelled proves the uid-crossing
// half (2.104). TestCancellingAPlayUnderThePlayUIDStopsIt proves the play's
// own processes actually die; it was skipped until the play supervisor
// (internal/worker/supervisor, docs/superpowers/specs/2026-10-29-cancel-stops-the-play-design.md)
// fixed 2.107, and runs unchanged apart from that one deleted skip.
// TestACancelledPlayLeavesNoZombie adds what StopsIt cannot see: that the
// play is reaped, not only dead. cancel_nouid_test.go holds the same proof
// without a play uid.
package uidboundary

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/worker"
	"github.com/noixdev/pulsix/internal/worker/runner"
)

// TestCancellingAPlayUnderThePlayUIDReportsCancelled proves the half of
// backlog 2.104 that Tasks 1-2 actually fixed: with a play uid configured,
// cancelling a run makes the job reach status "cancelled" within the
// TERM-then-grace-then-KILL window. Before Tasks 1-2 this was impossible —
// the worker's own kill(2) against the play uid's process group was EPERM,
// stopGroup's cancel watcher hung forever, and the job was never reported at
// all (the job simply never reports after 30s).
//
// It does not assert that the play's own processes are gone — that is
// TestCancellingAPlayUnderThePlayUIDStopsIt's job below, which was written
// when this test still left the play running (backlog 2.107).
func TestCancellingAPlayUnderThePlayUIDReportsCancelled(t *testing.T) {
	inImage(t)
	writeWorkerKey(t)
	// Until the play supervisor this test left the play running (2.107).
	// Everything in the package shares one container, so a survivor would
	// be the starting state of TestCancellingAPlayUnderThePlayUIDStopsIt:
	// it would satisfy that test's "the play has started" wait before its
	// own play did, and fail its closing "nothing of the play uid's is
	// left" whatever its cancel achieved. The cleanup stays as a guard: a
	// survivor now fails this test as the regression it is, and is still
	// cleared out of the way of the tests after it.
	t.Cleanup(func() { reapPlayUIDLeftovers(t) })

	job := localhostJob(t, `
- hosts: localhost
  gather_facts: false
  tasks:
    - name: sleep far longer than this test is willing to wait
      ansible.builtin.command: sleep 300
      changed_when: false
`)
	job.Id = "01900000-0000-7000-8000-00000000005a"

	cfg := workerConfig(t)
	var logs bytes.Buffer
	client := &fakeManager{job: job, reported: make(chan struct{})}
	executor := runner.NewRunner("", "", "", 0, slog.New(slog.NewTextHandler(&logs, nil)))
	loop := worker.NewLoop(cfg, client, executor, worker.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))

	ctx, stop := context.WithTimeout(context.Background(), 5*time.Minute)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()

	// The play must actually have started under the play uid before the
	// cancel means anything — otherwise a worker that cancelled a run
	// before it even spawned the helper would pass this test for the wrong
	// reason.
	if !waitUntil(time.Now().Add(60*time.Second), func() bool { return playUIDProcessExists(t) }) {
		t.Fatalf("no process with uid %d appeared within 60s; the play never started:\n%s", playUID, logs.String())
	}

	client.cancel()

	select {
	case <-client.reported:
	case <-time.After(30 * time.Second):
		t.Fatalf("the job was not reported within 30s of cancelling — before Tasks 1-2 this never happened at all, because the worker's own kill(2) across the uid boundary was EPERM:\n%s", logs.String())
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("Loop.Run = %v", err)
	}
	t.Logf("worker log:\n%s", logs.String())

	if got := client.result().GetStatus(); got != "cancelled" {
		t.Errorf("status = %q, want cancelled (error: %q):\n%s", got, client.result().GetError(), logs.String())
	}
}

// TestCancellingAPlayUnderThePlayUIDStopsIt is backlog 2.107: a cancel
// must stop the play itself, not only its leader. ansible-runner hands the
// play to a process it put in a NEW process group, and ansible-core's
// per-host worker puts it in a THIRD, deeper one, so the worker's
// kill(-pgid, …) reached ansible-runner and nothing below it, with or
// without a play uid (backlog row 2.107 in docs/BACKLOG.md has the
// diagnosis). The play supervisor fixes it: a child subreaper per run that
// TERMs and then KILLs every descendant, whatever group or session it
// moved to (docs/superpowers/specs/2026-10-29-cancel-stops-the-play-design.md).
//
// The test was written before that fix and was skipped until it landed; it
// is unmodified from the version that first caught the defect apart from
// the deleted t.Skip line. Its "the play has started" wait relies on
// playUIDProcessExists leaving the supervisor out: under the supervisor the
// first process of the play uid is the supervisor itself, and counting it
// let the cancel land before ansible-runner existed. The wait releases on
// ansible-runner, not on the play, so the cancel may land before the
// `sleep` exists.
//
// Its closing check is the lenient one: playUIDProcessExists leaves the
// supervisor out and does not count zombies. The strict proofs, which wait
// for the play's own `sleep` before cancelling and then count every
// process, are TestACancelledPlayLeavesNoZombie below (play uid: no entry
// of the play uid at all, live or zombie, supervisor included) and
// cancel_nouid_test.go's TestCancellingAPlayWithoutAPlayUIDStopsIt (no
// play uid: the `sleep` itself is gone).
func TestCancellingAPlayUnderThePlayUIDStopsIt(t *testing.T) {
	inImage(t)
	writeWorkerKey(t)

	job := localhostJob(t, `
- hosts: localhost
  gather_facts: false
  tasks:
    - name: sleep far longer than this test is willing to wait
      ansible.builtin.command: sleep 300
      changed_when: false
`)
	job.Id = "01900000-0000-7000-8000-00000000004a"

	cfg := workerConfig(t)
	var logs bytes.Buffer
	client := &fakeManager{job: job, reported: make(chan struct{})}
	executor := runner.NewRunner("", "", "", 0, slog.New(slog.NewTextHandler(&logs, nil)))
	loop := worker.NewLoop(cfg, client, executor, worker.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))

	ctx, stop := context.WithTimeout(context.Background(), 5*time.Minute)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()

	// The play must actually have started under the play uid before the
	// cancel means anything — otherwise a worker that cancelled a run
	// before it even spawned the helper would pass this test for the wrong
	// reason.
	if !waitUntil(time.Now().Add(60*time.Second), func() bool { return playUIDProcessExists(t) }) {
		t.Fatalf("no process with uid %d appeared within 60s; the play never started:\n%s", playUID, logs.String())
	}

	client.cancel()

	select {
	case <-client.reported:
	case <-time.After(30 * time.Second):
		t.Fatalf("the job was not reported within 30s of cancelling — the sleep likely survived the cancel:\n%s", logs.String())
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("Loop.Run = %v", err)
	}
	t.Logf("worker log:\n%s", logs.String())

	if got := client.result().GetStatus(); got != "cancelled" {
		t.Errorf("status = %q, want cancelled (error: %q):\n%s", got, client.result().GetError(), logs.String())
	}

	if !waitUntil(time.Now().Add(30*time.Second), func() bool { return !playUIDProcessExists(t) }) {
		t.Errorf("a process with uid %d still exists 30s after cancelling: the cancel did not cross the uid boundary\n%s", playUID, debugPlayUIDProcesses(t))
	}

	if leftovers := workDirs(t); len(leftovers) != 0 {
		t.Errorf("work dirs survived the cancelled run: %v", leftovers)
	}
}

// reapPlayUIDLeftovers kills everything still running under the play uid
// and clears whatever it left in the data dir. It was written for backlog
// 2.107, when a cancel reached the worker's own ansible-runner leader and
// nothing in the two process groups below it, so a cancelled run in this
// package left a `sleep 300` and the two ansible processes above it alive
// for the rest of the container's life. The play supervisor fixed that,
// and the function stays as a guard: it runs after the owning test's worker
// has stopped, so any live process of the play uid it finds outlived its
// run. That is a regression of 2.107, which it reports as a failure of the
// owning test before clearing it away so the tests after it start clean.
//
// The kill goes through the setuid helper for the same reason the worker's
// own does: this process is uid 65532 and the kernel will not let it
// signal 65533. Finding the groups by UID is what the worker itself must
// not do — a concurrent job's play would share that uid — but here it is
// the right primitive and the narrower one: this container runs one test
// at a time, and the premise of every test after this one is that nothing
// of the play uid's is left over — which is why a survivor fails the test
// that owns this cleanup instead of only being logged.
//
// What it kills stays behind as zombies: this binary is the container's
// PID 1, inherits every orphan, and never waits for a pid it did not start.
// livePlayUIDProcessExists counts only live processes for exactly that
// reason — and, unlike playUIDProcessExists, the supervisor too: a
// supervisor alive after its run is a survivor like any other.
func reapPlayUIDLeftovers(t *testing.T) {
	t.Helper()
	if livePlayUIDProcessExists(t) {
		t.Errorf("a process of uid %d outlived its run (a regression of backlog 2.107); killing it:\n%s", playUID, debugPlayUIDProcesses(t))
	}
	helper := workerConfig(t).SetuidHelper
	// Never signal our own group. A helper child spawned without Setpgid
	// is a member of it and is uid 65533 for the stretch between its drop
	// and its execv, so a scan that caught one mid-flight would otherwise
	// name this group and this cleanup would kill the test binary.
	self := syscall.Getpgrp()
	for _, pgid := range playUIDProcessGroups(t) {
		if pgid <= 1 || pgid == self {
			continue // the helper refuses <= 1, and rightly: kill(-1, ...)
		}
		if out, err := exec.Command(helper, strconv.Itoa(playUID), "--signal", "KILL", strconv.Itoa(pgid)).CombinedOutput(); err != nil {
			t.Logf("reaping play uid process group %d: %v: %s", pgid, err, out)
		}
	}
	if !waitUntil(time.Now().Add(30*time.Second), func() bool { return !livePlayUIDProcessExists(t) }) {
		t.Errorf("processes under uid %d survived the cleanup:\n%s", playUID, debugPlayUIDProcesses(t))
	}
	// The surviving play keeps writing into the run's work dir, which it
	// can recreate after the worker's own cleanup already removed it. What
	// is inside belongs to the play uid, so it comes off exactly the way
	// internal/worker/workdir.go's removeAsPlayUID takes it off: the
	// entries through the helper, the (now empty) directory as ourselves,
	// because the data dir is 0710 and the play uid may not unlink in it.
	for _, dir := range workDirs(t) {
		full := filepath.Join(dataDir, dir)
		entries, err := os.ReadDir(full)
		if err != nil {
			t.Logf("read %s: %v", full, err)
			continue
		}
		rm, err := exec.LookPath("rm")
		if err != nil {
			t.Logf("find rm to clear %s: %v", full, err)
			rm = "/bin/rm"
		}
		argv := []string{strconv.Itoa(playUID), rm, "-rf", "--"}
		for _, e := range entries {
			argv = append(argv, filepath.Join(full, e.Name()))
		}
		if len(entries) > 0 {
			if out, err := exec.Command(helper, argv...).CombinedOutput(); err != nil {
				t.Logf("clearing %s as uid %d: %v: %s", full, playUID, err, out)
			}
		}
		if err := os.RemoveAll(full); err != nil {
			t.Logf("remove %s: %v", full, err)
		}
	}
}

// TestACancelledPlayLeavesNoZombie is what TestCancellingAPlayUnderThePlayUIDStopsIt
// cannot see, kept in its own test so that one stays as it was written:
// playUIDProcessExists counts only live processes, so a play that died but
// was never reaped would pass it. Under the supervisor nothing of a
// cancelled play may be left in /proc at all within the grace plus one
// second: the supervisor is the play's subreaper and reaps every
// descendant before it exits. A play-uid zombie means some process
// escaped it to this binary, PID 1, which never reaps a pid it did not
// start.
func TestACancelledPlayLeavesNoZombie(t *testing.T) {
	inImage(t)
	writeWorkerKey(t)
	t.Cleanup(func() { reapPlayUIDLeftovers(t) })

	job := localhostJob(t, `
- hosts: localhost
  gather_facts: false
  tasks:
    - name: sleep far longer than this test is willing to wait
      ansible.builtin.command: sleep 305
      changed_when: false
`)
	job.Id = "01900000-0000-7000-8000-00000000009a"

	result, logs, cancelledAt := cancelRun(t, workerConfig(t), job, func() bool {
		_, state, ok := processWithCmdline(t, "sleep 305")
		return ok && state != "Z"
	})

	if got := result.GetStatus(); got != "cancelled" {
		t.Errorf("status = %q, want cancelled (error: %q):\n%s", got, result.GetError(), logs)
	}
	if !waitUntil(cancelledAt.Add(stopGrace+time.Second), func() bool { return !playUIDEntryExists(t) }) {
		t.Errorf("a /proc entry of uid %d, live or zombie, is left %v after the cancel:\n%s",
			playUID, stopGrace+time.Second, debugPlayUIDProcesses(t))
	}
}

// waitUntil polls cond every 100ms until it reports true or deadline
// passes, returning cond's last result either way.
func waitUntil(deadline time.Time, cond func() bool) bool {
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}
