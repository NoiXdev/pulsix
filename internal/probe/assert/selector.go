package assert

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The CSS selectors of html(…) are a small subset Pulsix parses and
// matches itself (internal/probe/http), so a match is one walk of the
// page that can stop at any node:
//
//	group     = part *( "," part )
//	part      = compound *( combinator compound )      ; at most 8 compounds
//	combinator = whitespace (descendant) | ">" (child)
//	compound  = [ type | "*" ] *simple                  ; at most 16 selectors
//	simple    = "#" ident | "." ident | attribute | pseudo
//	attribute = "[" name [ ( "=" | "~=" | "^=" | "$=" | "*=" ) value ] "]"
//	pseudo    = ":first-child" | ":last-child" | ":nth-child(" ( N | "odd" | "even" ) ")"
//
// Type and attribute names are compared in lower case (the HTML parser
// lowers them), ids, classes and attribute values exactly. A value is an
// identifier or a quoted string; nothing is escaped. Everything else — other
// combinators, pseudo-classes, pseudo-elements, escapes, comments — is
// refused, naming what is not supported.
const (
	maxCompounds = 8
	maxSimple    = 16
	// maxNth bounds :nth-child(N).
	maxNth = 1_000_000
)

// Selector is a parsed html(…) selector: an element is selected when any
// of its comma-separated parts matches it.
type Selector struct {
	Parts []Part
}

// Part is one comma-separated part: Compounds from the outermost to the
// selected element; Child[i] (i >= 1) says Compounds[i] must be a child
// (>) of the element Compounds[i-1] matched rather than any descendant.
type Part struct {
	Compounds []Compound
	Child     []bool
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

// PseudoKind is a supported pseudo-class.
type PseudoKind int

// The pseudo-classes; PseudoNth carries N.
const (
	PseudoFirst PseudoKind = iota
	PseudoLast
	PseudoNth
	PseudoOdd
	PseudoEven
)

// Pseudo is one pseudo-class of a compound.
type Pseudo struct {
	Kind PseudoKind
	N    int
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
	child := false
	for {
		c, err := p.compound()
		if err != nil {
			return Part{}, err
		}
		part.Compounds = append(part.Compounds, c)
		part.Child = append(part.Child, child)
		if len(part.Compounds) > maxCompounds {
			return Part{}, fmt.Errorf("a CSS selector of html(…) may have at most %d compound selectors in each comma-separated part", maxCompounds)
		}
		spaced := p.ws()
		switch {
		case p.done() || p.peek() == ',':
			return part, nil
		case p.peek() == '>':
			p.i++
			p.ws()
			child = true
		case spaced:
			child = false
		default:
			return Part{}, p.unexpected()
		}
	}
}

// compound reads a type or * and the simple selectors after it.
func (p *selParser) compound() (Compound, error) {
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
		switch p.peek() {
		case '#':
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
			ps, err = p.pseudo()
			c.Pseudos = append(c.Pseudos, ps)
		default:
			if n == 0 {
				return Compound{}, p.unexpected()
			}
			return c, nil
		}
		if err != nil {
			return Compound{}, err
		}
		if n++; n > maxSimple {
			return Compound{}, fmt.Errorf("a CSS selector of html(…) may have at most %d simple selectors in one compound", maxSimple)
		}
	}
	if n == 0 {
		return Compound{}, errors.New("a CSS selector of html(…) ends where a selector was expected")
	}
	return c, nil
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

// pseudo reads one of the supported pseudo-classes.
func (p *selParser) pseudo() (Pseudo, error) {
	p.i++
	if !p.done() && p.peek() == ':' {
		return Pseudo{}, errors.New("a CSS selector of html(…) may not use pseudo-elements (::)")
	}
	name := strings.ToLower(p.ident())
	switch name {
	case "first-child":
		return Pseudo{Kind: PseudoFirst}, nil
	case "last-child":
		return Pseudo{Kind: PseudoLast}, nil
	case "nth-child":
	default:
		return Pseudo{}, fmt.Errorf("a CSS selector of html(…) may not use :%s (only :first-child, :last-child and :nth-child(N), (odd) or (even) are supported)", name)
	}
	if p.done() || p.peek() != '(' {
		return Pseudo{}, errors.New("a CSS selector of html(…) needs :nth-child(N), (odd) or (even)")
	}
	p.i++
	p.ws()
	start := p.i
	for !p.done() && p.peek() != ')' && strings.IndexByte(" \t\n\r\f", p.peek()) < 0 {
		p.i++
	}
	arg := strings.ToLower(p.s[start:p.i])
	p.ws()
	if p.done() || p.peek() != ')' {
		return Pseudo{}, errors.New("a CSS selector of html(…) takes a whole number, odd or even in :nth-child(…)")
	}
	p.i++
	switch arg {
	case "odd":
		return Pseudo{Kind: PseudoOdd}, nil
	case "even":
		return Pseudo{Kind: PseudoEven}, nil
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 || n > maxNth || arg[0] < '0' || arg[0] > '9' {
		return Pseudo{}, fmt.Errorf("a CSS selector of html(…) takes a whole number from 1 to %d, odd or even in :nth-child(…), not %q", maxNth, arg)
	}
	return Pseudo{Kind: PseudoNth, N: n}, nil
}

// unexpected names what stops the parse at i.
func (p *selParser) unexpected() error {
	if p.done() {
		return errors.New("a CSS selector of html(…) ends where a selector was expected")
	}
	switch c := p.peek(); {
	case c == '~' || c == '+':
		return fmt.Errorf("a CSS selector of html(…) may not use the combinator %c (only a space and > are supported)", c)
	case c == '\\':
		return errors.New("a CSS selector of html(…) may not use escapes (\\)")
	case strings.HasPrefix(p.s[p.i:], "/*"):
		return errors.New("a CSS selector of html(…) may not contain comments")
	default:
		r, _ := utf8.DecodeRuneInString(p.s[p.i:])
		return fmt.Errorf("a CSS selector of html(…) cannot use %q here", r)
	}
}
