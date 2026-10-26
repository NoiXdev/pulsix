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

// TestShareRepoReadOpensTheCheckoutReadOnly pins the repo cache's shape
// once a play uid is set: a repo-backed playbook is never copied into the
// work dir, so the play has to read it where it was cloned — under the
// worker's own 0700 cache, which it cannot even traverse. Group gains read
// and execute down the whole path; group NEVER gains write, because one
// job that could write into the cache could poison the next job's
// checkout. World bits stay untouched.
func TestShareRepoReadOpensTheCheckoutReadOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("chmod root: %v", err)
	}
	clone := filepath.Join(root, "org", "repo")
	if err := os.MkdirAll(filepath.Join(clone, "playbooks"), 0o700); err != nil {
		t.Fatalf("mkdir clone: %v", err)
	}
	playbook := filepath.Join(clone, "playbooks", "site.yml")
	if err := os.WriteFile(playbook, []byte("- hosts: all\n"), 0o600); err != nil {
		t.Fatalf("write playbook: %v", err)
	}
	// A clone written under a 002 umask: group-writable on arrival, and it
	// must not stay that way.
	groupWritable := filepath.Join(clone, "roles.yml")
	if err := os.WriteFile(groupWritable, []byte("x"), 0o664); err != nil {
		t.Fatalf("write group-writable file: %v", err)
	}
	// An executable the checkout carries (a hook, a script a role runs):
	// group follows the owner's execute bit, still without write.
	script := filepath.Join(clone, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}

	if err := ShareRepoRead(root, clone); err != nil {
		t.Fatalf("ShareRepoRead: %v", err)
	}

	cases := []struct {
		path     string
		wantMode os.FileMode
	}{
		{root, 0o750},
		{filepath.Join(root, "org"), 0o750},
		{clone, 0o750},
		{filepath.Join(clone, "playbooks"), 0o750},
		{playbook, 0o640},
		{groupWritable, 0o644},
		{script, 0o750},
	}
	for _, c := range cases {
		info, err := os.Stat(c.path)
		if err != nil {
			t.Fatalf("stat %s: %v", c.path, err)
		}
		got := info.Mode().Perm()
		if got != c.wantMode {
			t.Errorf("%s: mode = %v, want %v", c.path, got, c.wantMode)
		}
		if got&0o020 != 0 {
			t.Errorf("%s: mode = %v is group-writable; the cache is read-only to the play", c.path, got)
		}
		if info.Mode()&os.ModeSetgid != 0 {
			t.Errorf("%s: setgid is for the work dir, where the play creates files; nothing is created here", c.path)
		}
	}
}

// TestShareRepoReadSkipsSymlinks: os.Chmod follows a symlink, and a
// checked-out role tree routinely carries them — including ones pointing
// outside the cache entirely. Same rule as ShareWorkDir.
func TestShareRepoReadSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	clone := filepath.Join(root, "org", "repo")
	if err := os.MkdirAll(clone, 0o700); err != nil {
		t.Fatalf("mkdir clone: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "host-file")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatalf("write outside target: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(clone, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(clone, "gone"), filepath.Join(clone, "dangling")); err != nil {
		t.Fatalf("dangling symlink: %v", err)
	}

	if err := ShareRepoRead(root, clone); err != nil {
		t.Fatalf("ShareRepoRead: %v", err)
	}

	info, err := os.Stat(outside)
	if err != nil {
		t.Fatalf("stat outside target: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("outside target mode = %v, want unchanged 0600", got)
	}
}

// TestShareRepoReadRefusesAPathOutsideTheCache: the walk is only ever
// allowed inside the cache root it was given.
func TestShareRepoReadRefusesAPathOutsideTheCache(t *testing.T) {
	root := t.TempDir()
	if err := ShareRepoRead(root, t.TempDir()); err == nil {
		t.Fatal("ShareRepoRead must refuse a directory outside the repo cache")
	}
}
