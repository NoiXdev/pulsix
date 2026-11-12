package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return string(b)
}

func TestRunReadsTheEnvFile(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "agent.env")
	if err := os.WriteFile(envFile, []byte("PULSIX_MANAGER_ADDR=http://x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	protectEnvFile(t, envFile)
	logPath := filepath.Join(dir, "agent.log")
	if code := run([]string{"--env-file", envFile, "--log-file", logPath}, nil); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if got := readLog(t, logPath); !strings.Contains(got, "must be host:port") {
		t.Fatalf("log = %q", got)
	}
}

func TestRunRefusesALooseEnvFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not the protection on Windows")
	}
	dir := t.TempDir()
	envFile := filepath.Join(dir, "agent.env")
	if err := os.WriteFile(envFile, []byte("PULSIX_MANAGER_ADDR=a:1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(envFile, 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "agent.log")
	if code := run([]string{"--env-file", envFile, "--log-file", logPath}, nil); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if got := readLog(t, logPath); !strings.Contains(got, envFile) {
		t.Fatalf("log does not name the file: %q", got)
	}
}

func TestRunUnknownFlag(t *testing.T) {
	if code := run([]string{"--nope"}, nil); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if code := run([]string{"stray"}, nil); code != 2 {
		t.Fatalf("stray arg: code = %d, want 2", code)
	}
}

// TestRunConfigFlagNamesTheSecretsFile: the secrets file defaults to
// agent.yaml in the data directory and --config replaces it. The context
// is already cancelled: the agent stops right after it logs its start.
func TestRunConfigFlagNamesTheSecretsFile(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	env := []string{"PULSIX_MANAGER_ADDR=manager.example:8444", "PULSIX_DATA_DIR=" + dataDir}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	defaultLog := filepath.Join(dir, "default.log")
	runWithContext(ctx, []string{"--log-file", defaultLog}, env)
	if got, want := readLog(t, defaultLog), filepath.Join(dataDir, "agent.yaml"); !strings.Contains(got, "secrets_file="+want) {
		t.Fatalf("log = %q, want secrets_file=%s", got, want)
	}

	custom := filepath.Join(dir, "custom.yaml")
	customLog := filepath.Join(dir, "custom.log")
	runWithContext(ctx, []string{"--config", custom, "--log-file", customLog}, env)
	if got := readLog(t, customLog); !strings.Contains(got, "secrets_file="+custom) {
		t.Fatalf("log = %q, want secrets_file=%s", got, custom)
	}
}

func TestRunConfigFlagNeedsAValue(t *testing.T) {
	if code := run([]string{"--config"}, nil); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}
