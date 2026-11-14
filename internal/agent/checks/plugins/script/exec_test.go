package script

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// runArgv runs argv with r, its program the file argv[0] names.
func runArgv(ctx context.Context, r execRunner, argv []string) (runResult, error) {
	return r.Run(ctx, argv[0], argv)
}

func TestExecRunnerExitCodes(t *testing.T) {
	for _, code := range []string{"0", "2", "7"} {
		t.Run(code, func(t *testing.T) {
			res, err := runArgv(context.Background(), newExecRunner(), helperArgv(t, "exit:"+code))
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !res.exited || strconv.Itoa(res.exitCode) != code {
				t.Errorf("exited %v code %d, want exit %s", res.exited, res.exitCode, code)
			}
			// Standard error is discarded, never mixed into standard output.
			if string(res.stdout) != "out:"+code || res.truncated {
				t.Errorf("stdout %q truncated %v", res.stdout, res.truncated)
			}
			if res.duration <= 0 {
				t.Errorf("duration %v", res.duration)
			}
		})
	}
}

// TestExecRunnerPassesEachArgumentWhole: no shell, so spaces, quotes and
// shell syntax arrive as exactly one argument each.
func TestExecRunnerPassesEachArgumentWhole(t *testing.T) {
	args := []string{"a b", `c"d`, "$(touch x); `y`", "'e'", "", `C:\f g\h`}
	res, err := runArgv(context.Background(), newExecRunner(), helperArgv(t, "args", args...))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got []string
	if err := json.Unmarshal(res.stdout, &got); err != nil {
		t.Fatalf("helper output %q: %v", res.stdout, err)
	}
	if !slices.Equal(got, args) {
		t.Fatalf("args = %q, want %q", got, args)
	}
}

// TestExecRunnerRunsThePathUnderTheConfiguredName: the file run is path
// (the one the checker passed), and the process sees argv[0] as its name.
func TestExecRunnerRunsThePathUnderTheConfiguredName(t *testing.T) {
	argv := helperArgv(t, "argv0")
	name := filepath.Join(t.TempDir(), "configured-name.exe")
	res, err := newExecRunner().Run(context.Background(), argv[0], append([]string{name}, argv[1:]...))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(res.stdout) != name {
		t.Fatalf("the process's name is %q, want %q", res.stdout, name)
	}
}

func TestExecRunnerCapsOutput(t *testing.T) {
	for _, tc := range []struct {
		n         int
		truncated bool
	}{{maxOutput, false}, {maxOutput + 1, true}, {4 * maxOutput, true}} {
		res, err := runArgv(context.Background(), newExecRunner(), helperArgv(t, "flood:"+strconv.Itoa(tc.n)))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(res.stdout) != min(tc.n, maxOutput) || res.truncated != tc.truncated || !res.exited || res.exitCode != 0 {
			t.Errorf("%d bytes written: kept %d, truncated %v, exit %v/%d", tc.n, len(res.stdout), res.truncated, res.exited, res.exitCode)
		}
	}
}

