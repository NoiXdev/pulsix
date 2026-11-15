package zfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// helperEnv makes the test binary act as a stand-in zpool (TestMain), so
// the exec runner is tested against a real process without the real one.
const helperEnv = "PULSIX_ZFS_TEST_HELPER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		os.Exit(helperMain(mode))
	}
	os.Exit(m.Run())
}

// helperOutput is what mode "args" prints.
type helperOutput struct {
	Args  []string `json:"args"`
	LCAll string   `json:"lc_all"`
	Keep  string   `json:"keep"`
}

// helperMain is the stand-in. Modes: "args" prints its arguments and two
// environment variables as JSON; "exit:<n>" writes two stderr lines and
// exits with n; "flood" writes 2 MiB; "sleep:<ms>" sleeps, then prints ok.
func helperMain(mode string) int {
	kind, arg, _ := strings.Cut(mode, ":")
	switch kind {
	case "args":
		b, _ := json.Marshal(helperOutput{Args: os.Args[1:], LCAll: os.Getenv("LC_ALL"), Keep: os.Getenv("PULSIX_ZFS_KEEP")})
		_, _ = os.Stdout.Write(b)
		return 0
	case "exit":
		_, _ = os.Stdout.WriteString("partial")
		_, _ = fmt.Fprint(os.Stderr, "cannot open 'tank': no such pool\nsecond line\n")
		code, _ := strconv.Atoi(arg)
		return code
	case "flood":
		chunk := []byte(strings.Repeat("x", 64*1024))
		for range 32 {
			if _, err := os.Stdout.Write(chunk); err != nil {
				return 3
			}
		}
		return 0
	case "sleep":
		ms, _ := strconv.Atoi(arg)
		time.Sleep(time.Duration(ms) * time.Millisecond)
		_, _ = os.Stdout.WriteString("ok")
		return 0
	}
	return 99
}

