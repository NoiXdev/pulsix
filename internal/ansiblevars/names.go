package ansiblevars

import (
	"errors"
	"strings"
)

// ErrReservedName refuses an untrusted variable name Ansible or Pulsix acts
// on (CheckUntrustedName).
var ErrReservedName = errors.New("the name is reserved: Ansible or Pulsix acts on it (ansible_…, pulsix_…, a magic variable or a playbook keyword)")

// untrustedReservedPrefixes are the name families an untrusted value may
// never be set under: every ansible_ variable (connection, interpreter,
// become, the magic ansible_* ones: Ansible executes or obeys them even
// when their value is marked unsafe), the pulsix_ namespace Pulsix sets
// itself, and Ansible's JSON markers.
var untrustedReservedPrefixes = []string{"ansible_", "pulsix_", "__ansible"}

// untrustedReservedNames are Ansible's own names outside the ansible_
// prefix, as ansible-core 2.21.5 reports them (the worker pins 2.21.4;
// neither list changed between the two): ansible.vars.reserved
// .get_reserved_names() — every play, role, block and task keyword plus
// the template globals (lookup, query, omit, …), which a variable of the
// same name shadows — and the non-ansible_ entries of
// ansible.constants.INTERNAL_STATIC_VARS, the magic variables (hostvars,
// groups, inventory_hostname, playbook_dir, role_path, …). Regenerate
// both from the worker's pin when ansible-core is bumped.
var untrustedReservedNames = map[string]bool{
	"action": true, "always": true, "any_errors_fatal": true, "args": true, "async": true,
	"async_val": true, "become": true, "become_exe": true, "become_flags": true,
	"become_method": true, "become_user": true, "block": true, "changed_when": true,
	"check_mode": true, "collections": true, "connection": true, "cycler": true, "debugger": true,
	"delay": true, "delegate_facts": true, "delegate_to": true, "dict": true, "diff": true,
	"environment": true, "fact_path": true, "failed_when": true, "force_handlers": true,
	"gather_facts": true, "gather_timeout": true, "group_names": true, "groups": true,
	"handlers": true, "hosts": true, "hostvars": true, "ignore_errors": true,
	"ignore_unreachable": true, "inventory_dir": true, "inventory_file": true,
	"inventory_hostname": true, "inventory_hostname_short": true, "joiner": true, "lipsum": true,
	"local_action": true, "lookup": true, "loop": true, "loop_control": true, "loop_with": true,
	"max_fail_percentage": true, "module_defaults": true, "name": true, "namespace": true,
	"no_log": true, "notify": true, "now": true, "omit": true, "order": true, "play_hosts": true,
	"playbook_dir": true, "poll": true, "port": true, "post_tasks": true, "pre_tasks": true,
	"q": true, "query": true, "range": true, "register": true, "remote_user": true, "rescue": true,
	"retries": true, "role_name": true, "role_names": true, "role_path": true, "role_uuid": true,
	"roles": true, "run_once": true, "serial": true, "strategy": true, "tags": true, "tasks": true,
	"throttle": true, "timeout": true, "undef": true, "until": true, "validate_argspec": true,
	"vars": true, "vars_files": true, "vars_prompt": true, "when": true, "with_": true,
}

// CheckUntrustedName reports ErrReservedName when an untrusted value (a
// webhook payload, a run request's extra vars) may not be set under name:
// a name of untrustedReservedPrefixes or untrustedReservedNames, compared
// case-insensitively, plus everything Forbidden refuses. Operators' own
// template vars are held to Forbidden and the schema's field-name rules
// only: they may use a keyword such as "name" or "port" as a variable.
func CheckUntrustedName(name string) error {
	n := strings.ToLower(name)
	if untrustedReservedNames[n] || Forbidden(n) {
		return ErrReservedName
	}
	for _, p := range untrustedReservedPrefixes {
		if strings.HasPrefix(n, p) {
			return ErrReservedName
		}
	}
	return nil
}
