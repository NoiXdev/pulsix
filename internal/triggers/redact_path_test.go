package triggers

import "testing"

// TestFirstSegmentKeepsOnlyTheConfiguredTarget covers the redaction the
// Task 10 review asked for and the suite never exercised (0 % of
// firstSegment was covered before this test).
//
// A schema validation problem's path is built as the validator walks the
// document, so everything past the first segment — nested keys, array
// indices — comes from the structure of the SENDER's payload, not from
// the operator's configuration. The 422 answer and the delivery reason
// echo the path back to the sender, which is harmless, and back into the
// delivery log an operator reads, which is not: that log would otherwise
// carry attacker-chosen text. Only the first segment is a mapping target
// the operator wrote, so only the first segment may survive.
func TestFirstSegmentKeepsOnlyTheConfiguredTarget(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"a plain target":        {"release", "release"},
		"a nested key":          {"release.inner", "release"},
		"an array index":        {"servers[3]", "servers"},
		"index then key":        {"servers[3].name", "servers"},
		"key then index":        {"release.hosts[0]", "release"},
		"an index at the front": {"[0].name", ""},
		"empty":                 {"", ""},
	} {
		if got := firstSegment(tc.in); got != tc.want {
			t.Errorf("%s: firstSegment(%q) = %q, want %q", name, tc.in, got, tc.want)
		}
	}
}
