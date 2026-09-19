package ansiblevars

import (
	"errors"
	"regexp"
)

// unsafeKey is the JSON form of Ansible's !unsafe tag: ansible-core's JSON
// loader turns {"__ansible_unsafe": "<s>"} into a string it never renders
// as a template, so "{{ … }}", "{% … %}" and lookups inside it stay
// literal text (verified with ansible-core 2.21.4, the worker image's
// pin). Dictionary keys are never rendered, so only values need it.
const unsafeKey = "__ansible_unsafe"

// Inert returns v with every string inside it — in every list and map it
// holds — marked unsafe for Ansible, so a value that came from an
// unauthenticated sender (a webhook payload) cannot run a Jinja expression
// or a lookup on the worker. Numbers, booleans and null are inert already
// and pass through unchanged. v itself is never modified.
//
// The markers only hold while Ansible loads the whole document as JSON;
// when that load fails, ansible-core silently re-reads the file as YAML
// and every marker becomes a plain dict. Callers must first refuse
// untrusted values CheckUntrusted rejects (reserved keys, nesting past
// MaxUntrustedDepth) and documents CheckMarkers rejects; the worker's file
// also carries a YAML guard so a failed JSON load fails the run.
func Inert(v any) any {
	switch x := v.(type) {
	case string:
		return map[string]any{unsafeKey: x}
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = Inert(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			out[k] = Inert(item)
		}
		return out
	}
	return v
}

// untrustedLimitPattern is the host pattern an unauthenticated sender may
// supply as a run's limit: host and group names (letters, digits, ".",
// "_", "-") joined by Ansible's pattern operators "," and ":" (union), "&"
// (intersection) and "!" (exclusion), with "*" as a wildcard. Everything
// else Ansible gives a meaning to is refused: "@" (read the pattern list
// from a file on the worker), "~" (regular expression), "[…]" (subscripts)
// as well as whitespace, quotes and template markers.
var untrustedLimitPattern = regexp.MustCompile(`^[A-Za-z0-9._\-,:!&*]*$`)

// ErrUntrustedLimit refuses a limit CheckUntrustedLimit does not accept.
var ErrUntrustedLimit = errors.New("the limit may only contain host and group names and the operators , : ! & *")

// CheckUntrustedLimit reports whether limit is a host pattern an
// unauthenticated sender may set (see untrustedLimitPattern).
func CheckUntrustedLimit(limit string) error {
	if !untrustedLimitPattern.MatchString(limit) {
		return ErrUntrustedLimit
	}
	return nil
}
