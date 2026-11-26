package http

import (
	"context"
	"strings"

	"github.com/noixdev/pulsix/internal/probe/assert"
	"golang.org/x/net/html"
)

// selectAll is the elements under doc that sel selects, in document
// order. It is one pre-order walk of the page that keeps, per
// comma-separated part, the set of states reached on the way (state i:
// compounds 0..i-1 matched in the relations the part needs). An element
// matching compound i-1 hands state i on by the combinator before
// compound i: a descendant combinator to its whole subtree, > to its
// children, + to its next element sibling and ~ to every later one. A
// parent's children share one siblings record, and the walk visits them
// in order with each one's subtree finished before the next, so the
// sibling states are exact there. The work is linear in nodes x
// compounds x simple selectors, with no backtracking and no sibling
// visited twice.
//
// Every step is charged before it is taken — one per node, per child
// counted and per condition tested, plus the attribute bytes read and
// compared and, for an of-type pseudo-class, the element names counted —
// to the assertion's budget (at most perSelector) and to *left, the
// check's; past either the walk stops with errHTMLTooMuch. It looks at
// ctx before every node and stops with errOutOfTime once it has ended.
func selectAll(ctx context.Context, doc *html.Node, sel *assert.Selector, perSelector int64, left *int64) ([]*html.Node, string) {
	s := selection{sel: sel, budget: min(perSelector, *left), left: left, ofType: sel.OfType(), siblings: usesSiblings(sel)}
	var matches []*html.Node
	stack := []visit{{n: doc}}
	for len(stack) > 0 {
		if ctx.Err() != nil {
			return nil, errOutOfTime
		}
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if !s.spend(1) {
			return nil, errHTMLTooMuch
		}
		desc, child := v.desc, []uint16(nil)
		if v.n.Type == html.ElementNode {
			e := element{n: v.n, pos: v.pos}
			matched, out, enough := s.states(&e, &v)
			if !enough {
				return nil, errHTMLTooMuch
			}
			if matched {
				matches = append(matches, v.n)
			}
			desc, child = orStates(v.desc, out[assert.Descendant]), out[assert.Child]
			if v.sib != nil {
				v.sib.adjacent = out[assert.Adjacent]
				v.sib.general = orStates(v.sib.general, out[assert.Sibling])
			}
		}
		var ok bool
		if stack, ok = s.pushChildren(stack, v.n, desc, child); !ok {
			return nil, errHTMLTooMuch
		}
	}
	return matches, ""
}

// usesSiblings reports whether a part of sel has a + or ~ combinator.
func usesSiblings(sel *assert.Selector) bool {
	for _, part := range sel.Parts {
		for _, c := range part.Combinators {
			if c == assert.Adjacent || c == assert.Sibling {
				return true
			}
		}
	}
	return false
}

// visit is a node to match: its place among its siblings and the part
// states it inherits — desc from any ancestor, child from its parent
// alone, and through sib what its earlier element siblings hand on; nil
// is none.
type visit struct {
	n           *html.Node
	pos         position
	desc, child []uint16
	sib         *siblings
}

// position is an element's place among its parent's element children:
// 1-based index of count, and among those of its name typeIndex of
// typeCount (counted only for a selector with an of-type pseudo-class).
type position struct {
	index, count         int32
	typeIndex, typeCount int32
}

// siblings is what one parent's element children so far hand on to the
// later ones: adjacent the states of the latest (for +), general those
// of all of them (for ~).
type siblings struct {
	adjacent, general []uint16
}

// element is a node being matched, with its class list read once.
type element struct {
	n           *html.Node
	pos         position
	classesRead bool
	classes     []string
	classSet    map[string]bool
}

// selection is one walk's selector and budgets.
type selection struct {
	sel      *assert.Selector
	budget   int64
	left     *int64
	ofType   bool
	siblings bool
	// typeIndex is scratch for pushChildren's of-type places.
	typeIndex []int32
}

// spend takes n steps from the budgets, or reports that they do not fit.
func (s *selection) spend(n int64) bool {
	if n > s.budget || n > *s.left {
		return false
	}
	s.budget -= n
	*s.left -= n
	return true
}

// states matches e against every compound its inherited states (and
// state 0, open everywhere) allow: matched when a part's last compound
// matched; out the states it hands on, by the combinator that hands them.
func (s *selection) states(e *element, v *visit) (matched bool, out [4][]uint16, enough bool) {
	parts := s.sel.Parts
	var adjacent, general []uint16
	if v.sib != nil {
		adjacent, general = v.sib.adjacent, v.sib.general
	}
	for p, part := range parts {
		avail := uint16(1)
		if v.desc != nil {
			avail |= v.desc[p]
		}
		if v.child != nil {
			avail |= v.child[p]
		}
		if adjacent != nil {
			avail |= adjacent[p]
		}
		if general != nil {
			avail |= general[p]
		}
		for i, c := range part.Compounds {
			if avail&(1<<i) == 0 {
				continue
			}
			ok, enough := s.compound(e, c)
			switch {
			case !enough:
				return false, out, false
			case !ok:
				continue
			case i+1 == len(part.Compounds):
				matched = true
			default:
				comb := part.Combinators[i+1]
				if out[comb] == nil {
					out[comb] = make([]uint16, len(parts))
				}
				out[comb][p] |= 1 << (i + 1)
			}
		}
	}
	return matched, out, true
}

