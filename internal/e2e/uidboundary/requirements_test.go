// A repo run's requirements install under the play uid (spec
// 2026-11-06-requirements-cache, "Owner decision 2026-11-06: galaxy runs
// under the play uid"): ansible-galaxy writes where the requirements'
// metadata tells it to, not only below -p, so only the kernel can keep it
// inside its temp cache entry. That is provable only here, in the image,
// where uid 65533, the shared group and the setuid helper exist.
//
// A legitimate install of roles and collections works under the play uid
// and is published worker-owned; the two traversal fixtures — a collection
// tarball whose MANIFEST.json name climbs to an absolute path and a role
// dependency named "../../../ESCAPED_DEP" — fail the run with "Permission
// denied", and nothing appears at their targets. internal/worker/runner's
// TestTraversalFixturesEscapeWithoutIsolationInRealAnsible is the control:
// without a play uid the same fixtures do write there.
package uidboundary

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker/requirements/requirementstest"
)

// debugTask is a task file that prints msg.
func debugTask(msg string) string { return "- ansible.builtin.debug:\n    msg: " + msg + "\n" }

// vendoredCollection is a type: dir collection pulsix_test.<name> at
// vendor/<name>, whose role greet prints msg.
func vendoredCollection(name, msg string) map[string]string {
	dir := "vendor/" + name + "/"
	return map[string]string{
		dir + "galaxy.yml":                 "namespace: pulsix_test\nname: " + name + "\nversion: 1.0.0\nreadme: README.md\nauthors: [pulsix]\n",
		dir + "README.md":                  "# " + name + "\n",
		dir + "roles/greet/tasks/main.yml": debugTask(msg),
	}
}

// installFixture is a repository whose playbooks/site.yml uses a role and
// two collections its requirements install: requirements.yml lists the
// vendored role tarball AND a collection (one file, so its collection
// install finds the role install's play-owned files in the temp entry),
// collections/requirements.yml a second collection. Every source is a
// path relative to its file's directory, galaxy's working directory.
func installFixture(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{
		"playbooks/site.yml": "- hosts: localhost\n  gather_facts: false\n  roles:\n" +
			"    - from_installed\n    - pulsix_test.demo.greet\n    - pulsix_test.both.greet\n",
		"requirements.yml": "roles:\n  - {name: from_installed, src: ./vendor/from_installed.tar.gz}\n" +
			"collections:\n  - {name: ./vendor/both, type: dir}\n",
		"collections/requirements.yml": "collections: [{name: ../vendor/demo, type: dir}]\n",
		"vendor/from_installed.tar.gz": requirementstest.RoleTarball(t, "from_installed", map[string]string{"tasks/main.yml": debugTask("from-installed-role")}),
	}
	for _, c := range []map[string]string{
		vendoredCollection("demo", "from-entry-collection"),
		vendoredCollection("both", "from-requirements-collection"),
	} {
		for rel, content := range c {
			files[rel] = content
		}
	}
	return files
}

// repoJob is a run of the repository at source's playbooks/site.yml,
// shaped like TestRepoPlaybookRunsUnderThePlayUID's.
func repoJob(t *testing.T, jobID, repoID, source string) *workerpb.Job {
	t.Helper()
	job := localhostJob(t, "")
	job.Id = jobID
	job.Playbook = "repo:" + repoID + ":playbooks/site.yml"
	job.KindPayload = `{"repo":{"id":"` + repoID + `","url":"` + source + `","branch":"main","subpath":"playbooks/site.yml","repo_subpath":""}}`
	return job
}

// orgCache is the organization's directory in the repo cache: the clones'
// and their requirements caches' parent.
func orgCache() string { return filepath.Join(dataDir, "repo-cache", orgID) }

// requirementsDir is the requirements cache of the repository's clone.
func requirementsDir(repoID string) string { return filepath.Join(orgCache(), repoID+".requirements") }

// cacheNames are the published entries and the temp entries (".tmp-*") in
// the requirements cache dir.
func cacheNames(t *testing.T, dir string) (entries, temps []string) {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, de := range des {
		switch {
		case strings.HasPrefix(de.Name(), ".tmp-"):
			temps = append(temps, de.Name())
		case de.IsDir() && !strings.HasPrefix(de.Name(), "."):
			entries = append(entries, de.Name())
		}
	}
	return entries, temps
}

// requirementsWarnings are the worker log's warnings about the install:
// a cleanup that failed (a leftover temp entry, marking or evicting an
// entry) or galaxy running without isolation.
func requirementsWarnings(logs string) []string {
	var out []string
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "level=WARN") &&
			(strings.Contains(line, "requirements") || strings.Contains(line, "galaxy") || strings.Contains(line, "take-over")) {
			out = append(out, line)
		}
	}
	return out
}

// lastText is the last event's state and its {"text": …} payload's text.
func lastText(t *testing.T, events []*workerpb.RunEvent) (string, string) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("no events")
	}
	last := events[len(events)-1]
	var p struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(last.GetPayload()), &p); err != nil {
		t.Fatalf("last event payload %q: %v", last.GetPayload(), err)
	}
	return last.GetState(), p.Text
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
	// Published 0700/0600 by the take-over; each run opens it to the play's
	// group read-only (ShareRepoRead), never to write and never to others.
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
		if perm := info.Mode().Perm(); perm&0o027 != 0 {
			t.Errorf("%s = %v: group-writable or open to others", p, perm)
		}
		if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			t.Errorf("%s = %v keeps a setuid, setgid or sticky bit", p, info.Mode())
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

