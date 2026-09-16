// Package runner executes claimed run and ad-hoc jobs through
// ansible-runner (backlog 2.11 worker side, 2.12 events). It implements the
// worker.Executor port with the loop's pointer signature; the loop owns the
// job id, the event sequence numbers and the event timestamps — the runner
// fills host, task, state and payload only.
//
// Materialization happens inside the loop's fresh 0700 work dir, which
// doubles as the ansible-runner private data dir (project/, env/,
// artifacts/): the inventory document (0600) plus an executable wrapper for
// it (Ansible reads a dynamic-inventory JSON through the script inventory
// plugin — a bare .json file is parsed by the yaml inventory plugin, whose
// group handling ignores _meta.hostvars), the playbook (inline verbatim, or
// the file the repo fetch resolved), the generated ansible.cfg,
// ssh_config with per-bastion key files, and the extra vars file.
//
// Invocation (pinned; the worker image must ship an ansible-runner of the
// 2.x series — the 1.x `stream` subcommand no longer exists):
//
//	ansible-runner run <work-dir> --json --playbook <path> \
//	    --inventory <work-dir>/inventory --ident <job-id-prefix> [--limit <limit>]
//
// `--json` streams each event as one JSON line on stdout, which the parser
// consumes; ansible-runner additionally keeps artifacts under
// <work-dir>/artifacts/<ident>/ (the stats.json fallback).
package runner

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
)

// defaultKillGrace is how long a SIGTERM'd runner gets to exit before it is
// SIGKILL'd.
const defaultKillGrace = 5 * time.Second

// The job kinds this executor runs; every other kind is reported as an
// executor error (the composite dispatch's default). bootstrap and
// rotate_key are the Task 11 system playbooks: their claims ship no
// playbook — the runner materializes the worker's embedded YAML (see
// materializePlaybook) against the one-host inventory the executor built
// from the kind payload.
const (
	kindRun       = "run"
	kindAdhoc     = "adhoc"
	kindBootstrap = "bootstrap"
	kindRotateKey = "rotate_key"
)

// The runner's own bookend events; the state values are the manager's
// RunEvent set.
const (
	evStateRunStart = "run_start"
	evStateRunEnd   = "run_end"
	evStateStdout   = "stdout"
)

// Runner executes claimed jobs by driving ansible-runner as a subprocess.
// The fields are the injection points; zero values fall back to their
// defaults at Run time.
type Runner struct {
	// RunnerBin is the ansible-runner binary. Empty falls back to the
	// claim's Config.AnsibleRunnerBin (PULSIX_ANSIBLE_RUNNER_BIN).
	RunnerBin string
	// AnsibleBin is the ansible-playbook binary, passed through as
	// ansible-runner's -b/--binary; empty lets the runner discover it via
	// PATH (the image ships it there).
	AnsibleBin string
	// GitBin is the git binary for repo playbooks; "git" when empty.
	GitBin string
	// KillGrace is the SIGTERM-to-SIGKILL grace on cancellation;
	// defaultKillGrace when zero.
	KillGrace time.Duration
	// Log receives run diagnostics: malformed-line warnings, cancellation
	// and failure notices. Payload material never lands here — the run's
	// output travels as events, failures name ids and exit codes only.
	Log *slog.Logger
}

// NewRunner wires the runner with explicit dependencies.
func NewRunner(runnerBin, ansibleBin, gitBin string, killGrace time.Duration, log *slog.Logger) *Runner {
	return &Runner{RunnerBin: runnerBin, AnsibleBin: ansibleBin, GitBin: gitBin, KillGrace: killGrace, Log: log}
}

