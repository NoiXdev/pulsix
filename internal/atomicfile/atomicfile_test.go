package atomicfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/noixdev/pulsix/internal/atomicfile"
)

func TestWrite(t *testing.T) {
	dir := t.TempDir()
	if err := atomicfile.Write(dir, "key.pem", []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "key.pem")
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "first" {
		t.Fatalf("content = %q, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// The mode is set on the temp file before the rename, so the target
	// never exists with the umask's own permissions even briefly —
	// these files hold private keys.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o, want 600", perm)
	}

	// A second write replaces the content and leaves nothing behind.
	if err := atomicfile.Write(dir, "key.pem", []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "second" {
		t.Fatalf("content after the replace = %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "key.pem" {
		t.Fatalf("directory holds %d entries, want only the target: %v", len(entries), entries)
	}
}

// TestWriteDoesNotCreateTheDirectory: dir must exist. Callers that want
// it created say so themselves, because what mode to create it with is
// theirs to decide — an identity directory is 0700, a mounted bundle
// directory is whatever the operator mounted.
func TestWriteDoesNotCreateTheDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	if err := atomicfile.Write(missing, "f", []byte("x"), 0o600); err == nil {
		t.Fatal("writing into a directory that does not exist must fail")
	}
}

// TestWriteLeavesNoTempFileBehindOnFailure: a write that cannot finish
// must not litter the directory with half-written temporaries — the
// next reader of an identity directory would have to tell them apart
// from the real files.
func TestWriteLeavesNoTempFileBehindOnFailure(t *testing.T) {
	dir := t.TempDir()
	// A name with a path separator makes the final rename fail while
	// the temp file has already been created and written.
	if err := atomicfile.Write(dir, "sub/f", []byte("x"), 0o600); err == nil {
		t.Fatal("a rename into a missing subdirectory must fail")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the failed write left %v behind", entries)
	}
}

// TestWriteFailsCleanlyWhenTheRenameFails: the temp file is written and
// flushed, then the rename onto the target fails — here because a
// non-empty directory sits where the file should go. The error comes back
// and the temp file is gone.
func TestWriteFailsCleanlyWhenTheRenameFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "target", "occupied"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.Write(dir, "target", []byte("x"), 0o600); err == nil {
		t.Fatal("renaming over a non-empty directory must fail")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "target" {
		t.Fatalf("the failed write left %v behind", entries)
	}
}

// TestWriteSucceedsWhenTheDirectoryCannotBeReadBack: a directory the
// writer may enter and write but not list cannot be opened to flush it;
// the content is in place by then, so that is not a failure.
func TestWriteSucceedsWhenTheDirectoryCannotBeReadBack(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if f, err := os.Open(dir); err == nil {
		_ = f.Close()
		t.Skip("this process can read a 0300 directory (running as root?)")
	}
	if err := atomicfile.Write(dir, "f", []byte("content"), 0o600); err != nil {
		t.Fatalf("Write = %v, want success: the content is renamed into place", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "f"))
	if err != nil || string(got) != "content" {
		t.Fatalf("read back %q, %v", got, err)
	}
}
