package assert

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// The CSS selectors of html(…) are a small subset Pulsix parses and
// matches itself (internal/probe/http), so a match is one walk of the
// page that can stop at any node:
//
//	group      = part *( "," part )
//	part       = compound *( combinator compound )      ; at most 8 compounds
//	combinator = whitespace (descendant) | ">" (child)
//	           | "+" (next sibling) | "~" (any later sibling)
//	compound   = [ type | "*" ] *simple                  ; at most 16 selectors
//	simple     = "#" ident | "." ident | attribute | pseudo
//	attribute  = "[" name [ ( "=" | "~=" | "^=" | "$=" | "*=" ) value ] "]"
//	pseudo     = ":first-child" | ":last-child" | ":only-child"
//	           | ":first-of-type" | ":last-of-type" | ":only-of-type"
//	           | ":empty" | ":root"
//	           | ( ":nth-child(" | ":nth-last-child(" | ":nth-of-type("
//	             | ":nth-last-of-type(" ) nth ")"
//	           | ":not(" compound *( "," compound ) ")"  ; no :not inside
//	nth        = "odd" | "even" | An+B                  ; |A|, |B| <= 1000000
//
// A :not counts as one simple selector of its compound, and so does
// every simple selector inside it (backlog 3.90).
//
// Type and attribute names are compared without ASCII case (the HTML
// parser lowers them, but keeps SVG's linearGradient and viewBox), ids,
// classes and attribute values exactly; namespaced attributes (SVG's
// xlink:href) are never matched. A value is an
// identifier or a quoted string; nothing is escaped. Everything else —
// other pseudo-classes (:has, :is, user-action and form states),
// pseudo-elements, escapes, comments — is refused, naming what is not
// supported.
const (
	maxCompounds = 8
	maxSimple    = 16
	// maxNth bounds A and B of an nth pseudo-class's An+B.
	maxNth = 1_000_000
)

// Selector is a parsed html(…) selector: an element is selected when any
// of its comma-separated parts matches it.
type Selector struct {
	Parts []Part
}

// OfType reports whether a compound of s, or one inside a :not, uses an
// of-type pseudo-class, which needs each element's place among its
// siblings of the same name.
func (s *Selector) OfType() bool {
	for _, part := range s.Parts {
		for _, c := range part.Compounds {
			if c.ofType() {
				return true
			}
		}
	}
	return false
}

// Combinator is how a compound relates to the element the compound
// before it matched.
type Combinator uint8

// The combinators.
const (
	// Descendant (whitespace): any descendant.
	Descendant Combinator = iota
	// Child (>): a child.
	Child
	// Adjacent (+): the next element sibling.
	Adjacent
	// Sibling (~): any later element sibling.
	Sibling
)

// Part is one comma-separated part: Compounds from the first written to
// the selected element; Combinators[i] (i >= 1) is how Compounds[i]
// relates to the element Compounds[i-1] matched. Combinators[0] is
// Descendant: the first compound may match anywhere.
type Part struct {
	Compounds   []Compound
	Combinators []Combinator
}

// Compound is what one element must be: all of its conditions hold.
// Tag "" is any element.
type Compound struct {
	Tag     string
	ID      string
	Classes []string
	Attrs   []AttrTest
	Pseudos []Pseudo
}

func (c Compound) ofType() bool {
	for _, ps := range c.Pseudos {
		switch ps.Kind {
		case PseudoFirstOfType, PseudoLastOfType, PseudoOnlyOfType, PseudoNthOfType, PseudoNthLastOfType:
			return true
		case PseudoNot:
			for _, n := range ps.Not {
				if n.ofType() {
					return true
				}
			}
		}
	}
	return false
}

// AttrOp is an attribute test's operator; AttrExists has no value.
type AttrOp string

// The attribute operators.
const (
	AttrExists   AttrOp = ""
	AttrEquals   AttrOp = "="
	AttrWord     AttrOp = "~="
	AttrPrefix   AttrOp = "^="
	AttrSuffix   AttrOp = "$="
	AttrContains AttrOp = "*="
)

// AttrTest is [Name Op "Value"].
type AttrTest struct {
	Name  string
	Op    AttrOp
	Value string
}

// ParseSelector parses an html(…) selector in the supported subset.
func ParseSelector(s string) (*Selector, error) {
	p := &selParser{s: s}
	sel := &Selector{}
	for {
		p.ws()
		part, err := p.part()
		if err != nil {
			return nil, err
		}
		sel.Parts = append(sel.Parts, part)
		p.ws()
		if p.done() {
			return sel, nil
		}
		if p.peek() != ',' {
			return nil, p.unexpected()
		}
		p.i++
	}
}

type selParser struct {
	s string
	i int
}

func (p *selParser) done() bool { return p.i >= len(p.s) }
func (p *selParser) peek() byte { return p.s[p.i] }

// ws skips whitespace and reports whether there was any.
func (p *selParser) ws() bool {
	start := p.i
	for !p.done() && strings.IndexByte(" \t\n\r\f", p.peek()) >= 0 {
		p.i++
	}
	return p.i > start
}

func (p *selParser) part() (Part, error) {
	var part Part
	comb := Descendant
	for {
		c, err := p.compound(false)
		if err != nil {
			return Part{}, err
		}
		part.Compounds = append(part.Compounds, c)
		part.Combinators = append(part.Combinators, comb)
		if len(part.Compounds) > maxCompounds {
			return Part{}, fmt.Errorf("a CSS selector of html(…) may have at most %d compound selectors in each comma-separated part", maxCompounds)
		}
		spaced := p.ws()
		if p.done() || p.peek() == ',' {
			return part, nil
		}
		if next, ok := combinators[p.peek()]; ok {
			p.i++
			p.ws()
			comb = next
			continue
		}
		if !spaced {
			return Part{}, p.unexpected()
		}
		comb = Descendant
	}
}

