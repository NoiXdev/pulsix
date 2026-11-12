package smart

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

// helperEnv makes the test binary act as a stand-in smartctl (TestMain),
// so the exec runner is tested against a real process without the real
// smartctl.
const helperEnv = "PULSIX_SMART_TEST_HELPER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		os.Exit(helperMain(mode))
	}
	os.Exit(m.Run())
}

// helperMain is the stand-in smartctl. Modes: "args:<exit>" prints its
// arguments as a JSON array and exits with <exit>; "flood" writes 2 MiB;
// "sleep:<ms>" sleeps, then prints {}.
func helperMain(mode string) int {
	kind, arg, _ := strings.Cut(mode, ":")
	switch kind {
	case "args":
		b, _ := json.Marshal(os.Args[1:])
		_, _ = os.Stdout.Write(b)
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("noise ", 1000))
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
		_, _ = os.Stdout.WriteString("{}")
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
	return execRunner{path: exe, busy: newInflight()}
}

// TestExecRunnerPassesEachArgumentWhole is Review Focus 3 at the process
// boundary: no shell, so a device name with spaces, backslashes or quote
// characters arrives as exactly one argument.
func TestExecRunnerPassesEachArgumentWhole(t *testing.T) {
	r := helperRunner(t, "args:0")
	args := []string{"--json", "-a", "-n", "standby", `\\.\PhysicalDrive0`, "/dev/disk/by-id/ata-Samsung SSD 860_S3Z", `/dev/a"b;$(x)'c`}
	res, err := r.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got []string
	if err := json.Unmarshal(res.stdout, &got); err != nil {
		t.Fatalf("helper output %q: %v", res.stdout, err)
	}
	if !slices.Equal(got, args) {
		t.Fatalf("argv = %q, want %q", got, args)
	}
	if res.exitCode != 0 {
		t.Fatalf("exit = %d", res.exitCode)
	}
}

// TestExecRunnerReturnsTheExitBits: a non-zero exit status is data for the
// plugin, not a runner error, and stdout is kept with it.
func TestExecRunnerReturnsTheExitBits(t *testing.T) {
	r := helperRunner(t, "args:88")
	res, err := r.Run(context.Background(), []string{"/dev/sda"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.exitCode != 88 || string(res.stdout) != `["/dev/sda"]` {
		t.Fatalf("Run = %d %q", res.exitCode, res.stdout)
	}
}

func TestExecRunnerCapsOutput(t *testing.T) {
	r := helperRunner(t, "flood")
	if _, err := r.Run(context.Background(), []string{"/dev/sda"}); !errors.Is(err, errOutputTooLarge) {
		t.Fatalf("Run error = %v, want errOutputTooLarge", err)
	}
}

func TestExecRunnerKillsOnTimeout(t *testing.T) {
	r := helperRunner(t, "sleep:10000")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := r.Run(ctx, []string{"/dev/sda"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want the deadline", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Run took %v", d)
	}
}

// TestExecRunnerStuckProcess: a smartctl the kill cannot stop at once (in
// the kernel waiting on a hung disk) does not hold Run past its deadline,
// and no second smartctl is started on that device until it has exited.
func TestExecRunnerStuckProcess(t *testing.T) {
	r := helperRunner(t, "sleep:800")
	r.noKill = true
	r.reapGrace = 50 * time.Millisecond
	args := []string{"--json", "-a", "/dev/sdh"}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := r.Run(ctx, args); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Run error = %v, want the deadline", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("first Run took %v", d)
	}
	if _, err := r.Run(context.Background(), args); !errors.Is(err, errStillRunning) {
		t.Fatalf("second Run error = %v, want errStillRunning", err)
	}
	other := []string{"--json", "-a", "/dev/sda"}
	if _, err := r.Run(context.Background(), other); err != nil {
		t.Fatalf("another device: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := r.Run(context.Background(), args)
		if err == nil {
			break
		}
		if !errors.Is(err, errStillRunning) || time.Now().After(deadline) {
			t.Fatalf("Run after the stuck process ended: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestExecRunnerStartFailure(t *testing.T) {
	r := execRunner{path: filepath.Join(t.TempDir(), "no-smartctl"), busy: newInflight()}
	if _, err := r.Run(context.Background(), []string{"--scan"}); err == nil {
		t.Fatal("Run error = nil")
	}
	// The failed start released its slot.
	if _, err := r.Run(context.Background(), []string{"--scan"}); errors.Is(err, errStillRunning) {
		t.Fatal("a failed start kept the device busy")
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
