package assert

import (
	"math"
	"strings"

	"golang.org/x/net/html"
)

// subjectFilter is a necessary condition of a part's subject (its
// rightmost compound) matching: the tag it names, the id, and a class
// that must occur in the class attribute. "" is no condition.
type subjectFilter struct {
	tag, id, class string
}

// may reports whether n can be the subject f describes.
func (f subjectFilter) may(n *html.Node) bool {
	if f == (subjectFilter{}) {
		return true
	}
	if n.Type != html.ElementNode || f.tag != "" && n.Data != f.tag {
		return false
	}
	return (f.id == "" || hasAttr(n, "id", func(v string) bool { return v == f.id })) &&
		(f.class == "" || hasAttr(n, "class", func(v string) bool { return strings.Contains(v, f.class) }))
}

func hasAttr(n *html.Node, key string, ok func(string) bool) bool {
	for _, at := range n.Attr {
		if at.Key == key && ok(at.Val) {
			return true
		}
	}
	return false
}

// Path is what MatchCost reads of a node's place in the page.
type Path struct {
	// Depth is the node's ancestors.
	Depth int
	// Siblings is the largest sibling count of the node and its
	// ancestors, or the node's child count when larger; OwnSiblings the
	// node's own sibling or child count, whichever is larger; SiblingSum
	// adds up 1 + the sibling count of the node and each ancestor.
	Siblings, OwnSiblings, SiblingSum int
	// Attrs is the largest attribute weight of the node and its
	// ancestors.
	Attrs int
}

// MatchCost is the worst case, in steps, of matching html's selector on n
// at p, and whether a match is possible at all (when not, Match need not
// run and the cost is 1). Per comma-separated part whose subject n can
// be: its ancestor walks cost C(depth, ancestors), its sibling walks
// (1+siblings)^siblings (the node's own count unless a walk runs on an
// ancestor), both together their product when an ancestor
// walk runs inside a sibling walk, C(depth, ancestors-1) times SiblingSum
// times (1+siblings)^(siblings-1) when a sibling walk runs inside an
// ancestor walk, and their sum otherwise; times 1+attributes when the
// part reads attributes. It saturates at math.MaxInt64. 1 and true for
// any other subject.
func (a Assertion) MatchCost(n *html.Node, p Path) (int64, bool) {
	total, possible := 1.0, len(a.costs) == 0
	for _, c := range a.costs {
		if !c.subject.may(n) {
			continue
		}
		possible = true
		siblings := p.OwnSiblings
		if c.sibUp {
			siblings = p.Siblings
		}
		up := binomial(p.Depth, c.ancestors)
		across := math.Pow(float64(1+siblings), float64(c.siblings))
		var cost float64
		switch {
		case c.ancInSib:
			cost = up * across
		case c.sibInAnc:
			cost = binomial(p.Depth, c.ancestors-1) * float64(p.SiblingSum) * math.Pow(float64(1+p.Siblings), float64(c.siblings-1))
		default:
			cost = up + across
		}
		if c.attrs {
			cost *= float64(1 + p.Attrs)
		}
		total += cost
	}
	if total >= math.MaxInt64 {
		return math.MaxInt64, possible
	}
	return int64(total), possible
}

// binomial is C(n, k) for small k, at least 1.
func binomial(n, k int) float64 {
	c := 1.0
	for i := range k {
		c = c * float64(n-i) / float64(i+1)
	}
	return max(c, 1)
}
