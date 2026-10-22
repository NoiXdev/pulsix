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
//
// Backlog 2.97 closed a second, smaller disagreement the same widening
// left behind: U+0085 (NEL) and U+FEFF are each caught by a different one
// of TrimSpace vs the Cc/Cf check on each side (the two trim()
// implementations disagree about which of the two counts as whitespace),
// so the reported reason used to differ by position and by side even
// though both ends always refused both characters. Invalid now reports
// the same merged message for either check, so "leading or trailing
// whitespace" and "control or format" below are no longer two different
// reasons — they are both substrings of the one message every case in
// this table that reaches either check gets.
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
		// U+0085 (NEL) is category Cc and is Unicode White_Space, so
		// Go's TrimSpace strips it — this lands on the whitespace half of
		// the merged check, not the Cc/Cf half, unlike the BOM below.
		{"leading NEL", "\u0085prod", "leading or trailing whitespace"},
		{"trailing NEL", "prod\u0085", "leading or trailing whitespace"},
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

// TestInvalidReportsTheSameMessageRegardlessOfWhichCheckCaught is backlog
// 2.97 itself: U+0085 (NEL, caught by the merged check's TrimSpace half —
// Unicode's White_Space property includes NEL) and U+FEFF (caught by its
// Cc/Cf half — Go's TrimSpace excludes the BOM, unlike ECMAScript's own
// trim()) used to produce two different reported reasons for what is, from
// an operator's point of view, the same complaint: this label has an
// invisible character in it. Every label below must now report
// byte-for-byte the same message, since the merged check has exactly one
// message regardless of which of its two conditions fired.
func TestInvalidReportsTheSameMessageRegardlessOfWhichCheckCaught(t *testing.T) {
	labels := []string{
		"\u0085prod", // NEL, leading: TrimSpace half of the merged check
		"prod\u0085", // NEL, trailing: same half
		"\ufeffprod", // BOM, leading: Cc/Cf half of the merged check
		"prod\ufeff", // BOM, trailing: same half
		"pro\u0085d", // NEL, mid-label: not whitespace-adjacent, lands on the Cc/Cf half instead
		"pro\ufeffd", // BOM, mid-label: Cc/Cf half, same as the leading/trailing BOM cases
	}
	want := vaultlabel.Invalid(labels[0])
	if want == "" {
		t.Fatalf("Invalid(%q) = %q, want a rejection", labels[0], want)
	}
	for _, label := range labels[1:] {
		if got := vaultlabel.Invalid(label); got != want {
			t.Fatalf("Invalid(%q) = %q, want the same message as Invalid(%q) = %q", label, got, labels[0], want)
		}
	}
}
