//go:build windows

package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestWindowsProtector runs against a real temp directory on the Windows
// CI job: a fresh directory inherits a loose list, Protect replaces it and
// makes Administrators the owner, and a file created inside inherits only
// SYSTEM and Administrators.
func TestWindowsProtector(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Pulsix")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := newProtector()

	found, err := p.Loose(dir)
	if err != nil {
		t.Fatalf("Loose(fresh dir) = %v", err)
	}
	if found == "" {
		t.Fatal("Loose(fresh dir) = \"\", want the inherited list reported")
	}
	t.Logf("fresh dir is accessible to: %s", found)

	if err := p.Protect(dir); err != nil {
		t.Fatalf("Protect = %v", err)
	}
	if found, err := p.Loose(dir); err != nil || found != "" {
		t.Fatalf("Loose(protected dir) = %q, %v; want empty", found, err)
	}
	if owner, err := p.ForeignOwner(dir); err != nil || owner != "" {
		t.Fatalf("ForeignOwner(protected dir) = %q, %v; want empty", owner, err)
	}
	assertOwnerIsAdministrators(t, dir)

	// The file's owner is whatever the test process's token assigns (the
	// runner user, or Administrators when the policy says so); the access
	// entries it inherits must name nobody else.
	file := filepath.Join(dir, "agent.key")
	if err := os.WriteFile(file, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	found, err = p.Loose(file)
	if err != nil {
		t.Fatalf("Loose(file in protected dir) = %v", err)
	}
	if found != "" && found != "owner "+trusteeName(currentUserSID(t)) {
		t.Fatalf("Loose(file in protected dir) = %q, want at most its owner reported", found)
	}

	if err := p.Protect(file); err != nil {
		t.Fatalf("Protect(file) = %v", err)
	}
	assertOwnerIsAdministrators(t, file)
	if err := CheckEnvFile(p, file); err != nil {
		t.Fatalf("CheckEnvFile(protected file) = %v", err)
	}
}

// TestWindowsProtectorReportsAnOwner: a file owned by the (non-SYSTEM)
// test user is loose even when only allowed principals are in its list,
// because the owner may rewrite that list.
func TestWindowsProtectorReportsAnOwner(t *testing.T) {
	user := currentUserSID(t)
	system, admins, err := allowedSIDs()
	if err != nil {
		t.Fatal(err)
	}
	if user.Equals(system) || user.Equals(admins) {
		t.Skip("the test runs as an allowed owner")
	}
	file := filepath.Join(t.TempDir(), "agent.env")
	if err := os.WriteFile(file, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(file, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION, user, nil, nil, nil); err != nil {
		t.Fatalf("set the owner to the test user: %v", err)
	}
	found, err := newProtector().Loose(file)
	if err != nil {
		t.Fatal(err)
	}
	if want := "owner " + trusteeName(user); !strings.Contains(found, want) {
		t.Fatalf("Loose = %q, want it to contain %q", found, want)
	}
}

// TestWindowsEnsureDataDirRefusesAUserOwnedDir: a default data directory
// that already exists and belongs to an ordinary user (a squatter under
// ProgramData) is refused, not taken over.
func TestWindowsEnsureDataDirRefusesAUserOwnedDir(t *testing.T) {
	user := currentUserSID(t)
	system, admins, err := allowedSIDs()
	if err != nil {
		t.Fatal(err)
	}
	if user.Equals(system) || user.Equals(admins) {
		t.Skip("the test runs as an allowed owner")
	}
	dir := filepath.Join(t.TempDir(), "Pulsix")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION, user, nil, nil, nil); err != nil {
		t.Fatalf("set the owner to the test user: %v", err)
	}
	err = ensureDataDir(newProtector(), dir, true, quietLogger())
	if !errors.Is(err, ErrSetupFailed) || !strings.Contains(err.Error(), "owner "+trusteeName(user)) {
		t.Fatalf("ensureDataDir(user-owned default dir) = %v, want ErrSetupFailed naming the owner", err)
	}
	if owner, err := newProtector().ForeignOwner(dir); err != nil || owner == "" {
		t.Fatalf("ForeignOwner after the refusal = %q, %v; want the user still reported (nothing changed)", owner, err)
	}
}

func TestWindowsProtectorRefusesALink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "Pulsix")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	p := newProtector()
	found, err := p.Loose(link)
	if err != nil || !strings.Contains(found, "link") {
		t.Fatalf("Loose(link) = %q, %v; want it reported as a link", found, err)
	}
	if owner, err := p.ForeignOwner(link); err != nil || !strings.Contains(owner, "link") {
		t.Fatalf("ForeignOwner(link) = %q, %v; want it reported as a link", owner, err)
	}
	if err := p.Protect(link); err == nil {
		t.Fatal("Protect(link) = nil, want a refusal")
	}
}

func assertOwnerIsAdministrators(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	_, admins, err := allowedSIDs()
	if err != nil {
		t.Fatal(err)
	}
	if !owner.Equals(admins) {
		t.Fatalf("owner of %s = %s, want Administrators", path, trusteeName(owner))
	}
}

func currentUserSID(t *testing.T) *windows.SID {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return u.User.Sid
}
