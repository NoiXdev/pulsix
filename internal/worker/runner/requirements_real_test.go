// A repo run's requirements with a real ansible-galaxy and ansible-playbook
// (spec 2026-11-06-requirements-cache, "Test strategy", real Ansible), fully
// offline: every source is vendored in the fixture repository and galaxy's
// network goes to a closed port. Role and collection installs fill the
// commit's entry, the playbook's own roles/ and collections/ win over it, a
// comment-only file installs nothing, a git+file source is refused — and
// the traversal fixtures the worker image's isolation test uses really do
// write outside the entry when galaxy is not isolated (the control for
// internal/e2e/uidboundary's TestRequirementsCannotWriteOutsideTheirEntry).
package runner

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
	"github.com/noixdev/pulsix/internal/worker/requirements"
	"github.com/noixdev/pulsix/internal/worker/requirements/requirementstest"
)

// requireAnsibleGalaxy is requireAnsiblePlaybook that also needs the
// ansible-galaxy next to it (the one the requirements service runs). It
// returns the ansible-playbook path.
func requireAnsibleGalaxy(t *testing.T) string {
	t.Helper()
	playbook := requireAnsiblePlaybook(t)
	galaxy := galaxyBin(playbook)
	if _, err := exec.LookPath(galaxy); err == nil {
		return playbook
	} else if os.Getenv("PULSIX_E2E_REAL_ANSIBLE") == "1" {
		t.Fatalf("PULSIX_E2E_REAL_ANSIBLE=1 but %s is not installed: %v", galaxy, err)
	} else {
		t.Skipf("%s not installed (set PULSIX_E2E_REAL_ANSIBLE=1 to require it): %v", galaxy, err)
	}
	return ""
}

// debugTask is a task file that prints msg.
func debugTask(msg string) string { return "- ansible.builtin.debug:\n    msg: " + msg + "\n" }

// realRequirementsRun is a repo run of the repository's root site.yml
// whose requirements install through the real ansible-galaxy, with no play
// uid (galaxy runs as the worker), and whose worker log is kept.
type realRequirementsRun struct {
	r    *Runner
	env  worker.RunEnv
	job  *workerpb.Job
	repo uuid.UUID
	log  *bytes.Buffer
}

// newRealRequirementsRun commits files to a fresh repository and prepares
// a run of its site.yml. Galaxy's proxy is a closed local port, so a
// source that would need the network fails instead of reaching it.
func newRealRequirementsRun(t *testing.T, ansible string, files map[string]string) *realRequirementsRun {
	t.Helper()
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		t.Setenv(name, "http://127.0.0.1:9")
	}
	for _, name := range []string{"NO_PROXY", "no_proxy"} {
		t.Setenv(name, "")
	}
	source := gitFixtureFiles(t, files)
	logs := &bytes.Buffer{}
	r := &Runner{GitBin: gitOnPATH(t), Log: slog.New(slog.NewTextHandler(logs, nil))}
	r.Requirements = NewRequirements(r, worker.Config{AnsibleBin: ansible})
	job := runJob(t)
	repo := uuid.Must(uuid.NewV7())
	job.Playbook = "repo:" + repo.String() + ":site.yml"
	job.KindPayload = `{"repo":{"id":"` + repo.String() + `","url":"` + source + `","branch":"main","subpath":"site.yml","repo_subpath":""}}`
	job.ExtraVars = nil
	return &realRequirementsRun{r: r, env: runEnv(t), job: job, repo: repo, log: logs}
}

// clone is the run's clone in the repo cache.
func (h *realRequirementsRun) clone() string { return cloneOf(h.env, h.repo) }

// ensure materializes the playbook and ensures its requirements the way
// Run does; it returns the playbook path, the entry and the step's lines.
func (h *realRequirementsRun) ensure(t *testing.T) (string, requirements.Entry, []string, error) {
	t.Helper()
	playbook, checkout, err := h.r.materializePlaybook(context.Background(), h.job, h.env)
	if err != nil {
		t.Fatalf("materializePlaybook: %v", err)
	}
	var lines []string
	entry, err := h.r.ensureRequirements(context.Background(), h.env, checkout, func(s string) { lines = append(lines, s) })
	return playbook, entry, lines, err
}

// mustNotExist fails t unless nothing exists at p.
func mustNotExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s: Lstat = %v, want it not to exist", p, err)
	}
}

