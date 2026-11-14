//go:build windows

package script

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// processAlive reports whether pid names a process that has not ended.
func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false // gone (or never ours to see)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}

func cmdExe(t *testing.T) string {
	t.Helper()
	root := os.Getenv("SystemRoot")
	if root == "" {
		t.Skip("SystemRoot is not set")
	}
	return filepath.Join(root, "System32", "cmd.exe")
}

// TestExecRunnerCmdExe: a command run through cmd.exe /c returns its exit
// code and output.
func TestExecRunnerCmdExe(t *testing.T) {
	res, err := newExecRunner().Run(context.Background(), []string{cmdExe(t), "/c", "echo hello& exit 3"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.exited || res.exitCode != 3 || string(res.stdout) != "hello\r\n" {
		t.Fatalf("result exited %v code %d stdout %q", res.exited, res.exitCode, res.stdout)
	}
}

// TestExecRunnerRefusesAProgramWithoutExtension: Windows would look for
// the name with an extension added, which is another file than the one
// the checker checked.
func TestExecRunnerRefusesAProgramWithoutExtension(t *testing.T) {
	dir := t.TempDir()
	prog := filepath.Join(dir, "check")
	if err := os.WriteFile(prog, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newExecRunner().Run(context.Background(), []string{prog}); err == nil {
		t.Fatal("a program without extension was started")
	}
}
