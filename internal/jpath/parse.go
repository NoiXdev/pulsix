// Package jpath parses and evaluates the JSONPath subset of the http
// check's json_values (backlog 3.6h): RFC 9535 syntax and semantics for
// what it accepts, restricted so that a path over a document the target
// writes cannot multiply its work.
//
// Accepted: `$`, then segments `.name`, `.*`, `['name']`, `[0]`, `[-1]`,
// `[*]` and `[?filter]`, exactly one selector per bracket, and at most
// one descendant segment (`..name`, `..*`, `..[selector]`) in the whole
// path. A filter is comparisons `@path op literal` and existence tests
// `@path`, joined by `&&`, `||`, `!` and parentheses: at most 8
// comparisons and tests and 32 expression nodes in all. An `@path` holds
// names and indexes only. Not accepted: unions, slices, `$` inside a
// filter, functions, nested filters. internal/triggers keeps the full
// RFC 9535 library for its own, operator-written documents.
//
// Cost: no segment reaches a node twice, so each segment's work is at
// most linear in the document, and a path's work is at most the document
// times its segments; Select's node-visit budget caps it, every unit of
// work charged to it or bounded next to a charged one. A document number
// longer than MaxNumberLen is not compared, so a comparison costs a fixed
// amount. Memory: a node list holds at most budget entries (16 bytes
// each); with slice growth and the previous segment's list, the peak above
// the decoded document is about 4 × 16 bytes × budget (about 128 MiB for a
// budget of 2 000 000, typically far less).
package jpath

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// maxTerms bounds the comparisons and existence tests of one filter.
	maxTerms = 8
	// maxFilterNodes bounds a filter's whole expression: comparisons,
	// existence tests, &&, ||, ! and parentheses each count, so no shape
	// of filter does more than a fixed amount of work per candidate.
	maxFilterNodes = 32
	// maxIndex is RFC 9535's I-JSON integer bound.
	maxIndex = 1<<53 - 1
)

// Path is a parsed path; Select evaluates it.
type Path struct {
	segs []segment
}

type segment struct {
	descendant bool
	sel        selector
}

type selKind int

const (
	selName selKind = iota
	selWildcard
	selIndex
	selFilter
)

type selector struct {
	kind   selKind
	name   string
	index  int64
	filter expr
}

// step is one segment of an @ path: a name, or an index when !isName.
type step struct {
	isName bool
	name   string
	index  int64
}

type litKind int

const (
	litNumber litKind = iota
	litString
	litBool
	litNull
)

type literal struct {
	kind litKind
	num  float64
	str  string
	b    bool
}

type expr interface {
	eval(e *evaluator, node any) bool
}

type (
	orExpr    []expr
	andExpr   []expr
	notExpr   struct{ x expr }
	existExpr struct{ q []step }
	cmpExpr   struct {
		q   []step
		op  string
		lit literal
	}
)

type parser struct {
	s     string
	i     int
	terms int
	nodes int
}

// Parse parses path. Its error says where and why, quoting nothing but
// the operator's own path.
func Parse(path string) (*Path, error) {
	if !utf8.ValidString(path) {
		return nil, errors.New("the path is not valid UTF-8")
	}
	p := &parser{s: path}
	if !p.eat("$") {
		return nil, p.errf("a path starts with $")
	}
	out := &Path{}
	descendants := 0
	for {
		save := p.i
		p.blank()
		if p.done() {
			if p.i != save {
				p.i = save
				return nil, p.errf("blank space after the path")
			}
			return out, nil
		}
		start := p.i
		seg, err := p.segment()
		if err != nil {
			return nil, err
		}
		if seg.descendant {
			descendants++
			if descendants > 1 {
				p.i = start
				return nil, p.errf("at most one descendant segment (..)")
			}
		}
		out.segs = append(out.segs, seg)
	}
}

func (p *parser) errf(format string, a ...any) error {
	return fmt.Errorf("at byte %d: %s", p.i, fmt.Sprintf(format, a...))
}

func (p *parser) done() bool { return p.i >= len(p.s) }

func (p *parser) peek(s string) bool { return strings.HasPrefix(p.s[p.i:], s) }

func (p *parser) eat(s string) bool {
	if p.peek(s) {
		p.i += len(s)
		return true
	}
	return false
}

