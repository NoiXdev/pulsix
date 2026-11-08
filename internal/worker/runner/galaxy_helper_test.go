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
	"github.com/noixdev/pulsix/internal/worker/urlcred"
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

// TestGalaxyLineWriterBoundsWhatItHolds: a word longer than maxGalaxyLine
// is left out, and only the last keep bytes are kept.
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
	want := []string{wordLeftOut, "abcd"}
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

// writeAll feeds chunks to w, then flushes it.
func writeAll(t *testing.T, w *lineWriter, chunks ...string) {
	t.Helper()
	for _, c := range chunks {
		if _, err := w.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	w.flush()
}

// TestGalaxyLineWriterNeverCutsAWord: the window's end falls inside a
// URL's token, and the line is cut before the URL, so its userinfo is never
// split and every line still redacts clean.
func TestGalaxyLineWriterNeverCutsAWord(t *testing.T) {
	pre := strings.Repeat("y", maxGalaxyLine-12)
	url := "https://ghp_tok3n@host/r.git"
	in := pre + " " + url + "\n"
	if edge := strings.Index(in, "ghp_tok3n"); edge >= maxGalaxyLine || edge+len("ghp_tok3n") <= maxGalaxyLine {
		t.Fatalf("the window edge %d must fall inside the token at %d", maxGalaxyLine, edge)
	}
	bytewise := make([]string, len(in))
	for i := range in {
		bytewise[i] = in[i : i+1]
	}
	for name, chunks := range map[string][]string{"one write": {in}, "byte by byte": bytewise} {
		t.Run(name, func(t *testing.T) {
			var got collect
			writeAll(t, &lineWriter{send: got.line}, chunks...)
			if want := []string{pre, url}; !slices.Equal(got.lines, want) {
				t.Fatalf("lines = %v, want %v", lens(got.lines), lens(want))
			}
			for _, l := range got.lines {
				if r := urlcred.Redact(l); strings.Contains(r, "ghp_tok3n") {
					t.Errorf("a redacted line still holds the token: %.60q", r)
				}
			}
		})
	}
}

// TestGalaxyLineWriterKeepsASlashPasswordWhole (backlog 7.98): a password
// with an unencoded / straddling the window edge is still one word, so the
// line that carries it is redacted whole.
func TestGalaxyLineWriterKeepsASlashPasswordWhole(t *testing.T) {
	pre := strings.Repeat("y", maxGalaxyLine-16)
	url := "https://user:s3/cr3t@host/r.git"
	in := pre + " " + url + "\n"
	if edge := strings.Index(in, "s3/cr3t"); edge >= maxGalaxyLine || edge+len("s3/cr3t") <= maxGalaxyLine {
		t.Fatalf("the window edge %d must fall inside the password at %d", maxGalaxyLine, edge)
	}
	var got collect
	writeAll(t, &lineWriter{send: got.line}, in)
	if want := []string{pre, url}; !slices.Equal(got.lines, want) {
		t.Fatalf("lines = %v, want %v", lens(got.lines), lens(want))
	}
	for _, l := range got.lines {
		if r := urlcred.Redact(l); strings.Contains(r, "s3/cr3t") || strings.Contains(r, "cr3t") {
			t.Errorf("a redacted line still holds the password: %.60q", r)
		}
	}
}

// TestGalaxyLineWriterTreatsTheEdgeTheSameWhereverTheWritesSplit: a line, or
// a word, of exactly maxGalaxyLine bytes is not over the bound, so it is sent
// whole and the white space behind it ends it.
func TestGalaxyLineWriterTreatsTheEdgeTheSameWhereverTheWritesSplit(t *testing.T) {
	edge := strings.Repeat("e", maxGalaxyLine)
	for _, tc := range []struct {
		name   string
		chunks []string
		want   []string
	}{
		{"line and newline in one write", []string{edge + "\n"}, []string{edge}},
		{"line, then the newline", []string{edge, "\n"}, []string{edge}},
		{"line and CRLF in one write", []string{edge + "\r\n"}, []string{edge}},
		{"line, then CRLF", []string{edge, "\r\n"}, []string{edge}},
		{"line and CR, then the LF", []string{edge + "\r", "\n"}, []string{edge}},
		{"line and CRLF, then more", []string{edge + "\r\nnext\n"}, []string{edge, "next"}},
		{"line and CR, then the LF and more", []string{edge + "\r", "\nnext\n"}, []string{edge, "next"}},
		{"word and a space in one write", []string{edge + " end\n"}, []string{edge, "end"}},
		{"word, then a space", []string{edge, " end\n"}, []string{edge, "end"}},
		{"word, then a space byte by byte", []string{edge, " ", "e", "n", "d", "\n"}, []string{edge, "end"}},
		{"one byte over the edge", []string{edge + "e\n"}, []string{wordLeftOut}},
		{"one byte over the edge, then a space", []string{edge + "e", " end\n"}, []string{wordLeftOut, "end"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got collect
			writeAll(t, &lineWriter{send: got.line}, tc.chunks...)
			if !slices.Equal(got.lines, tc.want) {
				t.Errorf("lines = %v, want %v", lens(got.lines), lens(tc.want))
			}
		})
	}
}

// TestGalaxyLineWriterLeavesOutALongWord: a word with no white space in
// 64 KiB is replaced by a marker, wherever the Writes split it.
func TestGalaxyLineWriterLeavesOutALongWord(t *testing.T) {
	in := strings.Repeat("z", maxGalaxyLine+5) + " end\n"
	for name, chunks := range map[string][]string{
		"one write":  {in},
		"two writes": {in[:maxGalaxyLine+2], in[maxGalaxyLine+2:]},
	} {
		t.Run(name, func(t *testing.T) {
			var got collect
			writeAll(t, &lineWriter{send: got.line}, chunks...)
			if want := []string{wordLeftOut, "end"}; !slices.Equal(got.lines, want) {
				t.Errorf("lines = %v, want %v", lens(got.lines), want)
			}
		})
	}
}

// TestGalaxyStderrTailStartsAtAWord: the kept tail, once its front was cut,
// starts after its first white space, so no URL is cut in front.
func TestGalaxyStderrTailStartsAtAWord(t *testing.T) {
	const in = "abc https://tok@h x\n"
	w := &lineWriter{send: func(string) {}, keep: 8}
	writeAll(t, w, in)
	if string(w.kept) != "tok@h x\n" || w.tail() != "x\n" {
		t.Errorf("kept = %q, tail = %q, want %q and %q", w.kept, w.tail(), "tok@h x\n", "x\n")
	}
	whole := &lineWriter{send: func(string) {}, keep: 64}
	writeAll(t, whole, in)
	if whole.tail() != in {
		t.Errorf("tail of an input that never overflowed = %q, want all of it", whole.tail())
	}
}
