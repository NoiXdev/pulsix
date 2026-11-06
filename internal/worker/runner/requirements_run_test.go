// A repo run installs the requirements of its commit before the play
// (spec 2026-11-06-requirements-cache, goals 2, 5 and 6): the step's lines
// stream as stdout events before run_start, the generated ansible.cfg
// names the entry, and a step that fails or is refused ends the run before
// any run file is written.
package runner

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
	"github.com/noixdev/pulsix/internal/worker/requirements"
	"github.com/noixdev/pulsix/internal/worker/requirements/requirementstest"
)

// repoRequirements is the fixture's requirements.yml: one role, one
// collection.
const repoRequirements = "roles:\n  - name: acme.web\ncollections:\n  - name: acme.tools\n"

// sitePlaybook is the fixture's playbooks/site.yml.
const sitePlaybook = "- hosts: all\n  tasks:\n    - name: ping\n      ansible.builtin.ping:\n"

// installedLine is the step's last line after an install.
var installedLine = regexp.MustCompile(`^Requirements: installed in \d+\.\d s$`)

// requirementsRunner is a Runner over the fake ansible-runner whose
// requirements install through galaxy (nil: no installer wired). The fake
// runner's argv goes to the returned file.
func requirementsRunner(t *testing.T, galaxy *requirementstest.Galaxy) (*Runner, string) {
	t.Helper()
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvArgs, args)
	t.Setenv(fakeEnvEvents, cleanFixtureAbs(t))
	t.Setenv(fakeEnvExit, "0")
	r := &Runner{RunnerBin: writeFakeRunner(t), GitBin: gitOnPATH(t)}
	if galaxy != nil {
		r.Requirements = requirements.NewService(galaxy, &requirementstest.Clock{Step: time.Second}, 0)
	}
	return r, args
}

