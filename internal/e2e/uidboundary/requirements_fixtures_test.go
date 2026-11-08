// Fixtures and readers shared by the in-image requirements tests: the
// repository they run, the requirements cache they inspect and the events
// and worker log they read.
package uidboundary

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// requirementsWarnings are the worker log's warnings about the install and
// about stopping it: a cleanup that failed (a leftover temp entry, marking
// or evicting an entry), galaxy running without isolation, and the stop
// path's (signalGroup's "setuid helper could not signal ..." and
// stopGroup's "play supervisor did not stop ..." or "reported a problem").
func requirementsWarnings(logs string) []string {
	var out []string
	for _, line := range strings.Split(logs, "\n") {
		if !strings.Contains(line, "level=WARN") {
			continue
		}
		for _, word := range []string{"requirements", "galaxy", "take-over", "setuid helper", "play supervisor"} {
			if strings.Contains(line, word) {
				out = append(out, line)
				break
			}
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
