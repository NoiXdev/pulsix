// The clone's read root as the requirements check sees it (OpenCloneFS):
// it reads links itself (fs.ReadLinkFS), confined like every other path,
// so a source that climbs out of a subdirectory and back into the clone
// is told apart from one that leaves it.
package worker

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/worker/requirements"
	"github.com/noixdev/pulsix/internal/worker/requirements/requirementstest"
)

func TestOpenCloneFSReadsLinksInsideTheRoot(t *testing.T) {
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"secret.yml": "x"})
	clone := t.TempDir()
	writeTree(t, clone, map[string]string{
		".git/config":     "[core]",
		"ansible/a.yml":   "a",
		"vendor/r.tar.gz": "r",
	})
	symlinks(t, clone, map[string]string{
		"ansible/up":  "../vendor",
		"ansible/abs": outside,
		"ansible/out": "../../" + filepath.Base(outside),
		"linkdir":     "ansible",
		"gitlink":     ".git",
	})
	root, err := OpenCloneFS(clone)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	links, ok := root.(fs.ReadLinkFS)
	if !ok {
		t.Fatalf("%T does not read links", root)
	}

	// A link is described and read, not followed.
	for name, want := range map[string]string{"ansible/up": "../vendor", "ansible/abs": outside, "linkdir": "ansible", "gitlink": ".git"} {
		fi, err := links.Lstat(name)
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			t.Errorf("Lstat(%s) = %v, %v; want the link itself", name, fi, err)
		}
		if got, err := links.ReadLink(name); err != nil || got != want {
			t.Errorf("ReadLink(%s) = %q, %v; want %q", name, got, err, want)
		}
	}
	// The parent is resolved like any path: a link inside is followed.
	if fi, err := links.Lstat("linkdir/a.yml"); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("Lstat(linkdir/a.yml) = %v, %v; want the file", fi, err)
	}
	if fi, err := links.Lstat("."); err != nil || !fi.IsDir() {
		t.Errorf("Lstat(.) = %v, %v; want the clone", fi, err)
	}
	// A parent that leads out of the clone, or into git's files, is refused.
	for _, name := range []string{"ansible/abs/secret.yml", "ansible/out/secret.yml", ".git", ".GIT/config", "gitlink/config", "../x"} {
		if fi, err := links.Lstat(name); err == nil || errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Lstat(%s) = %v, %v; want a refusal", name, fi, err)
		}
		if got, err := links.ReadLink(name); err == nil || errors.Is(err, fs.ErrNotExist) {
			t.Errorf("ReadLink(%s) = %q, %v; want a refusal", name, got, err)
		}
	}
	if _, err := links.Lstat("ansible/missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lstat(ansible/missing): err = %v, want not exist", err)
	}
	if got, err := links.ReadLink("ansible/a.yml"); err == nil {
		t.Errorf("ReadLink of a file = %q, want an error", got)
	}

	// No clone: no root, and no non-nil interface holding a nil one.
	if r, err := OpenCloneFS(filepath.Join(t.TempDir(), "missing")); err == nil || r != nil {
		t.Errorf("OpenCloneFS(missing) = %v, %v; want nil and an error", r, err)
	}
}

// A requirements file in a subdirectory may name a source beside it with
// "../": the check reads the links of the clone through OpenCloneFS and
// accepts it, and still refuses one that leaves the clone.
func TestOpenCloneFSLetsTheRequirementsCheckClimbInsideTheClone(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name, src, wantErr string
	}{
		{"beside the subpath", "../vendor/r.tar.gz", ""},
		{"out of the clone", "../../r.tar.gz", `the source "../../r.tar.gz" is outside the repository`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clone := filepath.Join(t.TempDir(), "clone")
			writeTree(t, clone, map[string]string{
				"ansible/requirements.yml": "roles:\n  - name: r\n    src: " + tc.src + "\n",
				"vendor/r.tar.gz":          "r",
			})
			root, err := OpenCloneFS(clone)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			files, err := requirements.Find(root, "ansible")
			if err != nil {
				t.Fatal(err)
			}
			galaxy := &requirementstest.Galaxy{}
			svc := requirements.NewService(galaxy, &requirementstest.Clock{Step: time.Second}, 0)
			_, err = svc.Ensure(context.Background(), requirements.Request{
				CloneDir: clone, Clone: root, Files: files, Commit: commit, RepoCacheDir: filepath.Dir(clone),
			})
			if tc.wantErr == "" {
				if err != nil || len(galaxy.Calls()) != 1 {
					t.Fatalf("Ensure: %v, %d galaxy calls; want one install", err, len(galaxy.Calls()))
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Ensure: %v; want %q", err, tc.wantErr)
			}
			if _, statErr := os.Stat(requirements.CacheDir(clone)); !errors.Is(statErr, fs.ErrNotExist) {
				t.Errorf("a refused file touched the cache: %v", statErr)
			}
		})
	}
}
