package requirements

import (
	"bytes"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// takeOverSource makes a temp entry in a fresh cache directory, as galaxy
// leaves it: input/1.yml and home/x beside roles/ and collections/.
func takeOverSource(t *testing.T) (dir string, root *os.Root, src string) {
	t.Helper()
	dir = t.TempDir()
	root = openTestRoot(t, dir)
	src, err := mkTemp(root)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, src, "input", "1.yml"), "- acme.web\n", 0o640)
	writeTestFile(t, filepath.Join(dir, src, "home", "x"), "home\n", 0o600)
	for _, d := range []string{"roles", "collections"} {
		if err := os.Mkdir(filepath.Join(dir, src, d), 0o770); err != nil {
			t.Fatal(err)
		}
	}
	return dir, root, src
}

func writeTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func symlinkTest(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// treeSnapshot is every path below dir with its mode, and its contents
// (files) or target (symlinks).
func treeSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		v := fi.Mode().String()
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			v += " -> " + target
		case fi.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			v += " " + string(b)
		}
		snap[rel] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// onlyNames fails unless the directory holds exactly want.
func onlyNames(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("cache holds %v, want %v", got, want)
	}
}

func TestTakeOverCopiesALegitimateInstall(t *testing.T) {
	dir, root, src := takeOverSource(t)
	s := filepath.Join(dir, src)
	writeTestFile(t, filepath.Join(s, "roles/acme.web/tasks/main.yml"), "- debug: msg=hi\n", 0o664)
	writeTestFile(t, filepath.Join(s, "roles/acme.web/files/run.sh"), "#!/bin/sh\necho run\n", 0o755)
	writeTestFile(t, filepath.Join(s, "roles/acme.web/files/suid"), "suid\n", 0o755)
	if err := os.Chmod(filepath.Join(s, "roles/acme.web/files/suid"), 0o755|os.ModeSetuid); err != nil {
		t.Fatal(err)
	}
	symlinkTest(t, "../tasks/main.yml", filepath.Join(s, "roles/acme.web/meta/link"))
	writeTestFile(t, filepath.Join(s, "collections/ansible_collections/acme/tools/README.md"), "# tools\n", 0o644)
	before := treeSnapshot(t, s)

	name, err := takeOver(root, src)
	if err != nil {
		t.Fatalf("takeOver: %v", err)
	}
	if name == src || !strings.HasPrefix(name, tempPrefix) {
		t.Fatalf("takeOver = %q, want a fresh temporary name besides %q", name, src)
	}
	d := filepath.Join(dir, name)

	files := map[string]os.FileMode{
		"roles/acme.web/tasks/main.yml":                        0o600,
		"roles/acme.web/files/run.sh":                          0o700,
		"roles/acme.web/files/suid":                            0o700,
		"collections/ansible_collections/acme/tools/README.md": 0o600,
	}
	for rel, mode := range files {
		want, err := os.ReadFile(filepath.Join(s, rel))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(d, rel))
		if err != nil {
			t.Errorf("read the copy of %s: %v", rel, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
		fi, err := os.Lstat(filepath.Join(d, rel))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode() != mode {
			t.Errorf("%s mode = %v, want %v", rel, fi.Mode(), mode)
		}
	}
	target, err := os.Readlink(filepath.Join(d, "roles/acme.web/meta/link"))
	if err != nil || target != "../tasks/main.yml" {
		t.Errorf("meta/link = %q, %v; want a symlink to ../tasks/main.yml", target, err)
	}
	err = filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			fi, err := e.Info()
			if err != nil {
				return err
			}
			if fi.Mode() != fs.ModeDir|0o700 {
				t.Errorf("directory %s mode = %v, want drwx------", p, fi.Mode())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"input", "home"} {
		if _, err := os.Lstat(filepath.Join(d, gone)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s was copied (err = %v)", gone, err)
		}
	}
	after := treeSnapshot(t, s)
	if len(after) != len(before) {
		t.Errorf("src changed: %d paths before, %d after", len(before), len(after))
	}
	for p, v := range before {
		if after[p] != v {
			t.Errorf("src %s changed: %q, now %q", p, v, after[p])
		}
	}
}

func TestTakeOverKeepsLinksThatStayInside(t *testing.T) {
	dir, root, src := takeOverSource(t)
	s := filepath.Join(dir, src)
	writeTestFile(t, filepath.Join(s, "roles/a/tasks/main.yml"), "- ok\n", 0o644)
	links := map[string]string{
		"roles/a/dangling":     "missing/../../x",
		"roles/a/to-input":     "../../input/1.yml",
		"roles/a/to-dir":       "tasks",
		"roles/a/through-dir":  "to-dir/main.yml",
		"roles/a/to-root":      "../..",
		"collections/to-file":  "../roles/a/through-dir",
		"collections/via-home": "../home/abs/passwd",
	}
	// home/ is not copied, so a link through it dangles in the copy.
	symlinkTest(t, "/etc", filepath.Join(s, "home/abs"))
	for link, target := range links {
		symlinkTest(t, target, filepath.Join(s, link))
	}

	name, err := takeOver(root, src)
	if err != nil {
		t.Fatalf("takeOver: %v", err)
	}
	for link, want := range links {
		got, err := os.Readlink(filepath.Join(dir, name, link))
		if err != nil || got != want {
			t.Errorf("%s = %q, %v; want a symlink to %q", link, got, err, want)
		}
	}
}

func TestTakeOverRefuses(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, src string)
		want  string
	}{
		{"an absolute symlink", func(t *testing.T, s string) {
			symlinkTest(t, "/etc/passwd", filepath.Join(s, "roles/abs"))
		}, "take over the install: roles/abs: a symlink leaving the install"},
		{"a symlink up and out", func(t *testing.T, s string) {
			symlinkTest(t, "../../../x", filepath.Join(s, "roles/a/up"))
		}, "take over the install: roles/a/up: a symlink leaving the install"},
		{"a symlink out through another symlink", func(t *testing.T, s string) {
			symlinkTest(t, "../../..", filepath.Join(s, "roles/a/b/s"))
			symlinkTest(t, "s/../../x", filepath.Join(s, "roles/a/b/l"))
		}, "take over the install: roles/a/b/l: a symlink leaving the install"},
		{"a loop of symlinks", func(t *testing.T, s string) {
			symlinkTest(t, "l2", filepath.Join(s, "roles/l1"))
			symlinkTest(t, "l1", filepath.Join(s, "roles/l2"))
		}, "take over the install: roles/l1: a chain of more than 40 symlinks"},
		{"a hard link", func(t *testing.T, s string) {
			if err := os.Link(filepath.Join(s, "input/1.yml"), filepath.Join(s, "roles/hard")); err != nil {
				t.Fatal(err)
			}
		}, "take over the install: roles/hard: a hard link"},
		{"a FIFO", func(t *testing.T, s string) {
			if err := syscall.Mkfifo(filepath.Join(s, "collections/fifo"), 0o644); err != nil {
				t.Skipf("mkfifo: %v", err)
			}
		}, "take over the install: collections/fifo: not a regular file, directory or symlink"},
		{"a socket", func(t *testing.T, s string) {
			// A relative name: the temp dir's path may exceed the socket path limit.
			t.Chdir(filepath.Join(s, "roles"))
			l, err := net.Listen("unix", "sock")
			if err != nil {
				t.Skipf("listen on a unix socket: %v", err)
			}
			t.Cleanup(func() { _ = l.Close() })
		}, "take over the install: roles/sock: not a regular file, directory or symlink"},
		{"a device", func(t *testing.T, s string) {
			if os.Geteuid() != 0 {
				t.Skip("only root makes a device node")
			}
			if err := syscall.Mknod(filepath.Join(s, "roles/null"), syscall.S_IFCHR|0o600, 0x0103); err != nil {
				t.Skipf("mknod: %v", err)
			}
		}, "take over the install: roles/null: not a regular file, directory or symlink"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, root, src := takeOverSource(t)
			tc.setup(t, filepath.Join(dir, src))

			name, err := takeOver(root, src)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("takeOver = %q, %v; want %q", name, err, tc.want)
			}
			if name != "" {
				t.Errorf("takeOver returned the name %q with an error", name)
			}
			onlyNames(t, dir, src)
		})
	}
}

func TestTakeOverNamesAnUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file without permissions")
	}
	dir, root, src := takeOverSource(t)
	writeTestFile(t, filepath.Join(dir, src, "roles/a/ok"), "ok\n", 0o644)
	writeTestFile(t, filepath.Join(dir, src, "roles/secret"), "secret\n", 0o000)

	name, err := takeOver(root, src)
	if err == nil || !strings.HasPrefix(err.Error(), "take over the install: roles/secret: ") || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("takeOver = %q, %v; want the open error naming roles/secret", name, err)
	}
	onlyNames(t, dir, src)
}

func TestTakeOverNamesAnUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory without permissions")
	}
	dir, root, src := takeOverSource(t)
	closed := filepath.Join(dir, src, "collections/ansible_collections/acme/closed")
	writeTestFile(t, filepath.Join(closed, "x"), "x", 0o644)
	if err := os.Chmod(closed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(closed, 0o700) })

	name, err := takeOver(root, src)
	want := "take over the install: collections/ansible_collections/acme/closed: "
	if err == nil || !strings.HasPrefix(err.Error(), want) || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("takeOver = %q, %v; want the open error naming the directory", name, err)
	}
	onlyNames(t, dir, src)
}

func TestTakeOverIsBounded(t *testing.T) {
	cases := []struct {
		name  string
		lim   takeOverLimits
		setup func(t *testing.T, src string)
		want  string
	}{
		{"entries", takeOverLimits{entries: 4, depth: 64, bytes: 1 << 20, lookups: 1000, names: 1 << 20}, func(t *testing.T, s string) {
			for _, n := range []string{"1", "2", "3", "4"} {
				writeTestFile(t, filepath.Join(s, "roles/many", n), n, 0o644)
			}
		}, "take over the install: roles/many: more than 4 files and directories"},
		{"depth", takeOverLimits{entries: 100, depth: 2, bytes: 1 << 20, lookups: 1000, names: 1 << 20}, func(t *testing.T, s string) {
			writeTestFile(t, filepath.Join(s, "roles/a/b/c/f"), "f", 0o644)
		}, "take over the install: roles/a/b/c: nested deeper than 2 directories"},
		{"bytes", takeOverLimits{entries: 100, depth: 64, bytes: 10, lookups: 1000, names: 1 << 20}, func(t *testing.T, s string) {
			writeTestFile(t, filepath.Join(s, "roles/small"), "12345", 0o644)
			writeTestFile(t, filepath.Join(s, "roles/xbig"), "123456", 0o644)
		}, "take over the install: roles/xbig: more than 10 bytes of files"},
		{"lookups", takeOverLimits{entries: 100, depth: 64, bytes: 1 << 20, lookups: 5, names: 1 << 20}, func(t *testing.T, s string) {
			writeTestFile(t, filepath.Join(s, "roles/a/f"), "f", 0o644)
			symlinkTest(t, "a/../a/../a/f", filepath.Join(s, "roles/l"))
		}, "take over the install: roles/l: more than 5 lookups to check its symlinks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, root, src := takeOverSource(t)
			tc.setup(t, filepath.Join(dir, src))

			name, err := takeOverWith(root, src, tc.lim, takeOverHooks{})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("takeOverWith = %q, %v; want %q", name, err, tc.want)
			}
			onlyNames(t, dir, src)
		})
	}
}

func TestTakeOverQuotesAnOddName(t *testing.T) {
	for name, want := range map[string]string{
		"roles/a\nb": `take over the install: "roles/a\nb": a symlink leaving the install`,
		"roles/a: b": `take over the install: "roles/a: b": a symlink leaving the install`,
	} {
		dir, root, src := takeOverSource(t)
		symlinkTest(t, "/etc/passwd", filepath.Join(dir, src, name))

		_, err := takeOver(root, src)
		if err == nil || err.Error() != want {
			t.Errorf("takeOver = %v, want %q", err, want)
		}
	}
}

func TestTakeOverWithoutRolesOrCollections(t *testing.T) {
	dir, root, src := takeOverSource(t)
	if err := os.Remove(filepath.Join(dir, src, "roles")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, src, "collections/ansible_collections/a/b/x"), "x", 0o644)

	name, err := takeOver(root, src)
	if err != nil {
		t.Fatalf("takeOver: %v", err)
	}
	onlyNames(t, filepath.Join(dir, name), "collections")
}
