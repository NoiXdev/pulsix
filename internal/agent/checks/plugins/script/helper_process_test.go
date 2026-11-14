package script

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// helperArg as the first argument makes the test binary act as a script
// (TestMain), so the exec runner is tested against real processes on every
// platform. The mode travels as an argument, not an environment variable:
// the runner builds the environment and would drop it.
const helperArg = "pulsix-script-test-helper"

func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == helperArg {
		os.Exit(helperMain(os.Args[2], os.Args[3:]))
	}
	os.Exit(m.Run())
}

// helperMain is the stand-in script. Modes:
//   - "exit:<n>": prints "out:<n>" to stdout, noise to stderr, exits n;
//   - "args": prints its remaining arguments as a JSON array;
//   - "argv0": prints the name it was started under (os.Args[0]);
//   - "flood:<n>": writes n bytes of 'x';
//   - "env": prints its environment as a JSON array;
//   - "cwd": prints its working directory;
//   - "sleep:<ms>": sleeps, then prints "slept";
//   - "tree <pidfile>": starts a child helper sleeping 60 s, writes the
//     child's pid to pidfile, then sleeps 60 s itself;
//   - "orphan <pidfile>": starts a child helper sleeping 60 s that keeps
//     the standard output open, writes its pid to pidfile, prints "done"
//     and exits 0 at once.
func helperMain(mode string, rest []string) int {
	kind, arg, _ := strings.Cut(mode, ":")
	switch kind {
	case "exit":
		fmt.Printf("out:%s", arg)
		fmt.Fprint(os.Stderr, strings.Repeat("stderr noise ", 100))
		code, _ := strconv.Atoi(arg)
		return code
	case "args":
		b, _ := json.Marshal(rest)
		_, _ = os.Stdout.Write(b)
		return 0
	case "argv0":
		fmt.Print(os.Args[0])
		return 0
	case "flood":
		n, _ := strconv.Atoi(arg)
		_, _ = os.Stdout.Write([]byte(strings.Repeat("x", n)))
		return 0
	case "env":
		b, _ := json.Marshal(os.Environ())
		_, _ = os.Stdout.Write(b)
		return 0
	case "cwd":
		wd, err := os.Getwd()
		if err != nil {
			return 3
		}
		fmt.Print(wd)
		return 0
	case "sleep":
		ms, _ := strconv.Atoi(arg)
		time.Sleep(time.Duration(ms) * time.Millisecond)
		fmt.Print("slept")
		return 0
	case "tree", "orphan":
		if len(rest) != 1 {
			return 4
		}
		child := exec.Command(os.Args[0], helperArg, "sleep:60000")
		child.Stdout = os.Stdout // the child holds the output pipe open
		if err := child.Start(); err != nil {
			return 5
		}
		if err := os.WriteFile(rest[0], []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			return 6
		}
		if kind == "orphan" {
			fmt.Print("done")
			return 0
		}
		time.Sleep(60 * time.Second)
		return 0
	}
	return 99
}

// helperArgv is the argv that runs the test binary as the helper in mode.
func helperArgv(t *testing.T, mode string, rest ...string) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return append([]string{exe, helperArg, mode}, rest...)
}

// readPid reads the pid a "tree" or "orphan" helper wrote to path.
func readPid(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the helper wrote no pid: %v", err)
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatalf("pid file %q: %v", b, err)
	}
	return pid
}

// waitGone fails the test unless the process pid is gone within 10 s.
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d still runs", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