// orStates is a | b, sharing a when b adds nothing; neither is changed.
func orStates(a, b []uint16) []uint16 {
	if b == nil {
		return a
	}
	if a == nil {
		return b
	}
	out := make([]uint16, len(a))
	for i := range a {
		out[i] = a[i] | b[i]
	}
	return out
}

// pushChildren pushes n's children last-first, so the first is visited
// first, with their position, the states n hands down and, for a
// selector with sibling combinators, one siblings record they share.
func (s *selection) pushChildren(stack []visit, n *html.Node, desc, child []uint16) ([]visit, bool) {
	children, elements, names := 0, 0, 0
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		children++
		if c.Type == html.ElementNode {
			elements++
			if s.ofType {
				names += 1 + len(c.Data)
			}
		}
	}
	if !s.spend(int64(children + names)) {
		return stack, false
	}
	var types map[string]int32
	if s.ofType && elements > 0 {
		// Forward: each element's 1-based place among its name
		// (s.typeIndex, reused across parents); types ends as the counts.
		types = make(map[string]int32, elements)
		s.typeIndex = s.typeIndex[:0]
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode {
				types[c.Data]++
				s.typeIndex = append(s.typeIndex, types[c.Data])
			}
		}
	}
	var sib *siblings
	if s.siblings && elements > 1 {
		sib = &siblings{}
	}
	index := int32(elements)
	for c := n.LastChild; c != nil; c = c.PrevSibling {
		v := visit{n: c, pos: position{index: index, count: int32(elements)}, desc: desc, child: child, sib: sib}
		if c.Type == html.ElementNode {
			if types != nil {
				v.pos.typeIndex, v.pos.typeCount = s.typeIndex[index-1], types[c.Data]
			}
			index--
		}
		stack = append(stack, v)
	}
	return stack, true
}

// compound tests every condition of c on e, cheapest first.
func (s *selection) compound(e *element, c assert.Compound) (ok, enough bool) {
	conditions := 1 + len(c.Classes) + len(c.Attrs) + len(c.Pseudos)
	if c.ID != "" {
		conditions++
	}
	if !s.spend(int64(conditions)) {
		return false, false
	}
	if c.Tag != "" && !asciiEqualFold(e.n.Data, c.Tag) {
		return false, true
	}
	for _, p := range c.Pseudos {
		if ok, enough := s.pseudoHolds(p, e); !ok || !enough {
			return false, enough
		}
	}
	if c.ID != "" {
		v, found, enough := s.attr(e.n, "id")
		if !enough || !found || v != c.ID {
			return false, enough
		}
	}
	if len(c.Classes) > 0 {
		if !s.readClasses(e) {
			return false, false
		}
		for _, class := range c.Classes {
			if !e.hasClass(class) {
				return false, true
			}
		}
	}
	for _, t := range c.Attrs {
		v, found, enough := s.attr(e.n, t.Name)
		if !enough || !found {
			return false, enough
		}
		if t.Op != assert.AttrExists && !s.spend(int64(len(v))) {
			return false, false
		}
		if !attrHolds(t, v) {
			return false, true
		}
	}
	return true, true
}

// attr finds n's attribute key (without ASCII case, never a namespaced
// one), charging the attributes scanned.
func (s *selection) attr(n *html.Node, key string) (value string, found, enough bool) {
	if !s.spend(int64(len(n.Attr))) {
		return "", false, false
	}
	for _, a := range n.Attr {
		if a.Namespace == "" && asciiEqualFold(a.Key, key) {
			return a.Val, true, true
		}
	}
	return "", false, true
}

// asciiEqualFold reports whether a and b are equal ignoring ASCII case,
// as HTML compares element and attribute names.
func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// readClasses splits e's class attribute once, charging its bytes.
func (s *selection) readClasses(e *element) bool {
	if e.classesRead {
		return true
	}
	e.classesRead = true
	v, found, enough := s.attr(e.n, "class")
	if !enough || !s.spend(int64(len(v))) {
		return false
	}
	if found {
		e.classes = strings.FieldsFunc(v, isHTMLSpace)
		if len(e.classes) > 8 {
			e.classSet = make(map[string]bool, len(e.classes))
			for _, c := range e.classes {
				e.classSet[c] = true
			}
		}
	}
	return true
}

func (e *element) hasClass(class string) bool {
	if e.classSet != nil {
		return e.classSet[class]
	}
	for _, c := range e.classes {
		if c == class {
			return true
		}
	}
	return false
}

// isHTMLSpace is the ASCII whitespace that separates an attribute's words.
func isHTMLSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\f' || r == '\r'
}

// attrHolds tests an attribute's value; an empty value never matches the
// word, prefix, suffix or substring operators, as in CSS.
func attrHolds(t assert.AttrTest, v string) bool {
	switch t.Op {
	case assert.AttrExists:
		return true
	case assert.AttrEquals:
		return v == t.Value
	}
	if t.Value == "" {
		return false
	}
	switch t.Op {
	case assert.AttrPrefix:
		return strings.HasPrefix(v, t.Value)
	case assert.AttrSuffix:
		return strings.HasSuffix(v, t.Value)
	case assert.AttrContains:
		return strings.Contains(v, t.Value)
	case assert.AttrWord:
		if strings.ContainsFunc(t.Value, isHTMLSpace) {
			return false
		}
		for _, w := range strings.FieldsFunc(v, isHTMLSpace) {
			if w == t.Value {
				return true
			}
		}
	}
	return false
}
