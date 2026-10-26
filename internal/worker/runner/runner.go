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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	kindRevokeKey = "revoke_key"
)

// The runner's own bookend events; the state values are the manager's
// RunEvent set.
const (
	evStateRunStart = "run_start"
	evStateRunEnd   = "run_end"
	evStateStdout   = "stdout"
)

// envPlayUID is PULSIX_WORKER_PLAY_UID, envSetuidHelper is
// PULSIX_WORKER_SETUID_HELPER and envRunnerBin is
// PULSIX_ANSIBLE_RUNNER_BIN. worker.Config keeps its own copies of these
// names unexported, so they are duplicated here: a refused spawn (see
// newRunnerCmd's caller) must name the variable in its error, not just
// read env.Config, or an operator is left with a bare "no such file or
// directory" and no idea which setting to revisit.
const (
	envPlayUID      = "PULSIX_WORKER_PLAY_UID"
	envSetuidHelper = "PULSIX_WORKER_SETUID_HELPER"
	envRunnerBin    = "PULSIX_ANSIBLE_RUNNER_BIN"
)

// defaultSetuidHelper mirrors worker.Load's default for
// PULSIX_WORKER_SETUID_HELPER. Load always fills Config.SetuidHelper (and
// refuses a non-absolute one), so this only catches a Config assembled by
// hand (tests, an embedded worker) and keeps an empty string from becoming
// an exec of "".
const defaultSetuidHelper = "/usr/local/bin/pulsix-setuid-exec"

