package sshprobe

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		name   string
		pinned string
		seen   string
		want   KeyState
	}{
		{"no pin, nothing seen", "", "", KeyStateUnpinned},
		{"no pin, key seen", "", "SHA256:AAAA", KeyStateUnpinned},
		{"pin matches", "SHA256:AAAA", "SHA256:AAAA", KeyStatePinned},
		{"pin differs", "SHA256:AAAA", "SHA256:BBBB", KeyStateChanged},
		{"pin set, nothing seen", "SHA256:AAAA", "", KeyStateChanged},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Compare(c.pinned, c.seen); got != c.want {
				t.Fatalf("Compare(%q, %q) = %q, want %q", c.pinned, c.seen, got, c.want)
			}
		})
	}
	// The state constants are stored in a CHECK-constrained column; their
	// spellings are the wire format.
	if KeyStateUnpinned != "unpinned" || KeyStatePinned != "pinned" || KeyStateChanged != "changed" {
		t.Fatal("key state constants drifted from the stored values")
	}
}
