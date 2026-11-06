// Galaxy: the requirements.Galaxy that runs ansible-galaxy the way plays
// run (spec 2026-11-06-requirements-cache, "Owner decision 2026-11-06:
// galaxy runs under the play uid"). Galaxy writes where the requirements'
// metadata tells it to, not only below -p, so under a play uid it runs as
// that uid, through the setuid helper and under the play supervisor, and
// can write only into the four 2770 directories of its temp entry
// (prepareInstall). The worker takes the result over afterwards.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/noixdev/pulsix/internal/worker"
	"github.com/noixdev/pulsix/internal/worker/requirements"
)

// envAnsibleBin is PULSIX_ANSIBLE_BIN: ansible-galaxy is found next to it.
const envAnsibleBin = "PULSIX_ANSIBLE_BIN"

const (
	// galaxyWaitDelay bounds the wait for galaxy's pipes after it exited
	// or was stopped (a git child holding them).
	galaxyWaitDelay = 5 * time.Second
	// galaxyStderrTail is how much of a failed command's stderr its
	// ExitError keeps.
	galaxyStderrTail = 16 << 10
	// galaxyRemoveTimeout bounds RemoveAll's removal as the play uid.
	galaxyRemoveTimeout = 10 * time.Second
)

// Galaxy runs ansible-galaxy for the requirements service.
type Galaxy struct {
	Runner *Runner       // kill grace, supervisor backstop, logger, stopGroup
	Config worker.Config // PlayUID, SetuidHelper, PlaySupervisor, AnsibleBin
}

// NewRequirements returns the requirements service that installs through
// r and cfg's ansible-galaxy; its cleanup warnings go to r's logger.
func NewRequirements(r *Runner, cfg worker.Config) *requirements.Service {
	return requirements.NewService(&Galaxy{Runner: r, Config: cfg}, requirements.SystemClock{}, 0, requirements.WithLogger(r.log()))
}

// galaxyBin is the ansible-galaxy next to ansibleBin when that is a path,
// so a sync and a run install with the Ansible they run; else the one on
// PATH.
func galaxyBin(ansibleBin string) string {
	if strings.ContainsRune(ansibleBin, os.PathSeparator) {
		return filepath.Join(filepath.Dir(ansibleBin), "ansible-galaxy")
	}
	return "ansible-galaxy"
}

// Install runs one ansible-galaxy command and sends every line of its
// stdout and stderr to line, all of them before it returns. Under a play
// uid it never runs galaxy as the worker: a missing supervisor, a missing
// helper or a refusal fails it. Without one it runs as the worker and logs
// that it is not isolated.
//
// An install as the play uid is safe only while no play runs as that uid
// at the same time: a play could write into the temp entry's 2770
// directories, and the take-over cannot tell. The worker loop runs one job
// at a time (loop.go, PULSIX_MAX_CONCURRENT above 1 only warns), so none
// does today; real concurrency must serialize installs against plays.
func (g *Galaxy) Install(ctx context.Context, in requirements.Install, line func(string)) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("ansible-galaxy %s install not started: %w", in.Kind, err)
	}
	if g.Config.PlayUID != 0 && g.Config.PlaySupervisor == "" {
		return errors.New("ansible-galaxy cannot be isolated: " + envPlayUID + " is set, but the play supervisor is not available on this system")
	}
	if _, err := worker.RunGitKnownHosts(in.Entry); err != nil {
		return err
	}
	if g.Config.PlayUID != 0 {
		if err := prepareInstall(in); err != nil {
			return err
		}
	} else {
		g.Runner.log().Warn("ansible-galaxy is not isolated: "+envPlayUID+" is not set, so it runs as the worker's own uid", "file", in.File)
	}
	cmd, err := g.command(in)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	send := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		line(s)
	}
	stdout, stderr := &lineWriter{send: send}, &lineWriter{send: send, keep: galaxyStderrTail}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = galaxyWaitDelay

	if err := cmd.Start(); err != nil {
		if uid := g.Config.PlayUID; uid != 0 {
			return fmt.Errorf("start %s through the setuid helper %s (%s=%d, %s): the helper must exist and be setuid-root: %w",
				galaxyBin(g.Config.AnsibleBin), cmd.Path, envPlayUID, uid, envSetuidHelper, err)
		}
		return fmt.Errorf("start %s: %w", cmd.Path, err)
	}
	var waitErr error
	exited := make(chan struct{})
	go func() { waitErr = cmd.Wait(); close(exited) }()
	stopped := false
	select {
	case <-exited:
	case <-ctx.Done():
		stopped = true
		g.Runner.stopGroup(worker.RunEnv{Config: g.Config}, "ansible-galaxy", cmd.Process.Pid, exited)
		<-exited
	}
	// Wait has returned, so both copies are done: nothing writes any more.
	stdout.flush()
	stderr.flush()

	var exit *exec.ExitError
	switch {
	case stopped:
		return fmt.Errorf("ansible-galaxy %s install stopped: %w", in.Kind, ctx.Err())
	case waitErr == nil:
		return nil
	case errors.As(waitErr, &exit) && exit.Exited():
		return &requirements.ExitError{Code: exit.ExitCode(), Stderr: string(stderr.kept)}
	}
	return fmt.Errorf("ansible-galaxy %s install: %w", in.Kind, waitErr)
}

