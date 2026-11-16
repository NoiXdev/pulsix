package assert

import (
	"fmt"
	"slices"
	"strings"
)

// costlyPseudo are the pseudo-classes whose match on one node searches
// the node's subtree or its text: on a hostile 1 MiB page that makes one
// selector quadratic or worse, and a match cannot be interrupted.
var costlyPseudo = []string{"has", "haschild", "contains", "containsown", "matches", "matchesown"}

// siblingPseudo are the pseudo-classes whose match on one node walks the
// node's siblings.
var siblingPseudo = []string{"nth-child", "nth-last-child", "nth-of-type", "nth-last-of-type",
	"last-child", "last-of-type", "only-child", "only-of-type", "first-of-type"}

// selectorShape reads a selector cascadia has parsed: whether matching a
// node walks its ancestors (a descendant combinator) or its siblings (a ~
// combinator, an nth-, last-, only- or of-type pseudo-class). It refuses
// the costly pseudo-classes and more than one descendant or ~ combinator
// in a comma-separated part: with n of them, matching one node walks up
// to depth^n ancestors, so one is the most that keeps a node's match a
// single walk.
func selectorShape(s string) (ancestors, siblings bool, err error) {
	inPart := 0
	parens := 0
	var quote byte
	bracket := false
	prev := byte(0)
	search := func(up bool) error {
		inPart++
		if up {
			ancestors = true
		} else {
			siblings = true
		}
		if inPart > 1 {
			return fmt.Errorf("a CSS selector of html(…) may have at most one descendant (space) or ~ combinator in each comma-separated part; use > or + for the others")
		}
		return nil
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			switch c {
			case '\\':
				i++
			case quote:
				quote = 0
			}
		case c == '\\':
			i++
		case c == '"' || c == '\'':
			quote = c
		case bracket:
			bracket = c != ']'
		case c == '[':
			bracket = true
		case c == '(':
			parens++
		case c == ')':
			parens--
		case c == ',' && parens == 0:
			inPart = 0
		case c == '~':
			if err := search(false); err != nil {
				return false, false, err
			}
		case c == ':':
			j := i + 1
			for j < len(s) && (isLetter(s[j]) || s[j] == '-' || s[j] >= '0' && s[j] <= '9') {
				j++
			}
			name := strings.ToLower(s[i+1 : j])
			if slices.Contains(costlyPseudo, name) {
				return false, false, fmt.Errorf("a CSS selector of html(…) may not use :%s, which searches whole subtrees", name)
			}
			siblings = siblings || slices.Contains(siblingPseudo, name)
			c, i = s[j-1], j-1
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			j := i
			for j < len(s) && strings.IndexByte(" \t\n\r\f", s[j]) >= 0 {
				j++
			}
			if prev != 0 && j < len(s) && strings.IndexByte(">+~,(", prev) < 0 && strings.IndexByte(">+~,)", s[j]) < 0 {
				if err := search(true); err != nil {
					return false, false, err
				}
			}
			i = j - 1
			continue
		}
		prev = c
	}
	return ancestors, siblings, nil
}
