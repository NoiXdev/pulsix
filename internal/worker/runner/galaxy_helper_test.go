package runner

import (
	"bytes"
	"context"
	"errors"
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

// TestGalaxyRemoveAllAsThePlayUID: what the play uid wrote can hold a
// directory without write permission (a role tarball's own mode), which
// neither the worker nor a plain rm of the play uid can empty. The helper
// stand-in execs what it is given, as the real one does after the drop.
func TestGalaxyRemoveAllAsThePlayUID(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores the permission bits this case is built from")
	}
	stuckDir := func(t *testing.T) string {
		dir := filepath.Join(t.TempDir(), ".tmp-x")
		sub := filepath.Join(dir, "sub")
		mkdirMode(t, sub, 0o700)
		writeMode(t, filepath.Join(sub, "file"), "x", 0o600)
		mkdirMode(t, sub, 0o500)
		t.Cleanup(func() { _ = os.Chmod(sub, 0o700) })
		return dir
	}
	calls := filepath.Join(t.TempDir(), "calls")
	helper := filepath.Join(t.TempDir(), "exec-helper")
	writeMode(t, helper, "#!/bin/sh\nprintf '%s\\n' \"$@\" >> "+shQuote(calls)+"\nprintf '===\\n' >> "+shQuote(calls)+"\nshift\nexec \"$@\"\n", 0o700)
	var logs bytes.Buffer

	dir := stuckDir(t)
	if err := testGalaxy(worker.Config{PlayUID: 65533, SetuidHelper: helper}, &logs).RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s survived: %v", dir, err)
	}
	got := recordedCalls(t, calls)
	if len(got) != 2 {
		t.Fatalf("helper calls = %q, want chmod then rm", got)
	}
	sub := filepath.Join(dir, "sub")
	chmod, rm := got[0], got[1]
	if len(chmod) != 6 || chmod[0] != "65533" || !filepath.IsAbs(chmod[1]) || filepath.Base(chmod[1]) != "chmod" ||
		!slices.Equal(chmod[2:], []string{"-R", "u+rwX", "--", sub}) {
		t.Errorf("first call = %q, want 65533 <abs chmod> -R u+rwX -- %s", chmod, sub)
	}
	if len(rm) != 5 || rm[0] != "65533" || !filepath.IsAbs(rm[1]) || filepath.Base(rm[1]) != "rm" ||
		!slices.Equal(rm[2:], []string{"-rf", "--", sub}) {
		t.Errorf("second call = %q, want 65533 <abs rm> -rf -- %s", rm, sub)
	}

	_ = os.Remove(calls)
	dir = stuckDir(t)
	err := testGalaxy(worker.Config{SetuidHelper: helper}, &logs).RemoveAll(dir)
	if err == nil || !strings.Contains(err.Error(), "sub") {
		t.Errorf("RemoveAll without a play uid = %v, want the error naming sub", err)
	}
	if c := recordedCalls(t, calls); len(c) != 0 {
		t.Errorf("the helper ran without a play uid: %q", c)
	}
}

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
