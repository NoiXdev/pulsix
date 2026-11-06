// The requirements journey (spec 2026-11-06-requirements-cache, "Test
// strategy"): one pass through the real ansible-runner. A repository whose
// requirements are vendored (a role tarball, a collection as a directory)
// is pushed and synced; the sync installs them for its commit and logs it,
// and the sync's read sees the installed role. A run of a template on that
// playbook then reuses the install and executes a task from the installed
// collection and the installed role — so ansible-runner overrode neither
// the roles path nor the collections path of the generated ansible.cfg.
//
// Real mode only (PULSIX_E2E_REAL_ANSIBLE=1, CI): the fake runner executes
// no playbook and galaxy is real there only.
package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"
	"testing"
	"time"
)

const requirementsFile = `roles:
  - name: req_role
    src: ./vendor/req_role.tar.gz
collections:
  - name: ./vendor/demo
    type: dir
`

const reqRoleSpecs = `argument_specs:
  main:
    short_description: Greets from the installed role
    options:
      req_role_greeting:
        type: str
        default: hi
        description: The greeting
`

const reqRoleTasks = `- name: Pulsix installed role
  ansible.builtin.debug:
    msg: from-installed-role
`

const demoGalaxy = `namespace: pulsix_e2e
name: demo
version: 1.0.0
readme: README.md
authors:
  - pulsix
`

// demoHello is a controller-side action plugin with no module, so the
// exec-only SSH host serves it like it serves debug.
const demoHello = `from ansible.plugins.action import ActionBase


class ActionModule(ActionBase):
    def run(self, tmp=None, task_vars=None):
        result = super(ActionModule, self).run(tmp, task_vars)
        result["msg"] = "from-collection-entry"
        return result
`

const requirementsDemoPlaybook = `- name: Pulsix requirements demo
  hosts: all
  gather_facts: false
  roles:
    - req_role
  tasks:
    - name: Pulsix collection hello
      pulsix_e2e.demo.hello:
      register: hello
    - name: Pulsix collection result
      ansible.builtin.debug:
        var: hello.msg
`

func TestRequirementsEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("no docker for the testcontainer database: %v", err)
	}
	if os.Getenv("PULSIX_E2E_REAL_ANSIBLE") != "1" {
		t.Skip("galaxy and ansible-runner are real only with PULSIX_E2E_REAL_ANSIBLE=1")
	}
	// Every source is vendored: a source that would need the network fails
	// against a closed port instead of reaching it. Loopback (the manager,
	// the worker's gRPC) is never proxied.
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		t.Setenv(name, "http://127.0.0.1:9")
	}
	for _, name := range []string{"NO_PROXY", "no_proxy"} {
		t.Setenv(name, "")
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := wireScenario(t, ctx, cancel, wiring{})
	s.driveRequirements()
}

