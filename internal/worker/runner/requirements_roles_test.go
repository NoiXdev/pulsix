// The roles and collections a repo run installed from its requirements
// (spec 2026-11-06-requirements-cache) are on the run's search paths: the
// generated ansible.cfg names the entry ahead of Ansible's system
// defaults, the play's uid can read the entry and not write it, the
// published entry is the worker's own, and a real ansible-playbook
// resolves a role from it while the playbook's own roles/ still wins.
package runner

import (
	"context"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
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

func TestAnsibleCfgNamesTheInstalledRequirements(t *testing.T) {
	// keyLine returns the line of cfg that sets key, or "".
	keyLine := func(cfg, key string) string {
		for _, line := range strings.Split(cfg, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), key+" ") {
				return line
			}
		}
		return ""
	}
	cases := []struct {
		name            string
		installed       requirements.Entry
		wantRoles       string
		wantCollections string
	}{
		{"no install", requirements.Entry{}, "", ""},
		{
			"installed entry",
			requirements.Entry{
				Dir:            "/c/o/r.requirements/k",
				RolesDir:       "/c/o/r.requirements/k/roles",
				CollectionsDir: "/c/o/r.requirements/k/collections",
			},
			"roles_path = /c/o/r.requirements/k/roles:/usr/share/ansible/roles:/etc/ansible/roles",
			"collections_path = /c/o/r.requirements/k/collections:/usr/share/ansible/collections",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := ansibleCfgText("/work", nil, tc.installed)
			if got := keyLine(cfg, "roles_path"); got != tc.wantRoles {
				t.Errorf("roles_path line = %q, want %q\n%s", got, tc.wantRoles, cfg)
			}
			if got := keyLine(cfg, "collections_path"); got != tc.wantCollections {
				t.Errorf("collections_path line = %q, want %q\n%s", got, tc.wantCollections, cfg)
			}
			// Both are [defaults] keys: they must sit before the
			// [ssh_connection] section starts.
			ssh := strings.Index(cfg, "[ssh_connection]")
			for _, want := range []string{tc.wantRoles, tc.wantCollections} {
				if want != "" && strings.Index(cfg, want) > ssh {
					t.Errorf("%q is outside [defaults]:\n%s", want, cfg)
				}
			}
		})
	}
}

// repoRunJob is runJob pointing at the playbook playbooks/site.yml of the
// repository at source, under a fresh repo id.
func repoRunJob(t *testing.T, source string) (*workerpb.Job, uuid.UUID) {
	t.Helper()
	job := runJob(t)
	repo := uuid.Must(uuid.NewV7())
	job.Playbook = "repo:" + repo.String() + ":playbooks/site.yml"
	job.KindPayload = `{"repo":{"id":"` + repo.String() + `","url":"` + source + `","branch":"main","subpath":"playbooks/site.yml"}}`
	return job, repo
}

// entryGalaxy is a fake galaxy that installs one role and one collection
// plugin.
func entryGalaxy() *requirementstest.Galaxy {
	return &requirementstest.Galaxy{
		Roles:       map[string]string{"galaxy_only/tasks/main.yml": "- ansible.builtin.debug:\n    msg: hi\n"},
		Collections: map[string]string{"ansible_collections/acme/tools/plugins/x.py": "# x\n"},
	}
}

// ensurePass materializes job's playbook and ensures its requirements the
// way Run does, and returns the entry.
func ensurePass(t *testing.T, r *Runner, job *workerpb.Job, env worker.RunEnv) requirements.Entry {
	t.Helper()
	_, checkout, err := r.materializePlaybook(context.Background(), job, env)
	if err != nil {
		t.Fatalf("materializePlaybook: %v", err)
	}
	entry, err := r.ensureRequirements(context.Background(), env, checkout, func(string) {})
	if err != nil {
		t.Fatalf("ensureRequirements: %v", err)
	}
	if entry.IsZero() {
		t.Fatal("ensureRequirements returned no entry")
	}
	return entry
}

