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
