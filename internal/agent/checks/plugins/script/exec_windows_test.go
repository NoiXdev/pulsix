//go:build windows

package script

import (
	"context"
	"errors"
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
	res, err := runArgv(context.Background(), newExecRunner(), []string{cmdExe(t), "/c", "echo hello& exit 3"})
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
	if _, err := runArgv(context.Background(), newExecRunner(), []string{prog}); !errors.Is(err, errNoExtension) {
		t.Fatalf("Run = %v, want errNoExtension", err)
	}
}

// TestFileArgument: besides absolute paths, Windows reads rooted paths
// without a drive and drive paths without a root as files, so both are
// checked, as the absolute path the command will open. Device and
// namespace paths and a \\x without a share (a switch like //Nologo) are
// left alone without a stat; a network path, and a file path in device
// form, go to the checker, which refuses them by their syntax.
func TestFileArgument(t *testing.T) {
	dir, err := workDir()
	if err != nil {
		t.Fatal(err)
	}
	sys := filepath.VolumeName(dir)
	cases := []struct {
		arg, want string
		ok        bool
	}{
		{`C:\scripts\x.ps1`, `C:\scripts\x.ps1`, true},
		{`\\host\share\x.ps1`, `\\host\share\x.ps1`, true},
		{`\scripts\x.ps1`, sys + `\scripts\x.ps1`, true},
		{`/scripts/x.ps1`, sys + `/scripts/x.ps1`, true},
		{`D:x.ps1`, `D:\x.ps1`, true},
		{`//host/share/x.ps1`, `//host/share/x.ps1`, true},
		{`\\.\PhysicalDrive0`, "", false},
		{`\\?\C:\scripts\x.ps1`, `\\?\C:\scripts\x.ps1`, true},
		{`\\.\UNC\host\share\x.ps1`, `\\.\UNC\host\share\x.ps1`, true},
		{`//./pipe/x`, "", false},
		{`//?/C:/x`, `//?/C:/x`, true},
		{`//Nologo`, "", false},
		{`//B`, "", false},
		{`\\host`, "", false},
		{`-File`, "", false},
		{`x.ps1`, "", false},
		{``, "", false},
	}
	for _, tc := range cases {
		got, ok, err := fileArgument(tc.arg)
		if err != nil || ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("fileArgument(%q) = %q, %v, %v; want %q, %v", tc.arg, got, ok, err, tc.want, tc.ok)
		}
	}
}
