package assert_test

import (
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/probe/assert"
)

// TestSelectorCost: a selector whose match on one node could cost more
// than a walk of the node's ancestors or siblings is refused — more than
// one descendant (space) or ~ combinator in a comma-separated part, and
// the pseudo-classes that search a subtree or its text.
func TestSelectorCost(t *testing.T) {
	ok := []struct {
		sel                 string
		groups              int
		ancestors, siblings bool
	}{
		{"p", 1, false, false},
		{" p ", 1, false, false},
		{"div p", 1, true, false},
		{"div > p > span", 1, false, false},
		{"h1 + p", 1, false, false},
		{"h1 ~ p", 1, false, true},
		{"a b, c d", 2, true, false},
		{"h1, h2,h3", 3, false, false},
		{`[title="a b  c"] p`, 1, true, false},
		{`[title='x ~ y'] > p`, 1, false, false},
		{"ul > li:first-child", 1, false, false},
		{"tr:nth-child(2n + 1) > td", 1, false, true},
		{"tr:Last-Child td", 1, true, true},
		{"div:not(.a) p", 1, true, false},
		{"div:not(.a, .b)", 1, false, false},
		{"div:not( .a ) > p", 1, false, false},
		{"div:NOT(.a)", 1, false, false},
	}
	for _, tc := range ok {
		a, err := assert.Parse(`x=html("` + strings.ReplaceAll(tc.sel, `"`, `\"`) + `") exists`)
		if err != nil {
			t.Errorf("selector %q refused: %v", tc.sel, err)
			continue
		}
		if groups, ancestors, siblings := a.SelectorShape(); groups != tc.groups || ancestors != tc.ancestors || siblings != tc.siblings {
			t.Errorf("selector %q: groups %d ancestors %v siblings %v, want %+v", tc.sel, groups, ancestors, siblings, tc)
		}
	}
	refused := []struct{ sel, want string }{
		{"div p span", "at most one"},
		{"div ~ p span", "at most one"},
		{"a ~ b ~ c", "at most one"},
		{"div:not(.a .b) p", "at most one"},
		{"a b, c d e", "at most one"},
		{"p:has(a)", ":has"},
		{"p:HAS(a)", ":has"},
		{"p:haschild(a)", ":haschild"},
		{`p:contains("x")`, ":contains"},
		{`p:containsOwn("x")`, ":containsown"},
		{"p:matches(x)", ":matches"},
		{"p:matchesown(x)", ":matchesown"},
	}
	for _, tc := range refused {
		_, err := assert.Parse(`x=html("` + strings.ReplaceAll(tc.sel, `"`, `\"`) + `") exists`)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("selector %q = %v, want an error containing %q", tc.sel, err, tc.want)
		}
	}
	if parts, ancestors, siblings := mustParse(t, "x=status == 200").SelectorShape(); parts != 0 || ancestors || siblings {
		t.Error("a status assertion has no selector to search with")
	}
}
