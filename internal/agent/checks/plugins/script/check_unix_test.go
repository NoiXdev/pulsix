//go:build !windows

package script

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestJudgeUnixFile(t *testing.T) {
	cases := []struct {
		name string
		file unixFile
		want string // "" passes
	}{
		{"root, closed", unixFile{0, 0o755}, ""},
		{"execute for the owner only", unixFile{0, 0o700}, ""},
		{"no execute bit is no refusal", unixFile{0, 0o644}, ""},
		{"setuid is no write right", unixFile{0, fs.ModeSetuid | 0o755}, ""},
		{"owned by a user", unixFile{1000, 0o755}, "owned by uid 1000, not by root"},
		{"group-writable", unixFile{0, 0o775}, "writable by group or others"},
		{"other-writable", unixFile{0, 0o757}, "writable by group or others"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRefusal(t, judgeUnixFile(tc.file), tc.want)
		})
	}
}

func TestJudgeUnixDir(t *testing.T) {
	cases := []struct {
		name string
		dir  unixFile
		want string // "" passes
	}{
		{"root, closed", unixFile{0, fs.ModeDir | 0o755}, ""},
		{"owned by a user", unixFile{1000, fs.ModeDir | 0o755}, "its directory is owned by uid 1000, not by root"},
		{"group-writable", unixFile{0, fs.ModeDir | 0o775}, "its directory is writable by group or others"},
		{"sticky world-writable", unixFile{0, fs.ModeDir | fs.ModeSticky | 0o777}, "its directory is writable by group or others"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRefusal(t, judgeUnixDir(tc.dir, "its directory"), tc.want)
		})
	}
}

func assertRefusal(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("refused: %v", err)
		}
		return
	}
	if !errors.Is(err, errRefused) || err.Error() != want {
		t.Fatalf("got %v, want the refusal %q", err, want)
	}
}

// rootOwnedShell is /bin/sh resolved, skipping the test unless it and
// every directory above it are root-owned and writable by nobody else.
func rootOwnedShell(t *testing.T) string {
	t.Helper()
	sh, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil {
		t.Skipf("resolve /bin/sh: %v", err)
	}
	for p := sh; ; p = filepath.Dir(p) {
		rootOwnedAndClosed(t, p)
		if filepath.Dir(p) == p {
			return sh
		}
	}
}

// rootOwnedAndClosed skips the test unless path (a file or a directory) is
// root-owned and writable by nobody else.
func rootOwnedAndClosed(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Skipf("stat %s: %v", path, err)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != 0 || fi.Mode().Perm()&0o022 != 0 {
		t.Skipf("%s is not root-owned and closed to others", path)
	}
}

// asUser skips a test whose temp files would be root-owned.
func asUser(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: a temp directory is root-owned")
	}
}

func TestFileCheckerAcceptsARootOwnedProgram(t *testing.T) {
	sh := rootOwnedShell(t)
	if err := (fileChecker{}).Check(sh); err != nil {
		t.Fatalf("Check(%s) = %v", sh, err)
	}
}

// TestFileCheckerFollowsSystemLinks: a path through a link the system
// keeps in a root-owned directory to a root-owned place passes (macOS:
// /etc -> private/etc; merged-usr Linux: /bin -> usr/bin; Debian:
// /bin/sh -> dash).
func TestFileCheckerFollowsSystemLinks(t *testing.T) {
	tried := 0
	for _, p := range []string{"/bin/sh", "/etc/hosts", "/etc/passwd"} {
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil || resolved == p || !rootOwnedAndClosedChain(p) || !rootOwnedAndClosedChain(resolved) {
			continue
		}
		tried++
		if err := (fileChecker{}).Check(p); err != nil {
			t.Errorf("Check(%s) (leads to %s) = %v, want it to pass", p, resolved, err)
		}
	}
	if tried == 0 {
		t.Skip("no system link to a root-owned file here")
	}
}

