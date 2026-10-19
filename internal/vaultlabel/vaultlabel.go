// Package vaultlabel is the one rule for what text may serve as a vault
// identity label (spec decision 4, amended): a label is rendered verbatim
// into a generated ansible.cfg as "label@path", entries joined with ", ".
// A comma or '@' forges an extra entry; a newline forges an entire
// configuration key — a label ending in a newline followed by
// "host_key_checking = False" would undo the host-key pinning that same
// generated file enforces.
//
// The rule is checked at two points in one run's life: internal/repos
// refuses it at write time, and internal/worker/runner refuses it again,
// last, right before rendering the config — the runner must not assume the
// write-time check ran, since nothing stops a future caller of the store
// from skipping the service layer. That is defence in depth through two
// points in the flow, not through two implementations: this package has no
// internal dependencies at all, on purpose, so internal/worker/runner can
// import it without dragging in the manager's service graph, and the two
// gates can never disagree about which characters are unsafe because they
// run the same code rather than two hand-maintained copies a comment
// promised would agree.
package vaultlabel

import "strings"

// Invalid reports why label cannot be written into ansible.cfg's
// vault_identity_list, or "" when it is fine. This package intentionally
// does not bound length: a long label is not unsafe to render, only
// inconvenient, and any length bound belongs to whichever caller needs to
// describe one as part of its own contract (see internal/repos's own
// MaxVaultLabelLength for why that is an API-contract concern, not a
// safety one).
func Invalid(label string) string {
	switch {
	case label == "":
		return "label is empty"
	case strings.TrimSpace(label) != label:
		return "label has leading or trailing whitespace"
	case strings.ContainsAny(label, "\n\r,@"):
		return "label contains a newline, carriage return, comma or '@', any of which ansible.cfg would read as more than one label"
	default:
		return ""
	}
}
