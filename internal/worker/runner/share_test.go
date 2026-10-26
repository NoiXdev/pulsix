package runner

import (
	"os"
	"path/filepath"
	"testing"
)

// TestShareWorkDirAddsGroupAccessMirroringOwner pins the mirroring rule:
// ShareWorkDir copies each entry's owner triplet into its group triplet
// (plus setgid on directories) and never touches world bits, regardless of
// what the owner bits happened to be.
func TestShareWorkDirAddsGroupAccessMirroringOwner(t *testing.T) {
	dir := t.TempDir()
	// t.TempDir() hands back 0755 (0777 under the harness's umask), not the
	// 0700 the loop's real work dir starts as — pin it to that shape so the
	// assertions below exercise the production starting point, not a wider
	// accident of the test harness.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}

	file := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	exe := filepath.Join(sub, "run.sh")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write exe: %v", err)
	}

	if err := ShareWorkDir(dir); err != nil {
		t.Fatalf("ShareWorkDir: %v", err)
	}

	cases := []struct {
		path     string
		wantMode os.FileMode
	}{
		{dir, 0o770 | os.ModeSetgid},
		{file, 0o660},
		{sub, 0o770 | os.ModeSetgid},
		{exe, 0o770},
	}
	for _, c := range cases {
		info, err := os.Stat(c.path)
		if err != nil {
			t.Fatalf("stat %s: %v", c.path, err)
		}
		// Mask down to permission bits + setgid: info.Mode() also carries
		// os.ModeDir for directories, which wantMode (built from octal
		// literals) never sets, and that bit is not under test here.
		if got := info.Mode() & (os.ModePerm | os.ModeSetgid); got != c.wantMode {
			t.Errorf("%s: mode = %v, want %v", c.path, got, c.wantMode)
		}
	}
}

// TestShareWorkDirSkipsSymlinks: os.Chmod follows a symlink to its target —
// there is no way to chmod the link itself — so ShareWorkDir must never call
// it on a symlink entry. A role tree an operator's git checkout or a
// tenant-supplied playbook drops into the work dir can contain symlinks as a
// matter of course; if ShareWorkDir computed a mode from the link's own
// (lstat'd, typically 0777) permissions and chmoded through it, it would set
// the TARGET's mode to world-writable wherever that target lives, including
// outside the work dir entirely — the opposite of "nothing gains world bits
// anywhere". A dangling symlink is the same bug with a different symptom:
// os.Chmod on a target that does not exist returns ENOENT and would abort
// the whole walk, failing the run over an entry that was never the worker's
// to touch in the first place.
func TestShareWorkDirSkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}

	// A real file inside the work dir, to confirm sharing still happens for
	// everything that isn't a symlink.
	real := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(real, []byte("x"), 0o600); err != nil {
		t.Fatalf("write real: %v", err)
	}

	// A target OUTSIDE the work dir entirely — the case that matters: a
	// symlink inside dir must never cause a chmod call that reaches out
	// past dir's own boundary.
	outsideDir := t.TempDir()
	outsideTarget := filepath.Join(outsideDir, "host-file")
	if err := os.WriteFile(outsideTarget, []byte("x"), 0o600); err != nil {
		t.Fatalf("write outside target: %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(outsideTarget, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// A dangling symlink: its target never existed, so a chmod attempted
	// through it fails with ENOENT.
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "does-not-exist"), dangling); err != nil {
		t.Fatalf("dangling symlink: %v", err)
	}

	if err := ShareWorkDir(dir); err != nil {
		t.Fatalf("ShareWorkDir: %v", err)
	}

	outsideInfo, err := os.Stat(outsideTarget)
	if err != nil {
		t.Fatalf("stat outside target: %v", err)
	}
	if got := outsideInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("outside target mode = %v, want unchanged 0600 (ShareWorkDir must never follow a symlink out of dir)", got)
	}

	realInfo, err := os.Stat(real)
	if err != nil {
		t.Fatalf("stat real: %v", err)
	}
	if got := realInfo.Mode().Perm(); got != 0o660 {
		t.Errorf("real file mode = %v, want 0660 (sharing must still happen for non-symlink entries)", got)
	}
}
