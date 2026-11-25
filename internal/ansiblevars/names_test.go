package ansiblevars_test

import (
	"errors"
	"testing"

	"github.com/noixdev/pulsix/internal/ansiblevars"
)

func TestCheckUntrustedNameRefusesWhatAnsibleActsOn(t *testing.T) {
	for _, name := range []string{
		// Every ansible_ name: connection, interpreter, become, magic.
		"ansible_python_interpreter", "ANSIBLE_BECOME_EXE", "ansible_shell_executable", "ansible_remote_tmp",
		"ansible_connection", "ansible_check_mode", "ansible_facts",
		// Ansible's magic variables (ansible.constants.INTERNAL_STATIC_VARS).
		"hostvars", "groups", "group_names", "Inventory_Hostname", "inventory_hostname_short", "inventory_dir",
		"inventory_file", "playbook_dir", "role_path", "role_name", "play_hosts", "role_uuid",
		// ansible.vars.reserved.get_reserved_names(): keywords and template globals.
		"omit", "environment", "vars", "lookup", "query", "q", "become_user", "hosts", "tags", "when", "delegate_to",
		"with_", "range", "now", "undef",
		// Pulsix's own namespace and Ansible's JSON markers.
		"pulsix_agent_enroll_token", "Pulsix_x", "__ansible_unsafe",
	} {
		if err := ansiblevars.CheckUntrustedName(name); !errors.Is(err, ansiblevars.ErrReservedName) {
			t.Errorf("CheckUntrustedName(%q) = %v, want ErrReservedName", name, err)
		}
	}
	for _, name := range []string{"version", "release", "app_port", "my_ansible_thing", "hostvars_copy", "deploy_tag", "_private"} {
		if err := ansiblevars.CheckUntrustedName(name); err != nil {
			t.Errorf("CheckUntrustedName(%q) = %v, want nil", name, err)
		}
	}
}
