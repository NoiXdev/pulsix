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

	"github.com/noixdev/pulsix/internal/worker/supervisor"
)

// removeGrace bounds the play-uid removal below. It is a plain `rm -rf` of
// one scratch directory, so a second is already generous; the bound exists
// so a wedged helper cannot hold the claim loop.
//
// It was 30 s, which contradicted that sentence and cost more than it
// looked (backlog 2.106): this removal runs in runOne's defer, so the
// bound is also how long a job can delay the worker's SHUTDOWN. Ten
// seconds keeps a wide margin over "generous" while making a wedged
// helper cost ten seconds of a restart rather than thirty. Past it the
// dir is left behind and logged as an error, which is the same failure
// the longer bound had — later.
const removeGrace = 10 * time.Second

// newWorkDir creates a fresh temp dir under the data dir; runOne removes
// it when the run ends. It is owner-only (MkdirTemp's 0700) whether or not
// PlayUID is set. The play that runs inside it needs group access, but
// only once the worker has written the run's files: runner.ShareWorkDir
// opens the dir, last and setgid, right before the spawn. Opening it here,
// as this used to, let any process of the play uid plant symlinks in it
// for the worker's own writes — private keys among them — to follow
// (backlog 7.21).
func (l *Loop) newWorkDir() (string, error) {
	return os.MkdirTemp(l.cfg.DataDir, "job-")
}

// sweepPlayUID kills every process of the play uid: the worker binary's
// sweep mode (supervisor.SweepArg), run through the setuid helper as that
// uid. The loop runs it when it starts and after every job, so nothing a
// play started outlives its job — not even a process it detached after
// killing its own supervisor, which runs as the same uid and so cannot
// stop it (backlog 7.21). A worker runs one job at a time, so between jobs
// every process of the play uid is a leftover.
//
// It needs the worker binary (Config.PlaySupervisor, filled on Linux) and
// a play uid; without either there is nothing to run or nothing to sweep.
// A failure is logged as an error and does not fail the job, which has
// already reported.
func (l *Loop) sweepPlayUID(ctx context.Context) {
	if l.cfg.PlayUID == 0 || l.cfg.PlaySupervisor == "" {
		return
	}
	helper := l.cfg.SetuidHelper
	if helper == "" {
		helper = defaultSetuidHelper
	}
	// Detached from ctx, like removeAsPlayUID: the loop's own shutdown is
	// a common reason a job ends, and the sweep must still run.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removeGrace)
	defer cancel()
	cmd := exec.CommandContext(sctx, helper, strconv.FormatUint(uint64(l.cfg.PlayUID), 10), l.cfg.PlaySupervisor, supervisor.SweepArg)
	cmd.Env = stripPrivateEnv(os.Environ())
	if out, err := cmd.CombinedOutput(); err != nil {
		l.log.ErrorContext(ctx, "could not stop the play uid's leftover processes; a play may still be running",
			"helper", helper, "err", err, "output", string(out))
	}
}

// removeWorkDir deletes one run's work dir, and says so when it cannot.
//
// With a play uid set, plain removal is not enough: ansible-runner creates
// artifacts/<ident>/ inside the work dir as the PLAY's uid with mode 0700,
// and the worker, which owns neither, may not list or unlink inside it.
// Everything else in the dir goes and that one stays, so the data volume
// grows by one directory per job forever.
//
// Ansible's own ~/.ansible/tmp is NOT one of these leftovers, though an
// earlier version of this comment said it was (backlog 2.106). It lives
// in the play uid's home, not in the work dir, so nothing here reaches it
// and nothing should: it is shared between concurrent jobs of the same
// uid, and clearing it from one job's cleanup would delete another job's
// scratch mid-run. deploy/worker/Dockerfile carries the whole argument. The second
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
	// The helper's final execv is a plain binary with unchanged
	// credentials, so this child is dumpable and its /proc/<pid>/environ
	// belongs to the play uid — readable by anything the play left behind
	// under it. The worker's own settings, PULSIX_ENROLL_TOKEN among them,
	// have no business there; the run spawn strips them for the same
	// reason.
	cmd.Env = stripPrivateEnv(os.Environ())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", helper, err, out)
	}
	return nil
}
