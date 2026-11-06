package runner

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/worker"
	"github.com/noixdev/pulsix/internal/worker/requirements"
	"github.com/noixdev/pulsix/internal/worker/supervisor"
)

// TestGalaxyHelperReceivesTheCommand: under a play uid, what starts is the
// setuid helper with the supervisor and galaxy's absolute path, in the
// file's directory, with galaxy's own environment and nothing of the
// worker's settings.
func TestGalaxyHelperReceivesTheCommand(t *testing.T) {
	t.Setenv("PULSIX_ENROLL_TOKEN", "enroll-secret-value")
	ansibleBin, _ := fakeGalaxy(t, "exit 0")
	helper, calls, envDump := writeRecordingSetuidHelper(t)
	in := galaxyInstall(t, requirements.KindCollection)
	var logs bytes.Buffer
	cfg := worker.Config{PlayUID: 65533, SetuidHelper: helper, PlaySupervisor: "/sup", AnsibleBin: ansibleBin}
	if err := testGalaxy(cfg, &logs).Install(context.Background(), in, func(string) {}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	galaxy := filepath.Join(filepath.Dir(ansibleBin), "ansible-galaxy")
	want := append([]string{"65533", "/sup"}, supervisor.Command(200*time.Millisecond, galaxy,
		[]string{"collection", "install", "-r", in.File, "-p", filepath.Join(in.Entry, "collections")})...)
	if got := recordedCalls(t, calls); len(got) != 1 || !slices.Equal(got[0], want) {
		t.Errorf("helper calls = %q, want one: %q", got, want)
	}
	env := envOf(t, envDump)
	if env["HOME"] != filepath.Join(in.Entry, "home") || env["GIT_ALLOW_PROTOCOL"] != "https:http:ssh:git" {
		t.Errorf("helper env HOME=%q GIT_ALLOW_PROTOCOL=%q, want galaxy's", env["HOME"], env["GIT_ALLOW_PROTOCOL"])
	}
	if _, ok := env["PULSIX_ENROLL_TOKEN"]; ok {
		t.Error("the worker's enroll token crossed the uid boundary")
	}
	wantDir, err := filepath.EvalSymlinks(in.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if pwd, err := filepath.EvalSymlinks(env["PWD"]); env["PWD"] != "" && (err != nil || pwd != wantDir) {
		t.Errorf("helper PWD = %q, want %q", env["PWD"], wantDir)
	}
}

// TestGalaxyLinesWithoutTheirNewline: a carriage return before the newline
// is not part of the line, and a last line without one still arrives.
func TestGalaxyLinesWithoutTheirNewline(t *testing.T) {
	ansibleBin, _ := fakeGalaxy(t, `printf 'x\r\ny'`)
	var logs bytes.Buffer
	var got collect
	if err := testGalaxy(worker.Config{AnsibleBin: ansibleBin}, &logs).Install(context.Background(), galaxyInstall(t, requirements.KindRole), got.line); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !slices.Equal(got.lines, []string{"x", "y"}) {
		t.Errorf("lines = %q, want [x y]", got.lines)
	}
}

// TestGalaxyLineWriterBoundsWhatItHolds: a line without a newline is sent
// in pieces of maxGalaxyLine, and only the last keep bytes are kept.
func TestGalaxyLineWriterBoundsWhatItHolds(t *testing.T) {
	var got collect
	w := &lineWriter{send: got.line, keep: 4}
	long := strings.Repeat("x", maxGalaxyLine+3)
	for _, p := range []string{long, "\nab", "cd\n"} {
		if _, err := w.Write([]byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	w.flush()
	want := []string{strings.Repeat("x", maxGalaxyLine), "xxx", "abcd"}
	if !slices.Equal(got.lines, want) {
		t.Errorf("lines = %d of lengths %v, want %v", len(got.lines), lens(got.lines), lens(want))
	}
	if string(w.kept) != "bcd\n" {
		t.Errorf("kept = %q, want the last 4 bytes %q", w.kept, "bcd\n")
	}
}

func lens(lines []string) []int {
	out := make([]int, len(lines))
	for i, l := range lines {
		out[i] = len(l)
	}
	return out
}

func TestNewRequirementsInstallsThroughGalaxy(t *testing.T) {
	var logs bytes.Buffer
	r := &Runner{Log: slog.New(slog.NewTextHandler(&logs, nil))}
	if NewRequirements(r, worker.Config{}) == nil {
		t.Fatal("NewRequirements returned nil")
	}
}

// TestGalaxyNamesTheInstallWhenItsSupervisorIsKilled: a supervisor that
// does not stop within grace and backstop is killed, and the warning
// names the install (its temp entry), not a bare program name.
func TestGalaxyNamesTheInstallWhenItsSupervisorIsKilled(t *testing.T) {
	ansibleBin, _ := fakeGalaxy(t, "exit 0")
	started := filepath.Join(t.TempDir(), "started")
	sup := filepath.Join(t.TempDir(), "stubborn-supervisor")
	writeMode(t, sup, "#!/bin/sh\ntrap '' TERM\ntouch "+shQuote(started)+"\nwhile :; do sleep 0.05; done\n", 0o700)
	in := galaxyInstall(t, requirements.KindRole)
	var logs bytes.Buffer
	g := testGalaxy(worker.Config{PlaySupervisor: sup, AnsibleBin: ansibleBin}, &logs)
	g.Runner.SupervisorBackstop = 100 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- g.Install(ctx, in, func(string) {}) }()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the supervisor never started")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Install of a stopped command returned nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Install did not return after the backstop KILL")
	}
	var warning string
	for l := range strings.Lines(logs.String()) {
		if strings.Contains(l, "play supervisor did not stop the run in time") {
			warning = l
		}
	}
	if !strings.Contains(warning, in.Entry) {
		t.Errorf("log = %q, want the backstop warning naming %s", logs.String(), in.Entry)
	}
}
