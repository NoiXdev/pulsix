// Package vaultlabel is the one rule for what text may serve as a vault
// identity label (spec decision 4, amended): a label is rendered verbatim
// into a generated ansible.cfg as "label@path", entries joined with ", ".
// A comma or '@' forges an extra entry; any Unicode control character
// (category Cc — this is where the newline and carriage return cases now
// live) can forge an entire configuration key — a label ending in a newline
// followed by "host_key_checking = False" would undo the host-key pinning
// that same generated file enforces. Any Unicode format character (category
// Cf — invisible by design: the byte-order mark U+FEFF and the
// bidirectional-override characters around U+202E are both in it) can make
// the label display on an operator's screen as something other than the
// bytes it actually is, which is a spoofing vector on a form whose whole
// job is telling that operator which password opens which file.
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
// promised would agree. web/src/lib/vaultIdentity.ts is a third copy this
// package's own type system can't hold to the same guarantee — it mirrors
// the rule below by hand, with this file as the reference it is checked
// against (see that file's own tests for the BOM and bidirectional-override
// cases that motivated widening the rule to a Unicode category rather than
// a fixed character list).
package vaultlabel

import (
	"strings"
	"unicode"
)

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
	case strings.ContainsAny(label, ",@"):
		return "label contains a comma or '@', either of which ansible.cfg would read as more than one label"
	case strings.ContainsFunc(label, isControlOrFormat):
		return "label contains a Unicode control or format character (for example a newline, carriage return, byte-order mark, or bidirectional-override character), which ansible.cfg could read as more than one label or which could make the label display as something other than what it is"
	default:
		return ""
	}
}

// isControlOrFormat reports whether r is a Unicode control character
// (category Cc — newlines and carriage returns live here, among others) or
// a format character (category Cf — invisible by design, which includes
// the byte-order mark U+FEFF and the bidirectional-override characters
// around U+202E). See the package doc comment for why both are unsafe here.
func isControlOrFormat(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Cf)
}
