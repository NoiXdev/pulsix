// package facts: the normalization guard's own unit tests (unexported by
// design — the collector's fixed key set can never exercise it through
// Collect).
package facts

import (
	"fmt"
	"strings"
	"testing"
)

func TestSanitizeKey(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{in: "os", want: "os"},
		{in: "OS", want: "os"},
		{in: "Mem.Total", want: "mem_total"},
		{in: "cpu cores!", want: "cpu_cores_"},
		{in: "Mémo", want: "m_mo"},
		{in: "", want: ""},
		{in: strings.Repeat("k", MaxKeyLen+10), want: strings.Repeat("k", MaxKeyLen)},
	}
	for _, tc := range cases {
		if got := sanitizeKey(tc.in); got != tc.want {
			t.Errorf("sanitizeKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestClampValue(t *testing.T) {
	if got := clampValue("short"); got != "short" {
		t.Errorf("clampValue short-circuits: %q", got)
	}
	exact := strings.Repeat("a", MaxValueLen)
	if got := clampValue(exact); got != exact {
		t.Errorf("the limit itself survives: %d chars", len(got))
	}
	over := strings.Repeat("b", MaxValueLen+1)
	if got := clampValue(over); got != over[:MaxValueLen] {
		t.Errorf("over-long ascii clipped to %d chars", len(got))
	}
	// A multi-byte rune must never be split: the clip ends on a rune
	// boundary at or below the limit.
	runic := strings.Repeat("é", 400) // 2 bytes each, 800 bytes
	got := clampValue(runic)
	if got+"" != got || len(got) > MaxValueLen {
		t.Fatalf("clip produced %d bytes", len(got))
	}
	if !strings.HasPrefix(runic, got) || len(got)%2 != 0 {
		t.Errorf("clip split a rune (odd byte count %d)", len(got))
	}
}

func TestNormalizeBoundsAndDedupes(t *testing.T) {
	raw := map[string]string{}
	for i := 0; i < MaxKeys+10; i++ {
		raw[fmt.Sprintf("key%03d", i)] = "v"
	}
	// An over-long value and an empty one, plus a collision the sort order
	// resolves deterministically.
	raw["IP"] = strings.Repeat("z", MaxValueLen+1)
	raw["empty"] = ""
	got := normalize(raw)
	if len(got) != MaxKeys {
		t.Fatalf("normalize kept %d keys, want %d", len(got), MaxKeys)
	}
	if _, ok := got["ip"]; !ok {
		t.Error("IP collides with ip after lowercasing — first sorted wins")
	}
	if got["ip"] != strings.Repeat("z", MaxValueLen) {
		t.Errorf("over-long value not clipped: %d chars", len(got["ip"]))
	}
	if _, ok := got["empty"]; ok {
		t.Error("an empty value means unknown — the entry must be dropped")
	}
	if _, ok := got[fmt.Sprintf("key%03d", MaxKeys)]; ok {
		t.Error("keys past the 64 limit must be dropped (sorted first-wins)")
	}
	for k := range got {
		if !keyPattern.MatchString(k) {
			t.Fatalf("key %q escaped sanitization", k)
		}
	}
}