// rootOwnedAndClosedChain reports whether path and every directory above
// it, as written, are root-owned and writable by nobody else (stat
// follows a link on the way, lstat would judge the link).
func rootOwnedAndClosedChain(path string) bool {
	for p := path; ; p = filepath.Dir(p) {
		fi, err := os.Stat(p)
		if err != nil {
			return false
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != 0 || fi.Mode().Perm()&0o022 != 0 {
			return false
		}
		if filepath.Dir(p) == p {
			return true
		}
	}
}

// TestFileCheckerRefusesLinksInAUserDirectory: a link the user may
// re-point is refused, wherever it leads (a root-owned shell, a
// root-owned directory), and so is a chain whose links lie in the user's
// directory. The test's temp directory belongs to the user.
func TestFileCheckerRefusesLinksInAUserDirectory(t *testing.T) {
	asUser(t)
	sh := rootOwnedShell(t)
	dir := t.TempDir()
	link := filepath.Join(dir, "sh")
	binLink := filepath.Join(dir, "bin")
	chain := filepath.Join(dir, "chain")
	for target, l := range map[string]string{sh: link, filepath.Dir(sh): binLink, link: chain} {
		if err := os.Symlink(target, l); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{link, binLink, filepath.Join(binLink, filepath.Base(sh)), filepath.Join(binLink, "evil.sh"), chain} {
		if err := (fileChecker{}).Check(p); !errors.Is(err, errRefused) {
			t.Errorf("Check(%s) = %v, want a refusal", p, err)
		}
	}
}

// TestFileCheckerRefusesUserFiles: running as a user, every file the test
// makes belongs to that user; each is refused, as is a missing file, a
// fifo and a directory in the user's directory.
func TestFileCheckerRefusesUserFiles(t *testing.T) {
	asUser(t)
	dir := t.TempDir()
	var paths []string
	for _, mode := range []os.FileMode{0o755, 0o775, 0o777, 0o644} {
		p := filepath.Join(dir, "f"+mode.String())
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	paths = append(paths, fifo, dir, filepath.Join(dir, "missing.py"), filepath.Join(dir, "missing", "deeper.py"))
	for _, p := range paths {
		if err := (fileChecker{}).Check(p); !errors.Is(err, errRefused) {
			t.Errorf("Check(%s) = %v, want a refusal", p, err)
		}
	}
}

// TestFileCheckerNamesNoFile: under root-owned directories, a missing
// name, a directory, the root, a device and a path below a file name no
// file, and pass as such.
func TestFileCheckerNamesNoFile(t *testing.T) {
	sh := rootOwnedShell(t)
	paths := []string{
		"/", filepath.Dir(sh), filepath.Join(sh, "below"),
		"/pulsix-test-missing", "/pulsix-test-missing/nfs",
		filepath.Join(filepath.Dir(sh), "pulsix-test-missing"),
	}
	if rootOwnedAndClosedChain("/dev") {
		paths = append(paths, "/dev/null")
	}
	for _, p := range paths {
		if err := (fileChecker{}).Check(p); !errors.Is(err, errNotFile) || errors.Is(err, errRefused) {
			t.Errorf("Check(%s) = %v, want errNotFile", p, err)
		}
	}
}

// TestFileCheckerStandardStreams: on Linux /dev/stderr leads through
// /proc to whatever the agent's standard error is (a socket, a pipe):
// the kernel's, no file to judge.
func TestFileCheckerStandardStreams(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/dev/stderr leads through /proc on Linux only")
	}
	if !rootOwnedAndClosedChain("/dev") {
		t.Skip("/dev is not root-owned and closed here")
	}
	for _, p := range []string{"/dev/stderr", "/dev/stdout", "/proc/self/status"} {
		if err := (fileChecker{}).Check(p); !errors.Is(err, errNotFile) || errors.Is(err, errRefused) {
			t.Errorf("Check(%s) = %v, want errNotFile", p, err)
		}
	}
}

// TestFileCheckerCouldNotCheck: a name the checker may not look up (in a
// root-only directory) is neither a refusal nor "no file": the plugin
// fails closed on it.
func TestFileCheckerCouldNotCheck(t *testing.T) {
	asUser(t)
	for _, dir := range []string{"/root", "/var/root"} {
		if _, err := os.Lstat(filepath.Join(dir, "pulsix-test")); !errors.Is(err, fs.ErrPermission) {
			continue
		}
		err := (fileChecker{}).Check(filepath.Join(dir, "pulsix-test"))
		if err == nil || errors.Is(err, errNotFile) || errors.Is(err, errRefused) {
			t.Fatalf("Check = %v, want an error that is neither errNotFile nor a refusal", err)
		}
		return
	}
	t.Skip("no root-only directory to test with here")
}

// TestCheckFilesRefusesTheSwapProbe: a link in the user's directory to a
// root-owned directory, which the user could re-point after the check,
// does not let a script below it pass, missing or not; nor does a link
// loop there.
func TestCheckFilesRefusesTheSwapProbe(t *testing.T) {
	asUser(t)
	sh := rootOwnedShell(t)
	user := t.TempDir()
	link := filepath.Join(user, "bin")
	if err := os.Symlink(filepath.Dir(sh), link); err != nil {
		t.Fatal(err)
	}
	a, b := filepath.Join(user, "a"), filepath.Join(user, "b")
	if err := errors.Join(os.Symlink(b, a), os.Symlink(a, b)); err != nil {
		t.Fatal(err)
	}
	p := newPlugin(nil, fileChecker{})
	for _, arg := range []string{filepath.Join(link, "evil.sh"), filepath.Join(link, filepath.Base(sh)), filepath.Join(a, "x")} {
		refusal := p.checkFiles([]string{sh, arg})
		if !strings.HasPrefix(refusal, arg+": ") {
			t.Errorf("checkFiles(%s) = %q, want a refusal naming it", arg, refusal)
		}
	}
	prog := filepath.Join(link, filepath.Base(sh))
	if refusal := p.checkFiles([]string{prog, "-c", "true"}); !strings.HasPrefix(refusal, prog+": ") {
		t.Errorf("a program through the link: %q, want a refusal naming it", refusal)
	}
}
