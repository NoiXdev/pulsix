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
// refused by its syntax, as program and as place, before anything asks
// the host it names (an unresolvable one here, which a stat would wait
// on); a device path is refused as a program.
func TestFileCheckerRefusesNetworkPathsWithoutAStat(t *testing.T) {
	for _, p := range []string{`\\pulsix-test.invalid\share\x.ps1`, `//pulsix-test.invalid/share/x.ps1`, `\\pulsix-test.invalid\share`} {
		start := time.Now()
		if _, err := (fileChecker{}).Check(p); !errors.Is(err, errRefused) || err.Error() != textNetworkPath {
			t.Errorf("Check(%s) = %v, want the refusal %q", p, err, textNetworkPath)
		}
		if err := (fileChecker{}).CheckPlace(p); !errors.Is(err, errRefused) || err.Error() != textNetworkPath {
			t.Errorf("CheckPlace(%s) = %v, want the refusal %q", p, err, textNetworkPath)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("%s took %s: a network lookup?", p, d)
		}
	}
	if _, err := (fileChecker{}).Check(`\\.\PhysicalDrive0`); !errors.Is(err, errRefused) {
		t.Errorf("Check(a device) = %v, want a refusal", err)
	}
	for _, p := range []string{`\\?\C:\Windows\System32\cmd.exe`, `\\.\C:\Windows\System32\cmd.exe`, `\\?\UNC\pulsix-test.invalid\share\x.ps1`} {
		if _, err := (fileChecker{}).Check(p); !errors.Is(err, errRefused) || err.Error() != textDeviceForm {
			t.Errorf("Check(%s) = %v, want the refusal %q", p, err, textDeviceForm)
		}
	}
}

// TestCollectWindowsPathArguments: through the real checker, a network
// path argument is refused with its path, while switches that look like
// paths and device paths do not stop the run.
func TestCollectWindowsPathArguments(t *testing.T) {
	cmd := cmdExe(t)
	// A switch like /c reads as a rooted path on the system drive, whose
	// root is then judged as the place of a missing file.
	root, err := workDir()
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := trustedSIDs()
	if err != nil {
		t.Fatal(err)
	}
	if err := judgePath(root, "its directory ", placeMask, trusted); err != nil {
		t.Skipf("the system drive's root does not pass here: %v", err)
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

// TestJudgePlaceSecurity: a missing file's own directory may let others
// add subdirectories (as C:\ lets every user), which puts no file at the
// name; a right to add a file there is refused.
func TestJudgePlaceSecurity(t *testing.T) {
	trusted, err := trustedSIDs()
	if err != nil {
		t.Fatal(err)
	}
	cDrive := "O:" + trustedInstallerSID + "D:(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)(A;OICI;0x1200a9;;;BU)(A;CI;0x4;;;BU)(A;OICIIO;SDGRGWGX;;;AU)(A;;0x4;;;AU)"
	for sddl, want := range map[string]string{
		cDrive:                             "",
		"O:BAD:(A;;FA;;;BA)(A;;0x2;;;BU)":  "its directory lets S-1-5-32-545 change it",
		"O:BAD:(A;;FA;;;BA)(A;;GW;;;BU)":   "its directory lets S-1-5-32-545 change it",
		"O:BAD:(A;;FA;;;BA)(A;;0x40;;;BU)": "its directory lets S-1-5-32-545 change it",
		"O:BUD:(A;;FA;;;BA)":               "its directory is owned by S-1-5-32-545",
	} {
		sd, err := windows.SecurityDescriptorFromString(sddl)
		if err != nil {
			t.Fatalf("SDDL %s: %v", sddl, err)
		}
		err = judgeSecurity(sd, "its directory ", placeMask, trusted)
		if want == "" {
			if err != nil {
				t.Errorf("%s refused: %v", sddl, err)
			}
			continue
		}
		if !errors.Is(err, errRefused) || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: got %v, want a refusal %q", sddl, err, want)
		}
	}
	// The deepest existing directory above a missing one is judged with
	// every write right: adding a subdirectory there creates the way.
	sd, err := windows.SecurityDescriptorFromString(cDrive)
	if err != nil {
		t.Fatal(err)
	}
	if err := judgeSecurity(sd, "a directory above it ", writeMask, trusted); !errors.Is(err, errRefused) {
		t.Errorf("C: style with every write right: %v, want a refusal", err)
	}
}

// TestFileCheckerPlace: a missing file in a directory users may change is
// refused for that directory, one below a missing directory for the
// deepest one that exists; a missing file in System32 passes.
func TestFileCheckerPlace(t *testing.T) {
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
	for p, want := range map[string]string{
		filepath.Join(dir, "missing.ps1"):           "its directory ",
		filepath.Join(dir, "missing", "deeper.ps1"): "a directory above it ",
	} {
		err := (fileChecker{}).CheckPlace(p)
		if !errors.Is(err, errRefused) || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("CheckPlace(%s) = %v, want a refusal starting %q", p, err, want)
		}
	}
	missing := filepath.Join(filepath.Dir(cmdExe(t)), "pulsix-test-missing.exe")
	if err := (fileChecker{}).CheckPlace(missing); err != nil {
		t.Errorf("CheckPlace(%s) = %v, want it to pass", missing, err)
	}
}

// TestFileCheckerPlaceFollowsLinks: the place is judged where links and
// junctions on the way lead, not where they lie, and one that leads to
// nothing is refused.
func TestFileCheckerPlaceFollowsLinks(t *testing.T) {
	system32 := filepath.Dir(cmdExe(t))
	dir := t.TempDir()
	links := map[string]string{} // link -> how it was made
	if l := filepath.Join(dir, "symlink"); os.Symlink(system32, l) == nil {
		links[l] = "symlink"
	}
	if l := filepath.Join(dir, "junction"); exec.Command(cmdExe(t), "/c", "mklink", "/J", l, system32).Run() == nil {
		links[l] = "junction"
	}
	if len(links) == 0 {
		t.Skip("neither a symlink nor a junction could be made here")
	}
	for l, kind := range links {
		for _, p := range []string{l, filepath.Join(l, "pulsix-test-missing.exe"), filepath.Join(l, "missing", "deeper.exe")} {
			if err := (fileChecker{}).CheckPlace(p); err != nil {
				t.Errorf("%s: CheckPlace(%s) = %v, want it to pass", kind, p, err)
			}
		}
	}

	nothing := filepath.Join(dir, "nothing")
	dangling := map[string]string{}
	if l := filepath.Join(dir, "dangling-symlink"); os.Symlink(nothing, l) == nil {
		dangling[l] = "symlink"
	}
	if l := filepath.Join(dir, "dangling-junction"); exec.Command(cmdExe(t), "/c", "mklink", "/J", l, nothing).Run() == nil {
		dangling[l] = "junction"
	}
	for l, kind := range dangling {
		for _, p := range []string{l, filepath.Join(l, "x.ps1")} {
			if err := (fileChecker{}).CheckPlace(p); !errors.Is(err, errRefused) || err.Error() != textDanglingLink {
				t.Errorf("%s: CheckPlace(%s) = %v, want the refusal %q", kind, p, err, textDanglingLink)
			}
		}
	}
}
