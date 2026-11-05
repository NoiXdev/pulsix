// The read root of the variable sources: symlinks are resolved inside the
// root, git's own files and special files are refused.
package worker

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func symlinks(t *testing.T, root string, links map[string]string) {
	t.Helper()
	for name, target := range links {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCloneFSResolvesLinksInsideTheRoot(t *testing.T) {
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"secret.yml": "x"})
	clone := t.TempDir()
	writeTree(t, clone, map[string]string{
		".git/config":     "[core]",
		"ansible/a.yml":   "a",
		"ansible/d/b.yml": "b",
		"shared.yml":      "s",
	})
	symlinks(t, clone, map[string]string{
		"ansible/in.yml":      "d/b.yml",
		"ansible/dirlink/x":   "../d",
		"ansible/up.yml":      "../shared.yml",
		"ansible/abs.yml":     filepath.Join(outside, "secret.yml"),
		"ansible/git.yml":     "../.git/config",
		"ansible/loop.yml":    "loop.yml",
		"ansible/chain.yml":   "in.yml",
		"gitlink":             ".git",
		"ansible/.GIT/config": "../a.yml", // a case variant of git's directory is refused by name
	})
	c, err := openCloneFS(clone, "ansible")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	for name, want := range map[string]string{"a.yml": "a", "in.yml": "b", "chain.yml": "b", "dirlink/x/b.yml": "b"} {
		if got, err := fs.ReadFile(c, name); err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	for _, name := range []string{"up.yml", "abs.yml", "git.yml", "loop.yml", ".GIT/config", "../shared.yml"} {
		if _, err := fs.ReadFile(c, name); err == nil || errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
		if _, err := fs.Stat(c, name); err == nil {
			t.Errorf("stat %s: no refusal", name)
		}
	}
	if _, err := fs.ReadFile(c, "missing.yml"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing.yml: err = %v, want not exist", err)
	}

	// The read root itself must not be git's directory.
	for _, sub := range []string{".git", "gitlink", "/ansible", "../x"} {
		if c, err := openCloneFS(clone, sub); err == nil {
			_ = c.Close()
			t.Errorf("subpath %q opened", sub)
		}
	}
	// Without a subpath the clone root is the read root, .git refused.
	root, err := openCloneFS(clone, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if _, err := fs.ReadFile(root, ".git/config"); err == nil {
		t.Error(".git/config read")
	}
	if _, err := fs.ReadFile(root, "ansible/a.yml"); err != nil {
		t.Errorf("ansible/a.yml: %v", err)
	}
	if c, err := openCloneFS(clone, "ansible/"); err != nil {
		t.Errorf("subpath with a trailing slash: %v", err)
	} else {
		_ = c.Close()
	}
}
