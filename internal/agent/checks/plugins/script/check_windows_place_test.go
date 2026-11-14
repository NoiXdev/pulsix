//go:build windows

package script

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestFileCheckerRefusesNetworkPathsWithoutAStat: a network path is
// refused by its syntax before anything asks the host it names (an
// unresolvable one here, which a stat would wait on); a device path and a
// path in device form are refused as a program.
func TestFileCheckerRefusesNetworkPathsWithoutAStat(t *testing.T) {
	for _, p := range []string{`\\pulsix-test.invalid\share\x.ps1`, `//pulsix-test.invalid/share/x.ps1`, `\\pulsix-test.invalid\share`} {
		start := time.Now()
		if err := (fileChecker{}).Check(p); !errors.Is(err, errRefused) || err.Error() != textNetworkPath {
			t.Errorf("Check(%s) = %v, want the refusal %q", p, err, textNetworkPath)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("%s took %s: a network lookup?", p, d)
		}
	}
	if err := (fileChecker{}).Check(`\\.\PhysicalDrive0`); !errors.Is(err, errRefused) {
		t.Errorf("Check(a device) = %v, want a refusal", err)
	}
	for _, p := range []string{`\\?\C:\Windows\System32\cmd.exe`, `\\.\C:\Windows\System32\cmd.exe`, `\\?\UNC\pulsix-test.invalid\share\x.ps1`, `\\?\HarddiskVolume1\Windows\System32\cmd.exe`} {
		if err := (fileChecker{}).Check(p); !errors.Is(err, errRefused) || err.Error() != textDeviceForm {
			t.Errorf("Check(%s) = %v, want the refusal %q", p, err, textDeviceForm)
		}
	}
}

// TestCollectWindowsPathArguments: through the real checker, a network
// path argument is refused with its path, while switches that look like
// paths and device paths do not stop the run.
func TestCollectWindowsPathArguments(t *testing.T) {
	cmd := cmdExe(t)
	// A switch like /c reads as a rooted path on the system drive, a
	// missing name in its root.
	root, err := workDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, use := range []dirUse{useLookup, useMissing} {
		if err := (osFS{}).judgeDir(root, use, "its directory"); err != nil {
			t.Skipf("the system drive's root does not pass here: %v", err)
		}
	}
	r := exitedWith(0, "OK")
	cmds := fakeCommands{argv: map[string][]string{
		"net":    {cmd, "/c", `\\pulsix-test.invalid\share\x.cmd`},
		"switch": {cmd, "//Nologo", "/c", `\\.\pipe\x`, `\\.\PhysicalDrive0`, "/T:30"},
		"device": {cmd, "/c", `\\?\C:\scripts\x.cmd`},
	}}
	got := collect(t, newPlugin(r, fileChecker{}), map[string]any{"commands": []string{"net", "switch", "device"}}, cmds)
	if len(got) != 8 || got[0] != `net script.status "unknown"` ||
		got[1] != `net script.output "\\\\pulsix-test.invalid\\share\\x.cmd: `+textNetworkPath+`"` ||
		got[2] != `switch script.status "ok"` ||
		got[6] != `device script.status "unknown"` ||
		got[7] != `device script.output "\\\\?\\C:\\scripts\\x.cmd: `+textDeviceForm+`"` {
		t.Fatalf("got %q", got)
	}
}

// TestJudgeDirMasks: a directory a name is looked up in may let others
// add entries (as C:\ lets every user), one where the last name is
// missing may let them add subdirectories, which puts no file at the
// name; the directory holding the file, or one where a directory on the
// way is missing, may let them change nothing.
func TestJudgeDirMasks(t *testing.T) {
	trusted, err := trustedSIDs()
	if err != nil {
		t.Fatal(err)
	}
	cDrive := "O:" + trustedInstallerSID + "D:(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)(A;OICI;0x1200a9;;;BU)(A;CI;0x4;;;BU)(A;OICIIO;SDGRGWGX;;;AU)(A;;0x4;;;AU)"
	cases := []struct {
		sddl string
		use  dirUse
		want string // "" passes, else the refusal's start
	}{
		{cDrive, useLookup, ""},
		{cDrive, useMissing, ""},
		{cDrive, useMissingWay, "its directory lets S-1-5-32-545 change it"},
		{cDrive, useHolds, "its directory lets S-1-5-32-545 change it"},
		{"O:BAD:(A;;FA;;;BA)(A;;0x2;;;BU)", useLookup, ""},
		{"O:BAD:(A;;FA;;;BA)(A;;0x2;;;BU)", useMissing, "its directory lets S-1-5-32-545 change it"},
		{"O:BAD:(A;;FA;;;BA)(A;;GW;;;BU)", useMissing, "its directory lets S-1-5-32-545 change it"},
		{"O:BAD:(A;;FA;;;BA)(A;;0x40;;;BU)", useLookup, "its directory lets S-1-5-32-545 change it"},
		{"O:BUD:(A;;FA;;;BA)", useLookup, "its directory is owned by S-1-5-32-545"},
	}
	for _, tc := range cases {
		sd, err := windows.SecurityDescriptorFromString(tc.sddl)
		if err != nil {
			t.Fatalf("SDDL %s: %v", tc.sddl, err)
		}
		err = judgeSecurity(sd, "its directory ", dirMask(tc.use), trusted)
		if tc.want == "" {
			if err != nil {
				t.Errorf("%s for use %d refused: %v", tc.sddl, tc.use, err)
			}
			continue
		}
		if !errors.Is(err, errRefused) || !strings.HasPrefix(err.Error(), tc.want) {
			t.Errorf("%s for use %d: got %v, want a refusal %q", tc.sddl, tc.use, err, tc.want)
		}
	}
}

