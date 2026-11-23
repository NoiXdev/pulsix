package varsource

import "testing"

// The playbook texts' stats keys and reasons are a wire contract between a
// worker and a manager of different versions: their values never change.
func TestPlaybookTextsWireValues(t *testing.T) {
	for got, want := range map[string]string{
		PlaybookTextsStatsKey:        "playbook_texts",
		PlaybookTextsLeftOutStatsKey: "playbook_texts_left_out",
		TextTooLarge:                 "too_large",
		TextBudget:                   "budget",
		TextNotUTF8:                  "not_utf8",
		TextUnreadable:               "unreadable",
	} {
		if got != want {
			t.Errorf("wire value %q, want %q", got, want)
		}
	}
	if MaxPlaybookTextsBytes != 4<<20 {
		t.Errorf("MaxPlaybookTextsBytes = %d, want 4 MiB", MaxPlaybookTextsBytes)
	}
}

// A text's cost against MaxPlaybookTextsBytes is what the result's JSON
// carries, its quotes left out: escapes count in full.
func TestEncodedTextLen(t *testing.T) {
	for text, want := range map[string]int{
		"":                0,
		"- hosts: all\n":  13 + 1,
		"a<b>&c":          6 + 3*5,
		"tab\there":       8 + 1,
		" ":               6,
		"\x01":            6,
		"ü":               2,
		`quote " back \ `: 15 + 2,
	} {
		if got := EncodedTextLen(text); got != want {
			t.Errorf("EncodedTextLen(%q) = %d, want %d", text, got, want)
		}
	}
}

// TextProblem is the reason a text cannot be stored, or "": over
// MaxFileBytes is too large, invalid UTF-8 or a NUL byte is no text.
func TestTextProblem(t *testing.T) {
	for name, tc := range map[string]struct {
		text string
		want string
	}{
		"empty":         {"", ""},
		"playbook":      {"- hosts: all\n", ""},
		"at the limit":  {stringOf('a', MaxFileBytes), ""},
		"over":          {stringOf('a', MaxFileBytes+1), TextTooLarge},
		"invalid utf-8": {"a\xffb", TextNotUTF8},
		"nul":           {"a\x00b", TextNotUTF8},
	} {
		if got := TextProblem(tc.text); got != tc.want {
			t.Errorf("%s: TextProblem = %q, want %q", name, got, tc.want)
		}
	}
}

func stringOf(c byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}
