package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeProtector reports a loose string per path and records Protect
// calls. Protect clears the path's entry unless sticky is set, which
// models an access list the system refused to change.
// entries switches the entry pass on, as the Windows protector does;
// LooseEntry answers from the same loose map as Loose.
type fakeProtector struct {
	loose     map[string]string
	owner     map[string]string
	sticky    bool
	entries   bool
	protected []string
}

func (f *fakeProtector) Loose(path string) (string, error) { return f.loose[path], nil }

func (f *fakeProtector) LooseEntry(path string) (string, error) { return f.loose[path], nil }

func (f *fakeProtector) ForeignOwner(path string) (string, error) { return f.owner[path], nil }

func (f *fakeProtector) checksEntries() bool { return f.entries }

func (f *fakeProtector) Protect(path string) error {
	f.protected = append(f.protected, path)
	if !f.sticky {
		delete(f.loose, path)
	}
	return nil
}

func TestEnsureDataDirTightensTheDefault(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Pulsix")
	p := &fakeProtector{loose: map[string]string{dir: `BUILTIN\Users`}}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	if err := ensureDataDir(p, dir, true, log); err != nil {
		t.Fatalf("ensureDataDir = %v, want nil", err)
	}
	if len(p.protected) != 1 || p.protected[0] != dir {
		t.Errorf("Protect calls = %v, want exactly [%s]", p.protected, dir)
	}
	if !strings.Contains(logs.String(), "tightened the data directory's access") {
		t.Errorf("log = %q, want the tightening line", logs.String())
	}
	if !strings.Contains(logs.String(), `found=BUILTIN\Users`) {
		t.Errorf("log = %q, want it to name what was found", logs.String())
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("the data dir was not created: %v", err)
	}
}

func TestEnsureDataDirRefusesALooseCustomDir(t *testing.T) {
	dir := t.TempDir() // the operator's directory, already there
	p := &fakeProtector{loose: map[string]string{dir: `BUILTIN\Users`}}

	err := ensureDataDir(p, dir, false, quietLogger())
	if !errors.Is(err, ErrSetupFailed) {
		t.Fatalf("ensureDataDir = %v, want ErrSetupFailed", err)
	}
	for _, want := range []string{dir, `BUILTIN\Users`, "PULSIX_DATA_DIR"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if len(p.protected) != 0 {
		t.Errorf("Protect calls = %v, want none on an operator's directory", p.protected)
	}
}

// TestEnsureDataDirProtectsACustomDirItCreated: a PULSIX_DATA_DIR that did
// not exist is the agent's own creation, so it gets the protected list like
// the default (the spec protects the directory "whenever it creates" it).
func TestEnsureDataDirProtectsACustomDirItCreated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "custom")
	p := &fakeProtector{loose: map[string]string{dir: `BUILTIN\Users`}}

	if err := ensureDataDir(p, dir, false, quietLogger()); err != nil {
		t.Fatalf("ensureDataDir = %v, want nil", err)
	}
	if len(p.protected) != 1 || p.protected[0] != dir {
		t.Errorf("Protect calls = %v, want exactly [%s]", p.protected, dir)
	}
}

// TestEnsureDataDirTightensAnExistingDefault: the default directory is
// already there with an allowed owner (the install script made it) but a
// loose list, and the agent replaces the list.
func TestEnsureDataDirTightensAnExistingDefault(t *testing.T) {
	dir := t.TempDir()
	p := &fakeProtector{loose: map[string]string{dir: `BUILTIN\Users`}}

	if err := ensureDataDir(p, dir, true, quietLogger()); err != nil {
		t.Fatalf("ensureDataDir = %v, want nil", err)
	}
	if len(p.protected) != 1 || p.protected[0] != dir {
		t.Errorf("Protect calls = %v, want exactly [%s]", p.protected, dir)
	}
}

