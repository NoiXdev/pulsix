// A repo run's requirements install under the play uid (spec
// 2026-11-06-requirements-cache, "Owner decision 2026-11-06: galaxy runs
// under the play uid"): ansible-galaxy writes where the requirements'
// metadata tells it to, not only below -p, so only the kernel can keep it
// inside its temp cache entry. That is provable only here, in the image,
// where uid 65533, the shared group and the setuid helper exist.
//
// A legitimate install of roles and collections works under the play uid
// and is published worker-owned (this file); the traversal fixtures that
// must not write outside their entry are in requirements_escape_test.go.
package uidboundary

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// assertPublishedInstall is what a successful run of installFixture's
// repository leaves: the run succeeded and said so, the play used what was
// installed, the worker logged no requirements warning, and the published
// entry in repoID's requirements cache is wholly the worker's with exactly
// the modes the take-over and ShareRepoRead give it, with nothing of the
// install left beside it.
func assertPublishedInstall(t *testing.T, repoID string, result *workerpb.ReportResultRequest, events []*workerpb.RunEvent, logs string) {
	t.Helper()
	if got := result.GetStatus(); got != "succeeded" {
		t.Fatalf("status = %q, want succeeded (error: %q):\n%s", got, result.GetError(), eventDump(events))
	}
	for _, want := range []string{
		"Requirements: installing for commit ", "from requirements.yml, collections/requirements.yml", "Requirements: installed in ",
		"from-installed-role", "from-entry-collection", "from-requirements-collection",
	} {
		if !eventsContain(events, want) {
			t.Errorf("no event contains %q: the install or the play using it did not run:\n%s", want, eventDump(events))
		}
	}
	if w := requirementsWarnings(logs); len(w) != 0 {
		t.Errorf("the worker logged %d requirements warnings, want none:\n%s", len(w), strings.Join(w, "\n"))
	}

	dir := requirementsDir(repoID)
	if info, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	} else if perm := info.Mode().Perm(); perm&0o022 != 0 {
		t.Errorf("%s = %v is group- or world-writable; a play could change a later run's install", dir, perm)
	}
	entries, temps := cacheNames(t, dir)
	if len(temps) != 0 {
		t.Errorf("temp entries left after the run: %q", temps)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %q, want one", entries)
	}
	// The take-over publishes directories 0700 and files 0600 (0700 when any
	// execute bit was set); each run then opens the entry to the play's group
	// read-only (ShareRepoRead mirrors the owner's read and execute bits into
	// the group and clears group write): directories end 0750, files 0640 or
	// 0750, and nothing is open to others. A symlink is recreated by the
	// take-over and skipped by ShareRepoRead, so only its owner is checked.
	var n int
	err := filepath.WalkDir(filepath.Join(dir, entries[0]), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		n++
		if uid := info.Sys().(*syscall.Stat_t).Uid; uid != workerUID {
			t.Errorf("%s is owned by uid %d, want the worker's %d: the play uid's files survived the take-over", p, uid, workerUID)
		}
		mode := info.Mode()
		if mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			t.Errorf("%s = %v keeps a setuid, setgid or sticky bit", p, mode)
		}
		switch perm := mode.Perm(); {
		case mode.IsDir():
			if perm != 0o750 {
				t.Errorf("directory %s = %v, want exactly 0750", p, perm)
			}
		case mode.IsRegular():
			if perm != 0o640 && perm != 0o750 {
				t.Errorf("file %s = %v, want 0640 or 0750", p, perm)
			}
		case mode&os.ModeSymlink != 0:
			// Owner-checked above; a link's own mode is 0777 on Linux.
		default:
			t.Errorf("%s is a %s (%v): neither a directory, a regular file nor a symlink survives the take-over", p, mode.Type(), mode)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the entry: %v", err)
	}
	if n < 10 {
		t.Errorf("the entry holds %d paths; the role and both collections should be there", n)
	}
}

// TestRequirementsInstallUnderThePlayUID: a role tarball and two type: dir
// collections, one file listing a role and a collection, install as the
// play uid and the play uses them; the published entry is wholly the
// worker's and nothing of the install is left beside it.
func TestRequirementsInstallUnderThePlayUID(t *testing.T) {
	inImage(t)
	writeWorkerKey(t)
	const repoID = "01900000-0000-7000-8000-0000000000f1"

	result, events, logs := runOneJob(t, repoJob(t, "01900000-0000-7000-8000-0000000000e1", repoID, gitFixture(t, installFixture(t))))

	assertPublishedInstall(t, repoID, result, events, logs)
}
