package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testBase = "http://127.0.0.1:18080"

// writeDist lays out a dist directory the way the CI job builds it: one
// binary and its checksum, under the names the manager serves.
func writeDist(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"pulsix-agent-darwin-arm64":        "agent binary bytes",
		"pulsix-agent-darwin-arm64.sha256": "abc123  pulsix-agent-darwin-arm64\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestStubServesRenderedScriptsAndBinaries(t *testing.T) {
	h := newHandler(testBase, writeDist(t))

	tests := []struct {
		path     string
		status   int
		contains string
		equals   string
	}{
		{path: "/install.sh", status: http.StatusOK, contains: `BASE_URL="` + testBase + `"`},
		{path: "/install.ps1", status: http.StatusOK, contains: `$BaseUrl = '` + testBase + `'`},
		{path: "/agents/bin/darwin/arm64", status: http.StatusOK, equals: "agent binary bytes"},
		{path: "/agents/bin/darwin/arm64.sha256", status: http.StatusOK, equals: "abc123  pulsix-agent-darwin-arm64\n"},
		{path: "/agents/bin/windows/amd64", status: http.StatusNotFound},
		{path: "/agents/bin/windows/amd64.sha256", status: http.StatusNotFound},
		{path: "/agents/bin/darwin/ARM64", status: http.StatusNotFound},
		{path: "/agents/bin/darwin/arm64.exe", status: http.StatusNotFound},
		{path: "/agents/bin/dar.win/arm64", status: http.StatusNotFound},
		{path: "/other", status: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := get(t, h, tt.path)
			if rec.Code != tt.status {
				t.Fatalf("GET %s = %d, want %d (body %q)", tt.path, rec.Code, tt.status, rec.Body.String())
			}
			body := rec.Body.String()
			if tt.contains != "" && !strings.Contains(body, tt.contains) {
				t.Errorf("GET %s does not contain %q", tt.path, tt.contains)
			}
			if tt.equals != "" && body != tt.equals {
				t.Errorf("GET %s = %q, want %q", tt.path, body, tt.equals)
			}
			if strings.Contains(body, "{{") {
				t.Errorf("GET %s still carries template syntax", tt.path)
			}
		})
	}
}

// A base URL the PowerShell installer cannot carry is answered with an
// error rather than a script.
func TestStubAnswersAnUnrenderableScriptWithAnError(t *testing.T) {
	h := newHandler("http://it's.example", writeDist(t))
	if rec := get(t, h, "/install.ps1"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /install.ps1 = %d, want 500", rec.Code)
	}
}

func TestRunRefusesBadArguments(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no dist", nil},
		{"unknown flag", []string{"-dist", t.TempDir(), "-nope"}},
		{"stray argument", []string{"-dist", t.TempDir(), "extra"}},
		{"unusable address", []string{"-dist", t.TempDir(), "-addr", "127.0.0.1:notaport"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := run(context.Background(), tt.args, io.Discard); err == nil {
				t.Fatal("run accepted the arguments")
			}
		})
	}
}

// run prints ready once it listens, serves the handler, and returns
// cleanly when its context ends.
func TestRunServesUntilCancelled(t *testing.T) {
	// Reserve a free port, then hand it to run.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"-addr", addr, "-dist", writeDist(t)}, outW)
		_ = outW.Close()
	}()

	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("first output line = %q (%v), want ready", line, err)
	}
	go func() { _, _ = io.Copy(io.Discard, out) }()

	resp, err := http.Get("http://" + addr + "/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), `BASE_URL="http://`+addr+`"`) {
		t.Errorf("the served script does not name the stub's own address")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run = %v, want nil after cancel", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after cancel")
	}
}