// TestEnsureDataDirRefusesAForeignOwner: whoever owns the directory can
// reopen it to change its list at any time and keeps what it planted
// inside, so no takeover is attempted — not for the default, not for an
// operator's directory, and not for one that appeared under a race after
// the agent found the path free.
func TestEnsureDataDirRefusesAForeignOwner(t *testing.T) {
	tests := []struct {
		name      string
		isDefault bool
		existing  bool
	}{
		{"existing default", true, true},
		{"existing custom", false, true},
		{"raced while created", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "Pulsix")
			if tt.existing {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			p := &fakeProtector{
				loose: map[string]string{dir: `owner PC\mallory, BUILTIN\Users`},
				owner: map[string]string{dir: `owner PC\mallory`},
			}
			err := ensureDataDir(p, dir, tt.isDefault, quietLogger())
			if !errors.Is(err, ErrSetupFailed) {
				t.Fatalf("ensureDataDir = %v, want ErrSetupFailed", err)
			}
			for _, want := range []string{dir, `PC\mallory`, "remove it"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			if len(p.protected) != 0 {
				t.Errorf("Protect calls = %v, want none on a foreign-owned directory", p.protected)
			}
		})
	}
}

func TestEnsureDataDirFailsWhenProtectDidNotStick(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Pulsix")
	p := &fakeProtector{loose: map[string]string{dir: `BUILTIN\Users`}, sticky: true}

	err := ensureDataDir(p, dir, true, quietLogger())
	if !errors.Is(err, ErrSetupFailed) {
		t.Fatalf("ensureDataDir = %v, want ErrSetupFailed", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error %q does not name the directory", err)
	}
}

func TestEnsureDataDirLeavesATightDirAlone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Pulsix")
	p := &fakeProtector{loose: map[string]string{}}

	if err := ensureDataDir(p, dir, true, quietLogger()); err != nil {
		t.Fatalf("ensureDataDir = %v, want nil", err)
	}
	if len(p.protected) != 0 {
		t.Errorf("Protect calls = %v, want none", p.protected)
	}
}

func TestEnsureDataDirReportsAnUncreatableDir(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := ensureDataDir(&fakeProtector{}, notADir, true, quietLogger())
	if !errors.Is(err, ErrSetupFailed) || !strings.Contains(err.Error(), "create data dir") {
		t.Fatalf("ensureDataDir on a file path = %v, want ErrSetupFailed naming the create", err)
	}
}

func TestCheckEnvFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "agent.env")
	if err := os.WriteFile(file, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.env")
	symlinkErr := os.Symlink(file, link) // needs a privilege on Windows

	tests := []struct {
		name    string
		path    string
		loose   string
		wantErr string
	}{
		{"directory", root, "", "not a regular file"},
		{"symlink", link, "", "symlink"},
		{"missing", filepath.Join(root, "nope.env"), "", "nope.env"},
		{"loose", file, "mode 0644", "mode 0644"},
		{"tight", file, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.path == link && symlinkErr != nil {
				t.Skipf("symlinks unavailable: %v", symlinkErr)
			}
			p := &fakeProtector{loose: map[string]string{tt.path: tt.loose}}
			err := CheckEnvFile(p, tt.path)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("CheckEnvFile = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), tt.path) {
				t.Fatalf("CheckEnvFile = %v, want an error naming %s and %q", err, tt.path, tt.wantErr)
			}
		})
	}
}

