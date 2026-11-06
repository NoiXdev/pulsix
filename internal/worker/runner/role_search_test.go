package runner

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/worker/requirements"
)

// TestRoleSearchFromASubdirectoryInRealAnsible pins where a real
// ansible-playbook, run with the run's own environment and ansible.cfg,
// finds the roles of a playbook in a subdirectory: <playbook dir>/roles,
// then roles_path (the galaxy dir), then the playbook's own directory —
// never roles/ at the repository root. This is the order the parser in
// internal/varsource mirrors (backlog 7.90).
func TestRoleSearchFromASubdirectoryInRealAnsible(t *testing.T) {
	ansible := requireAnsiblePlaybook(t)
	debug := func(msg string) string { return "- ansible.builtin.debug:\n    msg: " + msg + "\n" }
	play := func(roles ...string) string {
		return "- hosts: all\n  gather_facts: false\n  roles:\n    - " + strings.Join(roles, "\n    - ") + "\n"
	}

	// run materializes the repository files (with galaxy roles installed
	// next to the clone) and runs playbooks/site.yml.
	run := func(t *testing.T, files, galaxyRoles map[string]string) (string, error) {
		t.Helper()
		source := gitFixtureFiles(t, files)
		r := &Runner{GitBin: gitOnPATH(t)}
		env := runEnv(t)
		job, repo := repoRunJob(t, source)
		job.ExtraVars = nil
		galaxy := galaxyPath(env.RepoCacheDir, repo)
		for rel, content := range galaxyRoles {
			writeRoleFile(t, galaxy, rel, content)
		}
		playbook, rolesDir, err := r.materializePlaybook(context.Background(), job, env)
		if err != nil {
			t.Fatalf("materializePlaybook: %v", err)
		}
		if err := r.materializeRunnerConfig(job, env, requirements.Entry{RolesDir: rolesDir}); err != nil {
			t.Fatalf("materializeRunnerConfig: %v", err)
		}
		cmd := exec.Command(ansible, "-i", "localhost,", "-c", "local", playbook)
		cmd.Env = append(r.subprocessEnv(env, ansible), "ANSIBLE_NOCOLOR=1", "ANSIBLE_LOCALHOST_WARNING=False")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	t.Run("order", func(t *testing.T) {
		out, err := run(t, map[string]string{
			"playbooks/site.yml":                     play("inroles", "beside", "galaxy_only"),
			"playbooks/roles/inroles/tasks/main.yml": debug("from-inroles"),
			"playbooks/beside/tasks/main.yml":        debug("from-beside"),
		}, map[string]string{
			"galaxy_only/tasks/main.yml": debug("from-galaxy"),
			"beside/tasks/main.yml":      debug("from-galaxy-beside"),
		})
		if err != nil {
			t.Fatalf("ansible-playbook: %v\n%s", err, out)
		}
		for _, msg := range []string{"from-inroles", "from-galaxy", "from-galaxy-beside"} {
			if !strings.Contains(out, `"msg": "`+msg+`"`) {
				t.Errorf("%s did not run:\n%s", msg, out)
			}
		}
		if strings.Contains(out, `"msg": "from-beside"`) {
			t.Errorf("roles_path must come before the playbook's own directory:\n%s", out)
		}
	})

	t.Run("root roles", func(t *testing.T) {
		out, err := run(t, map[string]string{
			"playbooks/site.yml":            play("rootonly"),
			"roles/rootonly/tasks/main.yml": debug("from-root"),
		}, nil)
		if err == nil || !strings.Contains(out, "rootonly") || !strings.Contains(out, "was not found") {
			t.Fatalf("roles/ at the repository root must not be searched for playbooks/site.yml (err %v):\n%s", err, out)
		}
	})
}
