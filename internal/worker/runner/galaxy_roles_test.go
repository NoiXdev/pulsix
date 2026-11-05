// Roles that a repo sync installed from requirements.yml
// (`ansible-galaxy install -r requirements.yml -p <clone>.galaxy`) must be
// on the run's roles path (backlog 7.83): the generated ansible.cfg names
// the galaxy directory ahead of Ansible's two system defaults, the play's
// uid can read it, and a real ansible-playbook resolves a role from it
// while the playbook's own roles/ still wins.
package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
)

// rolesPathLine returns the roles_path line of an ansible.cfg text, or "".
func rolesPathLine(t *testing.T, cfg string) string {
	t.Helper()
	for _, line := range strings.Split(cfg, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "roles_path") {
			return line
		}
	}
	return ""
}

func TestAnsibleCfgRolesPath(t *testing.T) {
	cases := []struct {
		name     string
		rolesDir string
		want     string
	}{
		{"galaxy dir", "/c/o/r.galaxy", "roles_path = /c/o/r.galaxy:/usr/share/ansible/roles:/etc/ansible/roles"},
		{"no galaxy dir", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := ansibleCfgText("/work", nil, tc.rolesDir)
			if got := rolesPathLine(t, cfg); got != tc.want {
				t.Fatalf("roles_path line = %q, want %q\n%s", got, tc.want, cfg)
			}
			if tc.want == "" {
				return
			}
			// roles_path is a [defaults] key: it must sit before the
			// [ssh_connection] section starts.
			if strings.Index(cfg, tc.want) > strings.Index(cfg, "[ssh_connection]") {
				t.Fatalf("roles_path is outside [defaults]:\n%s", cfg)
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

// galaxyPath is where the sync installs a repo's requirements:
// <cache>/<org>/<repo>.galaxy, next to the clone.
func galaxyPath(cacheDir string, repo uuid.UUID) string {
	return filepath.Join(cacheDir, orgID.String(), repo.String()+".galaxy")
}

// writeRoleFile writes rel (slash-separated) under dir with owner-only
// modes, the way the worker's own cache tree is created.
func writeRoleFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRepoRunNamesTheGalaxyDirInItsConfig(t *testing.T) {
	source := gitFixture(t)

	// configFor materializes the playbook and the runner config and
	// returns the returned roles dir and the written ansible.cfg.
	configFor := func(t *testing.T, job *workerpb.Job, env worker.RunEnv) (string, string) {
		t.Helper()
		r := &Runner{GitBin: gitOnPATH(t)}
		_, rolesDir, err := r.materializePlaybook(context.Background(), job, env)
		if err != nil {
			t.Fatalf("materializePlaybook: %v", err)
		}
		if err := r.materializeRunnerConfig(job, env, rolesDir); err != nil {
			t.Fatalf("materializeRunnerConfig: %v", err)
		}
		return rolesDir, string(mustRead(t, filepath.Join(env.WorkDir, "ansible.cfg")))
	}

	t.Run("galaxy dir present", func(t *testing.T) {
		env := runEnv(t)
		job, repo := repoRunJob(t, source)
		galaxy := galaxyPath(env.RepoCacheDir, repo)
		writeRoleFile(t, galaxy, "galaxy_only/tasks/main.yml", "- ansible.builtin.debug:\n    msg: hi\n")
		rolesDir, cfg := configFor(t, job, env)
		if rolesDir != galaxy {
			t.Errorf("rolesDir = %q, want %q", rolesDir, galaxy)
		}
		want := "roles_path = " + galaxy + ":/usr/share/ansible/roles:/etc/ansible/roles"
		if got := rolesPathLine(t, cfg); got != want {
			t.Errorf("roles_path line = %q, want %q", got, want)
		}
	})

	t.Run("no galaxy dir", func(t *testing.T) {
		env := runEnv(t)
		job, _ := repoRunJob(t, source)
		rolesDir, cfg := configFor(t, job, env)
		if rolesDir != "" || rolesPathLine(t, cfg) != "" {
			t.Errorf("rolesDir = %q, cfg:\n%s\nwant no roles_path without a galaxy dir", rolesDir, cfg)
		}
	})

	t.Run("galaxy path is a symlink", func(t *testing.T) {
		env := runEnv(t)
		job, repo := repoRunJob(t, source)
		galaxy := galaxyPath(env.RepoCacheDir, repo)
		if err := os.MkdirAll(filepath.Dir(galaxy), 0o700); err != nil {
			t.Fatal(err)
		}
		target := t.TempDir()
		writeRoleFile(t, target, "galaxy_only/tasks/main.yml", "- ansible.builtin.debug:\n    msg: hi\n")
		if err := os.Symlink(target, galaxy); err != nil {
			t.Fatal(err)
		}
		rolesDir, cfg := configFor(t, job, env)
		if rolesDir != "" || rolesPathLine(t, cfg) != "" {
			t.Errorf("rolesDir = %q, cfg:\n%s\nwant no roles_path through a symlink", rolesDir, cfg)
		}
	})

	t.Run("inline playbook", func(t *testing.T) {
		env := runEnv(t)
		job := runJob(t)
		rolesDir, cfg := configFor(t, job, env)
		if rolesDir != "" || rolesPathLine(t, cfg) != "" {
			t.Errorf("rolesDir = %q, cfg:\n%s\nwant no roles_path for an inline playbook", rolesDir, cfg)
		}
	})
}

// TestGalaxyRolesAreReadableByThePlayUID mirrors
// TestRepoPlaybookIsReadableByThePlayUID for the galaxy directory: it sits
// next to the clone in the worker's 0700 repo cache, so with a play uid
// every directory from an installed role file up to the cache root has to
// be group-readable and -traversable, and none group-writable.
func TestGalaxyRolesAreReadableByThePlayUID(t *testing.T) {
	source := gitFixture(t)
	r := &Runner{GitBin: gitOnPATH(t)}
	env := runEnv(t)
	env.Config.PlayUID = 12345
	job, repo := repoRunJob(t, source)
	roleFile := writeRoleFile(t, galaxyPath(env.RepoCacheDir, repo),
		"galaxy_only/tasks/main.yml", "- ansible.builtin.debug:\n    msg: hi\n")

	if _, _, err := r.materializePlaybook(context.Background(), job, env); err != nil {
		t.Fatalf("materializePlaybook: %v", err)
	}

	for p := roleFile; ; p = filepath.Dir(p) {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		perm := info.Mode().Perm()
		want := os.FileMode(0o040) // group read
		if info.IsDir() {
			want |= 0o010 // and traverse
		}
		if perm&want != want {
			t.Errorf("%s: mode = %v, the play uid cannot reach the galaxy role through it", p, perm)
		}
		if perm&0o020 != 0 {
			t.Errorf("%s: mode = %v is group-writable; the galaxy dir is read-only to the play", p, perm)
		}
		if p == env.RepoCacheDir {
			break
		}
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

// TestGalaxyRoleResolvesInRealAnsible runs the materialized repo playbook
// through a real ansible-playbook with the run's own environment (and so
// its generated ansible.cfg): a role only the galaxy dir holds resolves,
// and a role both hold comes from the playbook's own roles/ — Ansible
// searches that before roles_path. The control removes the galaxy dir and
// the run fails naming the missing role.
func TestGalaxyRoleResolvesInRealAnsible(t *testing.T) {
	ansible := requireAnsiblePlaybook(t)
	debug := func(msg string) string { return "- ansible.builtin.debug:\n    msg: " + msg + "\n" }
	source := gitFixtureFiles(t, map[string]string{
		"playbooks/site.yml":                      "- hosts: all\n  gather_facts: false\n  roles:\n    - galaxy_only\n    - shadowed\n",
		"playbooks/roles/shadowed/tasks/main.yml": debug("from-repo"),
	})
	r := &Runner{GitBin: gitOnPATH(t)}
	env := runEnv(t)
	job, repo := repoRunJob(t, source)
	job.ExtraVars = nil
	galaxy := galaxyPath(env.RepoCacheDir, repo)
	writeRoleFile(t, galaxy, "galaxy_only/tasks/main.yml", debug("from-galaxy"))
	writeRoleFile(t, galaxy, "shadowed/tasks/main.yml", debug("from-galaxy-shadow"))

	playbook, rolesDir, err := r.materializePlaybook(context.Background(), job, env)
	if err != nil {
		t.Fatalf("materializePlaybook: %v", err)
	}
	run := func() (string, error) {
		t.Helper()
		if err := r.materializeRunnerConfig(job, env, rolesDir); err != nil {
			t.Fatalf("materializeRunnerConfig: %v", err)
		}
		cmd := exec.Command(ansible, "-i", "localhost,", "-c", "local", playbook)
		cmd.Env = append(r.subprocessEnv(env, ansible), "ANSIBLE_NOCOLOR=1", "ANSIBLE_LOCALHOST_WARNING=False")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := run()
	if err != nil {
		t.Fatalf("ansible-playbook: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"msg": "from-galaxy"`) {
		t.Errorf("the galaxy-only role did not run:\n%s", out)
	}
	if !strings.Contains(out, `"msg": "from-repo"`) || strings.Contains(out, "from-galaxy-shadow") {
		t.Errorf("the playbook's own roles/ must win over the galaxy dir:\n%s", out)
	}

	// Control: without the galaxy dir the role is found nowhere else.
	if err := os.RemoveAll(galaxy); err != nil {
		t.Fatal(err)
	}
	out, err = run()
	if err == nil || !strings.Contains(out, "galaxy_only") {
		t.Fatalf("without the galaxy dir the run must fail naming galaxy_only (err %v):\n%s", err, out)
	}
}