// TestCheckConfigFile: agent.yaml gets exactly the env file's rule, in
// words that name what the file holds and the flag that points at it.
func TestCheckConfigFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "agent.yaml")
	if err := os.WriteFile(file, []byte("secrets: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.yaml")
	symlinkErr := os.Symlink(file, link) // needs a privilege on Windows

	tests := []struct {
		name    string
		path    string
		loose   string
		wantErr string
	}{
		{"directory", root, "", "configuration file " + root + " is not a regular file"},
		{"symlink", link, "", "configuration file " + link + " is a symlink; point --config at the file itself"},
		{"loose", file, "mode 0644", "configuration file " + file + " holds secrets but is accessible to mode 0644; restrict it"},
		{"foreign owner", file, `owner PC\alice`, `configuration file ` + file + ` holds secrets but is accessible to owner PC\alice; restrict it`},
		{"tight", file, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.path == link && symlinkErr != nil {
				t.Skipf("symlinks unavailable: %v", symlinkErr)
			}
			p := &fakeProtector{loose: map[string]string{tt.path: tt.loose}}
			err := checkConfigFile(p, tt.path)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("checkConfigFile = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("checkConfigFile = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// TestCheckConfigFileUsesThePlatformRule: the exported check is the real
// protector's rule (a 0600 file of the test user passes on Unix).
func TestCheckConfigFileUsesThePlatformRule(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Windows rule needs an Administrators-owned file; protect_windows_test covers the protector")
	}
	file := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(file, []byte("secrets: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckConfigFile(file); err != nil {
		t.Fatalf("CheckConfigFile(0600) = %v", err)
	}
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckConfigFile(file); err == nil || !strings.Contains(err.Error(), "mode 0644") {
		t.Fatalf("CheckConfigFile(0644) = %v, want a refusal naming the mode", err)
	}
}

// TestEnsureDataDirChecksEntries: on Windows a data directory that passes
// can still hold files someone planted before it was protected (a fresh
// directory under ProgramData lets every user add files until then), and
// a planted agent.key or agent.ca.pem would be trusted. Every entry
// directly inside has to pass too; a protector without the entry pass
// (Unix) ignores them.
func TestEnsureDataDirChecksEntries(t *testing.T) {
	tests := []struct {
		name    string
		checks  bool
		owner   string
		loose   string
		wantErr []string
	}{
		{"foreign owner", true, `owner PC\mallory`, `owner PC\mallory`, []string{"agent.key", `PC\mallory`, "remove it"}},
		{"loose list", true, "", `BUILTIN\Users`, []string{"agent.key", `BUILTIN\Users`, "remove it"}},
		{"clean", true, "", "", nil},
		{"no entry pass", false, `owner PC\mallory`, `BUILTIN\Users`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			planted := filepath.Join(dir, "agent.key")
			if err := os.WriteFile(planted, []byte("k"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(dir, "logs"), 0o700); err != nil {
				t.Fatal(err)
			}
			p := &fakeProtector{
				entries: tt.checks,
				loose:   map[string]string{planted: tt.loose},
				owner:   map[string]string{planted: tt.owner},
			}
			err := ensureDataDir(p, dir, true, quietLogger())
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("ensureDataDir = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrSetupFailed) {
				t.Fatalf("ensureDataDir = %v, want ErrSetupFailed", err)
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

// TestEnsureDataDirChecksEntriesAfterTightening: the entry pass also runs
// on a directory this call just tightened — that is exactly the window
// in which a file could have been planted.
func TestEnsureDataDirChecksEntriesAfterTightening(t *testing.T) {
	dir := t.TempDir()
	planted := filepath.Join(dir, "agent.ca.pem")
	if err := os.WriteFile(planted, []byte("ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &fakeProtector{entries: true, loose: map[string]string{dir: `BUILTIN\Users`, planted: `owner PC\mallory`}}
	err := ensureDataDir(p, dir, true, quietLogger())
	if !errors.Is(err, ErrSetupFailed) || !strings.Contains(err.Error(), "agent.ca.pem") {
		t.Fatalf("ensureDataDir = %v, want ErrSetupFailed naming the planted file", err)
	}
	if len(p.protected) != 1 {
		t.Errorf("Protect calls = %v, want the directory tightened first", p.protected)
	}
}

// TestRunChecksTheDataDirBeforeLoadingTheIdentity: a complete identity
// already in the directory must not be loaded (and dialled with) when the
// directory itself fails the check — a planted agent.ca.pem would make the
// agent trust a stranger's manager.
func TestRunChecksTheDataDirBeforeLoadingTheIdentity(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	p := &fakeProtector{owner: map[string]string{lt.dir: `owner PC\mallory`}}
	l := NewLoop(lt.cfg, lt.dialer, lt.facts, append(lt.options(), withProtector(p))...)

	err := l.Run(context.Background())
	if !errors.Is(err, ErrSetupFailed) || !strings.Contains(err.Error(), `PC\mallory`) {
		t.Fatalf("Run = %v, want ErrSetupFailed naming the owner", err)
	}
	if lt.dialer.agentDials != 0 || lt.dialer.enrollDials != 0 {
		t.Errorf("dials = agent %d, enroll %d; want none before the data dir passes", lt.dialer.agentDials, lt.dialer.enrollDials)
	}
}
