package worker

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker/supervisor"
)

// unremovableWorkDir builds a work dir the worker's own RemoveAll cannot
// clear: a directory it may not write to, with a file inside. In
// production the same situation arises from ansible-runner creating
// artifacts/<ident>/ as the PLAY's uid with mode 0700 — a directory the
// worker neither owns nor may unlink inside. The symptom is identical and
// it needs no second uid to reproduce.
func unremovableWorkDir(t *testing.T, dataDir string) string {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("root ignores the permission bits this case is built from")
	}
	work, err := os.MkdirTemp(dataDir, "job-")
	if err != nil {
		t.Fatalf("mkdir work: %v", err)
	}
	stuck := filepath.Join(work, "artifacts")
	if err := os.Mkdir(stuck, 0o700); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stuck, "status"), []byte("ok"), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := os.Chmod(stuck, 0o500); err != nil {
		t.Fatalf("chmod artifacts: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(stuck, 0o700)
		_ = os.RemoveAll(work)
	})
	return work
}

// TestRemoveWorkDirRemovesAndStaysQuiet: the ordinary case must still just
// delete the dir, with nothing logged.
func TestRemoveWorkDirRemovesAndStaysQuiet(t *testing.T) {
	dir := t.TempDir()
	var logs bytes.Buffer
	l := NewLoop(cfgFor(dir, zoneID), &fakeWorkerClient{}, &scriptExecutor{}, WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))

	work, err := l.newWorkDir()
	if err != nil {
		t.Fatalf("newWorkDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "inventory.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write run file: %v", err)
	}

	l.removeWorkDir(work)

	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Errorf("the work dir survived: %v", err)
	}
	if logs.Len() != 0 {
		t.Errorf("a successful removal must log nothing:\n%s", logs.String())
	}
}

// TestRemoveWorkDirReportsWhatItCouldNotRemove: the removal used to be
// `_ = os.RemoveAll(workDir)`. Once the play runs under its own uid it
// leaves behind directories the worker may not unlink, so the removal
// fails on every job — and a leak nothing reports is how a data volume
// fills up. It has to reach the log, naming the directory.
func TestRemoveWorkDirReportsWhatItCouldNotRemove(t *testing.T) {
	dir := t.TempDir()
	var logs bytes.Buffer
	l := NewLoop(cfgFor(dir, zoneID), &fakeWorkerClient{}, &scriptExecutor{}, WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))

	work := unremovableWorkDir(t, dir)
	l.removeWorkDir(work)

	out := logs.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, work) {
		t.Errorf("a work dir that could not be removed must be logged as an error naming it:\n%s", out)
	}
}

// TestRemoveWorkDirClearsThePlayUIDsLeftovers pins the second attempt: with
// a play uid set, the leftovers are deleted AS the play uid, through the
// same setuid helper the run was started with. The worker enumerates the
// entries itself (no glob, no shell) and hands rm an absolute path,
// because the helper execs it and execv searches no PATH. The work dir
// itself is never handed over — the play could not unlink it anyway, its
// parent being the worker's data dir.
func TestRemoveWorkDirClearsThePlayUIDsLeftovers(t *testing.T) {
	dir := t.TempDir()
	work := unremovableWorkDir(t, dir)

	// A stand-in for the setuid helper: it cannot drop privilege in a
	// unit test, so it records how it was called and changes nothing.
	record := filepath.Join(t.TempDir(), "argv")
	helper := filepath.Join(t.TempDir(), "fake-setuid-exec")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + record + "\nexit 0\n"
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake helper: %v", err)
	}

	cfg := cfgFor(dir, zoneID)
	cfg.PlayUID = 65533
	cfg.SetuidHelper = helper
	var logs bytes.Buffer
	l := NewLoop(cfg, &fakeWorkerClient{}, &scriptExecutor{}, WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))

	l.removeWorkDir(work)

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the setuid helper was never invoked for the leftovers: %v", err)
	}
	argv := strings.Fields(string(raw))
	if len(argv) < 5 {
		t.Fatalf("helper argv = %q, want uid, rm, -rf, --, and at least one path", argv)
	}
	if argv[0] != "65533" {
		t.Errorf("argv[0] = %q, want the play uid", argv[0])
	}
	if !filepath.IsAbs(argv[1]) || filepath.Base(argv[1]) != "rm" {
		t.Errorf("argv[1] = %q, want an absolute path to rm (the helper execs it; execv searches no PATH)", argv[1])
	}
	if argv[2] != "-rf" || argv[3] != "--" {
		t.Errorf("argv[2:4] = %q, want -rf --", argv[2:4])
	}
	for _, p := range argv[4:] {
		if filepath.Dir(p) != work {
			t.Errorf("argv path %q is not an entry of the work dir %q", p, work)
		}
		if p == work {
			t.Errorf("the work dir itself must not be handed to the play uid: %q", p)
		}
	}
	// The stand-in deletes nothing, so the dir is still there and that
	// still has to be reported rather than swallowed.
	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("a work dir still present after both attempts must be logged as an error:\n%s", logs.String())
	}
}