// headOf is the commit the branch of the git repository at dir is at.
func headOf(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command(gitOnPATH(t), "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// eventText is a stdout event's {"text": …}.
func eventText(t *testing.T, ev *workerpb.RunEvent) string {
	t.Helper()
	var p struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(ev.GetPayload()), &p); err != nil {
		t.Fatalf("payload %q: %v", ev.GetPayload(), err)
	}
	return p.Text
}

// textsBeforeRunStart are the texts of the events before the first
// run_start (all of them without one); each must be a stdout event.
func textsBeforeRunStart(t *testing.T, c *collector) []string {
	t.Helper()
	var texts []string
	for _, ev := range c.events {
		if ev.GetState() == evStateRunStart {
			break
		}
		if ev.GetState() != evStateStdout {
			t.Fatalf("event %q before run_start, want stdout only", ev.GetState())
		}
		texts = append(texts, eventText(t, ev))
	}
	return texts
}

// cloneOf is the clone of repo in env's repo cache.
func cloneOf(env worker.RunEnv, repo uuid.UUID) string {
	return filepath.Join(env.RepoCacheDir, orgID.String(), repo.String())
}

// entryNames are the published entries in the requirements cache of clone.
func entryNames(t *testing.T, clone string) []string {
	t.Helper()
	des, err := os.ReadDir(requirements.CacheDir(clone))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, de := range des {
		if de.IsDir() && !strings.HasPrefix(de.Name(), ".") {
			names = append(names, de.Name())
		}
	}
	return names
}

func TestRepoRunInstallsItsRequirementsBeforeTheRunStarts(t *testing.T) {
	source := gitFixtureFiles(t, map[string]string{"playbooks/site.yml": sitePlaybook, "requirements.yml": repoRequirements})
	head := headOf(t, source)
	// A line of galaxy's goes through the run's redactor.
	galaxy := &requirementstest.Galaxy{Lines: []string{"galaxy saw host-key-material"}}
	r, _ := requirementsRunner(t, galaxy)
	env := runEnv(t)
	job, repo := repoRunJob(t, source)
	c := &collector{}

	if _, _, err := r.Run(context.Background(), job, env, c.sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	texts := textsBeforeRunStart(t, c)
	if len(texts) < 2 || texts[0] != "Requirements: installing for commit "+head[:7]+" from requirements.yml" ||
		!installedLine.MatchString(texts[len(texts)-1]) {
		t.Fatalf("texts before run_start = %q", texts)
	}
	if !slices.Contains(c.states(), evStateRunStart) {
		t.Fatal("the run never started")
	}
	for _, text := range texts {
		if strings.Contains(text, "host-key-material") {
			t.Errorf("a secret reached the run log: %q", text)
		}
	}
	if !slices.Contains(texts, "galaxy saw [redacted]") {
		t.Errorf("galaxy's line is missing: %q", texts)
	}
	if n := len(galaxy.Calls()); n != 2 {
		t.Errorf("galaxy calls = %d, want a role and a collection install", n)
	}

	names := entryNames(t, cloneOf(env, repo))
	if len(names) != 1 || !regexp.MustCompile(`^`+head+`-[0-9a-f]{16}$`).MatchString(names[0]) {
		t.Fatalf("entries = %q, want one named %s-<16 hex>", names, head)
	}
	entry := filepath.Join(requirements.CacheDir(cloneOf(env, repo)), names[0])
	cfg := string(mustRead(t, filepath.Join(env.WorkDir, "ansible.cfg")))
	for _, want := range []string{
		"roles_path = " + entry + "/roles:/usr/share/ansible/roles:/etc/ansible/roles\n",
		"collections_path = " + entry + "/collections:/usr/share/ansible/collections\n",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("ansible.cfg lacks %q:\n%s", want, cfg)
		}
	}
}

func TestRepoRunReusesTheInstallOfItsCommit(t *testing.T) {
	source := gitFixtureFiles(t, map[string]string{"playbooks/site.yml": sitePlaybook, "requirements.yml": repoRequirements})
	head := headOf(t, source)
	galaxy := &requirementstest.Galaxy{}
	r, _ := requirementsRunner(t, galaxy)
	env := runEnv(t)
	job, _ := repoRunJob(t, source)
	if _, _, err := r.Run(context.Background(), job, env, (&collector{}).sink); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	env.WorkDir = t.TempDir()
	c := &collector{}
	if _, _, err := r.Run(context.Background(), job, env, c.sink); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	want := []string{"Requirements: using the install for commit " + head[:7] + " on this worker"}
	if got := textsBeforeRunStart(t, c); !slices.Equal(got, want) {
		t.Errorf("texts before run_start = %q, want %q", got, want)
	}
	if n := len(galaxy.Calls()); n != 2 {
		t.Errorf("galaxy calls = %d, want the first run's 2 only", n)
	}
}

func TestRepoRunInstallsAgainForANewCommit(t *testing.T) {
	source := gitFixtureFiles(t, map[string]string{"playbooks/site.yml": sitePlaybook, "requirements.yml": repoRequirements})
	first := headOf(t, source)
	galaxy := &requirementstest.Galaxy{}
	r, _ := requirementsRunner(t, galaxy)
	env := runEnv(t)
	job, repo := repoRunJob(t, source)
	if _, _, err := r.Run(context.Background(), job, env, (&collector{}).sink); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	gitCommit(t, source, sitePlaybook+"    - name: again\n      ansible.builtin.ping:\n")
	second := headOf(t, source)
	env.WorkDir = t.TempDir()
	c := &collector{}
	if _, _, err := r.Run(context.Background(), job, env, c.sink); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if texts := textsBeforeRunStart(t, c); len(texts) < 2 || texts[0] != "Requirements: installing for commit "+second[:7]+" from requirements.yml" {
		t.Errorf("texts before run_start = %q, want a new install", texts)
	}
	if n := len(galaxy.Calls()); n != 4 {
		t.Errorf("galaxy calls = %d, want 4", n)
	}
	names := entryNames(t, cloneOf(env, repo))
	if len(names) != 2 {
		t.Fatalf("entries = %q, want one per commit", names)
	}
	for _, commit := range []string{first, second} {
		if !slices.ContainsFunc(names, func(n string) bool { return strings.HasPrefix(n, commit+"-") }) {
			t.Errorf("entries = %q, none for commit %s", names, commit)
		}
	}
}

func TestRepoRunWithoutRequirementsHasNoRequirementsStep(t *testing.T) {
	r, _ := requirementsRunner(t, nil)
	env := runEnv(t)
	job, _ := repoRunJob(t, gitFixture(t))
	c := &collector{}
	if _, _, err := r.Run(context.Background(), job, env, c.sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if texts := textsBeforeRunStart(t, c); len(texts) != 0 {
		t.Errorf("texts before run_start = %q, want none", texts)
	}
	cfg := string(mustRead(t, filepath.Join(env.WorkDir, "ansible.cfg")))
	if strings.Contains(cfg, "roles_path") || strings.Contains(cfg, "collections_path") {
		t.Errorf("ansible.cfg names an install:\n%s", cfg)
	}
}

func TestRepoRunUsesTheClaimsRepoSubpath(t *testing.T) {
	sub := "roles:\n  - name: acme.sub\n"
	source := gitFixtureFiles(t, map[string]string{
		"ansible/playbooks/site.yml": sitePlaybook,
		"ansible/requirements.yml":   sub,
		"requirements.yml":           repoRequirements,
	})
	var copies []string
	galaxy := &requirementstest.Galaxy{OnInstall: func(in requirements.Install) error {
		b, err := os.ReadFile(in.File)
		copies = append(copies, string(b))
		return err
	}}
	r, _ := requirementsRunner(t, galaxy)
	env := runEnv(t)
	job, repo := repoRunJob(t, source)
	job.KindPayload = `{"repo":{"id":"` + repo.String() + `","url":"` + source +
		`","branch":"main","subpath":"ansible/playbooks/site.yml","repo_subpath":"ansible"}}`

	if _, _, err := r.Run(context.Background(), job, env, (&collector{}).sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	calls := galaxy.Calls()
	if len(calls) != 1 || calls[0].Kind != requirements.KindRole {
		t.Fatalf("galaxy calls = %+v, want one role install", calls)
	}
	if copies[0] != sub {
		t.Errorf("installed file = %q, want ansible/requirements.yml %q", copies[0], sub)
	}
	if want := filepath.Join(cloneOf(env, repo), "ansible"); calls[0].Dir != want {
		t.Errorf("galaxy dir = %q, want %q", calls[0].Dir, want)
	}
}