// blank skips RFC 9535 blank space.
func (p *parser) blank() {
	for !p.done() {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *parser) segment() (segment, error) {
	switch {
	case p.eat(".."):
		switch {
		case p.peek("["):
			sel, err := p.bracket()
			return segment{descendant: true, sel: sel}, err
		case p.eat("*"):
			return segment{descendant: true, sel: selector{kind: selWildcard}}, nil
		}
		name, err := p.shorthand()
		return segment{descendant: true, sel: selector{kind: selName, name: name}}, err
	case p.eat("."):
		if p.eat("*") {
			return segment{sel: selector{kind: selWildcard}}, nil
		}
		name, err := p.shorthand()
		return segment{sel: selector{kind: selName, name: name}}, err
	case p.peek("["):
		sel, err := p.bracket()
		return segment{sel: sel}, err
	}
	return segment{}, p.errf("expected ., .. or [")
}

// bracket reads "[" selector "]", one selector only.
func (p *parser) bracket() (selector, error) {
	p.i++ // [
	p.blank()
	sel, err := p.selector()
	if err != nil {
		return sel, err
	}
	p.blank()
	switch {
	case p.peek(","):
		return sel, p.errf("a bracket holds one selector")
	case p.peek(":"):
		return sel, p.errf("slices are not supported")
	case !p.eat("]"):
		return sel, p.errf("expected ]")
	}
	return sel, nil
}

func (p *parser) selector() (selector, error) {
	if p.done() {
		return selector{}, p.errf("expected a selector")
	}
	switch c := p.s[p.i]; {
	case c == '\'' || c == '"':
		name, err := p.str()
		return selector{kind: selName, name: name}, err
	case c == '*':
		p.i++
		return selector{kind: selWildcard}, nil
	case c == '?':
		p.i++
		p.blank()
		p.terms, p.nodes = 0, 0
		f, err := p.or()
		return selector{kind: selFilter, filter: f}, err
	case c == '-' || isDigit(c):
		n, err := p.integer()
		return selector{kind: selIndex, index: n}, err
	case c == ':':
		return selector{}, p.errf("slices are not supported")
	}
	return selector{}, p.errf("expected a name, an index, * or a filter")
}

// shorthand reads a member-name-shorthand.
func (p *parser) shorthand() (string, error) {
	start := p.i
	for !p.done() {
		r, n := utf8.DecodeRuneInString(p.s[p.i:])
		digit := p.i > start && r < utf8.RuneSelf && isDigit(byte(r))
		if !nameFirst(r) && !digit {
			break
		}
		p.i += n
	}
	if p.i == start {
		return "", p.errf("expected a name")
	}
	return p.s[start:p.i], nil
}

func nameFirst(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' || r >= 0x80
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// integer reads an RFC 9535 int: 0, or an optional minus and digits
// without a leading zero, within the I-JSON range.
func (p *parser) integer() (int64, error) {
	start := p.i
	p.eat("-")
	if p.done() || !isDigit(p.s[p.i]) {
		return 0, p.errf("expected a digit")
	}
	if p.s[p.i] == '0' {
		p.i++
		if p.i-start > 1 {
			return 0, p.errf("-0 is not an index")
		}
		if !p.done() && isDigit(p.s[p.i]) {
			return 0, p.errf("an index has no leading zero")
		}
		return 0, nil
	}
	for !p.done() && isDigit(p.s[p.i]) {
		p.i++
	}
	n, err := strconv.ParseInt(p.s[start:p.i], 10, 64)
	if err != nil || n > maxIndex || n < -maxIndex {
		return 0, p.errf("the index is out of range")
	}
	return n, nil
}

// str reads a string literal in single or double quotes with RFC 9535's
// escapes.
func (p *parser) str() (string, error) {
	q := p.s[p.i]
	p.i++
	var b strings.Builder
	for {
		if p.done() {
			return "", p.errf("the string is not closed")
		}
		c := p.s[p.i]
		switch {
		case c == q:
			p.i++
			return b.String(), nil
		case c < 0x20:
			return "", p.errf("a control character in a string must be escaped")
		case c != '\\':
			b.WriteByte(c)
			p.i++
			continue
		}
		p.i++ // backslash
		if p.done() {
			return "", p.errf("the string is not closed")
		}
		e := p.s[p.i]
		p.i++
		switch e {
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '/', '\\':
			b.WriteByte(e)
		case '\'', '"':
			if e != q {
				return "", p.errf("invalid escape")
			}
			b.WriteByte(e)
		case 'u':
			r, err := p.unicodeEscape()
			if err != nil {
				return "", err
			}
			b.WriteRune(r)
		default:
			return "", p.errf("invalid escape")
		}
	}
}

// unicodeEscape reads the hex digits after \u, and a low surrogate's
// escape after a high one.
func (p *parser) unicodeEscape() (rune, error) {
	r, ok := p.hex4()
	switch {
	case !ok:
		return 0, p.errf("\\u needs four hex digits")
	case r >= 0xDC00 && r <= 0xDFFF:
		return 0, p.errf("a low surrogate without a high one")
	case r < 0xD800 || r > 0xDBFF:
		return r, nil
	}
	if !p.eat(`\u`) {
		return 0, p.errf("a high surrogate without a low one")
	}
	lo, ok := p.hex4()
	if !ok || lo < 0xDC00 || lo > 0xDFFF {
		return 0, p.errf("a high surrogate without a low one")
	}
	return 0x10000 + (r-0xD800)<<10 + (lo - 0xDC00), nil
}

func (p *parser) hex4() (rune, bool) {
	if len(p.s)-p.i < 4 {
		return 0, false
	}
	var r rune
	for _, c := range []byte(p.s[p.i : p.i+4]) {
		switch {
		case c >= '0' && c <= '9':
			r = r<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	p.i += 4
	return r, true
}

// or reads logical-or-expr.
func (p *parser) or() (expr, error) {
	first, err := p.and()
	if err != nil {
		return nil, err
	}
	list := orExpr{first}
	for {
		save := p.i
		p.blank()
		if !p.eat("||") {
			p.i = save
			break
		}
		if err := p.node(); err != nil {
			return nil, err
		}
		p.blank()
		next, err := p.and()
		if err != nil {
			return nil, err
		}
		list = append(list, next)
	}
	if len(list) == 1 {
		return first, nil
	}
	return list, nil
}

// and reads logical-and-expr.
func (p *parser) and() (expr, error) {
	first, err := p.basic()
	if err != nil {
		return nil, err
	}
	list := andExpr{first}
	for {
		save := p.i
		p.blank()
		if !p.eat("&&") {
			p.i = save
			break
		}
		if err := p.node(); err != nil {
			return nil, err
		}
		p.blank()
		next, err := p.basic()
		if err != nil {
			return nil, err
		}
		list = append(list, next)
	}
	if len(list) == 1 {
		return first, nil
	}
	return list, nil
}

// basic reads a parenthesised expression, a comparison or an existence
// test, the last and the first optionally negated.
func (p *parser) basic() (expr, error) {
	if p.eat("!") {
		if err := p.node(); err != nil {
			return nil, err
		}
		p.blank()
		switch {
		case p.peek("("):
			x, err := p.paren()
			return notExpr{x}, err
		case p.peek("@"):
			q, _, err := p.relQuery()
			if err != nil {
				return nil, err
			}
			if err := p.term(); err != nil {
				return nil, err
			}
			return notExpr{existExpr{q}}, nil
		}
		return nil, p.errf("! applies to ( or @ only")
	}
	if p.peek("(") {
		return p.paren()
	}
	if !p.peek("@") {
		return nil, p.notAFilterTerm()
	}
	q, spaced, err := p.relQuery()
	if err != nil {
		return nil, err
	}
	if err := p.term(); err != nil {
		return nil, err
	}
	save := p.i
	p.blank()
	op := p.cmpOp()
	if op == "" {
		p.i = save
		return existExpr{q}, nil
	}
	if spaced {
		// RFC 9535's singular query, which a comparison takes, has no
		// blank space inside its brackets; an existence test may.
		return nil, p.errf("a compared @ path has no blank space inside its brackets")
	}
	p.blank()
	lit, err := p.literal()
	if err != nil {
		return nil, err
	}
	return cmpExpr{q: q, op: op, lit: lit}, nil
}

// notAFilterTerm explains what the subset refuses where a filter term
// should start.
func (p *parser) notAFilterTerm() error {
	switch {
	case p.peek("$"):
		return p.errf("$ is not supported inside a filter")
	case !p.done() && nameFirst(rune(p.s[p.i])):
		if p.peek("true") || p.peek("false") || p.peek("null") {
			return p.errf("a comparison starts with an @ path")
		}
		return p.errf("functions are not supported")
	}
	return p.errf("a comparison starts with an @ path")
}

func (p *parser) term() error {
	p.terms++
	if p.terms > maxTerms {
		return p.errf("a filter holds at most %d comparisons", maxTerms)
	}
	return p.node()
}

// node counts one node of the filter's expression.
func (p *parser) node() error {
	p.nodes++
	if p.nodes > maxFilterNodes {
		return p.errf("a filter holds at most %d operators, parentheses and comparisons", maxFilterNodes)
	}
	return nil
}

func (p *parser) paren() (expr, error) {
	p.i++ // (
	if err := p.node(); err != nil {
		return nil, err
	}
	p.blank()
	x, err := p.or()
	if err != nil {
		return nil, err
	}
	p.blank()
	if !p.eat(")") {
		return nil, p.errf("expected )")
	}
	return x, nil
}

// relQuery reads "@" and its name and index segments, and reports
// whether a bracket held blank space.
func (p *parser) relQuery() ([]step, bool, error) {
	p.i++ // @
	var q []step
	spaced := false
	for {
		save := p.i
		p.blank()
		switch {
		case p.peek(".."):
			return nil, false, p.errf("a descendant segment is not supported inside a filter")
		case p.eat("."):
			if p.peek("*") {
				return nil, false, p.errf("a wildcard is not supported inside a filter")
			}
			name, err := p.shorthand()
			if err != nil {
				return nil, false, err
			}
			q = append(q, step{isName: true, name: name})
		case p.eat("["):
			s, blank, err := p.relBracket()
			if err != nil {
				return nil, false, err
			}
			spaced = spaced || blank
			q = append(q, s)
		default:
			p.i = save
			return q, spaced, nil
		}
	}
}

// relBracket reads one name or index and "]", and reports whether blank
// space surrounded it (RFC 9535 allows it in an existence test only).
func (p *parser) relBracket() (step, bool, error) {
	start := p.i
	p.blank()
	spaced := p.i != start
	if p.done() {
		return step{}, spaced, p.errf("expected a name or an index")
	}
	var s step
	switch c := p.s[p.i]; {
	case c == '\'' || c == '"':
		name, err := p.str()
		if err != nil {
			return s, spaced, err
		}
		s = step{isName: true, name: name}
	case c == '-' || isDigit(c):
		n, err := p.integer()
		if err != nil {
			return s, spaced, err
		}
		s = step{index: n}
	case c == '?':
		return s, spaced, p.errf("a filter inside a filter is not supported")
	case c == '*':
		return s, spaced, p.errf("a wildcard is not supported inside a filter")
	default:
		return s, spaced, p.errf("expected a name or an index")
	}
	before := p.i
	p.blank()
	spaced = spaced || p.i != before
	switch {
	case p.peek(","):
		return s, spaced, p.errf("a bracket holds one selector")
	case p.peek(":"):
		return s, spaced, p.errf("slices are not supported")
	case !p.eat("]"):
		return s, spaced, p.errf("expected ]")
	}
	return s, spaced, nil
}

func (p *parser) cmpOp() string {
	for _, op := range []string{"==", "!=", "<=", ">=", "<", ">"} {
		if p.eat(op) {
			return op
		}
	}
	return ""
}

// literal reads a number, a string, true, false or null.
func (p *parser) literal() (literal, error) {
	switch {
	case p.done():
		return literal{}, p.errf("expected a literal")
	case p.s[p.i] == '\'' || p.s[p.i] == '"':
		s, err := p.str()
		return literal{kind: litString, str: s}, err
	case p.eat("true"):
		return literal{kind: litBool, b: true}, nil
	case p.eat("false"):
		return literal{kind: litBool}, nil
	case p.eat("null"):
		return literal{kind: litNull}, nil
	case p.s[p.i] == '-' || isDigit(p.s[p.i]):
		return p.number()
	case p.peek("@"):
		return literal{}, p.errf("a comparison has a literal on its right")
	case p.peek("$"):
		return literal{}, p.errf("$ is not supported inside a filter")
	}
	return literal{}, p.errf("expected a literal")
}

// number reads (int / "-0") [frac] [exp].
func (p *parser) number() (literal, error) {
	start := p.i
	p.eat("-")
	switch {
	case p.done() || !isDigit(p.s[p.i]):
		return literal{}, p.errf("expected a digit")
	case p.s[p.i] == '0':
		p.i++
		if !p.done() && isDigit(p.s[p.i]) {
			return literal{}, p.errf("a number has no leading zero")
		}
	default:
		p.digits()
	}
	if p.eat(".") {
		if p.digits() == 0 {
			return literal{}, p.errf("expected a digit")
		}
	}
	if p.eat("e") || p.eat("E") {
		if !p.eat("-") {
			p.eat("+")
		}
		if p.digits() == 0 {
			return literal{}, p.errf("expected a digit")
		}
	}
	// A syntactically valid number out of float64's range is ±Inf, which
	// still orders against every finite number.
	f, _ := strconv.ParseFloat(p.s[start:p.i], 64)
	return literal{kind: litNumber, num: f}, nil
}

func (p *parser) digits() int {
	n := 0
	for !p.done() && isDigit(p.s[p.i]) {
		p.i++
		n++
	}
	return n
}
