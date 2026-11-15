package zfs

import (
	"strings"
	"testing"
)

func TestCleanStderr(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"\n\nsecond", ""},
		{"  padded  \nnext", "padded"},
		{"a\tb\x00c\x1bd e", "a bcde"},
		{"bad \xff utf8", "bad � utf8"},
		{strings.Repeat("x", 300), strings.Repeat("x", 256)},
		{strings.Repeat("x", 255) + "é", strings.Repeat("x", 255)},
	} {
		if got := cleanStderr([]byte(tc.in)); got != tc.want {
			t.Errorf("cleanStderr(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