func helperRunner(t *testing.T, mode string) execRunner {
	t.Helper()
	t.Setenv(helperEnv, mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return execRunner{find: func(string) (string, error) { return exe, nil }, busy: newInflight()}
}

// TestExecRunnerArgsAndEnvironment: no shell, every argument arrives whole;
// the agent's environment is passed on with LC_ALL=C over whatever it set.
func TestExecRunnerArgsAndEnvironment(t *testing.T) {
	r := helperRunner(t, "args")
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("PULSIX_ZFS_KEEP", "kept")
	args := []string{"get", "-Hp", "-o", "name,value", "creation", "my pool", `a"b;$(x)'c`}
	stdout, _, err := r.Run(context.Background(), "zfs", args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got helperOutput
	if err := json.Unmarshal(stdout, &got); err != nil {
		t.Fatalf("helper output %q: %v", stdout, err)
	}
	if !slices.Equal(got.Args, args) {
		t.Fatalf("argv = %q, want %q", got.Args, args)
	}
	if got.LCAll != "C" || got.Keep != "kept" {
		t.Fatalf("environment: LC_ALL=%q keep=%q", got.LCAll, got.Keep)
	}
}

// TestExecRunnerExitStatus: a non-zero exit is an exitStatus error, with
// stdout and stderr kept.
func TestExecRunnerExitStatus(t *testing.T) {
	r := helperRunner(t, "exit:2")
	stdout, stderr, err := r.Run(context.Background(), "zpool", []string{"status", "-p"})
	var st exitStatus
	if !errors.As(err, &st) || st != 2 {
		t.Fatalf("err = %v, want exit status 2", err)
	}
	if string(stdout) != "partial" || !strings.HasPrefix(string(stderr), "cannot open 'tank': no such pool\n") {
		t.Fatalf("stdout %q stderr %q", stdout, stderr)
	}
	if err.Error() != "exit status 2" {
		t.Fatalf("text = %q", err)
	}
}

func TestExecRunnerCapsOutput(t *testing.T) {
	r := helperRunner(t, "flood")
	if _, _, err := r.Run(context.Background(), "zpool", []string{"status", "-p"}); !errors.Is(err, errOutputTooLarge) {
		t.Fatalf("Run error = %v, want errOutputTooLarge", err)
	}
}

func TestExecRunnerKillsOnTimeout(t *testing.T) {
	r := helperRunner(t, "sleep:10000")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := r.Run(ctx, "zpool", []string{"status", "-p"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want the deadline", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Run took %v", d)
	}
}

// TestExecRunnerStuckProcess: a zpool the kill cannot stop at once (in the
// kernel, waiting on a suspended pool) does not hold Run past its deadline,
// and no second one is started with the same arguments until it exited.
func TestExecRunnerStuckProcess(t *testing.T) {
	r := helperRunner(t, "sleep:800")
	r.noKill = true
	r.reapGrace = 50 * time.Millisecond
	args := []string{"status", "-p"}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, _, err := r.Run(ctx, "zpool", args); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Run error = %v, want the deadline", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("first Run took %v", d)
	}
	if _, _, err := r.Run(context.Background(), "zpool", args); !errors.Is(err, errStillRunning) {
		t.Fatalf("second Run error = %v, want errStillRunning", err)
	}
	if _, _, err := r.Run(context.Background(), "zpool", []string{"list", "-Hp"}); err != nil {
		t.Fatalf("other arguments: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		_, _, err := r.Run(context.Background(), "zpool", args)
		if err == nil {
			break
		}
		if !errors.Is(err, errStillRunning) || time.Now().After(deadline) {
			t.Fatalf("Run after the stuck process ended: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestExecRunnerNotFound(t *testing.T) {
	r := execRunner{find: func(name string) (string, error) { return "", fmt.Errorf("%s %w", name, errNotFound) }, busy: newInflight()}
	_, _, err := r.Run(context.Background(), "zpool", []string{"list"})
	if !errors.Is(err, errNotFound) || err.Error() != "zpool not found" {
		t.Fatalf("err = %v", err)
	}
}

func TestExecRunnerStartFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-zpool")
	r := execRunner{find: func(string) (string, error) { return path, nil }, busy: newInflight()}
	if _, _, err := r.Run(context.Background(), "zpool", []string{"list"}); err == nil || errors.Is(err, errNotFound) {
		t.Fatalf("Run error = %v", err)
	}
	if r.busy.stuck("zpool\x00list") {
		t.Fatal("a failed start marked the arguments")
	}
}

func TestInflight(t *testing.T) {
	g := newInflight()
	a, b, c := &runState{}, &runState{}, &runState{}

	g.exited("k", a) // exited before Run gave up: never marked
	g.giveUp("k", a)
	if g.stuck("k") {
		t.Fatal("a run that had exited marked its key")
	}
	g.giveUp("k", b)
	g.giveUp("k", b) // idempotent
	g.giveUp("k", c)
	if !g.stuck("k") || g.stuck("other") {
		t.Fatal("given-up runs not marked, or the mark leaked to another key")
	}
	g.exited("k", b)
	if !g.stuck("k") {
		t.Fatal("one exit cleared the mark while another given-up run lives")
	}
	g.exited("k", c)
	if g.stuck("k") {
		t.Fatal("mark kept after every given-up run exited")
	}
}

func TestCappedWriter(t *testing.T) {
	w := &cappedWriter{max: 4}
	for _, s := range []string{"ab", "cd", "ef"} {
		if n, err := w.Write([]byte(s)); n != 2 || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	if string(w.buf) != "abcd" || !w.overflow {
		t.Fatalf("buf %q overflow %v", w.buf, w.overflow)
	}
}

// TestLocatorOrder: the standard locations first, in order, then PATH;
// only an absolute path is accepted.
func TestLocatorOrder(t *testing.T) {
	for _, tc := range []struct {
		desc  string
		found map[string]string // name asked → path returned
		want  string
		err   bool
	}{
		{"usr/sbin first", map[string]string{"/usr/sbin/zpool": "/usr/sbin/zpool", "/sbin/zpool": "/sbin/zpool", "zpool": "/opt/bin/zpool"}, "/usr/sbin/zpool", false},
		{"then /sbin", map[string]string{"/sbin/zpool": "/sbin/zpool", "/usr/local/sbin/zpool": "/usr/local/sbin/zpool"}, "/sbin/zpool", false},
		{"then /usr/local/sbin", map[string]string{"/usr/local/sbin/zpool": "/usr/local/sbin/zpool", "zpool": "/opt/bin/zpool"}, "/usr/local/sbin/zpool", false},
		{"then PATH", map[string]string{"zpool": "/opt/bin/zpool"}, "/opt/bin/zpool", false},
		{"relative PATH entry refused", map[string]string{"zpool": "bin/zpool"}, "", true},
		{"nowhere", nil, "", true},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			var asked []string
			l := locator{lookPath: func(name string) (string, error) {
				asked = append(asked, name)
				if p, ok := tc.found[name]; ok {
					return p, nil
				}
				return "", errors.New("not found")
			}}
			got, err := l.find("zpool")
			if tc.err {
				if !errors.Is(err, errNotFound) || err.Error() != "zpool not found" {
					t.Fatalf("find = %q, %v; want zpool not found", got, err)
				}
				if !slices.Equal(asked, []string{"/usr/sbin/zpool", "/sbin/zpool", "/usr/local/sbin/zpool", "zpool"}) {
					t.Fatalf("asked %q", asked)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("find = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
