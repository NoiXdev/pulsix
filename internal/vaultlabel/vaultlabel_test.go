package vaultlabel_test

import (
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/vaultlabel"
)

// TestInvalid is the one place the shared rule's whole character table is
// exercised directly, rather than through internal/repos's or
// internal/worker/runner's own wrapper. The BOM and bidirectional-override
// cases are the whole-branch review's finding: a reviewer verified with Go
// and Node that a label beginning with U+FEFF was refused by the client
// (JavaScript's trim() strips it) and accepted by the server (Go's
// strings.TrimSpace does not treat it as whitespace) — a three-way
// disagreement the two copies were never supposed to be able to have. The
// fix widens the rule itself (category Cc or Cf, not only "\n\r,@"), so
// this file is the reference the client's own regex is checked against.
func TestInvalid(t *testing.T) {
	cases := []struct {
		name    string
		label   string
		wantErr string // substring of the reason, or "" for valid
	}{
		{"empty", "", "label is empty"},
		{"leading whitespace", " prod", "leading or trailing whitespace"},
		{"trailing whitespace", "prod ", "leading or trailing whitespace"},
		{"non-breaking space", "prod ", "leading or trailing whitespace"},
		{"comma", "prod,staging", "comma"},
		{"at sign", "prod@file", "'@'"},
		{"newline mid-label", "prod\nhost_key_checking = False", "control or format"},
		{"carriage return mid-label", "prod\rx", "control or format"},
		// U+FEFF (BYTE ORDER MARK) is category Cf. JavaScript's String.trim()
		// strips it as whitespace (the client used to reject this on that
		// accident alone); Go's strings.TrimSpace does not, so before this
		// fix the server accepted it outright. It must be rejected on its
		// own rule now, not as a side effect of either trim implementation.
		{"leading BOM", "\ufeffprod", "control or format"},
		{"BOM mid-label", "pro\ufeffd", "control or format"},
		// U+202E (RIGHT-TO-LEFT OVERRIDE) is category Cf and renders
		// everything after it right-to-left until a pop/reset — a label
		// carrying one can display on an operator's screen as something
		// other than the bytes it actually is, which matters here more than
		// anywhere else in this codebase: this form's whole job is telling
		// an operator which password opens which file.
		{"bidi override mid-label", "pro\u202ed", "control or format"},
		{"bidi override leading", "\u202eprod", "control or format"},
		{"overlong is not this package's concern", strings.Repeat("a", 1000), ""},
		{"umlauts are fine", "prod-ä", ""},
		{"valid", "prod", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := vaultlabel.Invalid(tc.label)
			if tc.wantErr == "" {
				if got != "" {
					t.Fatalf("Invalid(%q) = %q, want valid", tc.label, got)
				}
				return
			}
			if !strings.Contains(got, tc.wantErr) {
				t.Fatalf("Invalid(%q) = %q, want a reason containing %q", tc.label, got, tc.wantErr)
			}
		})
	}
}