// command builds the ansible-galaxy command of in: its argv, its working
// directory (the file's own directory in the clone: galaxy resolves
// relative sources against it) and its environment, under the setuid
// helper and the supervisor as Config says (newPlayCmd).
func (g *Galaxy) command(in requirements.Install) (*exec.Cmd, error) {
	var target string
	switch in.Kind {
	case requirements.KindRole:
		target = "roles"
	case requirements.KindCollection:
		target = "collections"
	default:
		return nil, fmt.Errorf("ansible-galaxy has no install kind %q", in.Kind)
	}
	bin := galaxyBin(g.Config.AnsibleBin)
	if strings.ContainsRune(bin, os.PathSeparator) && !filepath.IsAbs(bin) {
		// exec would resolve a relative path against cmd.Dir, the clone.
		abs, err := filepath.Abs(bin)
		if err != nil {
			return nil, fmt.Errorf("resolve the binary %q (%s) to an absolute path: %w", bin, envAnsibleBin, err)
		}
		bin = abs
	}
	args := []string{string(in.Kind), "install", "-r", in.File, "-p", filepath.Join(in.Entry, target)}
	return newPlayCmd(bin, envAnsibleBin, args, g.Config, in.Dir, galaxyEnv(in, os.Environ()), g.Runner.killGrace())
}

// galaxyEnv is galaxy's environment: the allowlisted part of the worker's
// own (galaxyEnvAllowed; never its settings or secrets) and the fixed set.
// HOME and TMPDIR are inside the temp entry, so galaxy's scratch goes with
// it; the generated config is read instead of the repository's; the
// collections already installed elsewhere are not seen, so every listed
// collection lands in the entry; git reads no system or global config,
// never prompts, and speaks no local (file) or ext protocol.
func galaxyEnv(in requirements.Install, environ []string) []string {
	out := make([]string, 0, len(environ)+11)
	for _, kv := range environ {
		if name, _, _ := strings.Cut(kv, "="); galaxyEnvAllowed(name) {
			out = append(out, kv)
		}
	}
	knownHosts := filepath.Join(in.Entry, worker.RunHomeDirName, ".ssh", "known_hosts")
	return append(out,
		"HOME="+filepath.Join(in.Entry, worker.RunHomeDirName),
		"TMPDIR="+filepath.Join(in.Entry, "tmp"),
		"ANSIBLE_CONFIG="+filepath.Join(in.Entry, "input", "galaxy.cfg"),
		"ANSIBLE_COLLECTIONS_PATH="+filepath.Join(in.Entry, "collections"),
		"ANSIBLE_COLLECTIONS_SCAN_SYS_PATH=false",
		"GIT_SSH_COMMAND="+worker.GitSSHCommand("", knownHosts),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL=https:http:ssh:git",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
	)
}

// galaxyEnvAllowed reports whether galaxy inherits the worker's variable
// name: the program search path, the locale and time zone, the CA bundle
// and the proxy, which reaching a galaxy server needs.
func galaxyEnvAllowed(name string) bool {
	switch name {
	case "PATH", "LANG", "LANGUAGE", "TZ",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "REQUESTS_CA_BUNDLE",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy":
		return true
	}
	return strings.HasPrefix(name, "LC_")
}

// RemoveAll removes dir. What the play uid wrote there may not be
// removable by the worker; then, under a play uid, it is cleared as the
// play uid (only logged when that fails) and the worker removes dir again.
func (g *Galaxy) RemoveAll(dir string) error {
	err := os.RemoveAll(dir)
	if err == nil || g.Config.PlayUID == 0 {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), galaxyRemoveTimeout)
	defer cancel()
	if perr := worker.RemoveAsPlayUID(ctx, g.Config, dir); perr != nil {
		g.Runner.log().Warn("could not clear the play uid's files from a requirements install", "dir", dir, "err", perr)
	}
	return os.RemoveAll(dir)
}

// maxGalaxyLine bounds a line held without its newline; longer output is
// sent in pieces of this size.
const maxGalaxyLine = 64 << 10

// lineWriter sends what one stream writes, a line at a time, and keeps the
// last keep bytes of it. Each stream has its own writer (exec copies each
// from one goroutine); send is shared and serialized by the caller.
type lineWriter struct {
	send func(string)
	keep int
	buf  []byte
	kept []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	if w.keep > 0 {
		w.kept = append(w.kept, p...)
		if len(w.kept) > w.keep {
			w.kept = append(w.kept[:0], w.kept[len(w.kept)-w.keep:]...)
		}
	}
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.send(strings.TrimSuffix(string(w.buf[:i]), "\r"))
		w.buf = w.buf[i+1:]
	}
	for len(w.buf) >= maxGalaxyLine {
		w.send(string(w.buf[:maxGalaxyLine]))
		w.buf = w.buf[maxGalaxyLine:]
	}
	return len(p), nil
}

// flush sends a last line without its newline.
func (w *lineWriter) flush() {
	if len(w.buf) > 0 {
		w.send(strings.TrimSuffix(string(w.buf), "\r"))
		w.buf = nil
	}
}
