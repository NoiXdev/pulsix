// runner.Galaxy against fake ansible-galaxy and setuid helper scripts: the
// command line, the environment, the refusals under a play uid, the output
// and the stop on cancel. The kernel half (a real uid drop) is tested in the
// worker image (internal/e2e/uidboundary).
package runner

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/worker"
	"github.com/noixdev/pulsix/internal/worker/requirements"
	"github.com/noixdev/pulsix/internal/worker/supervisor"
)

// galaxyInstall lays out what Ensure hands Install: a clone under
// <cache>/org/repo with the requirements file's directory sub/, and a temp
// entry as Ensure's prepare leaves it (directories 0700, files 0600).
func galaxyInstall(t *testing.T, kind requirements.Kind) requirements.Install {
	t.Helper()
	cache := t.TempDir()
	clone := filepath.Join(cache, "org", "repo")
	entry := filepath.Join(clone+".requirements", ".tmp-x")
	for _, d := range []string{cache, filepath.Join(cache, "org"), clone, filepath.Join(clone, "sub"), clone + ".requirements", entry} {
		mkdirMode(t, d, 0o700)
	}
	writeMode(t, filepath.Join(clone, "sub", "requirements.yml"), "roles: []\n", 0o600)
	for _, d := range []string{"input", "home", "tmp", "roles", "collections"} {
		mkdirMode(t, filepath.Join(entry, d), 0o700)
	}
	writeMode(t, filepath.Join(entry, "input", "1.yml"), "roles: []\n", 0o600)
	writeMode(t, filepath.Join(entry, "input", "galaxy.cfg"), "[defaults]\n", 0o600)
	return requirements.Install{
		Kind:         kind,
		File:         filepath.Join(entry, "input", "1.yml"),
		Dir:          filepath.Join(clone, "sub"),
		Entry:        entry,
		CloneDir:     clone,
		RepoCacheDir: cache,
	}
}

