// Package ansiblevars names the Ansible variables that user-provided input
// (template extra vars, and the worker's claim) must never set: the
// connection-security family that decides whether a host's pinned key is
// checked and with which SSH arguments (re-review N1). Extra vars carry
// Ansible's highest precedence, so one such variable in a template would
// switch pin enforcement off for every host of a run. The worker forces
// these variables itself; the manager refuses them at the input.
//
// Inventory group and host vars cannot carry them: schema field names
// reserve the ansible_ and pulsix_ prefixes and values are validated
// strictly against declared fields.
package ansiblevars

import (
	"slices"
	"strings"
)

// forbiddenNames are the exact connection-security variables: host key
// checking toggles, every SSH argument list ansible passes before or after
// the pinned options, the SSH executable, and the connection plugin itself
// (switching to another transport bypasses the SSH pin options).
var forbiddenNames = map[string]bool{
	"ansible_host_key_checking":     true,
	"ansible_ssh_host_key_checking": true,
	"ansible_ssh_args":              true,
	"ansible_ssh_common_args":       true,
	"ansible_ssh_extra_args":        true,
	"ansible_scp_extra_args":        true,
	"ansible_sftp_extra_args":       true,
	"ansible_ssh_executable":        true,
	"ansible_connection":            true,
}

// forbiddenPrefixes cover whole families: the paramiko connection's host
// key options, and the pulsix_ namespace the worker uses for its own
// per-host values (pulsix_ssh_common_args carries the pinned options).
var forbiddenPrefixes = []string{"ansible_paramiko_", "pulsix_"}

// Forbidden reports whether user input may not set the variable name.
// Matching ignores case: a differently cased spelling is refused too.
func Forbidden(name string) bool {
	n := strings.ToLower(name)
	if forbiddenNames[n] {
		return true
	}
	for _, p := range forbiddenPrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// ForbiddenIn returns the forbidden top-level names of vars, sorted.
func ForbiddenIn(vars map[string]any) []string {
	var out []string
	for name := range vars {
		if Forbidden(name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}
