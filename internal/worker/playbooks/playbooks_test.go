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
	for _, name := range []string{workerplaybooks.NameBootstrap, workerplaybooks.NameRotateKey} {
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
		"ansible.builtin.authorized_key:", "key: \"{{ pulsix_public_key }}\"", "manage_dir: true",
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
	for _, want := range []string{"hosts: all", "become: true", "gather_facts: false", "ansible.builtin.authorized_key:", "pulsix_public_key"} {
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
