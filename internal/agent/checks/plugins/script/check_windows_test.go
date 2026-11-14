//go:build windows

package script

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestJudgeSecurityDescriptor(t *testing.T) {
	cases := []struct {
		name, sddl string
		want       string // "" passes; else a substring of the refusal
	}{
		{"System32 style", "O:" + trustedInstallerSID + "D:PAI(A;;FA;;;" + trustedInstallerSID + ")(A;;0x1200a9;;;BA)(A;;0x1200a9;;;SY)(A;;0x1200a9;;;BU)(A;;0x1200a9;;;AC)", ""},
		{"owner SYSTEM", "O:SYD:(A;;FA;;;SY)(A;;FA;;;BA)(A;;0x1200a9;;;BU)", ""},
		{"owner Administrators", "O:BAD:(A;;FA;;;SY)(A;;FA;;;BA)", ""},
		{"inherit-only entries do not apply", "O:BAD:(A;;FA;;;BA)(A;OICIIO;GA;;;CO)(A;OICIIO;GA;;;BU)", ""},
		{"deny entries grant nothing", "O:BAD:(A;;FA;;;BA)(D;;FA;;;BU)", ""},
		{"owner a user", "O:BUD:(A;;FA;;;BA)", "is owned by S-1-5-32-545"},
		{"no owner", "D:(A;;FA;;;BA)", "is owned by nobody"},
		{"users may write", "O:BAD:(A;;FA;;;BA)(A;;0x2;;;BU)", "lets S-1-5-32-545 change it"},
		{"users may append", "O:BAD:(A;;FA;;;BA)(A;;0x4;;;BU)", "lets S-1-5-32-545"},
		{"write extended attributes", "O:BAD:(A;;0x10;;;AU)", "lets S-1-5-11"},
		{"delete child", "O:BAD:(A;;0x40;;;WD)", "lets S-1-1-0"},
		{"write attributes", "O:BAD:(A;;0x100;;;BU)", "lets S-1-5-32-545"},
		{"delete", "O:BAD:(A;;SD;;;BU)", "lets S-1-5-32-545"},
		{"write DAC", "O:BAD:(A;;WD;;;BU)", "lets S-1-5-32-545"},
		{"write owner", "O:BAD:(A;;WO;;;BU)", "lets S-1-5-32-545"},
		{"generic write", "O:BAD:(A;;GW;;;BU)", "lets S-1-5-32-545"},
		{"generic all", "O:BAD:(A;;GA;;;BU)", "lets S-1-5-32-545"},
		{"full control", "O:BAD:(A;;FA;;;BU)", "lets S-1-5-32-545"},
		{"no access list", "O:BAD:NO_ACCESS_CONTROL", "has no access list"},
	}
	trusted, err := trustedSIDs()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(tc.sddl)
			if err != nil {
				t.Fatalf("SDDL %s: %v", tc.sddl, err)
			}
			err = judgeSecurity(sd, "", writeMask, trusted)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if !errors.Is(err, errRefused) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want a refusal containing %q", err, tc.want)
			}
		})
	}
	sd, err := windows.SecurityDescriptorFromString("O:BUD:(A;;FA;;;BA)")
	if err != nil {
		t.Fatal(err)
	}
	if err := judgeSecurity(sd, "its directory ", writeMask, trusted); err == nil || !strings.HasPrefix(err.Error(), "its directory is owned by") {
		t.Fatalf("directory refusal %v", err)
	}
}

// TestJudgeAncestorSecurity: above the file's own directory only the
// rights to swap an entry count; adding one (as every user may in C:\)
// does not.
func TestJudgeAncestorSecurity(t *testing.T) {
	cases := []struct {
		name, sddl string
		want       string
	}{
		{"C: style", "O:" + trustedInstallerSID + "D:(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)(A;OICI;0x1200a9;;;BU)(A;CI;0x4;;;BU)(A;OICIIO;SDGRGWGX;;;AU)(A;;0x4;;;AU)", ""},
		{"add file", "O:BAD:(A;;0x2;;;BU)", ""},
		{"write attributes", "O:BAD:(A;;0x110;;;BU)", ""},
		{"generic write", "O:BAD:(A;;GW;;;BU)", ""},
		{"delete", "O:BAD:(A;;SD;;;AU)", "a directory above it lets S-1-5-11 change it"},
		{"delete child", "O:BAD:(A;;0x40;;;BU)", "lets S-1-5-32-545"},
		{"write DAC", "O:BAD:(A;;WD;;;BU)", "lets S-1-5-32-545"},
		{"write owner", "O:BAD:(A;;WO;;;BU)", "lets S-1-5-32-545"},
		{"generic all", "O:BAD:(A;;GA;;;BU)", "lets S-1-5-32-545"},
		{"modify", "O:BAD:(A;;0x1301bf;;;AU)", "lets S-1-5-11"},
		{"owner a user", "O:BUD:(A;;FA;;;BA)", "a directory above it is owned by S-1-5-32-545"},
	}
	trusted, err := trustedSIDs()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(tc.sddl)
			if err != nil {
				t.Fatalf("SDDL %s: %v", tc.sddl, err)
			}
			err = judgeSecurity(sd, "a directory above it ", ancestorMask, trusted)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if !errors.Is(err, errRefused) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want a refusal containing %q", err, tc.want)
			}
		})
	}
}

func TestStripPrefix(t *testing.T) {
	for in, want := range map[string]string{
		`\\?\C:\Windows\System32\cmd.exe`: `C:\Windows\System32\cmd.exe`,
		`\\?\UNC\host\share\x.ps1`:        `\\host\share\x.ps1`,
		`C:\x`:                            `C:\x`,
	} {
		if got := stripPrefix(in); got != want {
			t.Errorf("stripPrefix(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestFileCheckerAcceptsCmdExe(t *testing.T) {
	resolved, err := (fileChecker{}).Check(cmdExe(t))
	if err != nil {
		t.Fatalf("Check(cmd.exe): %v", err)
	}
	if !strings.EqualFold(resolved, cmdExe(t)) {
		t.Errorf("resolved to %s, want %s", resolved, cmdExe(t))
	}
}

// TestFileCheckerRefusesAUserFile: a file whose access list lets Users
// change it is refused, whoever owns it.
func TestFileCheckerRefusesAUserFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "check.ps1")
	if err := os.WriteFile(p, []byte("exit 0"), 0o600); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;BA)(A;;FA;;;BU)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(p, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatalf("set the access list: %v", err)
	}
	if _, err := (fileChecker{}).Check(p); !errors.Is(err, errRefused) {
		t.Fatalf("Check = %v, want a refusal", err)
	}
}

func TestFileCheckerNotAFile(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{dir, filepath.Join(dir, "missing"), filepath.Join(dir, "missing", "below")} {
		if _, err := (fileChecker{}).Check(p); !errors.Is(err, errNotFile) || errors.Is(err, errRefused) {
			t.Errorf("Check(%s) = %v, want errNotFile", p, err)
		}
	}
}