// TestRequirementsCannotWriteOutsideTheirEntry: the two traversal fixtures
// fail the run with galaxy's "Permission denied", nothing exists where
// their metadata points, nothing is published and the clone is unchanged.
// A failure with the target present is the isolation not holding: fix the
// install's preparation (runner's prepareInstall), never this test.
func TestRequirementsCannotWriteOutsideTheirEntry(t *testing.T) {
	cases := []struct {
		name   string
		jobID  string
		repoID string
		target string
		// also are further paths galaxy tries and must not create.
		also  []string
		files func(t *testing.T, target string) map[string]string
	}{
		{
			name:   "collection tarball naming an absolute path",
			jobID:  "01900000-0000-7000-8000-0000000000e2",
			repoID: "01900000-0000-7000-8000-0000000000f2",
			target: filepath.Join(orgCache(), "ESCAPED_COLLECTION"),
			files: func(t *testing.T, target string) map[string]string {
				return map[string]string{
					"collections/requirements.yml": "collections: [{name: ../vendor/evil.tar.gz, type: file}]\n",
					"vendor/evil.tar.gz":           requirementstest.EvilCollectionTarball(t, target),
				}
			},
		},
		{
			// Beside the clones: roles/ -> .tmp-<x> -> <repo>.requirements -> <org>.
			name:   "role dependency named with a path",
			jobID:  "01900000-0000-7000-8000-0000000000e3",
			repoID: "01900000-0000-7000-8000-0000000000f3",
			target: filepath.Join(orgCache(), "ESCAPED_DEP"),
			// On EACCES galaxy retries a role in the next roles path (the
			// default ~/.ansible/roles, /usr/share/ansible/roles,
			// /etc/ansible/roles): ~ is the temp entry's home/, so the
			// same name climbs to <tmp>/ESCAPED_DEP (gone with the temp
			// entry), /usr/ESCAPED_DEP and /ESCAPED_DEP.
			also: []string{"/usr/ESCAPED_DEP", "/ESCAPED_DEP"},
			files: func(t *testing.T, _ string) map[string]string {
				return map[string]string{
					"requirements.yml": "- {name: outer, src: ./vendor/outer.tar.gz}\n",
					"vendor/outer.tar.gz": requirementstest.RoleTarball(t, "outer", map[string]string{
						"meta/main.yml":  "galaxy_info: {author: pulsix}\ndependencies:\n  - {src: ./vendor/inner.tar.gz, name: ../../../ESCAPED_DEP}\n",
						"tasks/main.yml": debugTask("outer"),
					}),
					"vendor/inner.tar.gz": requirementstest.RoleTarball(t, "inner", map[string]string{"tasks/main.yml": debugTask("inner")}),
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inImage(t)
			writeWorkerKey(t)
			targets := append([]string{tc.target}, tc.also...)
			for _, p := range targets {
				if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("%s exists before the run (%v): the test cannot tell an escape from a leftover", p, err)
				}
			}

			job := repoJob(t, tc.jobID, tc.repoID, gitFixture(t, tc.files(t, tc.target)))
			result, events, logs := runOneJob(t, job)

			if got := result.GetStatus(); got != "error" {
				t.Errorf("status = %q, want error (error: %q)", got, result.GetError())
			}
			msg := result.GetError()
			t.Logf("error: %q", msg)
			if !strings.HasPrefix(msg, "install requirements: ansible-galaxy ") || !strings.Contains(msg, "Permission denied") {
				t.Errorf("error = %q, want it to start with %q and contain %q", msg, "install requirements: ansible-galaxy ", "Permission denied")
			}
			if state, text := lastText(t, events); state != "stdout" || !strings.HasPrefix(text, "Requirements: install requirements:") {
				t.Errorf("last event = %s %q, want stdout starting %q", state, text, "Requirements: install requirements:")
			}
			for _, p := range targets {
				if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("%s: Lstat = %v, want ENOENT — galaxy wrote outside its entry, the isolation does not hold", p, err)
				}
			}
			entries, temps := cacheNames(t, requirementsDir(tc.repoID))
			if len(entries) != 0 || len(temps) != 0 {
				t.Errorf("requirements cache holds entries %q and temp entries %q, want neither", entries, temps)
			}
			if w := requirementsWarnings(logs); len(w) != 0 {
				t.Errorf("the worker logged %d requirements warnings, want none:\n%s", len(w), strings.Join(w, "\n"))
			}
			clone := filepath.Join(orgCache(), tc.repoID)
			out, err := exec.Command("git", "-C", clone, "status", "--porcelain").CombinedOutput()
			if err != nil {
				t.Fatalf("git status in %s: %v\n%s", clone, err, out)
			}
			if len(out) != 0 {
				t.Errorf("the clone changed:\n%s", out)
			}
		})
	}
}
