// A repo run whose requirements step fails, is refused or is cancelled
// ends before any run file is written (spec 2026-11-06-requirements-cache,
// goal 5; the plan's Rulings 6 and 12): the error names the reason, and
// the same text is the run log's last line unless the run was cancelled.
package runner

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/worker"
	"github.com/noixdev/pulsix/internal/worker/requirements"
	"github.com/noixdev/pulsix/internal/worker/requirements/requirementstest"
)

func TestRepoRunFailsWhenItsRequirementsDoNotInstall(t *testing.T) {
	source := gitFixtureFiles(t, map[string]string{"playbooks/site.yml": sitePlaybook, "requirements.yml": repoRequirements})
	galaxy := &requirementstest.Galaxy{Err: &requirements.ExitError{Code: 1, Stderr: "ERROR! boom"}}
	r, args := requirementsRunner(t, galaxy)
	env := runEnv(t)
	job, _ := repoRunJob(t, source)
	c := &collector{}

	status, _, err := r.Run(context.Background(), job, env, c.sink)
	const want = "install requirements: ansible-galaxy role install -r requirements.yml: exit 1: ERROR! boom"
	if status != worker.StatusError || err == nil || err.Error() != want {
		t.Fatalf("Run = %q, %v; want error %q", status, err, want)
	}
	assertEndsWithRequirementsFailure(t, c, want)
	if _, err := os.Stat(args); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ansible-runner was started: %v", err)
	}
}

// assertEndsWithRequirementsFailure checks that the run never started and
// that its last event is the stdout line "Requirements: <msg>".
func assertEndsWithRequirementsFailure(t *testing.T, c *collector, msg string) {
	t.Helper()
	if slices.Contains(c.states(), evStateRunStart) {
		t.Errorf("events = %q, want no run_start", c.states())
	}
	if len(c.events) == 0 {
		t.Fatal("no events")
	}
	last := c.events[len(c.events)-1]
	if last.GetState() != evStateStdout || eventText(t, last) != "Requirements: "+msg {
		t.Errorf("last event = %q %q, want stdout %q", last.GetState(), last.GetPayload(), "Requirements: "+msg)
	}
}

func TestRepoRunFailsWhenTheRequirementsCacheIsRefused(t *testing.T) {
	cases := []struct {
		name   string
		plant  func(t *testing.T, path string) string // returns the link's target dir, or ""
		reason string
	}{
		{"symlink to a directory", func(t *testing.T, path string) string {
			target := t.TempDir()
			if err := os.WriteFile(filepath.Join(target, "keep"), []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			return target
		}, "a symlink, which is not followed"},
		{"dangling symlink", func(t *testing.T, path string) string {
			if err := os.Symlink(filepath.Join(t.TempDir(), "gone"), path); err != nil {
				t.Fatal(err)
			}
			return ""
		}, "a symlink, which is not followed"},
		{"file", func(t *testing.T, path string) string {
			if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
				t.Fatal(err)
			}
			return ""
		}, "not a directory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := gitFixtureFiles(t, map[string]string{"playbooks/site.yml": sitePlaybook, "requirements.yml": repoRequirements})
			galaxy := &requirementstest.Galaxy{}
			r, _ := requirementsRunner(t, galaxy)
			env := runEnv(t)
			job, repo := repoRunJob(t, source)
			cache := requirements.CacheDir(cloneOf(env, repo))
			if err := os.MkdirAll(filepath.Dir(cache), 0o700); err != nil {
				t.Fatal(err)
			}
			target := tc.plant(t, cache)
			before, err := os.Lstat(cache)
			if err != nil {
				t.Fatal(err)
			}
			c := &collector{}

			status, _, err := r.Run(context.Background(), job, env, c.sink)
			want := "install requirements: open requirements cache " + cache + ": " + tc.reason
			if status != worker.StatusError || err == nil || err.Error() != want {
				t.Fatalf("Run = %q, %v; want error %q", status, err, want)
			}
			assertEndsWithRequirementsFailure(t, c, want)
			if _, err := os.Stat(filepath.Join(env.WorkDir, "ansible.cfg")); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("ansible.cfg was written: %v", err)
			}
			if n := len(galaxy.Calls()); n != 0 {
				t.Errorf("galaxy calls = %d, want none", n)
			}
			after, err := os.Lstat(cache)
			if err != nil || after.Mode().Type() != before.Mode().Type() {
				t.Errorf("the refused path changed: %v, %v", after, err)
			}
			if target != "" {
				if b, err := os.ReadFile(filepath.Join(target, "keep")); err != nil || string(b) != "keep" {
					t.Errorf("the link's target changed: %q, %v", b, err)
				}
				if des, _ := os.ReadDir(target); len(des) != 1 {
					t.Errorf("the link's target holds %d entries, want keep only", len(des))
				}
			}
		})
	}
}

func TestRepoRunCancelledDuringTheInstall(t *testing.T) {
	source := gitFixtureFiles(t, map[string]string{"playbooks/site.yml": sitePlaybook, "requirements.yml": repoRequirements})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	galaxy := &requirementstest.Galaxy{Hold: make(chan struct{}), OnInstall: func(requirements.Install) error {
		cancel()
		return nil
	}}
	r, args := requirementsRunner(t, galaxy)
	env := runEnv(t)
	job, repo := repoRunJob(t, source)
	c := &collector{}

	_, _, err := r.Run(ctx, job, env, c.sink)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v, want context.Canceled", err)
	}
	for _, ev := range c.events {
		if text := eventText(t, ev); strings.HasPrefix(text, "Requirements: install requirements") {
			t.Errorf("a cancelled install logged a failure: %q", text)
		}
	}
	des, err := os.ReadDir(requirements.CacheDir(cloneOf(env, repo)))
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range des {
		if strings.HasPrefix(de.Name(), ".tmp-") {
			t.Errorf("leftover %s", de.Name())
		}
	}
	if _, err := os.Stat(args); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ansible-runner was started: %v", err)
	}
}

func TestRepoRunWithRequirementsAndNoInstaller(t *testing.T) {
	source := gitFixtureFiles(t, map[string]string{"playbooks/site.yml": sitePlaybook, "requirements.yml": repoRequirements})
	r, _ := requirementsRunner(t, nil)
	env := runEnv(t)
	job, _ := repoRunJob(t, source)
	c := &collector{}

	status, _, err := r.Run(context.Background(), job, env, c.sink)
	const want = "install requirements: no requirements installer wired"
	if status != worker.StatusError || err == nil || err.Error() != want {
		t.Fatalf("Run = %q, %v; want error %q", status, err, want)
	}
	assertEndsWithRequirementsFailure(t, c, want)
}

func TestRepoRunEndsWhenTheRequirementsLogCannotBeSent(t *testing.T) {
	source := gitFixtureFiles(t, map[string]string{"playbooks/site.yml": sitePlaybook, "requirements.yml": repoRequirements})
	galaxy := &requirementstest.Galaxy{}
	r, args := requirementsRunner(t, galaxy)
	env := runEnv(t)
	job, _ := repoRunJob(t, source)
	gone := errors.New("stream gone")

	status, _, err := r.Run(context.Background(), job, env, (&collector{err: gone}).sink)
	if status != worker.StatusError || !errors.Is(err, gone) || !strings.HasPrefix(err.Error(), "send requirements log: ") {
		t.Fatalf("Run = %q, %v; want send requirements log: …", status, err)
	}
	if _, err := os.Stat(args); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ansible-runner was started: %v", err)
	}
}
