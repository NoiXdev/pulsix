package selector

import (
	"strings"
	"testing"
)

// TestParseBoundsTheSelector is backlog 7.41: every condition becomes its
// own correlated sub-query in one statement on the shared pool, and
// nothing bounded how many a selector may carry or how long it may be.
func TestParseBoundsTheSelector(t *testing.T) {
	conds := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = "env=prod"
		}
		return strings.Join(parts, " and ")
	}
	if _, err := Parse(conds(MaxConditions)); err != nil {
		t.Fatalf("Parse with %d conditions = %v, want it accepted", MaxConditions, err)
	}
	if _, err := Parse(conds(MaxConditions + 1)); err == nil {
		t.Fatalf("Parse with %d conditions was accepted", MaxConditions+1)
	}
	long := "env in (" + strings.Repeat("a,", MaxLen/2) + "b)"
	if _, err := Parse(long); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("Parse of a %d-byte selector = %v, want refused for its length", len(long), err)
	}
}