// Run runs one claimed job. Terminal mapping:
//
//	exit 0 with clean stats (stats present)  → succeeded
//	exit 0/2/3/4 with failed or unreachable hosts → failed, with stats
//	any other exit code                     → error
//	a run the context cancelled             → cancelled
//
// A run whose process ends without stats is an error — the manager's run
// view is built on the stats, and a stats-less success would lie.
func (r *Runner) Run(ctx context.Context, job *workerpb.Job, env worker.RunEnv, sink func(*workerpb.RunEvent) error) (worker.Status, map[string]any, error) {
	switch job.GetKind() {
	case kindRun, kindAdhoc, kindBootstrap, kindRotateKey:
	default:
		return worker.StatusError, nil, fmt.Errorf("job kind %q has no ansible-runner executor", job.GetKind())
	}

	bin := r.RunnerBin
	if bin == "" {
		bin = env.Config.AnsibleRunnerBin
	}
	ansibleBin := r.AnsibleBin
	if ansibleBin == "" {
		ansibleBin = env.Config.AnsibleBin
	}
	playbook, err := r.materializePlaybook(ctx, job, env)
	if err != nil {
		return worker.StatusError, nil, fmt.Errorf("materialize playbook: %w", err)
	}
	if err := r.materializeInventory(job, env); err != nil {
		return worker.StatusError, nil, fmt.Errorf("materialize inventory: %w", err)
	}
	if err := r.materializeRunnerConfig(job, env); err != nil {
		return worker.StatusError, nil, fmt.Errorf("materialize runner config: %w", err)
	}

	// The bookends are the runner's own events; everything between them is
	// ansible-runner's stream, forwarded in order.
	if err := sink(runEvent(evStateRunStart, "", "", runStartPayload(job.GetKind(), playbook))); err != nil {
		return worker.StatusError, nil, fmt.Errorf("send run start event: %w", err)
	}

	args := []string{"run", env.WorkDir, "--json",
		"--playbook", playbook,
		"--inventory", filepath.Join(env.WorkDir, inventoryWrapperName),
		"--ident", ident(job.GetId()),
	}
	if ansibleBin != "" {
		// An explicitly pinned ansible-playbook rides ansible-runner's
		// -b/--binary; an empty config lets the runner discover it via
		// PATH.
		args = append(args, "-b", ansibleBin)
	}
	if limit := job.GetLimit(); limit != "" {
		args = append(args, "--limit", limit)
	}

	cmd := exec.Command(bin, args...)
	cmd.Dir = env.WorkDir
	cmd.Env = r.subprocessEnv(env)
	// A private process group: the cancel path signals the runner and the
	// ansible-playbook child together, whatever ansible-runner forwards
	// itself.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return worker.StatusError, nil, fmt.Errorf("pipe runner stdout: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return worker.StatusError, nil, fmt.Errorf("start %s: %w", bin, err)
	}

	cancelledCh := make(chan struct{}, 1)
	grace := r.killGrace()
	// The cancel watcher: SIGTERM to the process group (the runner and its
	// ansible child together), then SIGKILL after the grace. The second
	// signal is best-effort — a runner that died on the first one is fine
	// (its pid is gone; the group kill is a no-op).
	go func() {
		<-ctx.Done()
		cancelledCh <- struct{}{}
		terminateGroup(cmd.Process.Pid, grace)
	}()

	stats, scanErr := r.streamEvents(ctx, stdout, sink)
	if scanErr != nil {
		// The event stream broke (the manager is gone): the run cannot be
		// observed anymore, so it must not keep going — stop the process
		// group immediately (the same TERM→KILL policy as the cancel path)
		// before waiting for it. The claim is at-least-once; the reaper
		// requeues what this worker never reported.
		terminateGroup(cmd.Process.Pid, grace)
		_ = cmd.Wait()
		return worker.StatusError, nil, fmt.Errorf("stream runner events: %w", scanErr)
	}
	waitErr := cmd.Wait()

	select {
	case <-cancelledCh:
		if stats != nil {
			return worker.StatusCancelled, buildStats(stats, job.GetConnections()), context.Canceled
		}
		return worker.StatusCancelled, nil, context.Canceled
	default:
	}

	status, statsMap, errText := summarize(cmd.ProcessState.ExitCode(), stats, waitErr, stderr.String(), job.GetConnections())
	if errText != "" {
		// The failure detail belongs to the result (the manager stores it
		// on the job row for the run view); the log names the outcome only.
		r.log().WarnContext(ctx, "runner run failed", "job_id", job.GetId(), "status", string(status))
	}
	// The run_end bookend carries the final status for the run view.
	if err := sink(runEvent(evStateRunEnd, "", "", runEndPayload(status))); err != nil {
		return worker.StatusError, nil, fmt.Errorf("send run end event: %w", err)
	}
	return status, statsMap, nil
}

// streamEvents forwards the runner's stdout event stream: each parsed event
// through the sink, the stdout follow-ups where the callback captured
// display output, and the last stats event's recap out. A malformed line is
// a warning, never fatal; a sink error (the stream is gone) aborts the scan
// and stops the run.
func (r *Runner) streamEvents(ctx context.Context, stdout io.Reader, sink func(*workerpb.RunEvent) error) (map[string]hostStat, error) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), 16<<20) // verbose results are big
	var lastStats map[string]hostStat
	for scanner.Scan() {
		ev, ok, err := ParseEvent(scanner.Bytes())
		if err != nil {
			// Malformed line: skip with a warning. The line's content is
			// never logged — a broken line can hold anything.
			r.log().WarnContext(ctx, "skipping malformed runner event", "err", err)
			continue
		}
		if ev.Stats != nil {
			lastStats = ev.Stats
		}
		// The primary event first (mapped events), then the stdout
		// follow-up; banner events (the runner's internal ones) carry
		// nothing but their stdout.
		if ok && ev.State != "" {
			if err := sink(runEvent(ev.State, ev.Host, ev.Task, ev.Payload)); err != nil {
				return lastStats, err
			}
		}
		if ev.Stdout != "" {
			// The display text the callback captured: forwarded as the
			// run's stdout event, the payload is the raw event line.
			if err := sink(runEvent(evStateStdout, "", "", ev.Payload)); err != nil {
				return lastStats, err
			}
		}
	}
	return lastStats, scanner.Err()
}