// helperErrPrefix is how deploy/worker/setuid-exec.c introduces every one
// of its refusals on stderr. Only a MISSING helper fails at cmd.Start();
// every other refusal — uid drift, a lost setuid bit, a drop that did not
// take — starts fine and exits 1, where summarize would otherwise render
// the useless "ansible-runner exited with code 1" and throw the helper's
// own words away.
const helperErrPrefix = "pulsix-setuid-exec:"

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
	case kindRun, kindAdhoc, kindBootstrap, kindRotateKey, kindRevokeKey:
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
	if env.Config.PlayUID != 0 {
		// Every run file is now in place (inventory, playbook, ansible.cfg,
		// ssh_config, vault passwords); ShareWorkDir must run after all of
		// them and before the spawn below, or whatever lands outside this
		// window stays owner-only and the play — about to start under its
		// own uid — cannot reach it.
		if err := ShareWorkDir(env.WorkDir); err != nil {
			return worker.StatusError, nil, fmt.Errorf("share work dir with play uid: %w", err)
		}
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
	if limit := job.GetLimit(); limit != "" {
		args = append(args, "--limit", limit)
	}

	cmd, err := newRunnerCmd(bin, args, env, r.subprocessEnv(env, ansibleBin))
	if err != nil {
		return worker.StatusError, nil, err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return worker.StatusError, nil, fmt.Errorf("pipe runner stdout: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		if uid := env.Config.PlayUID; uid != 0 {
			// There is no fallback: a worker configured to drop privilege
			// that silently ran the play as itself instead would defeat
			// the whole point, so a helper that will not start fails the
			// job. What fails here is the HELPER, not a setuid call, so
			// the error names it and both variables — an operator lands on
			// what to check instead of a bare "no such file or directory".
			return worker.StatusError, nil, fmt.Errorf("start %s through the setuid helper %s (%s=%d, %s): the helper must exist and be setuid-root: %w",
				bin, cmd.Path, envPlayUID, uid, envSetuidHelper, err)
		}
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

	stats, scanErr := r.streamEvents(ctx, stdout, sink, newRedactor(job))
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

	status, statsMap, errText := summarize(cmd.ProcessState.ExitCode(), stats, waitErr, stderr.String(), job.GetConnections(), env.Config.PlayUID)
	if errText != "" {
		// The failure detail belongs to the result (the manager stores it
		// on the job row for the run view); the log names the outcome only.
		r.log().WarnContext(ctx, "runner run failed", "job_id", job.GetId(), "status", string(status))
	}
	// The run_end bookend carries the final status for the run view.
	if err := sink(runEvent(evStateRunEnd, "", "", runEndPayload(status))); err != nil {
		return worker.StatusError, nil, fmt.Errorf("send run end event: %w", err)
	}
	if status == worker.StatusError && errText != "" {
		// An errored run reports why (exit code or signal — never output)
		// instead of an empty error on the job.
		return status, statsMap, errors.New(errText)
	}
	return status, statsMap, nil
}

// newRunnerCmd builds the ansible-runner subprocess's *exec.Cmd.
//
// With PlayUID set, the command becomes
//
//	<helper> <uid> <runner bin> <args...>
//
// and the drop happens inside the setuid-root helper
// (deploy/worker/setuid-exec.c), NOT here. SysProcAttr.Credential would be
// the shorter route, but it requires CAP_SETUID on the worker process, and
// a process holding that capability can setuid(0): granting it would hand a
// compromised worker a path to root it does not have today. The helper can
// only ever become the single uid compiled into it, which is why the uid is
// passed along — the helper refuses when it disagrees, so the worker's
// PULSIX_WORKER_PLAY_UID and the image's PLAY_UID cannot drift apart
// silently. The helper setgid()s to the WORKER's own gid, deliberately not
// a group derived from the play uid, because the worker must still be able
// to read whatever the play writes back into the work dir — that is what
// keeps the event stream working.
//
// The runner binary is resolved to an absolute path first. exec.Command
// would do that itself for the direct spawn, but the helper's argv[2] is
// handed to execv — deliberately not execvp, so that only argv decides
// which program runs — and execv does no PATH search at all. The default
// PULSIX_ANSIBLE_RUNNER_BIN is the bare name "ansible-runner", so without
// this every playbook job in the shipped image would die on ENOENT.
//
// Setpgid is unconditional regardless of PlayUID: the cancel path signals
// the whole process group and breaks without it. It is set on the helper,
// which execs (not forks) the runner, so the runner keeps the helper's pid
// and the new group — one process, not two.
func newRunnerCmd(bin string, args []string, env worker.RunEnv, subprocessEnv []string) (*exec.Cmd, error) {
	name, argv := bin, args
	if uid := env.Config.PlayUID; uid != 0 {
		abs, err := resolveRunnerBin(bin)
		if err != nil {
			return nil, err
		}
		name = env.Config.SetuidHelper
		if name == "" {
			name = defaultSetuidHelper
		}
		argv = append([]string{strconv.FormatUint(uint64(uid), 10), abs}, args...)
	}
	cmd := exec.Command(name, argv...)
	cmd.Dir = env.WorkDir
	cmd.Env = subprocessEnv
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd, nil
}

// resolveRunnerBin turns the configured ansible-runner binary into an
// absolute path for the setuid helper's execv. LookPath covers the bare
// name (the default) against the worker's own PATH; Abs covers a relative
// one, which execv would otherwise resolve against the subprocess's
// working directory — the run's work dir, where no binary of the
// operator's lives. A binary that cannot be found fails the job by name,
// naming the variable that chose it: ENOENT from inside a setuid helper is
// otherwise indistinguishable from the helper itself being absent.
func resolveRunnerBin(bin string) (string, error) {
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("find the ansible-runner binary %q (%s): %w", bin, envRunnerBin, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve the ansible-runner binary %q (%s) to an absolute path: %w", bin, envRunnerBin, err)
	}
	return abs, nil
}

// streamEvents forwards the runner's stdout event stream: each ansible
// event ONCE — a mapped event (task, ok, failed, ...) with its display text
// inside its own payload, a display-only event (a banner, the recap) as a
// stdout event carrying {"text": ...} — every payload redacted by red, and
// the last stats event's recap out. A malformed line is a warning, never
// fatal; a sink error (the stream is gone) aborts the scan and stops the
// run.
func (r *Runner) streamEvents(ctx context.Context, stdout io.Reader, sink func(*workerpb.RunEvent) error, red *redactor) (map[string]hostStat, error) {
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
		switch {
		case ok && ev.State != "":
			if err := sink(runEvent(ev.State, red.text(ev.Host), red.text(ev.Task), red.eventPayload(ev))); err != nil {
				return lastStats, err
			}
		case ev.Stdout != "":
			if err := sink(runEvent(evStateStdout, "", "", red.textPayload(ev.Stdout))); err != nil {
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
//
// A configured ansible-playbook (PULSIX_ANSIBLE_BIN) is found through PATH:
// its directory leads the run's PATH. It is deliberately NOT passed as
// ansible-runner's -b/--binary — ansible-runner 2.4.3 then invokes the
// binary without the playbook argument (the real-ansible E2E caught it).
func (r *Runner) subprocessEnv(env worker.RunEnv, ansibleBin string) []string {
	out := stripPrivateEnv(os.Environ())
	if dir := filepath.Dir(ansibleBin); ansibleBin != "" && strings.ContainsRune(ansibleBin, os.PathSeparator) {
		for i, kv := range out {
			if strings.HasPrefix(kv, "PATH=") {
				out[i] = "PATH=" + dir + string(os.PathListSeparator) + strings.TrimPrefix(kv, "PATH=")
			}
		}
	}
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
// playUID is the configured play uid (0 = the feature is off): when it is
// set, the process that ran was the setuid helper, and a refusal of its own
// has to reach the operator instead of the generic exit-code text.
func summarize(rc int, stats map[string]hostStat, waitErr error, stderr string, connections map[string]string, playUID uint32) (worker.Status, map[string]any, string) {
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
		if reason := setuidHelperFailure(stderr, playUID); reason != "" {
			return worker.StatusError, nil, reason
		}
		if reason := extraVarsLoadFailure(stderr); reason != "" {
			return worker.StatusError, nil, reason
		}
		return worker.StatusError, nil, fmt.Sprintf("ansible-runner exited with code %d", rc)
	}
}

// setuidHelperFailure recognises a refusal by the setuid helper and returns
// it as the run's error. Only a missing helper fails at cmd.Start() with a
// legible message; a helper that runs and refuses (a uid that disagrees
// with the one it was built for, a lost setuid bit, a drop the kernel did
// not make) exits 1 with its reason on stderr, which the generic branch
// below renders as "ansible-runner exited with code 1" — a message that
// sends an operator looking at Ansible for a problem in the image.
//
// The helper writes its line and never execs, so it is the whole of stderr
// when it speaks; anything else on stderr is the runner's and must keep
// taking the generic path, or a play that legitimately exits 1 would start
// reading as a privilege problem. The variable is named alongside, because
// not every refusal the helper can print names it itself.
func setuidHelperFailure(stderr string, playUID uint32) string {
	if playUID == 0 {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(stderr), "\n")
	if !strings.HasPrefix(line, helperErrPrefix) {
		return ""
	}
	return fmt.Sprintf("%s (%s=%d)", strings.TrimSpace(line), envPlayUID, playUID)
}

// extraVarsLoadFailure recognises the one failure the extra-vars guard
// is there to cause, and names it (backlog 2.71). ansible-core loads
// "-e @file" as JSON and, when that fails for any reason, silently
// re-reads the file as YAML — where every {"__ansible_unsafe": …}
// marker becomes a plain dict whose strings are templated. The guard
// (extraVarsGuardVar, inventory.go) is a key YAML also refuses, written
// as the document's FIRST member, so a failed JSON load fails the run
// instead of running a webhook's lookup.
//
// What reached the operator was "ansible-runner exited with code 1" —
// this branch dropped stderr — while the run log carried Ansible's own
// YAML complaint pointing at the guard's column, with the hint "Colons
// in unquoted values must be followed by a non-space character".
// Neither says what is actually wrong, and the hint sends the reader
// looking for a quoting mistake in a document they did not write.
//
// Matching on the guard's name is sound precisely because it is ours:
// it appears in the error only because it is the first thing on the
// line Ansible quotes, and it is in no playbook or inventory of the
// operator's own.
func extraVarsLoadFailure(stderr string) string {
	if !strings.Contains(stderr, extraVarsGuardVar) {
		return ""
	}
	return "the run's extra vars are not a document Ansible can load as JSON, so the run was stopped rather than run with the values re-read as YAML; check the template's extra vars for an object with a key starting with \"__ansible\" that is not exactly {\"__ansible_unsafe\": \"<text>\"} or {\"__ansible_vault\": \"<text>\"}"
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
