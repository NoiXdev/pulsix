package runner

import (
	"strings"
	"testing"
)

// TestRoleSearchFromASubdirectoryInRealAnsible pins where a real
// ansible-playbook, run with the run's own environment and ansible.cfg,
// finds the roles of a playbook in a subdirectory: <playbook dir>/roles,
// then roles_path (the requirements entry), then the playbook's own
// directory — never roles/ at the repository root. This is the order the
// parser in internal/varsource mirrors (backlog 7.90). The subtests for slash
// names, a role beside the playbook and a file at a candidate pin that the
// same search order applies to a slash name, that an earlier candidate wins,
// and that a regular file at a candidate is a role without content; the
// parser mirrors this too (backlog 7.91).
func TestRoleSearchFromASubdirectoryInRealAnsible(t *testing.T) {
	ansible := requireAnsiblePlaybook(t)
	debug := func(msg string) string { return "- ansible.builtin.debug:\n    msg: " + msg + "\n" }
	play := func(roles ...string) string {
		return "- hosts: all\n  gather_facts: false\n  roles:\n    - " + strings.Join(roles, "\n    - ") + "\n"
	}

	t.Run("order", func(t *testing.T) {
		out, err := runRepoPlaybook(t, ansible, map[string]string{
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
		out, err := runRepoPlaybook(t, ansible, map[string]string{
			"playbooks/site.yml":            play("rootonly"),
			"roles/rootonly/tasks/main.yml": debug("from-root"),
		}, nil)
		if err == nil || !strings.Contains(out, "rootonly") || !strings.Contains(out, "was not found") {
			t.Fatalf("roles/ at the repository root must not be searched for playbooks/site.yml (err %v):\n%s", err, out)
		}
	})

	// ran checks err == nil and that exactly the wanted msg lines are printed.
	ran := func(t *testing.T, out string, err error, want, notWant []string) {
		t.Helper()
		if err != nil {
			t.Fatalf("ansible-playbook: %v\n%s", err, out)
		}
		for _, msg := range want {
			if !strings.Contains(out, `"msg": "`+msg+`"`) {
				t.Errorf("%s did not run:\n%s", msg, out)
			}
		}
		for _, msg := range notWant {
			if strings.Contains(out, `"msg": "`+msg+`"`) {
				t.Errorf("%s must not run:\n%s", msg, out)
			}
		}
	}

	t.Run("beside only", func(t *testing.T) {
		out, err := runRepoPlaybook(t, ansible, map[string]string{
			"playbooks/site.yml":                  play("besideonly"),
			"playbooks/besideonly/tasks/main.yml": debug("from-beside-only"),
		}, nil)
		ran(t, out, err, []string{"from-beside-only"}, nil)
	})

	t.Run("slash names", func(t *testing.T) {
		out, err := runRepoPlaybook(t, ansible, map[string]string{
			"playbooks/site.yml":                       play("a/inroles", "b/installed", "c/beside"),
			"playbooks/roles/a/inroles/tasks/main.yml": debug("from-a-inroles"),
			"playbooks/a/inroles/tasks/main.yml":       debug("from-a-beside"),
			"playbooks/c/beside/tasks/main.yml":        debug("from-c-beside"),
		}, map[string]string{
			"b/installed/tasks/main.yml": debug("from-b-installed"),
		})
		ran(t, out, err, []string{"from-a-inroles", "from-b-installed", "from-c-beside"}, []string{"from-a-beside"})
	})

	t.Run("a file at a candidate", func(t *testing.T) {
		out, err := runRepoPlaybook(t, ansible, map[string]string{
			"playbooks/site.yml":                   play("fileonly", "p/q", "after"),
			"playbooks/roles/fileonly":             "not a role\n",
			"playbooks/roles/p":                    "not a directory\n",
			"playbooks/p/q/tasks/main.yml":         debug("from-p-q"),
			"playbooks/roles/after/tasks/main.yml": debug("from-after"),
		}, map[string]string{
			"fileonly/tasks/main.yml": debug("from-installed-fileonly"),
		})
		ran(t, out, err, []string{"from-p-q", "from-after"}, []string{"from-installed-fileonly"})
	})
}
