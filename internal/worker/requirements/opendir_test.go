package requirements

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testCommit = "0123456789abcdef0123456789abcdef01234567"
	testHash   = "89abcdef01234567"
)

// layouts are the two paths the cache opens: the cache directory of a repo and
// an entry inside it.
var layouts = []struct {
	name string
	rel  string
}{
	{"cache", filepath.Join("org", "repo.requirements")},
	{"entry", filepath.Join("org", "repo.requirements", testCommit+"-"+testHash)},
}

func TestOpenDir(t *testing.T) {
	for _, layout := range layouts {
		t.Run(layout.name, func(t *testing.T) {
			cases := []struct {
				name  string
				setup func(t *testing.T, path string) (target string)
				check func(t *testing.T, path string, root *os.Root, err error)
			}{
				{
					name:  "missing",
					setup: func(t *testing.T, path string) string { mkParent(t, path); return "" },
					check: func(t *testing.T, path string, root *os.Root, err error) {
						if !errors.Is(err, fs.ErrNotExist) {
							t.Fatalf("err = %v, want fs.ErrNotExist", err)
						}
						if root != nil {
							t.Fatal("root is not nil")
						}
						if !strings.Contains(err.Error(), path) {
							t.Fatalf("err %q does not contain the path", err)
						}
					},
				},
				{
					name: "directory",
					setup: func(t *testing.T, path string) string {
						mkParent(t, path)
						if err := os.Mkdir(path, 0o700); err != nil {
							t.Fatal(err)
						}
						return ""
					},
					check: func(t *testing.T, path string, root *os.Root, err error) {
						if err != nil {
							t.Fatalf("OpenDir: %v", err)
						}
						t.Cleanup(func() { _ = root.Close() })
						want, statErr := os.Stat(path)
						if statErr != nil {
							t.Fatal(statErr)
						}
						got, statErr := root.Stat(".")
						if statErr != nil {
							t.Fatal(statErr)
						}
						if !os.SameFile(want, got) {
							t.Fatal("root is not the directory at path")
						}
					},
				},
				{
					name: "symlink to a directory",
					setup: func(t *testing.T, path string) string {
						mkParent(t, path)
						target := filepath.Join(filepath.Dir(path), "elsewhere")
						if err := os.Mkdir(target, 0o700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(target, "keep"), []byte("data"), 0o600); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(target, path); err != nil {
							t.Fatal(err)
						}
						return target
					},
					check: wantRefusal(ErrCacheSymlink, "symlink"),
				},
				{
					name: "dangling symlink",
					setup: func(t *testing.T, path string) string {
						mkParent(t, path)
						if err := os.Symlink(filepath.Join(filepath.Dir(path), "nowhere"), path); err != nil {
							t.Fatal(err)
						}
						return ""
					},
					check: wantRefusal(ErrCacheSymlink, "symlink"),
				},
				{
					name: "regular file",
					setup: func(t *testing.T, path string) string {
						mkParent(t, path)
						if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
							t.Fatal(err)
						}
						return ""
					},
					check: func(t *testing.T, path string, root *os.Root, err error) {
						wantRefusal(ErrCacheNotDirectory, "not a directory")(t, path, root, err)
						if strings.Contains(err.Error(), "symlink") {
							t.Fatalf("err %q mentions a symlink", err)
						}
					},
				},
				{
					name: "parent without permission",
					setup: func(t *testing.T, path string) string {
						if os.Geteuid() == 0 {
							t.Skip("root ignores directory permissions")
						}
						mkParent(t, path)
						if err := os.Mkdir(path, 0o700); err != nil {
							t.Fatal(err)
						}
						parent := filepath.Dir(path)
						if err := os.Chmod(parent, 0o000); err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
						return ""
					},
					check: func(t *testing.T, path string, root *os.Root, err error) {
						if err == nil {
							_ = root.Close()
							t.Fatal("OpenDir succeeded")
						}
						if root != nil {
							t.Fatal("root is not nil")
						}
						for _, sentinel := range []error{fs.ErrNotExist, ErrCacheSymlink, ErrCacheNotDirectory, ErrCacheReplaced} {
							if errors.Is(err, sentinel) {
								t.Fatalf("err = %v, is %v", err, sentinel)
							}
						}
						if !errors.Is(err, fs.ErrPermission) {
							t.Fatalf("err = %v, want fs.ErrPermission", err)
						}
						if !strings.Contains(err.Error(), path) {
							t.Fatalf("err %q does not contain the path", err)
						}
					},
				},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), layout.rel)
					target := tc.setup(t, path)
					root, err := OpenDir(path)
					tc.check(t, path, root, err)
					if err != nil && tc.name == "symlink to a directory" {
						assertLinkIntact(t, path, target)
					}
				})
			}
		})
	}
}