// combinators are the combinators written with a character.
var combinators = map[byte]Combinator{'>': Child, '+': Adjacent, '~': Sibling}

// compound reads a type or * and the simple selectors after it. Inside a
// :not (inNot) it may not hold another :not.
func (p *selParser) compound(inNot bool) (Compound, error) {
	c, _, err := p.weighedCompound(inNot)
	return c, err
}

// weighedCompound is compound with the number of simple selectors it
// holds, a :not's own counted with it.
func (p *selParser) weighedCompound(inNot bool) (Compound, int, error) {
	var c Compound
	n := 0
	if !p.done() && p.peek() == '*' {
		p.i++
		n++
	} else if name := p.ident(); name != "" {
		c.Tag = strings.ToLower(name)
		n++
	}
	for !p.done() {
		var err error
		weight := 1
		switch p.peek() {
		case '#':
			if c.ID != "" {
				return Compound{}, 0, errors.New("a CSS selector of html(…) may name one #id per compound")
			}
			p.i++
			c.ID, err = p.needIdent("#")
		case '.':
			p.i++
			var class string
			class, err = p.needIdent(".")
			c.Classes = append(c.Classes, class)
		case '[':
			var at AttrTest
			at, err = p.attribute()
			c.Attrs = append(c.Attrs, at)
		case ':':
			var ps Pseudo
			ps, weight, err = p.pseudo(inNot)
			c.Pseudos = append(c.Pseudos, ps)
		default:
			if n == 0 {
				return Compound{}, 0, p.unexpected()
			}
			return c, n, nil
		}
		if err != nil {
			return Compound{}, 0, err
		}
		if n += weight; n > maxSimple {
			return Compound{}, 0, fmt.Errorf("a CSS selector of html(…) may have at most %d simple selectors in one compound", maxSimple)
		}
	}
	if n == 0 {
		return Compound{}, 0, errors.New("a CSS selector of html(…) ends where a selector was expected")
	}
	return c, n, nil
}

func isIdentByte(c byte, first bool) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_', c == '-', c >= utf8.RuneSelf:
		return true
	case c >= '0' && c <= '9':
		return !first
	}
	return false
}

// ident reads an identifier, "" when there is none.
func (p *selParser) ident() string {
	start := p.i
	for !p.done() && isIdentByte(p.peek(), p.i == start) {
		p.i++
	}
	return p.s[start:p.i]
}

func (p *selParser) needIdent(after string) (string, error) {
	if name := p.ident(); name != "" {
		return name, nil
	}
	if !p.done() && p.peek() == '\\' {
		return "", errors.New("a CSS selector of html(…) may not use escapes (\\)")
	}
	return "", fmt.Errorf("a CSS selector of html(…) needs a name after %q", after)
}

// attribute reads [name], [name op value].
func (p *selParser) attribute() (AttrTest, error) {
	p.i++
	p.ws()
	name := p.ident()
	if name == "" {
		return AttrTest{}, errors.New("a CSS selector of html(…) needs an attribute name after [")
	}
	at := AttrTest{Name: strings.ToLower(name)}
	p.ws()
	if !p.done() && p.peek() != ']' {
		op := ""
		for !p.done() && strings.IndexByte("=~^$*|!#", p.peek()) >= 0 {
			op += string(p.peek())
			p.i++
		}
		switch AttrOp(op) {
		case AttrEquals, AttrWord, AttrPrefix, AttrSuffix, AttrContains:
			at.Op = AttrOp(op)
		default:
			return AttrTest{}, fmt.Errorf("a CSS selector of html(…) may not use the attribute operator %q (only =, ~=, ^=, $= and *= are supported)", op)
		}
		p.ws()
		value, err := p.value()
		if err != nil {
			return AttrTest{}, err
		}
		at.Value = value
		p.ws()
	}
	if p.done() || p.peek() != ']' {
		return AttrTest{}, errors.New("a CSS selector of html(…) needs ] to close an attribute test (flags such as i are not supported)")
	}
	p.i++
	return at, nil
}

// value reads an identifier or a quoted string without escapes.
func (p *selParser) value() (string, error) {
	if p.done() {
		return "", errors.New("a CSS selector of html(…) needs a value in an attribute test")
	}
	if q := p.peek(); q == '"' || q == '\'' {
		end := strings.IndexByte(p.s[p.i+1:], q)
		if end < 0 {
			return "", errors.New("a CSS selector of html(…) has a string without its closing quote")
		}
		v := p.s[p.i+1 : p.i+1+end]
		if strings.Contains(v, `\`) {
			return "", errors.New("a CSS selector of html(…) may not use escapes (\\)")
		}
		p.i += end + 2
		return v, nil
	}
	return p.needIdent("the attribute operator")
}

// unexpected names what stops the parse at i.
func (p *selParser) unexpected() error {
	if p.done() {
		return errors.New("a CSS selector of html(…) ends where a selector was expected")
	}
	switch c := p.peek(); {
	case c == '\\':
		return errors.New("a CSS selector of html(…) may not use escapes (\\)")
	case strings.HasPrefix(p.s[p.i:], "/*"):
		return errors.New("a CSS selector of html(…) may not contain comments")
	default:
		r, _ := utf8.DecodeRuneInString(p.s[p.i:])
		return fmt.Errorf("a CSS selector of html(…) cannot use %q here", r)
	}
}
