package ansiblevars_test

import (
	"errors"
	"testing"

	"github.com/noixdev/pulsix/internal/ansiblevars"
)

// executionNames are names Ansible or Pulsix act on whatever the value:
// refused for every untrusted value, webhook or run request.
var executionNames = []string{
	// Every ansible_ name: connection, interpreter, become, magic.
	"ansible_python_interpreter", "ANSIBLE_BECOME_EXE", "ansible_shell_executable", "ansible_remote_tmp",
	"ansible_connection", "ansible_check_mode", "ansible_facts",
	// The magic variables (ansible.constants.INTERNAL_STATIC_VARS).
	"hostvars", "groups", "group_names", "Inventory_Hostname", "inventory_hostname_short", "inventory_dir",
	"inventory_file", "playbook_dir", "role_path", "role_name", "role_names", "play_hosts", "role_uuid",
	// Template globals a variable would shadow, and vars.
	"omit", "lookup", "query", "q", "undef", "range", "dict", "now", "lipsum", "cycler", "joiner", "namespace", "vars",
	// Pulsix's own namespace and Ansible's JSON markers.
	"pulsix_agent_enroll_token", "Pulsix_x", "__ansible_unsafe",
}

// keywordNames are playbook keywords: a variable of that name changes no
// keyword, so only a run request (a new surface) refuses them.
var keywordNames = []string{"tags", "name", "port", "timeout", "environment", "hosts", "when", "become_user", "delegate_to", "with_", "Notify"}

func TestCheckUntrustedNameRefusesWhatAnsibleActsOn(t *testing.T) {
	for _, name := range executionNames {
		if err := ansiblevars.CheckUntrustedName(name); !errors.Is(err, ansiblevars.ErrReservedName) {
			t.Errorf("CheckUntrustedName(%q) = %v, want ErrReservedName", name, err)
		}
	}
	// Webhooks keep mapping onto keyword-named variables.
	for _, name := range append([]string{"version", "release", "my_ansible_thing", "hostvars_copy", "_private"}, keywordNames...) {
		if err := ansiblevars.CheckUntrustedName(name); err != nil {
			t.Errorf("CheckUntrustedName(%q) = %v, want nil", name, err)
		}
	}
}

func TestCheckRequestedNameAlsoRefusesKeywords(t *testing.T) {
	for _, name := range append(append([]string{}, executionNames...), keywordNames...) {
		if err := ansiblevars.CheckRequestedName(name); !errors.Is(err, ansiblevars.ErrReservedName) {
			t.Errorf("CheckRequestedName(%q) = %v, want ErrReservedName", name, err)
		}
	}
	for _, name := range []string{"version", "release", "app_port", "my_ansible_thing", "_private"} {
		if err := ansiblevars.CheckRequestedName(name); err != nil {
			t.Errorf("CheckRequestedName(%q) = %v, want nil", name, err)
		}
	}
}