func (s *scenario) driveRequirements() {
	s.t.Helper()
	prefix := "/api/v1/" + s.apiPath()
	s.pinHostKey()

	roleArchive := roleTarball(s.t, "req_role", map[string]string{
		"meta/argument_specs.yml": reqRoleSpecs,
		"tasks/main.yml":          reqRoleTasks,
	})
	pushFixtureChange(s.t, s.git.bare, map[string]string{
		"requirements.yml":                    requirementsFile,
		"vendor/req_role.tar.gz":              roleArchive,
		"vendor/demo/galaxy.yml":              demoGalaxy,
		"vendor/demo/README.md":               "The demo collection.\n",
		"vendor/demo/plugins/action/hello.py": demoHello,
		"requirements_demo.yml":               requirementsDemoPlaybook,
	})

	// --- The sync installs the requirements for its commit ------------------
	var syncOut repoSyncOut
	s.post(prefix+"/repos/"+s.repoID+"/sync", struct{}{}, &syncOut)
	sync := s.awaitJob(syncOut.Job.ID, "sync_repo", 5*time.Minute)
	if sync.Job.Status != "succeeded" {
		s.t.Fatalf("repo sync with requirements = %q (error %q)", sync.Job.Status, sync.Job.Error)
	}
	if got := sync.Job.Stats["requirements_installed"]; got != true {
		s.t.Fatalf("sync stats requirements_installed = %v, want true", got)
	}
	syncEvents := s.awaitEvents(syncOut.Job.ID, 30*time.Second, func(e jobEventsOut) bool {
		return hasStdoutPrefix(e, "Requirements: installing for commit")
	})

	// --- The sync's read saw the installed role ------------------------------
	var playbooks repoPlaybooksOut
	s.get(prefix+"/repos/"+s.repoID+"/playbooks", &playbooks)
	if playbooks.Sources == nil || len(playbooks.Sources.Commit) != 40 {
		s.t.Fatalf("repo sources = %+v, want a 40-character commit", playbooks.Sources)
	}
	commit := playbooks.Sources.Commit
	if !hasStdoutPrefix(syncEvents, "Requirements: installing for commit "+commit[:7]+" from ") {
		s.t.Fatalf("the sync's install line does not name commit %s: %s", commit[:7], stdoutLines(syncEvents))
	}
	found := false
	for _, p := range playbooks.Playbooks {
		if p.Path != "requirements_demo.yml" {
			continue
		}
		found = true
		if p.Failed || p.Fields != 1 {
			s.t.Fatalf("discovered requirements_demo.yml = %+v, want 1 field (req_role_greeting) read from the installed role", p)
		}
	}
	if !found {
		s.t.Fatalf("discovered playbooks %+v do not list requirements_demo.yml", playbooks.Playbooks)
	}

	// --- A run reuses the install and executes from both entries -------------
	var picked playbookOut
	s.post(prefix+"/repos/"+s.repoID+"/playbooks", map[string]any{"path": "requirements_demo.yml"}, &picked)
	tid := s.createTemplate("requirements-demo", picked.Playbook.ID, map[string]any{})
	runID := s.runTemplateSucceeds(tid, "with installed requirements")

	reuse := "Requirements: using the install for commit " + commit[:7] + " on this worker"
	events := s.awaitEvents(runID, 30*time.Second, func(e jobEventsOut) bool {
		raw, _ := json.Marshal(e.Events)
		return hasStdoutPrefix(e, reuse) &&
			strings.Contains(string(raw), "from-collection-entry") && strings.Contains(string(raw), "from-installed-role")
	})
	if hasStdoutPrefix(events, "Requirements: installing") {
		s.t.Fatalf("the run installed again instead of reusing the sync's install: %s", stdoutLines(events))
	}
}

// hasStdoutPrefix reports whether a stdout event's text starts with prefix.
func hasStdoutPrefix(e jobEventsOut, prefix string) bool {
	return slices.ContainsFunc(stdoutTexts(e), func(text string) bool { return strings.HasPrefix(text, prefix) })
}

// stdoutLines is the stdout texts, for failure messages.
func stdoutLines(e jobEventsOut) string { return strings.Join(stdoutTexts(e), " | ") }

func stdoutTexts(e jobEventsOut) []string {
	var texts []string
	for _, ev := range e.Events {
		if ev.State != "stdout" {
			continue
		}
		if text, ok := ev.Payload["text"].(string); ok {
			texts = append(texts, text)
		}
	}
	return texts
}

// roleTarball is a role tarball as galaxy installs it: files (paths
// relative to the role) under the top directory name, always with a
// meta/main.yml (galaxy refuses a role tarball without one), which files
// may replace. Test packages do not share helpers; this mirrors
// internal/worker/runner/requirements_real_test.go. The result is a string
// because pushFixtureChange carries file contents as strings.
func roleTarball(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	members := map[string]string{name + "/meta/main.yml": "galaxy_info: {author: pulsix}\ndependencies: []\n"}
	for rel, content := range files {
		members[name+"/"+rel] = content
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	dirs := map[string]bool{}
	for _, member := range slices.Sorted(func(yield func(string) bool) {
		for m := range members {
			if !yield(m) {
				return
			}
		}
	}) {
		var parents []string
		for d := path.Dir(member); d != "." && !dirs[d]; d = path.Dir(d) {
			dirs[d] = true
			parents = append(parents, d)
		}
		slices.Reverse(parents)
		for _, d := range parents {
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: d + "/", Mode: 0o755}); err != nil {
				t.Fatal(err)
			}
		}
		content := members[member]
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: member, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
