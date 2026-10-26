// The run's work dir, both ends of its life: created fresh under the data
// dir before a job runs, removed when it ends. Both ends change shape once
// PULSIX_WORKER_PLAY_UID is set — the play's own uid has to be able to
// work inside the dir, and what it leaves behind there is owned by a uid
// the worker cannot delete.
package worker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// removeGrace bounds the play-uid removal below. It is a plain `rm -rf` of
// one scratch directory, so a second is already generous; the bound exists
// so a wedged helper cannot hold the claim loop.
const removeGrace = 30 * time.Second

// newWorkDir creates a fresh temp dir under the data dir; runOne removes
// it when the run ends. MkdirTemp's own default (0700) is exactly right
// when PlayUID is unset: owner-only. When it is set, the play that is
// about to run inside this dir needs group access to it (not just to the
// files ShareWorkDir later opens up inside it, but to the directory
// itself, or it could never list or enter it) — setgid so anything the
// play creates in turn inherits the worker's group too.
func (l *Loop) newWorkDir() (string, error) {
	dir, err := os.MkdirTemp(l.cfg.DataDir, "job-")
	if err != nil {
		return "", err
	}
	if l.cfg.PlayUID != 0 {
		if err := os.Chmod(dir, 0o770|os.ModeSetgid); err != nil {
			return "", fmt.Errorf("share work dir: %w", err)
		}
	}
	return dir, nil
}

// removeWorkDir deletes one run's work dir, and says so when it cannot.
//
// With a play uid set, plain removal is not enough: ansible-runner creates
// artifacts/<ident>/ — and ansible its own ~/.ansible/tmp — as the PLAY's
// uid with mode 0700, and the worker, which owns neither, may not list or
// unlink inside them. Everything else in the dir goes and those two stay,
// so the data volume grows by one directory per job forever. The second
// attempt therefore deletes the leftovers as the play uid itself, through
// the same setuid helper the run was started with: the play owns its own
// directories, and the worker's own files sit in group-writable (2770)
// ones, so that identity can clear the whole tree while holding strictly
// less privilege than the worker — no new privileged surface, the helper's
// contract is unchanged. The work dir itself is unlinked by the worker
// afterwards; the play cannot, the data dir above it being the worker's.
//
// A failure is logged, never swallowed. A leak nothing reports is how the
// volume fills.
func (l *Loop) removeWorkDir(workDir string) {
	err := os.RemoveAll(workDir)
	if err != nil && l.cfg.PlayUID != 0 {
		if playErr := l.removeAsPlayUID(workDir); playErr != nil {
			l.log.WarnContext(context.Background(), "could not clear the play uid's leftovers from the work dir",
				"dir", workDir, "err", playErr)
		}
		err = os.RemoveAll(workDir)
	}
	if err != nil {
		l.log.ErrorContext(context.Background(), "could not remove the run's work dir; it will keep using space on the worker's data volume until it is removed by hand",
			"dir", workDir, "err", err)
	}
}

// removeAsPlayUID clears the work dir's contents as the play uid. The
// entries are enumerated by the worker and passed explicitly — no glob, so
// nothing depends on a shell, and the one path the play could not delete
// anyway (the work dir itself) is never handed to it. rm is resolved to an
// absolute path because the helper execs it: there is no PATH search on
// the other side of the drop.
func (l *Loop) removeAsPlayUID(workDir string) error {
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return fmt.Errorf("list %s: %w", workDir, err)
	}
	if len(entries) == 0 {
		return nil
	}
	rm, err := exec.LookPath("rm")
	if err != nil {
		return fmt.Errorf("find rm to clear the play uid's leftovers: %w", err)
	}
	argv := []string{strconv.FormatUint(uint64(l.cfg.PlayUID), 10), rm, "-rf", "--"}
	for _, e := range entries {
		argv = append(argv, filepath.Join(workDir, e.Name()))
	}
	helper := l.cfg.SetuidHelper
	if helper == "" {
		// Load always fills this; a Config assembled by hand (tests, the
		// embedded worker) may not.
		helper = defaultSetuidHelper
	}
	// A detached context on purpose: the usual reason a run ends is the
	// loop's own shutdown, and the cleanup must still get to run.
	ctx, cancel := context.WithTimeout(context.Background(), removeGrace)
	defer cancel()
	cmd := exec.CommandContext(ctx, helper, argv...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", helper, err, out)
	}
	return nil
}
