//go:build windows

package script

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// adminOnly is a protected access list that gives SYSTEM and
// Administrators full control and nobody else anything.
const adminOnly = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

// secure gives the entry at p (itself, a link or junction not followed)
// the owner Administrators and the protected access list sddl.
func secure(t *testing.T, p, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatalf("SDDL %s: %v", sddl, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	u, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(u, windows.WRITE_OWNER|windows.WRITE_DAC|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatalf("open %s: %v", p, err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	if err := windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		admins, nil, dacl, nil); err != nil {
		t.Fatalf("secure %s: %v", p, err)
	}
}

// adminFolder is a new folder in the system drive's root that only
// SYSTEM and Administrators may change, removed when the test ends. It
// skips unless the test runs elevated (CI runners do), which making it
// and owning it by Administrators needs.
func adminFolder(t *testing.T) string {
	t.Helper()
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("not elevated: cannot make a folder only administrators may change")
	}
	root, err := workDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(root, "pulsix-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeLinksThenAll(t, dir) })
	secure(t, dir, adminOnly)
	return dir
}

// removeLinksThenAll removes every entry of dir that is a link or junction
// by itself first, so nothing walks into System32, then dir. Lstat reports
// a junction as a directory that is also irregular, so any type bit beside
// the directory bit marks a link here.
func removeLinksThenAll(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if fi, err := os.Lstat(p); err == nil && fi.Mode().Type()&^os.ModeDir != 0 {
			if err := os.Remove(p); err != nil {
				t.Errorf("remove the link %s: %v", p, err)
			}
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Errorf("remove %s: %v", dir, err)
	}
}

// dirSymlink makes a directory symlink at link with target as written.
func dirSymlink(t *testing.T, link, target string) {
	t.Helper()
	l, err := windows.UTF16PtrFromString(link)
	if err != nil {
		t.Fatal(err)
	}
	tg, err := windows.UTF16PtrFromString(target)
	if err != nil {
		t.Fatal(err)
	}
	const allowUnprivileged = 0x2 // SYMBOLIC_LINK_FLAG_ALLOW_UNPRIVILEGED_CREATE
	if err := windows.CreateSymbolicLink(l, tg, windows.SYMBOLIC_LINK_FLAG_DIRECTORY|allowUnprivileged); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
}

// TestFileCheckerFollowsAdminLinks: links and junctions to System32 in a
// folder only administrators may change, owned and closed like it, pass:
// absolute, relative and rooted symlinks and a junction. A link whose own
// access list lets Users write it is refused for that link; a folder
// Users may change is refused when the path names it, and a missing file
// written with its drive is refused in it.
func TestFileCheckerFollowsAdminLinks(t *testing.T) {
	dir := adminFolder(t)
	system32 := filepath.Dir(cmdExe(t))
	vol := filepath.VolumeName(system32)
	rel, err := filepath.Rel(dir, system32)
	if err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"absolute": system32,
		"relative": rel,
		"rooted":   system32[len(vol):],
	}
	for name, target := range links {
		dirSymlink(t, filepath.Join(dir, name), target)
		secure(t, filepath.Join(dir, name), adminOnly)
	}
	junction := filepath.Join(dir, "junction")
	if out, err := exec.Command(cmdExe(t), "/c", "mklink", "/J", junction, system32).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v: %s", err, out)
	}
	secure(t, junction, adminOnly)
	links["junction"] = system32

	for name := range links {
		l := filepath.Join(dir, name)
		if err := (fileChecker{}).Check(filepath.Join(l, "cmd.exe"), formExact); err != nil {
			t.Errorf("%s: Check(%s) = %v, want it to pass", name, filepath.Join(l, "cmd.exe"), err)
		}
		if err := (fileChecker{}).Check(l, formExact); !errors.Is(err, errNotFile) || errors.Is(err, errRefused) {
			t.Errorf("%s: Check(%s) = %v, want errNotFile (a folder)", name, l, err)
		}
	}

	open := filepath.Join(dir, "open-link")
	dirSymlink(t, open, system32)
	secure(t, open, "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;BU)")
	want := "a link on its way lets S-1-5-32-545 change it"
	if err := (fileChecker{}).Check(filepath.Join(open, "cmd.exe"), formExact); !errors.Is(err, errRefused) || err.Error() != want {
		t.Errorf("Check(through a link Users may write) = %v, want the refusal %q", err, want)
	}

	folder := filepath.Join(dir, "open")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	secure(t, folder, "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;BU)")
	for p, want := range map[string]string{
		folder:                               "the directory it names lets S-1-5-32-545 change it",
		filepath.Join(folder, "missing.ps1"): "its directory lets S-1-5-32-545 change it",
	} {
		if err := (fileChecker{}).Check(p, formExact); !errors.Is(err, errRefused) || err.Error() != want {
			t.Errorf("Check(%s) = %v, want the refusal %q", p, err, want)
		}
	}
}
