package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/worker/requirements"
)

// modeOf is path's permission bits plus setgid, not following a link.
func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode() & (os.ModePerm | os.ModeSetgid)
}

// shareLayout is galaxyInstall's layout plus what prepareInstall must not
// touch: the cache's lock, another entry, and a file already in roles/
// (on a second command it belongs to the play uid).
func shareLayout(t *testing.T) (in requirements.Install, untouched map[string]os.FileMode) {
	t.Helper()
	in = galaxyInstall(t, requirements.KindRole)
	cacheDir := filepath.Dir(in.Entry)
	other := filepath.Join(cacheDir, strings.Repeat("a", 40)+"-"+strings.Repeat("b", 16))
	mkdirMode(t, filepath.Join(other, "roles", "x"), 0o700)
	mkdirMode(t, filepath.Join(other, "roles"), 0o700)
	mkdirMode(t, other, 0o700)
	writeMode(t, filepath.Join(cacheDir, ".lock"), "", 0o600)
	mkdirMode(t, filepath.Join(in.Entry, "home", ".ssh"), 0o700)
	writeMode(t, filepath.Join(in.Entry, "roles", "installed"), "x", 0o600)
	return in, map[string]os.FileMode{
		filepath.Join(cacheDir, ".lock"):              0o600,
		other:                                         0o700,
		filepath.Join(other, "roles"):                 0o700,
		filepath.Join(other, "roles", "x"):            0o700,
		filepath.Join(in.Entry, "roles", "installed"): 0o600,
	}
}

func TestPrepareInstallOpensOnlyItsTargetsToThePlayGroup(t *testing.T) {
	in, untouched := shareLayout(t)
	if err := prepareInstall(in); err != nil {
		t.Fatalf("prepareInstall: %v", err)
	}
	if err := prepareInstall(in); err != nil {
		t.Fatalf("prepareInstall a second time (the collection install of the same file): %v", err)
	}

	cacheDir := filepath.Dir(in.Entry)
	input := filepath.Join(in.Entry, "input")
	for _, dir := range []string{in.RepoCacheDir, filepath.Dir(in.CloneDir), cacheDir, in.Entry, input} {
		if got := modeOf(t, dir); got&0o050 != 0o050 || got&0o022 != 0 {
			t.Errorf("%s: mode %v, want group read and traverse, nothing group- or world-writable", dir, got)
		}
	}
	for _, f := range []string{"1.yml", "galaxy.cfg"} {
		if got := modeOf(t, filepath.Join(input, f)); got != 0o640 {
			t.Errorf("input/%s: mode %v, want 0640", f, got)
		}
	}
	for _, d := range []string{"home", "home/.ssh", "tmp", "roles", "collections"} {
		if got := modeOf(t, filepath.Join(in.Entry, d)); got != 0o770|os.ModeSetgid {
			t.Errorf("%s: mode %v, want 2770", d, got)
		}
	}
	for path, want := range untouched {
		if got := modeOf(t, path); got != want {
			t.Errorf("%s: mode %v, want it untouched at %v", path, got, want)
		}
	}
	if err := filepath.WalkDir(in.CloneDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if got := modeOf(t, path); got&0o040 == 0 || got&0o020 != 0 {
			t.Errorf("clone %s: mode %v, want group-readable, not group-writable", path, got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareInstallRefusesASymlinkedParent(t *testing.T) {
	in, _ := shareLayout(t)
	cacheDir := filepath.Dir(in.Entry)
	moved := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Rename(cacheDir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, cacheDir); err != nil {
		t.Fatal(err)
	}
	err := prepareInstall(in)
	if err == nil || !strings.Contains(err.Error(), "symlink on the path") {
		t.Fatalf("prepareInstall = %v, want a refusal naming the symlink on the path", err)
	}
	for _, path := range []string{moved, filepath.Join(moved, ".tmp-x"), filepath.Join(moved, ".tmp-x", "input"), filepath.Join(moved, ".tmp-x", "roles")} {
		if got := modeOf(t, path); got != 0o700 {
			t.Errorf("%s behind the link: mode %v, want it untouched at 0700", path, got)
		}
	}
}

func TestPrepareInstallRefusesASymlinkInItsWritableDirectories(t *testing.T) {
	in, _ := shareLayout(t)
	target := t.TempDir()
	mkdirMode(t, target, 0o700)
	ssh := filepath.Join(in.Entry, "home", ".ssh")
	if err := os.Remove(ssh); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, ssh); err != nil {
		t.Fatal(err)
	}
	if err := prepareInstall(in); err == nil {
		t.Fatal("prepareInstall followed home/.ssh, a symlink the play uid could have planted")
	}
	if got := modeOf(t, target); got != 0o700 {
		t.Errorf("the link's target: mode %v, want it untouched at 0700", got)
	}
}
