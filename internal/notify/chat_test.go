package notify_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/notify"
)

func TestSafeLink(t *testing.T) {
	for u, want := range map[string]bool{
		"https://pulsix.example/o/acme/alerts?host=1&x=2": true,
		"http://10.0.0.1:8080/o/a":                        true,
		"":                                                false,
		"javascript:alert(1)":                             false,
		"https://pulsix.example/a b":                      false,
		"https://pulsix.example/a|b":                      false,
		"https://pulsix.example/a>b":                      false,
		"https://pulsix.example/a\"b":                     false,
		"https://pulsix.example/(a)":                      false,
		"ftp://pulsix.example/":                           false,
		"https:///nohost":                                 false,
		"https://" + strings.Repeat("a", 3000):            false,
	} {
		if got := notify.SafeLink(u); got != want {
			t.Errorf("SafeLink(%q) = %v", u, got)
		}
	}
}

func TestClip(t *testing.T) {
	if got := notify.Clip("äbc", 3); got != "äbc" {
		t.Fatalf("short = %q", got)
	}
	if got := notify.Clip("äbcd", 3); got != "äb…" || utf8.RuneCountInString(got) != 3 {
		t.Fatalf("long = %q", got)
	}
}

func TestMoreNote(t *testing.T) {
	if got := notify.MoreNote(3); got != "… and 3 more" {
		t.Fatalf("note = %q", got)
	}
}

func TestFit(t *testing.T) {
	runes := utf8.RuneCountInString
	l := strings.Repeat("x", 20) // 21 with the separator; a note reserves 13
	lines := []string{l, l, l}
	cases := []struct {
		name            string
		budget, omitted int
		kept, more      int
	}{
		{"everything fits", 63, 0, 3, 0},
		{"one short", 62, 0, 2, 1},
		{"room for one and the note", 40, 0, 1, 2},
		{"all fit beside the event's own note", 76, 4, 3, 4},
		{"the event's note does not fit beside all", 75, 4, 2, 5},
		{"nothing fits", 10, 0, 0, 3},
	}
	for _, c := range cases {
		kept, more := notify.Fit(lines, c.budget, runes, c.omitted)
		if len(kept) != c.kept || more != c.more {
			t.Errorf("%s: kept %d more %d, want %d/%d", c.name, len(kept), more, c.kept, c.more)
		}
		if used := total(kept, runes) + noteSize(more, runes); used > c.budget && len(kept) > 0 {
			t.Errorf("%s: used %d of %d", c.name, used, c.budget)
		}
	}
}

func total(lines []string, size func(string) int) int {
	n := 0
	for _, l := range lines {
		n += size(l) + 1
	}
	return n
}

func noteSize(more int, size func(string) int) int {
	if more == 0 {
		return 0
	}
	return size(notify.MoreNote(more)) + 1
}

func TestFitWithinHonoursEveryBudget(t *testing.T) {
	wide := strings.Repeat("𝔘", 10) // 10 runes, 40 bytes
	lines := []string{wide, wide, wide, wide}
	bytes := func(s string) int { return len(s) }
	// Runes alone would keep all four (44 of 100); bytes keep two: each
	// line costs 41, the note reserve 14 (… is three bytes).
	kept, more := notify.FitWithin(lines, 0,
		notify.Budget{Limit: 100, Size: utf8.RuneCountInString},
		notify.Budget{Limit: 100, Size: bytes})
	if len(kept) != 2 || more != 2 {
		t.Fatalf("kept %d more %d", len(kept), more)
	}
}

func TestJSONSize(t *testing.T) {
	for s, want := range map[string]int{
		"abc":  3,
		`a"b`:  4,
		`a\b`:  4,
		"a\nb": 4,
		"ä":    2,
		"\x01": 6,
		"&<>":  3,
	} {
		if got := notify.JSONSize(s); got != want {
			t.Errorf("JSONSize(%q) = %d, want %d", s, got, want)
		}
	}
}
