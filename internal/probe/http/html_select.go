package http

import (
	"context"
	"strings"

	"github.com/noixdev/pulsix/internal/probe/assert"
	"golang.org/x/net/html"
)

// selectAll is the elements under doc that sel selects, in document
// order. It is one pre-order walk of the page that keeps, per
// comma-separated part, the set of states reached on the way down (state
// i: compounds 0..i-1 matched by ancestors in the order the part needs);
// a descendant combinator hands a state to the whole subtree, > only to
// the children. So the work is linear in nodes x compounds x simple
// selectors, with no backtracking.
//
// Every step is charged before it is taken — one per node, per child
// counted and per condition tested, plus the attribute bytes read and
// compared — to the assertion's budget (at most perSelector) and to *left,
// the check's; past either the walk stops with errHTMLTooMuch. It looks at
// ctx before every node and stops with errOutOfTime once it has ended.
func selectAll(ctx context.Context, doc *html.Node, sel *assert.Selector, perSelector int64, left *int64) ([]*html.Node, string) {
	s := selection{sel: sel, budget: min(perSelector, *left), left: left}
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
			e := element{n: v.n, index: v.index, count: v.count}
			matched, newDesc, newChild, enough := s.states(&e, v)
			if !enough {
				return nil, errHTMLTooMuch
			}
			if matched {
				matches = append(matches, v.n)
			}
			desc, child = orStates(v.desc, newDesc), newChild
		}
		var ok bool
		if stack, ok = s.pushChildren(stack, v.n, desc, child); !ok {
			return nil, errHTMLTooMuch
		}
	}
	return matches, ""
}

// visit is a node to match: its place among its element siblings
// (1-based index, count) and the part states it inherits — desc from any
// ancestor, child from its parent alone; nil is none.
type visit struct {
	n            *html.Node
	index, count int
	desc, child  []uint16
}

// element is a node being matched, with its class list read once.
type element struct {
	n            *html.Node
	index, count int
	classesRead  bool
	classes      []string
	classSet     map[string]bool
}

// selection is one walk's selector and budgets.
type selection struct {
	sel    *assert.Selector
	budget int64
	left   *int64
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
// matched; newDesc and newChild the states it hands down.
func (s *selection) states(e *element, v visit) (matched bool, newDesc, newChild []uint16, enough bool) {
	parts := s.sel.Parts
	for p, part := range parts {
		avail := uint16(1)
		if v.desc != nil {
			avail |= v.desc[p]
		}
		if v.child != nil {
			avail |= v.child[p]
		}
		for i, c := range part.Compounds {
			if avail&(1<<i) == 0 {
				continue
			}
			ok, enough := s.compound(e, c)
			switch {
			case !enough:
				return false, nil, nil, false
			case !ok:
				continue
			case i+1 == len(part.Compounds):
				matched = true
			case part.Child[i+1]:
				if newChild == nil {
					newChild = make([]uint16, len(parts))
				}
				newChild[p] |= 1 << (i + 1)
			default:
				if newDesc == nil {
					newDesc = make([]uint16, len(parts))
				}
				newDesc[p] |= 1 << (i + 1)
			}
		}
	}
	return matched, newDesc, newChild, true
}

// orStates is a | b, sharing a when b adds nothing.
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
// first, with their element index and count.
func (s *selection) pushChildren(stack []visit, n *html.Node, desc, child []uint16) ([]visit, bool) {
	children, elements := 0, 0
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		children++
		if c.Type == html.ElementNode {
			elements++
		}
	}
	if !s.spend(int64(children)) {
		return stack, false
	}
	index := elements
	for c := n.LastChild; c != nil; c = c.PrevSibling {
		stack = append(stack, visit{n: c, index: index, count: elements, desc: desc, child: child})
		if c.Type == html.ElementNode {
			index--
		}
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
	if c.Tag != "" && e.n.Data != c.Tag {
		return false, true
	}
	for _, p := range c.Pseudos {
		if !pseudoHolds(p, e.index, e.count) {
			return false, true
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

// attr finds n's attribute key, charging the attributes scanned.
func (s *selection) attr(n *html.Node, key string) (value string, found, enough bool) {
	if !s.spend(int64(len(n.Attr))) {
		return "", false, false
	}
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true, true
		}
	}
	return "", false, true
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

// pseudoHolds tests a pseudo-class on the element at index of count
// element siblings.
func pseudoHolds(p assert.Pseudo, index, count int) bool {
	switch p.Kind {
	case assert.PseudoFirst:
		return index == 1
	case assert.PseudoLast:
		return index == count
	case assert.PseudoNth:
		return index == p.N
	case assert.PseudoOdd:
		return index%2 == 1
	case assert.PseudoEven:
		return index%2 == 0
	}
	return false
}
