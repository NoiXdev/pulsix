package assert_test

import (
	"math"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/probe/assert"
	"golang.org/x/net/html"
)

func parseSelector(sel string) (assert.Assertion, error) {
	return assert.Parse(`x=html("` + strings.ReplaceAll(strings.ReplaceAll(sel, `\`, `\\`), `"`, `\"`) + `") exists`)
}

// TestSelectorShapeAccepts: ordinary selectors, read from what cascadia
// parsed, so spacing inside nth expressions and escapes count nothing.
func TestSelectorShapeAccepts(t *testing.T) {
	for _, sel := range []string{
		"p", " p ", "div p", "main div p", "nav ul li a", "table tbody tr td", ".content .price",
		"div > p > span", "h1 + p", "h1 ~ p", "a b, c d", "h1, h2,h3",
		`[title="a b  c"] p`, `[title='x ~ y'] > p`,
		"ul > li:first-child", "tr:nth-child(2n + 1) > td", "tr:nth-child(2n - 1) td", "tr:Last-Child td",
		`#\31 23 p`, "div:not(.a) p", "div:not(.a, .b)", "div:not( .a ) > p", "div:NOT(.a)",
		"p:empty", "p:not(:empty)", "div p:lang(en)", "input:enabled", "a:link", "input:checked",
	} {
		if _, err := parseSelector(sel); err != nil {
			t.Errorf("selector %q refused: %v", sel, err)
		}
	}
}

// TestSelectorShapeRefuses: comments (which cascadia reads as a
// descendant combinator), the subtree searches, and nesting past the
// bounds.
func TestSelectorShapeRefuses(t *testing.T) {
	cases := []struct{ sel, want string }{
		{"x/**/div/**/p", "comments"},
		{"a /**/ b", "comments"},
		{"div:not(a/**/b)", "comments"},
		{`[title="/*"]`, "comments"},
		{"a b c d e", "at most 3 descendant"},
		{"a:not(b c d e) f", "at most 3 descendant"},
		{"a b, c d e f g", "at most 3 descendant"},
		{"a ~ b ~ c", "~"},
		{"a b ~ c", "~"},
		{"a ~ b c", "~"},
		{"a:not(b ~ c) d", "~"},
		{"p:empty a", ":empty"},
		{"p:empty > a", ":empty"},
		{"p:empty + a", ":empty"},
		{"a:lang(en) b c d", "nests too many"},
		{"a:enabled b c d", "nests too many"},
		{"p:has(a)", ":has"},
		{"p:HAS(a)", ":has"},
		{"p:haschild(a)", ":haschild"},
		{`p:contains("x")`, ":contains"},
		{`p:containsOwn("x")`, ":contains"},
		{"p:matches(x)", ":matches"},
		{"p:matchesown(x)", ":matches"},
	}
	for _, tc := range cases {
		_, err := parseSelector(tc.sel)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("selector %q = %v, want an error containing %q", tc.sel, err, tc.want)
		}
	}
}

func element(tag string, attrs ...string) *html.Node {
	n := &html.Node{Type: html.ElementNode, Data: tag}
	for i := 0; i+1 < len(attrs); i += 2 {
		n.Attr = append(n.Attr, html.Attribute{Key: attrs[i], Val: attrs[i+1]})
	}
	return n
}

// TestMatchCost: the charge grows with C(depth, descendant walks), with
// the siblings per sibling walk (the node's own unless a walk runs on an
// ancestor), with SiblingSum when a sibling walk runs on every ancestor,
// and with the attributes when the selector reads them; a node the
// subject cannot be costs 1 and needs no match.
func TestMatchCost(t *testing.T) {
	at := assert.Path{Depth: 10, Siblings: 100, OwnSiblings: 5, SiblingSum: 200, Attrs: 4}
	cases := []struct {
		sel      string
		n        *html.Node
		want     int64
		possible bool
	}{
		{"p", element("p"), 3, true},
		{"div p", element("p"), 12, true},
		{"main div p", element("p"), 47, true},
		{"table tbody tr td", element("td"), 122, true},
		{".content .price", element("span", "class", "a price"), 56, true},
		{"li:first-child a", element("a"), 201, true},
		{"h1 + p", element("p"), 8, true},
		{"h1 ~ p", element("p"), 8, true},
		{"li:first-child > a", element("a"), 103, true},
		{"p:lang(en)", element("p"), 56, true},
		{"input:enabled", element("input"), 1001, true},
		{"div:last-child p", element("p"), 201, true},
		{"a:lang(en) ~ p", element("p"), 301, true},
		{"a, p", element("p"), 3, true},
		{"#x", element("p", "id", "x"), 11, true},
		{"div p", element("span"), 1, false},
		{".price", element("p", "class", "cost"), 1, false},
		{"#x", element("p", "id", "xy"), 1, false},
		{"div p", &html.Node{Type: html.TextNode, Data: "p"}, 1, false},
	}
	for _, tc := range cases {
		a, err := parseSelector(tc.sel)
		if err != nil {
			t.Fatalf("selector %q: %v", tc.sel, err)
		}
		if got, possible := a.MatchCost(tc.n, at); got != tc.want || possible != tc.possible {
			t.Errorf("MatchCost(%q) = %d, %v, want %d, %v", tc.sel, got, possible, tc.want, tc.possible)
		}
	}
	a, _ := parseSelector("a:enabled ~ b")
	huge := assert.Path{Depth: 512, Siblings: math.MaxInt32, OwnSiblings: math.MaxInt32, SiblingSum: math.MaxInt32, Attrs: math.MaxInt32}
	if got, _ := a.MatchCost(element("b"), huge); got != math.MaxInt64 {
		t.Errorf("a hostile cost = %d, want it saturated", got)
	}
	if got, possible := mustParse(t, "x=status == 200").MatchCost(element("p"), at); got != 1 || !possible {
		t.Errorf("a status assertion's match cost = %d, %v, want 1, true", got, possible)
	}
}