// subprocessEnv builds the runner subprocess's private environment: the
// worker's own environment minus every ANSIBLE_* and PULSIX_* variable (the
// worker's own settings must never leak into a run), plus the fixed private
// set. HOME points into the work dir so ansible's scratch files (connection
// sockets, the fact cache) land in the dir the loop wipes.
func (r *Runner) subprocessEnv(env worker.RunEnv) []string {
	out := stripPrivateEnv(os.Environ())
	return append(out,
		// Host key checking stays on: pinned hosts are checked strictly
		// against the run's known_hosts, everything else is trusted on
		// first use inside it (see pinHosts and sshConfigText).
		"ANSIBLE_HOST_KEY_CHECKING=True",
		"ANSIBLE_RETRY_FILES_ENABLED=False",
		"ANSIBLE_SSH_ARGS="+sshArgs(env.WorkDir),
		"ANSIBLE_CONFIG="+filepath.Join(env.WorkDir, ansibleCfgName),
		"ANSIBLE_LOCAL_TEMP="+filepath.Join(env.WorkDir, localTmpName),
		"HOME="+filepath.Join(env.WorkDir, homeDirName),
	)
}

// log returns the runner's logger, defaulting to slog's.
func (r *Runner) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// ident is the ansible-runner artifacts identifier: the job id's short
// prefix — every run has a fresh work dir, so artifacts never collide.
func ident(jobID string) string {
	if len(jobID) > 8 {
		return jobID[:8]
	}
	return jobID
}

// killGrace returns the effective SIGTERM-to-SIGKILL grace.
func (r *Runner) killGrace() time.Duration {
	if r.KillGrace > 0 {
		return r.KillGrace
	}
	return defaultKillGrace
}

// terminateGroup stops a run's process group: SIGTERM first, SIGKILL after
// the grace — a runner that exits on the TERM is left alone (its group is
// gone; the liveness check spares the wait), one that ignores it is killed
// bounded. The cancel path and a broken event stream share the policy.
func terminateGroup(pid int, grace time.Duration) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pid, 0); err != nil {
			return // the group is gone; the TERM did its job
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// summarize maps the process exit onto the loop's terminal statuses.
func summarize(rc int, stats map[string]hostStat, waitErr error, stderr string, connections map[string]string) (worker.Status, map[string]any, string) {
	if rc < 0 {
		return worker.StatusError, nil, fmt.Sprintf("ansible-runner terminated by signal (%v)", waitErr)
	}
	switch rc {
	// 0 ok, 2 host failures (RUN_FAILED_HOSTS), 4 unreachable hosts
	// (RUN_UNREACHABLE_HOSTS — e.g. a host whose pinned key no longer
	// matches); 3 is kept for runner versions that report unreachable so.
	case 0, 2, 3, 4:
		if stats == nil {
			// No recap: the manager's run view would be empty. Fall back to
			// the artifacts stats before giving up.
			return worker.StatusError, nil, fmt.Sprintf("ansible-runner exited with code %d but reported no run stats", rc)
		}
		out := buildStats(stats, connections)
		if failedOrUnreachable(stats) || rc != 0 {
			return worker.StatusFailed, out, ""
		}
		return worker.StatusSucceeded, out, ""
	default:
		// Anything else (1: ansible error, 5: bad options, 250: internal
		// error, ...) is an infrastructure failure of the run itself.
		return worker.StatusError, nil, fmt.Sprintf("ansible-runner exited with code %d", rc)
	}
}

// failedOrUnreachable reports whether the recap carries host failures.
func failedOrUnreachable(stats map[string]hostStat) bool {
	for _, s := range stats {
		if s.Failed > 0 || s.Unreachable > 0 {
			return true
		}
	}
	return false
}

// runStartPayload is the run_start bookend's payload.
func runStartPayload(kind, playbook string) string {
	return fmt.Sprintf(`{"kind":%q,"playbook":%q}`, kind, filepath.Base(playbook))
}

// runEndPayload is the run_end bookend's payload.
func runEndPayload(status worker.Status) string {
	return fmt.Sprintf(`{"status":%q}`, string(status))
}

// runEvent builds one forwarded event: the loop stamps job id, seq and
// timestamp (and never mutates this one — Task 8's clone contract).
func runEvent(state, host, task, payload string) *workerpb.RunEvent {
	return &workerpb.RunEvent{State: state, Host: host, Task: task, Payload: payload}
}

// stripPrivateEnv drops every ANSIBLE_* and PULSIX_* variable.
func stripPrivateEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "ANSIBLE_") || strings.HasPrefix(name, "PULSIX_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}