// openFolder is a folder in the test's temp directory that Users may
// change.
func openFolder(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "open")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;BA)(A;OICI;FA;;;BU)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatalf("set the access list: %v", err)
	}
	return dir
}

// TestFileCheckerRefusesAMissingFileInAnOpenFolder: a missing file, or
// one below a missing folder, in a folder users may change is refused.
func TestFileCheckerRefusesAMissingFileInAnOpenFolder(t *testing.T) {
	dir := openFolder(t)
	for _, p := range []string{filepath.Join(dir, "missing.ps1"), filepath.Join(dir, "missing", "deeper.ps1")} {
		if err := (fileChecker{}).Check(p); !errors.Is(err, errRefused) {
			t.Errorf("Check(%s) = %v, want a refusal", p, err)
		}
	}
}

// links makes a symlink and a junction at name in dir to target, those
// the machine lets the test make, and returns them with their kind.
func links(t *testing.T, dir, name, target string) map[string]string {
	t.Helper()
	made := map[string]string{}
	if l := filepath.Join(dir, name+"-symlink"); os.Symlink(target, l) == nil {
		made[l] = "symlink"
	}
	if l := filepath.Join(dir, name+"-junction"); exec.Command(cmdExe(t), "/c", "mklink", "/J", l, target).Run() == nil {
		made[l] = "junction"
	}
	return made
}

// TestFileCheckerRefusesLinksInAUserFolder: a symlink or junction in the
// test's temp folder, which the user may re-point, is refused though it
// leads to System32, and so is one that leads to nothing.
func TestFileCheckerRefusesLinksInAUserFolder(t *testing.T) {
	system32 := filepath.Dir(cmdExe(t))
	dir := t.TempDir()
	made := links(t, dir, "system32", system32)
	for l, kind := range links(t, dir, "dangling", filepath.Join(dir, "nothing")) {
		made[l] = kind
	}
	if len(made) == 0 {
		t.Skip("neither a symlink nor a junction could be made here")
	}
	for l, kind := range made {
		for _, p := range []string{l, filepath.Join(l, "cmd.exe"), filepath.Join(l, "pulsix-test-missing.exe")} {
			if err := (fileChecker{}).Check(p); !errors.Is(err, errRefused) {
				t.Errorf("%s: Check(%s) = %v, want a refusal", kind, p, err)
			}
		}
	}
	p := newPlugin(nil, fileChecker{})
	for l := range made {
		arg := filepath.Join(l, "evil.ps1")
		if refusal := p.checkFiles([]string{cmdExe(t), "/c", arg}); !strings.HasPrefix(refusal, arg+": ") {
			t.Errorf("checkFiles(%s) = %q, want a refusal naming it", arg, refusal)
		}
	}
}

// TestFileCheckerFollowsSystemLinks: a link Windows keeps for old paths,
// owned by the system, leads to a folder: no file, and nothing refused on
// the way.
func TestFileCheckerFollowsSystemLinks(t *testing.T) {
	tried := 0
	for _, p := range []string{`C:\Users\All Users`, `C:\Documents and Settings`} {
		if fi, err := os.Lstat(p); err != nil || fi.Mode().Type() == 0 {
			continue
		}
		err := (fileChecker{}).Check(p)
		switch {
		case errors.Is(err, errRefused):
			t.Errorf("Check(%s) = %v, want no refusal", p, err)
		case errors.Is(err, errNotFile):
			tried++
		default:
			t.Logf("Check(%s) = %v: the link cannot be read here", p, err)
		}
	}
	if tried == 0 {
		t.Skip("no readable system link here")
	}
}
