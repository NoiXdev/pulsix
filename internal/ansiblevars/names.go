package ansiblevars

import (
	"errors"
	"strings"
)

// ErrReservedName refuses an untrusted variable name Ansible or Pulsix acts
// on (CheckUntrustedName), or a playbook keyword a run request may not use
// (CheckRequestedName).
var ErrReservedName = errors.New("the name is reserved: Ansible or Pulsix acts on it (ansible_…, pulsix_…, a magic variable, a template global or a playbook keyword)")

// The names are taken from ansible-core 2.21.5 (the worker pins 2.21.4;
// neither list changed between the two). Regenerate both levels from the
// worker's pin when ansible-core is bumped.

// executionPrefixes are the name families Ansible or Pulsix act on whatever
// the value: every ansible_ variable (connection, interpreter, become, the
// magic ansible_* ones: Ansible executes or obeys them even when the value
// is marked unsafe), the pulsix_ namespace Pulsix sets itself, and
// Ansible's JSON markers.
var executionPrefixes = []string{"ansible_", "pulsix_", "__ansible"}

// executionNames are the names outside those prefixes that change what a
// run does: the non-ansible_ entries of ansible.constants
// .INTERNAL_STATIC_VARS, the magic variables, and the template globals of
// ansible.vars.reserved.get_reserved_names() that a variable of the same
// name would shadow in every template, plus vars, the template-visible
// dictionary of all variables.
var executionNames = map[string]bool{
	// Magic variables.
	"group_names": true, "groups": true, "hostvars": true, "inventory_dir": true, "inventory_file": true,
	"inventory_hostname": true, "inventory_hostname_short": true, "play_hosts": true, "playbook_dir": true,
	"role_name": true, "role_names": true, "role_path": true, "role_uuid": true,
	// Template globals, and vars.
	"cycler": true, "dict": true, "joiner": true, "lipsum": true, "lookup": true, "namespace": true,
	"now": true, "omit": true, "q": true, "query": true, "range": true, "undef": true, "vars": true,
}

// keywordNames are the rest of get_reserved_names(): the play, role, block
// and task keywords. A variable of such a name changes no keyword (Ansible
// only warns), so webhooks keep mapping onto them; a run request, a new
// surface, refuses them too (CheckRequestedName).
var keywordNames = map[string]bool{
	"action": true, "always": true, "any_errors_fatal": true, "args": true, "async": true, "async_val": true,
	"become": true, "become_exe": true, "become_flags": true, "become_method": true, "become_user": true,
	"block": true, "changed_when": true, "check_mode": true, "collections": true, "connection": true,
	"debugger": true, "delay": true, "delegate_facts": true, "delegate_to": true, "diff": true,
	"environment": true, "fact_path": true, "failed_when": true, "force_handlers": true, "gather_facts": true,
	"gather_timeout": true, "handlers": true, "hosts": true, "ignore_errors": true, "ignore_unreachable": true,
	"local_action": true, "loop": true, "loop_control": true, "loop_with": true, "max_fail_percentage": true,
	"module_defaults": true, "name": true, "no_log": true, "notify": true, "order": true, "poll": true,
	"port": true, "post_tasks": true, "pre_tasks": true, "register": true, "remote_user": true, "rescue": true,
	"retries": true, "roles": true, "run_once": true, "serial": true, "strategy": true, "tags": true,
	"tasks": true, "throttle": true, "timeout": true, "until": true, "validate_argspec": true,
	"vars_files": true, "vars_prompt": true, "when": true, "with_": true,
}

// CheckUntrustedName reports ErrReservedName when an untrusted value (a
// webhook payload, a run request's extra vars) may not be set under name
// because Ansible or Pulsix acts on it: executionPrefixes,
// executionNames and everything Forbidden refuses, compared
// case-insensitively. Operators' own template vars are held to Forbidden
// and the schema's field-name rules only.
func CheckUntrustedName(name string) error {
	n := strings.ToLower(name)
	if executionNames[n] || Forbidden(n) {
		return ErrReservedName
	}
	for _, p := range executionPrefixes {
		if strings.HasPrefix(n, p) {
			return ErrReservedName
		}
	}
	return nil
}

// CheckRequestedName is CheckUntrustedName plus the playbook keywords
// (keywordNames): the rule for a run request's extra vars, which no
// existing caller depends on.
func CheckRequestedName(name string) error {
	if keywordNames[strings.ToLower(name)] {
		return ErrReservedName
	}
	return CheckUntrustedName(name)
}
