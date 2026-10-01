package items_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/items"
)

// TestMatch exercises the whole glob syntax exhaustively (task 1 brief,
// "Tests"): a literal walk anchored at both ends, with '*' standing for any
// run of characters including none, and nothing else carrying meaning.
func TestMatch(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		item    string
		want    bool
	}{
		{"exact match", "sshd", "sshd", true},
		{"exact mismatch", "sshd", "sshd2", false},
		{"star alone matches a non-empty item", "*", "anything", true},
		{"star alone matches the empty item", "*", "", true},
		{"leading star", "*.log", "app.log", true},
		{"leading star mismatch", "*.log", "app.txt", false},
		{"trailing star", "/var/*", "/var/log", true},
		{"trailing star mismatch", "/var/*", "/etc/log", false},
		{"interior star", "/mnt/*-db", "/mnt/scratch-db", true},
		{"interior star mismatch", "/mnt/*-db", "/mnt/scratch-web", false},
		{"several stars", "a*b*c", "aXbYc", true},
		{"several stars mismatch", "a*b*c", "aXcYb", false},
		{"several stars, literals touching", "a*b*c", "abc", true},
		{"only stars matches anything", "***", "whatever goes here", true},
		{"only stars matches the empty item", "***", "", true},
		{"empty item against a literal pattern", "sshd", "", false},
		{"empty item against a pattern with a star", "sshd*", "", false},
		{"out-of-order literals do not match", "*a*b*", "ba", false},
		{"out-of-order literals, longer", "x*y*z", "zyx", false},
		{"literal dot matches itself", ".", ".", true},
		{"literal dot does not act as any-char", "a.b", "axb", false},
		{"literal dot matches itself in context", "a.b", "a.b", true},
		{"literal question mark matches itself", "a?b", "a?b", true},
		{"literal question mark does not act as any-char", "a?b", "axb", false},
		{"literal bracket matches itself", "a[b]c", "a[b]c", true},
		{"literal bracket does not act as a class", "a[b]c", "abc", false},
		{"literal backslash matches itself", `a\b`, `a\b`, true},
		{"literal backslash does not act as an escape", `a\b`, `axb`, false},
		{"literal percent matches itself", "50%", "50%", true},
		{"literal percent does not act as a LIKE wildcard", "50%", "50", false},
		{"literal underscore matches itself", "a_b", "a_b", true},
		{"literal underscore does not act as a single-char wildcard", "a_b", "axb", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := items.Match(tt.pattern, tt.item); got != tt.want {
				t.Fatalf("Match(%q, %q) = %v, want %v", tt.pattern, tt.item, got, tt.want)
			}
		})
	}
}

func TestValidPatternRejectsEmpty(t *testing.T) {
	err := items.ValidPattern("")
	if !errors.Is(err, items.ErrInvalidInput) {
		t.Fatalf("ValidPattern(\"\") = %v, want an error wrapping ErrInvalidInput", err)
	}
}

func TestValidPatternRejectsOverLong(t *testing.T) {
	p := strings.Repeat("a", items.MaxPatternLen+1)
	err := items.ValidPattern(p)
	if !errors.Is(err, items.ErrInvalidInput) {
		t.Fatalf("ValidPattern(over-long) = %v, want an error wrapping ErrInvalidInput", err)
	}
}

func TestValidPatternAcceptsBoundaryLength(t *testing.T) {
	p := strings.Repeat("a", items.MaxPatternLen)
	if err := items.ValidPattern(p); err != nil {
		t.Fatalf("ValidPattern(exactly MaxPatternLen) = %v, want nil", err)
	}
}

func TestValidPatternAcceptsOrdinaryPattern(t *testing.T) {
	if err := items.ValidPattern("/mnt/scratch-*"); err != nil {
		t.Fatalf("ValidPattern(ordinary pattern) = %v, want nil", err)
	}
}
