package worker

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
