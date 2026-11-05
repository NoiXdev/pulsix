//go:build unix

package varsource

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestSpecialFilesAreNotOpened: a FIFO (here reached through a symlink
// inside the repository) would block an open forever; it is refused as not
// a regular file before any open.
func TestSpecialFilesAreNotOpened(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"site.yml": "- hosts: all\n  roles:\n    - x\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "roles", "x", "meta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	for link, target := range map[string]string{
		"roles/x/meta/argument_specs.yml": "../../../pipe",
		"fifo.yml":                        "pipe",
	} {
		if err := os.Symlink(target, filepath.Join(dir, filepath.FromSlash(link))); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan Report, 1)
	go func() { done <- ParseRepo(context.Background(), os.DirFS(dir), "c") }()
	var r Report
	select {
	case r = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the read blocked on a FIFO")
	}
	if got := playbookPaths(r); len(got) != 1 || got[0] != "site.yml" {
		t.Fatalf("playbooks = %q", got)
	}
	if roles := r.Playbooks[0].Roles; len(roles) != 1 || roles[0].State != RoleUnreadable {
		t.Fatalf("roles = %+v", roles)
	}
	wantNotes(t, r.Playbooks[0].Notes, "warning read_failed roles/x/meta/argument_specs.yml:0")
}
