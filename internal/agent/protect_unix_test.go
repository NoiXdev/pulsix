//go:build !windows

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnixProtectorFileModes(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name string
		dir  bool
		mode os.FileMode
		want string
	}{
		{"owner only file", false, 0o600, ""},
		{"group readable file", false, 0o640, "mode 0640"},
		{"world writable file", false, 0o602, "mode 0602"},
		{"open directory", true, 0o755, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(root, strings.ReplaceAll(tt.name, " ", "-"))
			if tt.dir {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tt.mode); err != nil {
				t.Fatal(err)
			}
			got, err := newProtector().Loose(path)
			if err != nil {
				t.Fatalf("Loose = %v", err)
			}
			if got != tt.want {
				t.Errorf("Loose(%s) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestUnixProtectorProtect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.env")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	p := newProtector()
	if err := p.Protect(path); err != nil {
		t.Fatalf("Protect = %v", err)
	}
	if got, err := p.Loose(path); err != nil || got != "" {
		t.Errorf("Loose after Protect = %q, %v; want empty", got, err)
	}
}

func TestLoadEnvFile(t *testing.T) {
	root := t.TempDir()
	tight := filepath.Join(root, "tight.env")
	if err := os.WriteFile(tight, []byte("PULSIX_MANAGER_ADDR=m:8444\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadEnvFile(tight)
	if err != nil {
		t.Fatalf("LoadEnvFile(0600) = %v", err)
	}
	if got["PULSIX_MANAGER_ADDR"] != "m:8444" {
		t.Errorf("LoadEnvFile = %v", got)
	}

	loose := filepath.Join(root, "loose.env")
	if err := os.WriteFile(loose, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEnvFile(loose); err == nil || !strings.Contains(err.Error(), loose) || !strings.Contains(err.Error(), "mode 0644") {
		t.Errorf("LoadEnvFile(0644) = %v, want an error naming the path and the mode", err)
	}
}

// TestUnixProtectorForeignOwner: a directory's owner is never a finding on
// Unix, and a file the agent's own user created has an allowed owner.
func TestUnixProtectorForeignOwner(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "agent.env")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, file} {
		if got, err := newProtector().ForeignOwner(path); err != nil || got != "" {
			t.Errorf("ForeignOwner(%s) = %q, %v; want empty", path, got, err)
		}
	}
	if _, err := newProtector().ForeignOwner(filepath.Join(dir, "nope")); err == nil {
		t.Error("ForeignOwner(missing) = nil error, want one")
	}
}

// TestUnixEnsureDataDirIgnoresEntries: Unix gains no refusal for what sits
// in the data directory, however loose a file in it is.
func TestUnixEnsureDataDirIgnoresEntries(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "agent.key")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureDataDir(newProtector(), dir, false, "", quietLogger()); err != nil {
		t.Fatalf("ensureDataDir = %v, want nil", err)
	}
}

// TestUnixEnsureDataDirFollowsASymlinkedDir: a data directory that is a
// symlink to a directory elsewhere (another disk) passes as before, for the
// default and a custom directory alike, and its target's mode is untouched.
func TestUnixEnsureDataDirFollowsASymlinkedDir(t *testing.T) {
	for _, isDefault := range []bool{true, false} {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "pulsix")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := ensureDataDir(newProtector(), link, isDefault, "", quietLogger()); err != nil {
			t.Errorf("ensureDataDir(symlink, isDefault=%v) = %v, want nil", isDefault, err)
		}
		fi, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o755 {
			t.Errorf("target mode after ensureDataDir(isDefault=%v) = %04o, want 0755", isDefault, got)
		}
	}
}

// TestUnixProtectorSymlinkedDir: a symlink to a directory is a directory
// for Loose, ForeignOwner and Protect (0700 on the target), while a
// symlinked file keeps the link's own (Lstat) view.
func TestUnixProtectorSymlinkedDir(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	p := newProtector()
	if got, err := p.Loose(link); err != nil || got != "" {
		t.Errorf("Loose(dir link) = %q, %v; want empty", got, err)
	}
	if got, err := p.ForeignOwner(link); err != nil || got != "" {
		t.Errorf("ForeignOwner(dir link) = %q, %v; want empty", got, err)
	}
	if err := p.Protect(link); err != nil {
		t.Fatalf("Protect(dir link) = %v", err)
	}
	if fi, err := os.Stat(target); err != nil {
		t.Fatal(err)
	} else if got := fi.Mode().Perm(); got != 0o700 {
		t.Errorf("target after Protect(dir link) = %04o, want 0700", got)
	}

	file := filepath.Join(root, "agent.env")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fileLink := filepath.Join(root, "agent.env.link")
	if err := os.Symlink(file, fileLink); err != nil {
		t.Fatal(err)
	}
	// The link's own mode (0755 on macOS, 0777 on Linux) still counts.
	if got, err := p.Loose(fileLink); err != nil || got == "" {
		t.Errorf("Loose(file link) = %q, %v; want the link's mode", got, err)
	}
}
