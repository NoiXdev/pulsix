// The traversal half of the requirements-under-the-play-uid proof: the two
// traversal fixtures — a collection tarball whose MANIFEST.json name climbs
// to an absolute path and a role dependency named "../../../ESCAPED_DEP" —
// fail the run with "Permission denied", and nothing appears at their
// targets. internal/worker/runner's
// TestTraversalFixturesEscapeWithoutIsolationInRealAnsible is the control:
// without a play uid the same fixtures do write there.
package uidboundary

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/worker/requirements/requirementstest"
)

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