// TestRequirementsEntryIsReadableByThePlayUID mirrors
// TestRepoPlaybookIsReadableByThePlayUID for the requirements entry: it
// sits next to the clone in the worker's 0700 repo cache, so with a play
// uid every path in it and every directory up to the cache root has to be
// group-readable (directories also traversable), and none group-writable —
// on every run, the run that reuses the entry too.
func TestRequirementsEntryIsReadableByThePlayUID(t *testing.T) {
	source := gitFixtureFiles(t, map[string]string{"playbooks/site.yml": sitePlaybook, "requirements.yml": repoRequirements})
	r := &Runner{GitBin: gitOnPATH(t), Requirements: requirements.NewService(entryGalaxy(), &requirementstest.Clock{Step: time.Second}, 0)}
	env := runEnv(t)
	env.Config.PlayUID = 12345
	job, _ := repoRunJob(t, source)
	entry := ensurePass(t, r, job, env)

	// What the share must clear: group-writable files and directories from
	// each file up to the requirements cache directory.
	chmod := func(p string, mode os.FileMode) {
		t.Helper()
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	cache := filepath.Dir(entry.Dir)
	for _, f := range []string{
		filepath.Join(entry.RolesDir, "galaxy_only", "tasks", "main.yml"),
		filepath.Join(entry.CollectionsDir, "ansible_collections", "acme", "tools", "plugins", "x.py"),
	} {
		chmod(f, 0o660)
		for d := filepath.Dir(f); ; d = filepath.Dir(d) {
			chmod(d, 0o770)
			if d == cache {
				break
			}
		}
	}
	// A link the share must skip: a chmod through it would open its target.
	// The take-over refuses a link out of the entry, so it is planted after
	// publishing.
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(entry.RolesDir, "link")); err != nil {
		t.Fatal(err)
	}

	if again := ensurePass(t, r, job, env); again != entry {
		t.Fatalf("second pass entry = %+v, want the first %+v", again, entry)
	}

	check := func(p string, info fs.FileInfo) {
		t.Helper()
		perm := info.Mode().Perm()
		want := os.FileMode(0o040) // group read
		if info.IsDir() {
			want |= 0o010 // and traverse
		}
		if perm&want != want {
			t.Errorf("%s: mode = %v, the play uid cannot read the installed requirements through it", p, perm)
		}
		if perm&0o020 != 0 {
			t.Errorf("%s: mode = %v is group-writable; the entry is read-only to the play", p, perm)
		}
	}
	if err := filepath.WalkDir(entry.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink != 0 {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		check(p, info)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for p := filepath.Dir(entry.Dir); ; p = filepath.Dir(p) {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		check(p, info)
		if p == env.RepoCacheDir {
			break
		}
	}
	if info, err := os.Stat(outside); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the link's target: %v, %v; want it untouched at 0600", info, err)
	}
}

// TestPublishedEntryIsTheWorkersOwn: whatever modes galaxy wrote, the
// published entry is owner-only, so no later play can change it.
func TestPublishedEntryIsTheWorkersOwn(t *testing.T) {
	source := gitFixtureFiles(t, map[string]string{"playbooks/site.yml": sitePlaybook, "requirements.yml": repoRequirements})
	galaxy := entryGalaxy()
	galaxy.OnInstall = func(in requirements.Install) error {
		if in.Kind != requirements.KindRole {
			return nil
		}
		dir := filepath.Join(in.Entry, "roles", "wide", "tasks")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		file := filepath.Join(dir, "main.yml")
		if err := os.WriteFile(file, []byte("- ansible.builtin.debug:\n"), 0o600); err != nil {
			return err
		}
		for p, mode := range map[string]os.FileMode{dir: 0o777, filepath.Dir(dir): 0o775, file: 0o666} {
			if err := os.Chmod(p, mode); err != nil {
				return err
			}
		}
		return nil
	}
	r, _ := requirementsRunner(t, galaxy)
	env := runEnv(t)
	job, repo := repoRunJob(t, source)
	if _, _, err := r.Run(context.Background(), job, env, (&collector{}).sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	names := entryNames(t, cloneOf(env, repo))
	if len(names) != 1 {
		t.Fatalf("entries = %q, want one", names)
	}
	entry := filepath.Join(requirements.CacheDir(cloneOf(env, repo)), names[0])
	if _, err := os.Stat(filepath.Join(entry, "roles", "wide", "tasks", "main.yml")); err != nil {
		t.Fatalf("the wide role was not published: %v", err)
	}
	if err := filepath.WalkDir(entry, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		want := os.FileMode(0o600)
		if d.IsDir() {
			want = 0o700
		}
		if got := info.Mode().Perm(); got != want || !d.IsDir() && !info.Mode().IsRegular() {
			t.Errorf("%s: mode = %v, want %v", p, info.Mode(), want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// gitFixtureFiles prepares a local git repository whose branch main holds
// files (slash-separated paths), and returns its path.
func gitFixtureFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	git := gitOnPATH(t)
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=pulsix", "GIT_AUTHOR_EMAIL=pulsix@example.org",
			"GIT_COMMITTER_NAME=pulsix", "GIT_COMMITTER_EMAIL=pulsix@example.org")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	for rel, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-m", "fixture")
	return dir
}

// runRepoPlaybook materializes a repository holding files the way Run does
// and runs its playbooks/site.yml through a real ansible-playbook with the
// run's own environment (and so its generated ansible.cfg). With
// installed roles (slash path to content) the repository also gets a
// requirements.yml naming each, and a fake galaxy installs them.
func runRepoPlaybook(t *testing.T, ansible string, files, installed map[string]string) (string, error) {
	t.Helper()
	files = maps.Clone(files)
	if len(installed) > 0 {
		var names []string
		for rel := range installed {
			name, _, _ := strings.Cut(rel, "/")
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
		slices.Sort(names)
		files["requirements.yml"] = "roles:\n  - name: " + strings.Join(names, "\n  - name: ") + "\n"
	}
	source := gitFixtureFiles(t, files)
	galaxy := &requirementstest.Galaxy{Roles: installed}
	r := &Runner{GitBin: gitOnPATH(t), Requirements: requirements.NewService(galaxy, &requirementstest.Clock{Step: time.Second}, 0)}
	env := runEnv(t)
	job, _ := repoRunJob(t, source)
	job.ExtraVars = nil
	playbook, checkout, err := r.materializePlaybook(context.Background(), job, env)
	if err != nil {
		t.Fatalf("materializePlaybook: %v", err)
	}
	entry, err := r.ensureRequirements(context.Background(), env, checkout, func(string) {})
	if err != nil {
		t.Fatalf("ensureRequirements: %v", err)
	}
	if entry.IsZero() != (len(installed) == 0) {
		t.Fatalf("entry = %+v for %d installed files", entry, len(installed))
	}
	if err := r.materializeRunnerConfig(job, env, entry); err != nil {
		t.Fatalf("materializeRunnerConfig: %v", err)
	}
	cmd := exec.Command(ansible, "-i", "localhost,", "-c", "local", playbook)
	cmd.Env = append(r.subprocessEnv(env, ansible), "ANSIBLE_NOCOLOR=1", "ANSIBLE_LOCALHOST_WARNING=False")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestGalaxyRoleResolvesInRealAnsible: a role only the requirements entry
// holds resolves, and a role both hold comes from the playbook's own
// roles/ — Ansible searches that before roles_path. The control has no
// requirements.yml, and the run fails naming the missing role.
func TestGalaxyRoleResolvesInRealAnsible(t *testing.T) {
	ansible := requireAnsiblePlaybook(t)
	debug := func(msg string) string { return "- ansible.builtin.debug:\n    msg: " + msg + "\n" }
	files := map[string]string{
		"playbooks/site.yml":                      "- hosts: all\n  gather_facts: false\n  roles:\n    - galaxy_only\n    - shadowed\n",
		"playbooks/roles/shadowed/tasks/main.yml": debug("from-repo"),
	}
	installed := map[string]string{
		"galaxy_only/tasks/main.yml": debug("from-galaxy"),
		"shadowed/tasks/main.yml":    debug("from-galaxy-shadow"),
	}

	out, err := runRepoPlaybook(t, ansible, files, installed)
	if err != nil {
		t.Fatalf("ansible-playbook: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"msg": "from-galaxy"`) {
		t.Errorf("the installed role did not run:\n%s", out)
	}
	if !strings.Contains(out, `"msg": "from-repo"`) || strings.Contains(out, "from-galaxy-shadow") {
		t.Errorf("the playbook's own roles/ must win over the installed roles:\n%s", out)
	}

	// Control: without the installed roles the role is found nowhere else.
	out, err = runRepoPlaybook(t, ansible, files, nil)
	if err == nil || !strings.Contains(out, "galaxy_only") {
		t.Fatalf("without the installed roles the run must fail naming galaxy_only (err %v):\n%s", err, out)
	}
}