func TestRequirementsInstallAndResolveInRealAnsible(t *testing.T) {
	ansible := requireAnsibleGalaxy(t)
	collection := func(name string) map[string]string {
		dir := "vendor/" + name + "/"
		return map[string]string{
			dir + "galaxy.yml":                 "namespace: pulsix_test\nname: " + name + "\nversion: 1.0.0\nreadme: README.md\nauthors: [pulsix]\n",
			dir + "README.md":                  "# " + name + "\n",
			dir + "roles/greet/tasks/main.yml": debugTask("from-entry-" + name),
		}
	}
	files := map[string]string{
		"site.yml": "- hosts: all\n  gather_facts: false\n  roles:\n    - vendored_role\n    - shadowed_role\n" +
			"    - pulsix_test.demo.greet\n    - pulsix_test.shadow.greet\n",
		"requirements.yml":                   "roles: [{name: vendored_role, src: ./vendor/vendored_role.tar.gz}]\n",
		"roles/requirements.yml":             "- {name: shadowed_role, src: ../vendor/shadowed_role.tar.gz}\n",
		"collections/requirements.yml":       "collections: [{name: ../vendor/demo, type: dir}, {name: ../vendor/shadow, type: dir}]\n",
		"vendor/vendored_role.tar.gz":        requirementstest.RoleTarball(t, "vendored_role", map[string]string{"tasks/main.yml": debugTask("from-vendored-role")}),
		"vendor/shadowed_role.tar.gz":        requirementstest.RoleTarball(t, "shadowed_role", map[string]string{"tasks/main.yml": debugTask("from-entry-shadowed-role")}),
		"roles/shadowed_role/tasks/main.yml": debugTask("from-repo-role"),
		"collections/ansible_collections/pulsix_test/shadow/roles/greet/tasks/main.yml": debugTask("from-adjacent-collection"),
	}
	for _, name := range []string{"demo", "shadow"} {
		for rel, content := range collection(name) {
			files[rel] = content
		}
	}
	h := newRealRequirementsRun(t, ansible, files)
	playbook, entry, lines, err := h.ensure(t)
	if err != nil {
		t.Fatalf("ensureRequirements: %v\nlines:\n%s", err, strings.Join(lines, "\n"))
	}
	if entry.IsZero() {
		t.Fatalf("no entry; lines:\n%s", strings.Join(lines, "\n"))
	}
	for _, p := range []string{
		filepath.Join(entry.RolesDir, "vendored_role", "tasks", "main.yml"),
		filepath.Join(entry.RolesDir, "shadowed_role", "tasks", "main.yml"),
		filepath.Join(entry.CollectionsDir, "ansible_collections", "pulsix_test", "demo", "roles", "greet", "tasks", "main.yml"),
		filepath.Join(entry.CollectionsDir, "ansible_collections", "pulsix_test", "shadow", "roles", "greet", "tasks", "main.yml"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("the entry lacks %s: %v\nlines:\n%s", p, err, strings.Join(lines, "\n"))
		}
	}

	if err := h.r.materializeRunnerConfig(h.job, h.env, entry); err != nil {
		t.Fatalf("materializeRunnerConfig: %v", err)
	}
	cmd := exec.Command(ansible, "-i", "localhost,", "-c", "local", playbook)
	cmd.Env = append(h.r.subprocessEnv(h.env, ansible), "ANSIBLE_NOCOLOR=1", "ANSIBLE_LOCALHOST_WARNING=False")
	raw, err := cmd.CombinedOutput()
	out := string(raw)
	if err != nil {
		t.Fatalf("ansible-playbook: %v\n%s", err, out)
	}
	for _, want := range []string{`"msg": "from-vendored-role"`, `"msg": "from-repo-role"`, `"msg": "from-entry-demo"`, `"msg": "from-adjacent-collection"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %s:\n%s", want, out)
		}
	}
	// The playbook's own roles/ and collections/ win over the entry.
	for _, unwanted := range []string{"from-entry-shadowed-role", "from-entry-shadow"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("output has %s, the entry's copy shadowed the playbook's own:\n%s", unwanted, out)
		}
	}
}

func TestRequirementsCommentOnlyInstallsNothingInRealAnsible(t *testing.T) {
	ansible := requireAnsibleGalaxy(t)
	h := newRealRequirementsRun(t, ansible, map[string]string{
		"site.yml":         "- hosts: all\n  gather_facts: false\n  tasks: []\n",
		"requirements.yml": "# none\n",
	})
	_, entry, lines, err := h.ensure(t)
	if err != nil {
		t.Fatalf("ensureRequirements: %v", err)
	}
	if !entry.IsZero() {
		t.Errorf("entry = %+v, want none", entry)
	}
	if want := []string{"Requirements: requirements.yml lists nothing to install"}; !slices.Equal(lines, want) {
		t.Errorf("lines = %q, want %q", lines, want)
	}
	mustNotExist(t, requirements.CacheDir(h.clone()))
}

// TestTraversalFixturesEscapeWithoutIsolationInRealAnsible is the control
// for the worker image's isolation test: without a play uid nothing stops
// galaxy, and both fixtures write where their metadata points — outside
// the entry, though only inside this test's temp dirs.
func TestTraversalFixturesEscapeWithoutIsolationInRealAnsible(t *testing.T) {
	ansible := requireAnsibleGalaxy(t)
	const site = "- hosts: all\n  gather_facts: false\n  tasks: []\n"
	const warning = "ansible-galaxy is not isolated: " + envPlayUID + " is not set"

	t.Run("collection tarball naming an absolute path", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "outside", "ESCAPED_COLLECTION")
		h := newRealRequirementsRun(t, ansible, map[string]string{
			"site.yml":                     site,
			"collections/requirements.yml": "collections: [{name: ../vendor/evil.tar.gz, type: file}]\n",
			"vendor/evil.tar.gz":           requirementstest.EvilCollectionTarball(t, target),
		})
		mustNotExist(t, target)
		_, _, lines, err := h.ensure(t)
		if err != nil {
			t.Fatalf("ensureRequirements: %v\nlines:\n%s", err, strings.Join(lines, "\n"))
		}
		if _, err := os.Stat(filepath.Join(target, "MANIFEST.json")); err != nil {
			t.Fatalf("the collection did not escape to %s (%v); the fixture does not attack\nlines:\n%s", target, err, strings.Join(lines, "\n"))
		}
		if !strings.Contains(h.log.String(), warning) {
			t.Errorf("worker log lacks %q:\n%s", warning, h.log)
		}
	})

	t.Run("role dependency named with a path", func(t *testing.T) {
		h := newRealRequirementsRun(t, ansible, map[string]string{
			"site.yml":         site,
			"requirements.yml": "- {name: outer, src: ./vendor/outer.tar.gz}\n",
			"vendor/outer.tar.gz": requirementstest.RoleTarball(t, "outer", map[string]string{
				"meta/main.yml":  "galaxy_info: {author: pulsix}\ndependencies:\n  - {src: ./vendor/inner.tar.gz, name: ../../../ESCAPED_DEP}\n",
				"tasks/main.yml": debugTask("outer"),
			}),
			"vendor/inner.tar.gz": requirementstest.RoleTarball(t, "inner", map[string]string{"tasks/main.yml": debugTask("inner")}),
		})
		// Beside the clone: roles/ -> .tmp-<x> -> <repo>.requirements -> <org>.
		escaped := filepath.Join(filepath.Dir(h.clone()), "ESCAPED_DEP")
		mustNotExist(t, escaped)
		_, _, lines, err := h.ensure(t)
		if err != nil {
			t.Fatalf("ensureRequirements: %v\nlines:\n%s", err, strings.Join(lines, "\n"))
		}
		if _, err := os.Stat(filepath.Join(escaped, "meta", "main.yml")); err != nil {
			t.Fatalf("the dependency did not escape to %s (%v); the fixture does not attack\nlines:\n%s", escaped, err, strings.Join(lines, "\n"))
		}
		if !strings.Contains(h.log.String(), warning) {
			t.Errorf("worker log lacks %q:\n%s", warning, h.log)
		}
	})
}

func TestRequirementsRefuseLocalGitInRealAnsible(t *testing.T) {
	ansible := requireAnsibleGalaxy(t)
	role := gitFixtureFiles(t, map[string]string{
		"meta/main.yml":  "galaxy_info: {author: pulsix}\ndependencies: []\n",
		"tasks/main.yml": debugTask("from-git-role"),
	})
	h := newRealRequirementsRun(t, ansible, map[string]string{
		"site.yml":         "- hosts: all\n  gather_facts: false\n  roles:\n    - gitrole\n",
		"requirements.yml": `- {name: gitrole, src: "git+file://` + role + `"}` + "\n",
	})
	_, entry, lines, err := h.ensure(t)
	if err == nil || !strings.Contains(err.Error(), "transport 'file' not allowed") {
		t.Fatalf("ensureRequirements = %v, want an error containing \"transport 'file' not allowed\"\nlines:\n%s", err, strings.Join(lines, "\n"))
	}
	if !entry.IsZero() {
		t.Errorf("entry = %+v, want none", entry)
	}
	if names := entryNames(t, h.clone()); len(names) != 0 {
		t.Errorf("entries = %q, want none", names)
	}
	des, err := os.ReadDir(requirements.CacheDir(h.clone()))
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range des {
		if strings.HasPrefix(de.Name(), ".tmp-") {
			t.Errorf("leftover temp entry %s", de.Name())
		}
	}
}
