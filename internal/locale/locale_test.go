package locale_test

import (
	"testing"

	"github.com/noixdev/pulsix/internal/locale"
)

func TestValid(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"en", true},
		{"de", true},
		{"", false},
		{"EN", false},
		{"fr", false},
		{"de-DE", false},
	}
	for _, tt := range tests {
		if got := locale.Valid(tt.in); got != tt.want {
			t.Errorf("Valid(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestResolve(t *testing.T) {
	str := func(s string) *string { return &s }
	tests := []struct {
		name            string
		user            *string
		instanceDefault string
		want            string
	}{
		{"user wins over the default", str("de"), "en", "de"},
		{"no user falls back to the default", nil, "de", "de"},
		{"invalid user falls back to the default", str("xx"), "de", "de"},
		{"empty user falls back to the default", str(""), "de", "de"},
		{"nothing set falls back to English", nil, "", "en"},
		{"invalid default falls back to English", nil, "fr", "en"},
		{"invalid user and invalid default", str("xx"), "fr", "en"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := locale.Resolve(tt.user, tt.instanceDefault); got != tt.want {
				t.Errorf("Resolve = %q, want %q", got, tt.want)
			}
		})
	}
}
