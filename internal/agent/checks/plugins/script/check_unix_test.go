//go:build !windows

package script

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestJudgeUnix(t *testing.T) {
	rootDir := unixFile{uid: 0, mode: fs.ModeDir | 0o755}
	cases := []struct {
		name      string
		file, dir unixFile
		want      string // "" passes
	}{
		{"root file, root dir", unixFile{0, 0o755}, rootDir, ""},
		{"execute for the owner only", unixFile{0, 0o700}, rootDir, ""},
		{"setuid is no write right", unixFile{0, fs.ModeSetuid | 0o755}, rootDir, ""},
		{"owned by a user", unixFile{1000, 0o755}, rootDir, "owned by uid 1000, not by root"},
		{"group-writable", unixFile{0, 0o775}, rootDir, "writable by group or others"},
		{"other-writable", unixFile{0, 0o757}, rootDir, "writable by group or others"},
		{"not executable", unixFile{0, 0o644}, rootDir, "not executable"},
		{"directory owned by a user", unixFile{0, 0o755}, unixFile{1000, fs.ModeDir | 0o755}, "its directory is owned by uid 1000, not by root"},
		{"group-writable directory", unixFile{0, 0o755}, unixFile{0, fs.ModeDir | 0o775}, "its directory is writable by group or others"},
		{"sticky world-writable directory", unixFile{0, 0o755}, unixFile{0, fs.ModeDir | fs.ModeSticky | 0o777}, "its directory is writable by group or others"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := judgeUnix(tc.file, tc.dir)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if !errors.Is(err, errRefused) || err.Error() != tc.want {
				t.Fatalf("got %v, want the refusal %q", err, tc.want)
			}
		})
	}
}

// rootOwnedShell is /bin/sh resolved, skipping the test unless it and its
// directory are root-owned and writable by nobody else.
func rootOwnedShell(t *testing.T) string {
	t.Helper()
	sh, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil {
		t.Skipf("resolve /bin/sh: %v", err)
	}
	for _, p := range []string{sh, filepath.Dir(sh)} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Skipf("stat %s: %v", p, err)
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != 0 || fi.Mode().Perm()&0o022 != 0 {
			t.Skipf("%s is not root-owned and closed to others", p)
		}
	}
	return sh
}

func TestFileCheckerAcceptsARootOwnedProgram(t *testing.T) {
	sh := rootOwnedShell(t)
	if err := (fileChecker{}).Check(sh); err != nil {
		t.Fatalf("Check(%s): %v", sh, err)
	}
}

// TestFileCheckerResolvesSymlinks: a link is judged by its target and the
// target's directory; the link's own directory does not matter.
func TestFileCheckerResolvesSymlinks(t *testing.T) {
	sh := rootOwnedShell(t)
	dir := t.TempDir()
	link := filepath.Join(dir, "sh")
	if err := os.Symlink(sh, link); err != nil {
		t.Fatal(err)
	}
	if err := (fileChecker{}).Check(link); err != nil {
		t.Fatalf("a link to %s: %v", sh, err)
	}

	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	evil := filepath.Join(dir, "evil")
	if err := os.Symlink(target, evil); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		if err := (fileChecker{}).Check(evil); !errors.Is(err, errRefused) {
			t.Fatalf("a link to a file the user owns: %v, want a refusal", err)
		}
	}
}

// TestFileCheckerRefusesUserFiles: running as a user, every file the test
// makes belongs to that user; each is refused.
func TestFileCheckerRefusesUserFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a temp file is root-owned (judgeUnix covers the rules)")
	}
	dir := t.TempDir()
	for _, mode := range []os.FileMode{0o755, 0o775, 0o777, 0o644} {
		p := filepath.Join(dir, "f"+mode.String())
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		err := (fileChecker{}).Check(p)
		if !errors.Is(err, errRefused) {
			t.Errorf("mode %v: %v, want a refusal", mode, err)
		}
	}
}

func TestFileCheckerNotAFile(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "nothing"), dangling); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, fifo, filepath.Join(dir, "missing"), filepath.Join(file, "below"), dangling, "/dev/null"} {
		if err := (fileChecker{}).Check(p); !errors.Is(err, errNotFile) || errors.Is(err, errRefused) {
			t.Errorf("Check(%s) = %v, want errNotFile", p, err)
		}
	}
}

// TestFileCheckerCouldNotCheck: a path the checker cannot resolve (a
// directory it may not search) is neither a refusal nor "no file": the
// plugin fails closed on it.
func TestFileCheckerCouldNotCheck(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root searches any directory")
	}
	dir := filepath.Join(t.TempDir(), "closed")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "x")
	if err := os.WriteFile(p, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	err := (fileChecker{}).Check(p)
	if err == nil || errors.Is(err, errNotFile) || errors.Is(err, errRefused) {
		t.Fatalf("Check = %v, want an error that is neither errNotFile nor a refusal", err)
	}
}
