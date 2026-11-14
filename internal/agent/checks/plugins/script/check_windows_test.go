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
			err = judgeSecurity(sd, "", trusted)
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
	if err := judgeSecurity(sd, "its directory ", trusted); err == nil || !strings.HasPrefix(err.Error(), "its directory is owned by") {
		t.Fatalf("directory refusal %v", err)
	}
}

func TestFileCheckerAcceptsCmdExe(t *testing.T) {
	if err := (fileChecker{}).Check(cmdExe(t)); err != nil {
		t.Fatalf("Check(cmd.exe): %v", err)
	}
}

// TestFileCheckerRefusesAUserFile: a file in the test user's temp
// directory is owned by, or at least writable by, that user.
func TestFileCheckerRefusesAUserFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "check.ps1")
	if err := os.WriteFile(p, []byte("exit 0"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (fileChecker{}).Check(p); !errors.Is(err, errRefused) {
		t.Fatalf("Check = %v, want a refusal", err)
	}
}

func TestFileCheckerNotAFile(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{dir, filepath.Join(dir, "missing"), filepath.Join(dir, "missing", "below")} {
		if err := (fileChecker{}).Check(p); !errors.Is(err, errNotFile) || errors.Is(err, errRefused) {
			t.Errorf("Check(%s) = %v, want errNotFile", p, err)
		}
	}
}