// TestExecRunnerBuildsTheEnvironment is Review Focus 4: the script sees
// the built variables and nothing the agent's own environment holds.
func TestExecRunnerBuildsTheEnvironment(t *testing.T) {
	t.Setenv("PULSIX_SCRIPT_TEST_SECRET", "MARKER")
	res, err := runArgv(context.Background(), newExecRunner(), helperArgv(t, "env"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got []string
	if err := json.Unmarshal(res.stdout, &got); err != nil {
		t.Fatalf("helper output %q: %v", res.stdout, err)
	}
	if strings.Contains(string(res.stdout), "MARKER") {
		t.Fatalf("the agent's environment reached the script: %q", got)
	}
	// Windows keeps per-drive working directories as "=C:=C:\..." entries
	// a process may add for itself; they are no inherited variable.
	got = slices.DeleteFunc(got, func(kv string) bool { return strings.HasPrefix(kv, "=") })
	want := commandEnv(runtime.GOOS, os.LookupEnv)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("environment\ngot  %q\nwant %q", got, want)
	}
}

func TestExecRunnerWorkingDirectory(t *testing.T) {
	want := "/"
	if runtime.GOOS == "windows" {
		want = os.Getenv("SystemDrive") + `\`
	}
	res, err := runArgv(context.Background(), newExecRunner(), helperArgv(t, "cwd"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.EqualFold(string(res.stdout), want) {
		t.Fatalf("working directory %q, want %q", res.stdout, want)
	}
}

// TestExecRunnerKillsTheTreeOnTimeout is Review Focus 3: on timeout the
// script and the child it started are killed, and the child, which holds
// the output pipe, does not keep Run waiting.
func TestExecRunnerKillsTheTreeOnTimeout(t *testing.T) {
	// A first run of the helper binary can be slow (a virus scanner on
	// Windows); the timed run must get to start its child in time.
	if _, err := runArgv(context.Background(), newExecRunner(), helperArgv(t, "exit:0")); err != nil {
		t.Fatalf("warm-up run: %v", err)
	}
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	timeout := 3 * time.Second
	if runtime.GOOS == "windows" {
		timeout = 10 * time.Second // process starts are slow there
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	res, err := runArgv(ctx, newExecRunner(), helperArgv(t, "tree", pidFile))
	if !errors.Is(err, errTimeout) {
		t.Fatalf("Run error = %v, want errTimeout", err)
	}
	if took := time.Since(start); took > timeout+5*time.Second {
		t.Fatalf("Run took %v", took)
	}
	if res.duration < timeout/2 {
		t.Errorf("duration %v, want about %v", res.duration, timeout)
	}
	waitGone(t, readPid(t, pidFile))
}

// TestExecRunnerEndsWhatTheScriptLeftBehind: a script that exits while a
// child it started still holds the output pipe returns its result within
// the wait delay, and the child does not outlive the run.
func TestExecRunnerEndsWhatTheScriptLeftBehind(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	res, err := runArgv(ctx, newExecRunner(), helperArgv(t, "orphan", pidFile))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if took := time.Since(start); took > waitDelay+5*time.Second {
		t.Fatalf("Run took %v", took)
	}
	if !res.exited || res.exitCode != 0 || string(res.stdout) != "done" {
		t.Errorf("result exited %v code %d stdout %q", res.exited, res.exitCode, res.stdout)
	}
	waitGone(t, readPid(t, pidFile))
}

// TestExecRunnerAgentStopping: a cancelled (not timed-out) context is
// returned as such, not as a timeout.
func TestExecRunnerAgentStopping(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	_, err := runArgv(ctx, newExecRunner(), helperArgv(t, "sleep:20000"))
	if !errors.Is(err, context.Canceled) || errors.Is(err, errTimeout) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
}

// TestExecRunnerStartFailure: a program that cannot start is an error that
// names no argument.
func TestExecRunnerStartFailure(t *testing.T) {
	prog := filepath.Join(t.TempDir(), "missing.exe")
	_, err := runArgv(context.Background(), newExecRunner(), []string{prog, "--password", "MARKER"})
	if err == nil || errors.Is(err, errTimeout) || errors.Is(err, errBusy) {
		t.Fatalf("Run error = %v, want a start failure", err)
	}
	if strings.Contains(err.Error(), "MARKER") {
		t.Fatalf("the error quotes an argument: %v", err)
	}
	if runtime.GOOS != "windows" {
		var errno syscall.Errno
		if !errors.As(err, &errno) {
			t.Errorf("error %v carries no errno", err)
		}
	}
}

// TestExecRunnerStuckProcess: a process the kill could not stop does not
// hold Run past its deadline, and the same argv is not started again until
// it has exited; another argv still runs.
func TestExecRunnerStuckProcess(t *testing.T) {
	r := newExecRunner()
	r.noKill = true
	r.reapGrace = 50 * time.Millisecond
	argv := helperArgv(t, "sleep:1500")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := runArgv(ctx, r, argv)
	if !errors.Is(err, errTimeout) {
		t.Fatalf("first Run error = %v, want errTimeout", err)
	}
	if res.duration <= 0 {
		t.Errorf("duration %v", res.duration)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("first Run took %v", d)
	}
	if _, err := runArgv(context.Background(), r, argv); !errors.Is(err, errBusy) {
		t.Fatalf("second Run error = %v, want errBusy", err)
	}
	if _, err := runArgv(context.Background(), r, helperArgv(t, "exit:0")); err != nil {
		t.Fatalf("another command: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		_, err := runArgv(context.Background(), r, argv)
		if err == nil {
			break
		}
		if !errors.Is(err, errBusy) || time.Now().After(deadline) {
			t.Fatalf("Run after the stuck process ended: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestExecRunnerConcurrentSameArgv: two checks running the same command at
// once both run; only a run given up on marks it.
func TestExecRunnerConcurrentSameArgv(t *testing.T) {
	r := newExecRunner()
	argv := helperArgv(t, "sleep:300")
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := runArgv(context.Background(), r, argv)
			errs <- err
		}()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
}

// TestInflight: giveUp marks a key only while that run's process has not
// exited, whichever of the two happens first, and each given-up run counts.
func TestInflight(t *testing.T) {
	g := newInflight()
	a, b, c := &runState{}, &runState{}, &runState{}

	g.exited("k", a)
	g.giveUp("k", a)
	if g.stuck("k") {
		t.Fatal("a run that had exited marked its key")
	}
	g.giveUp("k", b)
	g.giveUp("k", b)
	g.giveUp("k", c)
	if !g.stuck("k") || g.stuck("other") {
		t.Fatal("given-up runs not marked, or the mark leaked to another key")
	}
	g.exited("k", b)
	if !g.stuck("k") {
		t.Fatal("one of two given-up runs exited and the key was unmarked")
	}
	g.exited("k", c)
	if g.stuck("k") {
		t.Fatal("every given-up run exited and the key is still marked")
	}
}

func TestCommandEnv(t *testing.T) {
	unix := []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	for _, goos := range []string{"linux", "darwin", "freebsd"} {
		got := commandEnv(goos, func(string) (string, bool) { return "x", true })
		if !slices.Equal(got, unix) {
			t.Errorf("%s: %q, want %q", goos, got, unix)
		}
	}
	vars := map[string]string{
		"SystemRoot": `C:\Windows`, "windir": `C:\Windows`, "SystemDrive": "C:",
		"ComSpec": `C:\Windows\system32\cmd.exe`, "PATHEXT": ".COM;.EXE;.BAT",
		"TEMP": `C:\Windows\TEMP`, "TMP": `C:\Windows\TEMP`,
		"PATH": `C:\agent;C:\Windows`, "PULSIX_TOKEN": "secret", "USERPROFILE": `C:\Users\x`,
	}
	lookup := func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
	want := []string{
		`SystemRoot=C:\Windows`, `windir=C:\Windows`, "SystemDrive=C:",
		`ComSpec=C:\Windows\system32\cmd.exe`, "PATHEXT=.COM;.EXE;.BAT",
		`TEMP=C:\Windows\TEMP`, `TMP=C:\Windows\TEMP`,
		`PATH=C:\Windows\System32;C:\Windows;C:\Windows\System32\WindowsPowerShell\v1.0`,
	}
	if got := commandEnv("windows", lookup); !slices.Equal(got, want) {
		t.Errorf("windows:\ngot  %q\nwant %q", got, want)
	}
	delete(vars, "TEMP")
	delete(vars, "SystemRoot")
	want = []string{
		`windir=C:\Windows`, "SystemDrive=C:",
		`ComSpec=C:\Windows\system32\cmd.exe`, "PATHEXT=.COM;.EXE;.BAT", `TMP=C:\Windows\TEMP`,
		`PATH=C:\Windows\System32;C:\Windows;C:\Windows\System32\WindowsPowerShell\v1.0`,
	}
	if got := commandEnv("windows", lookup); !slices.Equal(got, want) {
		t.Errorf("windows without SystemRoot and TEMP:\ngot  %q\nwant %q", got, want)
	}
	if got := commandEnv("windows", func(string) (string, bool) { return "", false }); got == nil || len(got) != 0 {
		t.Errorf("windows with nothing set: %#v, want an empty, non-nil environment", got)
	}
}

// TestCommandArg0: on Windows a .bat or .cmd program runs through
// cmd.exe, which runs the file the command line's first word names, not
// the one the process was created from: that word is the checked path.
// Any other program keeps its configured name.
func TestCommandArg0(t *testing.T) {
	cases := []struct {
		goos, path, arg0, want string
	}{
		{"windows", `C:\real\check.bat`, `C:\link\check.bat`, `C:\real\check.bat`},
		{"windows", `C:\real\check.CMD`, `C:\link\check.CMD`, `C:\real\check.CMD`},
		{"windows", `C:\real\check.exe`, `C:\link\check.exe`, `C:\link\check.exe`},
		{"windows", `C:\real\check.bat.exe`, `C:\link\x.bat.exe`, `C:\link\x.bat.exe`},
		{"linux", "/real/check.bat", "/link/check.bat", "/link/check.bat"},
		{"darwin", "/real/busybox", "/bin/ping", "/bin/ping"},
	}
	for _, tc := range cases {
		if got := commandArg0(tc.goos, tc.path, tc.arg0); got != tc.want {
			t.Errorf("commandArg0(%s, %s, %s) = %s, want %s", tc.goos, tc.path, tc.arg0, got, tc.want)
		}
	}
}
