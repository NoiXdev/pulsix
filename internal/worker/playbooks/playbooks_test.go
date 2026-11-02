// The embedded playbooks cannot regress: both files must still parse as
// valid Ansible playbooks (the same validator the playbooks registry uses)
// and keep their pinned task shape — the executor tests assert the exact
// materialized text against these files.
package playbooks_test

import (
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/playbooks"
	workerplaybooks "github.com/noixdev/pulsix/internal/worker/playbooks"
)

func TestEmbeddedPlaybooksAreValidYAML(t *testing.T) {
	for _, name := range []string{workerplaybooks.NameBootstrap, workerplaybooks.NameRotateKey, workerplaybooks.NameRevokeKey} {
		raw, err := workerplaybooks.Get(name)
		if err != nil {
			t.Fatalf("Get(%s): %v", name, err)
		}
		if err := playbooks.ValidateYAML(string(raw)); err != nil {
			t.Errorf("embedded playbook %s does not validate: %v", name, err)
		}
	}
}

func TestBootstrapPlaybookShapeIsPinned(t *testing.T) {
	raw, err := workerplaybooks.Get(workerplaybooks.NameBootstrap)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"hosts: all", "become: true", "gather_facts: false",
		"ansible.builtin.user:", "shell: /bin/bash", "state: present",
		"ansible.builtin.getent:", "ansible.builtin.file:", `mode: "0700"`,
		"ansible.builtin.lineinfile:", `line: "{{ pulsix_public_key | trim }}"`, "create: true", `mode: "0600"`,
		"ansible.builtin.copy:", "NOPASSWD:ALL", "visudo -cf %s", `mode: "0440"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("bootstrap playbook lacks %q:\n%s", want, text)
		}
	}
}

func TestRotatePlaybookHasOnlyTheAuthorizedKeyTask(t *testing.T) {
	raw, err := workerplaybooks.Get(workerplaybooks.NameRotateKey)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"hosts: all", "become: true", "gather_facts: false", "ansible.builtin.lineinfile:", "pulsix_public_key"} {
		if !strings.Contains(text, want) {
			t.Errorf("rotate playbook lacks %q:\n%s", want, text)
		}
	}
	// The rotate playbook installs a key onto an existing account only: no
	// user creation and no sudo rule.
	for _, absent := range []string{"ansible.builtin.user:", "ansible.builtin.copy:", "NOPASSWD"} {
		if strings.Contains(text, absent) {
			t.Errorf("rotate playbook must not carry %q", absent)
		}
	}
}

func TestGetUnknownNameIsAnError(t *testing.T) {
	if _, err := workerplaybooks.Get("pulsix.nope.yml"); err == nil {
		t.Fatal("an unknown playbook name must error")
	}
}

// TestRevokePlaybookRemovesOnlyThePreviousKey (final review I1): the
// revocation's single task removes the PREVIOUS public key from the
// account's authorized_keys — never adds anything, never touches the
// account or sudo.
func TestRevokePlaybookRemovesOnlyThePreviousKey(t *testing.T) {
	raw, err := workerplaybooks.Get(workerplaybooks.NameRevokeKey)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if err := playbooks.ValidateYAML(text); err != nil {
		t.Fatalf("revoke playbook does not validate: %v", err)
	}
	for _, want := range []string{"hosts: all", "gather_facts: false", "ansible.builtin.lineinfile:",
		"pulsix_old_public_key", "pulsix_also_users", "state: absent"} {
		if !strings.Contains(text, want) {
			t.Errorf("revoke playbook lacks %q:\n%s", want, text)
		}
	}
	for _, absent := range []string{"pulsix_public_key", "ansible.builtin.user:", "ansible.builtin.copy:", "NOPASSWD", "state: present", "line:"} {
		if strings.Contains(text, absent) {
			t.Errorf("revoke playbook must not carry %q", absent)
		}
	}
}

// coreModules are the modules the system playbooks may use: ones
// ansible-core itself ships. The worker image carries ansible-core and no
// collection, and a playbook naming anything else is refused at load, so
// it could not run at all — which is what happened with authorized_key, a
// module of the ansible.posix collection (backlog 7.46).
var coreModules = map[string]bool{"user": true, "getent": true, "file": true, "lineinfile": true, "copy": true}

func TestSystemPlaybooksUseOnlyAnsibleCoresOwnModules(t *testing.T) {
	for _, name := range []string{workerplaybooks.NameBootstrap, workerplaybooks.NameRotateKey, workerplaybooks.NameRevokeKey} {
		raw, err := workerplaybooks.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			field := strings.TrimSpace(line)
			if strings.HasPrefix(field, "#") || !strings.HasSuffix(field, ":") || !strings.Contains(field, ".") {
				continue
			}
			module := strings.TrimSuffix(field, ":")
			short, ok := strings.CutPrefix(module, "ansible.builtin.")
			if !ok || !coreModules[short] {
				t.Errorf("%s uses %q, which is not one of ansible-core's own modules this list allows", name, module)
			}
		}
	}
}