// wantRefusal asserts a refusal with the given sentinel whose message names the
// reason text and the path, and that no root was returned.
func wantRefusal(sentinel error, text string) func(*testing.T, string, *os.Root, error) {
	return func(t *testing.T, path string, root *os.Root, err error) {
		t.Helper()
		if err == nil {
			if root != nil {
				_ = root.Close()
			}
			t.Fatalf("OpenDir succeeded, want %v", sentinel)
		}
		if root != nil {
			t.Fatal("root is not nil")
		}
		if !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want %v", err, sentinel)
		}
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("err %q does not contain %q", err, text)
		}
		if !strings.Contains(err.Error(), path) {
			t.Fatalf("err %q does not contain the path", err)
		}
		if _, lerr := os.Lstat(path); lerr != nil {
			t.Fatalf("the refused path is gone: %v", lerr)
		}
	}
}

// assertLinkIntact checks that the symlink at path is still a symlink to
// target and that target still holds its file.
func assertLinkIntact(t *testing.T, path, target string) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced")
	}
	data, err := os.ReadFile(filepath.Join(target, "keep"))
	if err != nil || string(data) != "data" {
		t.Fatalf("the link target changed: %q, %v", data, err)
	}
}

func TestOpenDirLeavesRefusedPathsAlone(t *testing.T) {
	for _, layout := range layouts {
		t.Run(layout.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), layout.rel)
			mkParent(t, path)
			target := filepath.Join(filepath.Dir(path), "elsewhere")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target, "keep"), []byte("data"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenDir(path); !errors.Is(err, ErrCacheSymlink) {
				t.Fatalf("err = %v, want ErrCacheSymlink", err)
			}
			assertLinkIntact(t, path, target)
		})
	}
}

func TestOpenDirRefusesADirectorySwappedAfterTheOpen(t *testing.T) {
	cases := []struct {
		name string
		swap func(t *testing.T, path string)
		want error
	}{
		{
			name: "another directory",
			swap: func(t *testing.T, path string) {
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrCacheReplaced,
		},
		{
			name: "a symlink",
			swap: func(t *testing.T, path string) {
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".old", path); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrCacheSymlink,
		},
		{
			name: "a regular file",
			swap: func(t *testing.T, path string) {
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrCacheNotDirectory,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repo.requirements")
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			ran := false
			root, err := openDirWith(path, func() {
				ran = true
				tc.swap(t, path)
			})
			if !ran {
				t.Fatal("the hook did not run")
			}
			if root != nil {
				_ = root.Close()
				t.Fatal("root is not nil")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("err %q does not contain the path", err)
			}
		})
	}
}

func TestOpenDirReportsAPathRemovedAfterTheOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo.requirements")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := openDirWith(path, func() {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	})
	if root != nil {
		_ = root.Close()
		t.Fatal("root is not nil")
	}
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want fs.ErrNotExist naming the path", err)
	}
}

func TestOpenDirReturnsAUsableRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo.requirements")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := OpenDir(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if err := root.Mkdir("child", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, "child")); err != nil {
		t.Fatal(err)
	}
}

func mkParent(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
}