func mkdirMode(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(dir, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
}

func writeMode(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// fakeGalaxy writes <bin>/ansible-galaxy running body, and returns the
// ansible-playbook path next to it (Config.AnsibleBin) and the directory
// its dumps land in: args (one per line), pwd (physical) and env.
func fakeGalaxy(t *testing.T, body string) (ansibleBin, out string) {
	t.Helper()
	bin, out := t.TempDir(), t.TempDir()
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + shQuote(filepath.Join(out, "args")) + "\n" +
		"pwd -P > " + shQuote(filepath.Join(out, "pwd")) + "\n" +
		"env > " + shQuote(filepath.Join(out, "env")) + "\n" +
		body + "\n"
	writeMode(t, filepath.Join(bin, "ansible-galaxy"), script, 0o700)
	return filepath.Join(bin, "ansible-playbook"), out
}

// testGalaxy is a Galaxy whose runner logs into logs.
func testGalaxy(cfg worker.Config, logs *bytes.Buffer) *Galaxy {
	return &Galaxy{
		Runner: &Runner{KillGrace: 200 * time.Millisecond, Log: slog.New(slog.NewTextHandler(logs, nil))},
		Config: cfg,
	}
}

// collect gathers Install's lines.
type collect struct{ lines []string }

func (c *collect) line(s string) { c.lines = append(c.lines, s) }

func TestGalaxyBin(t *testing.T) {
	for in, want := range map[string]string{
		"/opt/x/bin/ansible-playbook": "/opt/x/bin/ansible-galaxy",
		"ansible-playbook":            "ansible-galaxy",
		"":                            "ansible-galaxy",
	} {
		if got := galaxyBin(in); got != want {
			t.Errorf("galaxyBin(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGalaxyCommandLine(t *testing.T) {
	for kind, dir := range map[requirements.Kind]string{requirements.KindRole: "roles", requirements.KindCollection: "collections"} {
		t.Run(string(kind), func(t *testing.T) {
			ansibleBin, out := fakeGalaxy(t, "exit 0")
			in := galaxyInstall(t, kind)
			var logs bytes.Buffer
			if err := testGalaxy(worker.Config{AnsibleBin: ansibleBin}, &logs).Install(context.Background(), in, func(string) {}); err != nil {
				t.Fatalf("Install: %v", err)
			}
			want := []string{string(kind), "install", "-r", in.File, "-p", filepath.Join(in.Entry, dir)}
			if got := strings.Fields(string(mustRead(t, filepath.Join(out, "args")))); !slices.Equal(got, want) {
				t.Errorf("argv = %q, want %q", got, want)
			}
			wantDir, err := filepath.EvalSymlinks(in.Dir)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(mustRead(t, filepath.Join(out, "pwd")))); got != wantDir {
				t.Errorf("working directory = %q, want the file's directory in the clone %q (galaxy resolves relative sources against it)", got, wantDir)
			}
		})
	}
}

// envOf parses an env dump into a map.
func envOf(t *testing.T, path string) map[string]string {
	t.Helper()
	env := map[string]string{}
	for _, kv := range strings.Split(string(mustRead(t, path)), "\n") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}

func TestGalaxyEnvironment(t *testing.T) {
	t.Setenv("ANSIBLE_COLLECTIONS_PATH", "/usr/share/x")
	t.Setenv("ANSIBLE_STRAY", "1")
	t.Setenv("PULSIX_STRAY", "1")
	t.Setenv("STRAY_SECRET", "1")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("PYTHONPATH", "/stray")
	t.Setenv("HTTPS_PROXY", "http://proxy:3128")
	ansibleBin, out := fakeGalaxy(t, "exit 0")
	in := galaxyInstall(t, requirements.KindCollection)
	var logs bytes.Buffer
	if err := testGalaxy(worker.Config{AnsibleBin: ansibleBin}, &logs).Install(context.Background(), in, func(string) {}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	env := envOf(t, filepath.Join(out, "env"))
	for k, want := range map[string]string{
		"HOME":                              filepath.Join(in.Entry, "home"),
		"TMPDIR":                            filepath.Join(in.Entry, "tmp"),
		"ANSIBLE_CONFIG":                    filepath.Join(in.Entry, "input", "galaxy.cfg"),
		"ANSIBLE_COLLECTIONS_PATH":          filepath.Join(in.Entry, "collections"),
		"ANSIBLE_COLLECTIONS_SCAN_SYS_PATH": "false",
		"GIT_ALLOW_PROTOCOL":                "https:http:ssh:git",
		"GIT_TERMINAL_PROMPT":               "0",
		"GIT_CONFIG_NOSYSTEM":               "1",
		"GIT_CONFIG_GLOBAL":                 os.DevNull,
		"PYTHONNOUSERSITE":                  "1",
		"PYTHONDONTWRITEBYTECODE":           "1",
		"PATH":                              os.Getenv("PATH"),
		"HTTPS_PROXY":                       "http://proxy:3128",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	knownHosts := "UserKnownHostsFile='" + filepath.Join(in.Entry, "home", ".ssh", "known_hosts") + "'"
	if !strings.Contains(env["GIT_SSH_COMMAND"], knownHosts) {
		t.Errorf("GIT_SSH_COMMAND = %q, want it to contain %s", env["GIT_SSH_COMMAND"], knownHosts)
	}
	for _, k := range []string{"ANSIBLE_STRAY", "PULSIX_STRAY", "STRAY_SECRET", "GIT_CONFIG_COUNT", "PYTHONPATH"} {
		if v, ok := env[k]; ok {
			t.Errorf("%s=%q reached galaxy; only the allowlisted environment may", k, v)
		}
	}
	if info, err := os.Stat(filepath.Join(in.Entry, "home", ".ssh")); err != nil || !info.IsDir() {
		t.Errorf("home/.ssh: %v, want a directory for ssh's known_hosts", err)
	}
}

const notIsolated = "ansible-galaxy is not isolated: PULSIX_WORKER_PLAY_UID is not set, so it runs as the worker's own uid"

func TestGalaxyWarnsWhenNotIsolated(t *testing.T) {
	ansibleBin, _ := fakeGalaxy(t, "exit 0")
	in := galaxyInstall(t, requirements.KindRole)
	var logs bytes.Buffer
	if err := testGalaxy(worker.Config{AnsibleBin: ansibleBin}, &logs).Install(context.Background(), in, func(string) {}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), notIsolated) || !strings.Contains(logs.String(), in.File) {
		t.Errorf("log = %q, want one warning %q naming %s", logs.String(), notIsolated, in.File)
	}

	logs.Reset()
	helper, calls, _ := writeRecordingSetuidHelper(t)
	cfg := worker.Config{PlayUID: 65533, SetuidHelper: helper, PlaySupervisor: "/sup", AnsibleBin: ansibleBin}
	if err := testGalaxy(cfg, &logs).Install(context.Background(), galaxyInstall(t, requirements.KindRole), func(string) {}); err != nil {
		t.Fatalf("Install under the play uid: %v", err)
	}
	if strings.Contains(logs.String(), notIsolated) {
		t.Errorf("an install under the play uid must not warn: %q", logs.String())
	}
	if len(recordedCalls(t, calls)) != 1 {
		t.Errorf("the helper ran %d times, want once", len(recordedCalls(t, calls)))
	}
}

func TestGalaxyCommandUnderThePlayUID(t *testing.T) {
	ansibleBin, _ := fakeGalaxy(t, "exit 0")
	in := galaxyInstall(t, requirements.KindRole)
	var logs bytes.Buffer
	g := testGalaxy(worker.Config{PlayUID: 65533, SetuidHelper: "/helper", PlaySupervisor: "/sup", AnsibleBin: ansibleBin}, &logs)
	cmd, err := g.command(in)
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	galaxy := filepath.Join(filepath.Dir(ansibleBin), "ansible-galaxy")
	want := append([]string{"/helper", "65533", "/sup"},
		supervisor.Command(200*time.Millisecond, galaxy, []string{"role", "install", "-r", in.File, "-p", filepath.Join(in.Entry, "roles")})...)
	if cmd.Path != "/helper" || !slices.Equal(cmd.Args, want) {
		t.Errorf("command = %q %q, want /helper %q", cmd.Path, cmd.Args, want)
	}
	if cmd.Dir != in.Dir {
		t.Errorf("Dir = %q, want %q", cmd.Dir, in.Dir)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Error("Setpgid = false, want the command in a process group of its own")
	}
}

func TestGalaxyRefusesThePlayUIDWithoutTheSupervisor(t *testing.T) {
	ansibleBin, out := fakeGalaxy(t, "exit 0")
	helper, calls, _ := writeRecordingSetuidHelper(t)
	var logs bytes.Buffer
	var got collect
	err := testGalaxy(worker.Config{PlayUID: 65533, SetuidHelper: helper, AnsibleBin: ansibleBin}, &logs).
		Install(context.Background(), galaxyInstall(t, requirements.KindRole), got.line)
	const want = "ansible-galaxy cannot be isolated: PULSIX_WORKER_PLAY_UID is set, but the play supervisor is not available on this system"
	if err == nil || err.Error() != want {
		t.Fatalf("Install error = %v, want %q", err, want)
	}
	if c := recordedCalls(t, calls); len(c) != 0 {
		t.Errorf("the helper ran: %q", c)
	}
	if _, err := os.Stat(filepath.Join(out, "args")); err == nil {
		t.Error("galaxy ran without isolation")
	}
	if len(got.lines) != 0 {
		t.Errorf("lines = %q, want none", got.lines)
	}
}

func TestGalaxyNamesAHelperThatDoesNotStart(t *testing.T) {
	ansibleBin, _ := fakeGalaxy(t, "exit 0")
	missing := filepath.Join(t.TempDir(), "no-such-helper")
	var logs bytes.Buffer
	err := testGalaxy(worker.Config{PlayUID: 65533, SetuidHelper: missing, PlaySupervisor: "/sup", AnsibleBin: ansibleBin}, &logs).
		Install(context.Background(), galaxyInstall(t, requirements.KindRole), func(string) {})
	if err == nil {
		t.Fatal("Install succeeded without a helper")
	}
	for _, want := range []string{missing, "PULSIX_WORKER_PLAY_UID=65533", "PULSIX_WORKER_SETUID_HELPER"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestGalaxyReportsAHelperRefusal(t *testing.T) {
	ansibleBin, _ := fakeGalaxy(t, "exit 0")
	const refusal = "pulsix-setuid-exec: not setuid-root (euid 501); refusing to exec without dropping privilege"
	helper := filepath.Join(t.TempDir(), "refusing-helper")
	writeMode(t, helper, "#!/bin/sh\necho "+shQuote(refusal)+" >&2\nexit 1\n", 0o700)
	var logs bytes.Buffer
	err := testGalaxy(worker.Config{PlayUID: 65533, SetuidHelper: helper, PlaySupervisor: "/sup", AnsibleBin: ansibleBin}, &logs).
		Install(context.Background(), galaxyInstall(t, requirements.KindRole), func(string) {})
	var exit *requirements.ExitError
	if !errors.As(err, &exit) || exit.Code != 1 || !strings.Contains(exit.Stderr, refusal) {
		t.Fatalf("Install error = %#v, want an ExitError with code 1 and the helper's refusal", err)
	}
}

func TestGalaxyStreamsItsOutput(t *testing.T) {
	ansibleBin, _ := fakeGalaxy(t, "echo a; echo b >&2; echo c")
	var logs bytes.Buffer
	var got collect
	if err := testGalaxy(worker.Config{AnsibleBin: ansibleBin}, &logs).Install(context.Background(), galaxyInstall(t, requirements.KindRole), got.line); err != nil {
		t.Fatalf("Install: %v", err)
	}
	ia, ib, ic := slices.Index(got.lines, "a"), slices.Index(got.lines, "b"), slices.Index(got.lines, "c")
	if ia < 0 || ib < 0 || ic < 0 || ia > ic || len(got.lines) != 3 {
		t.Errorf("lines = %q, want a, b and c, a before c", got.lines)
	}
}

func TestGalaxyExitError(t *testing.T) {
	ansibleBin, _ := fakeGalaxy(t, "echo boom >&2; exit 3")
	var logs bytes.Buffer
	err := testGalaxy(worker.Config{AnsibleBin: ansibleBin}, &logs).Install(context.Background(), galaxyInstall(t, requirements.KindRole), func(string) {})
	var exit *requirements.ExitError
	if !errors.As(err, &exit) || exit.Code != 3 || !strings.Contains(exit.Stderr, "boom") {
		t.Fatalf("Install error = %#v, want ExitError{3, boom}", err)
	}
}

func TestGalaxyStopsItsProcessGroupOnCancel(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	ansibleBin, _ := fakeGalaxy(t, "sleep 300 & echo $! > "+shQuote(pidFile)+"; wait")
	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- testGalaxy(worker.Config{AnsibleBin: ansibleBin}, &logs).Install(ctx, galaxyInstall(t, requirements.KindRole), func(string) {})
	}()
	var pid int
	for deadline := time.Now().Add(5 * time.Second); pid == 0; {
		if raw, err := os.ReadFile(pidFile); err == nil && strings.HasSuffix(string(raw), "\n") {
			pid = atoi(t, strings.TrimSpace(string(raw)))
		} else if time.Now().After(deadline) {
			t.Fatal("the fake galaxy never wrote its child's pid")
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Install error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Install did not return within 5 s of the cancel")
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("galaxy's child %d survived the cancel", pid)
		}
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