// TestRemoveWorkDirDoesNotHandThePlayUIDTheWorkersSecrets: the cleanup
// child is the one subprocess that used to inherit the worker's whole
// environment. The helper's final execv is a plain non-setuid binary with
// unchanged credentials, so the child is dumpable and its
// /proc/<pid>/environ belongs to the play uid — readable by anything the
// play left running under that uid. PULSIX_ENROLL_TOKEN in there is
// exactly the secret this feature exists to keep away from a play, so the
// cleanup strips the private environment the way the run spawn does.
func TestRemoveWorkDirDoesNotHandThePlayUIDTheWorkersSecrets(t *testing.T) {
	dir := t.TempDir()
	work := unremovableWorkDir(t, dir)

	t.Setenv("PULSIX_ENROLL_TOKEN", "enroll-secret-value")
	t.Setenv("ANSIBLE_CONFIG", "/etc/pulsix-worker/ansible.cfg")

	record := filepath.Join(t.TempDir(), "env")
	helper := filepath.Join(t.TempDir(), "fake-setuid-exec")
	script := "#!/bin/sh\nenv > " + record + "\nexit 0\n"
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake helper: %v", err)
	}

	cfg := cfgFor(dir, zoneID)
	cfg.PlayUID = 65533
	cfg.SetuidHelper = helper
	l := NewLoop(cfg, &fakeWorkerClient{}, &scriptExecutor{}, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))

	l.removeWorkDir(work)

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the setuid helper was never invoked for the leftovers: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		name, _, _ := strings.Cut(line, "=")
		if strings.HasPrefix(name, "PULSIX_") || strings.HasPrefix(name, "ANSIBLE_") {
			t.Errorf("the cleanup child inherited %q; the worker's private environment must not cross the uid boundary", name)
		}
	}
	// The strip must not take the rest of the environment with it: rm
	// itself is resolved absolutely, but a child with no PATH at all is a
	// different program than the one the run spawn builds.
	if !strings.Contains(string(raw), "PATH=") {
		t.Errorf("the cleanup child lost its whole environment, not just the private part:\n%s", raw)
	}
}

// fakeSetuidHelper writes a stand-in for the setuid helper that appends
// each invocation's argv, one line per call, to the returned record file.
func fakeSetuidHelper(t *testing.T) (helper, record string) {
	t.Helper()
	record = filepath.Join(t.TempDir(), "argv")
	helper = filepath.Join(t.TempDir(), "fake-setuid-exec")
	script := "#!/bin/sh\necho \"$*\" >> " + record + "\nexit 0\n"
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake helper: %v", err)
	}
	return helper, record
}

// TestSweepPlayUIDRunsTheSweepAsThePlayUID is backlog 7.21: between jobs
// the worker kills every process of the play uid, through the setuid
// helper and the worker binary's sweep mode, so nothing a play started —
// not even after killing its own supervisor — outlives its job.
func TestSweepPlayUIDRunsTheSweepAsThePlayUID(t *testing.T) {
	helper, record := fakeSetuidHelper(t)
	cfg := cfgFor(t.TempDir(), zoneID)
	cfg.PlayUID = 65533
	cfg.SetuidHelper = helper
	cfg.PlaySupervisor = "/usr/local/bin/pulsix-worker"
	l := NewLoop(cfg, &fakeWorkerClient{}, &scriptExecutor{})

	l.sweepPlayUID(context.Background())

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the setuid helper was never invoked: %v", err)
	}
	if got, want := strings.TrimSpace(string(raw)), "65533 /usr/local/bin/pulsix-worker "+supervisor.SweepArg; got != want {
		t.Fatalf("helper argv = %q, want %q", got, want)
	}
}

// TestSweepPlayUIDIsOffWithoutAPlayUID: no play uid, no second uid to
// sweep, and no helper call at all.
func TestSweepPlayUIDIsOffWithoutAPlayUID(t *testing.T) {
	helper, record := fakeSetuidHelper(t)
	cfg := cfgFor(t.TempDir(), zoneID)
	cfg.SetuidHelper = helper
	cfg.PlaySupervisor = "/usr/local/bin/pulsix-worker"
	l := NewLoop(cfg, &fakeWorkerClient{}, &scriptExecutor{})

	l.sweepPlayUID(context.Background())

	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatalf("the helper ran without a play uid: %v", err)
	}
}

// TestLoopSweepsThePlayUIDAtStartAndAfterEachJob: once when the loop
// starts (a previous worker process may have left plays behind) and once
// after every job, before its work dir is removed.
func TestLoopSweepsThePlayUIDAtStartAndAfterEachJob(t *testing.T) {
	helper, record := fakeSetuidHelper(t)
	dir := t.TempDir()
	cfg := cfgFor(dir, zoneID)
	cfg.PlayUID = 65533
	cfg.SetuidHelper = helper
	cfg.PlaySupervisor = "/usr/local/bin/pulsix-worker"
	client := &fakeWorkerClient{claimQueue: []*workerpb.ClaimJobResponse{jobFor(uuid.Must(uuid.NewV7()), "tok")}}
	exec := &scriptExecutor{run: func(_ int, _ context.Context, _ *workerpb.Job, _ RunEnv, _ func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		return StatusSucceeded, nil, nil
	}}

	cancel, done := runLoop(t, NewLoop(cfg, client, exec))
	waitFor(t, 5*time.Second, func() bool {
		raw, _ := os.ReadFile(record)
		return strings.Count(string(raw), supervisor.SweepArg) >= 2
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
}
