package rules

import (
	"errors"
	"fmt"
)

// ParseError is what Parse returns for malformed input: a byte offset into
// the source and a message an editor can put under the caret. Parse
// converts a TypeCheck failure into one of these too (see Parse's doc
// comment), so a caller only ever handles this one type to find where a
// rule went wrong.
type ParseError struct {
	Pos int
	Msg string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("parse error at %d: %s", e.Pos, e.Msg)
}

func (e *ParseError) Unwrap() error {
	return ErrInvalidRule
}

// Parse turns expression source into a type-checked tree: a recursive-
// descent parse against the grammar in the design plan, followed by
// TypeCheck. A tree Parse hands back is always one the evaluator can walk
// — Task 3 and Task 4 both rely on that, not just this task's own tests.
//
// A TypeCheck failure surfaces as a *ParseError carrying the same Pos and
// Msg as the *TypeCheckError it wraps, rather than as the TypeCheckError
// itself: a caller asking "where's the caret" never has to unwrap two
// different error types depending on whether the source was syntactically
// or semantically wrong.
func Parse(src string) (Node, error) {
	toks, err := lex(src)
	if err != nil {
		return Node{}, err
	}
	p := &parser{toks: toks}
	n, err := p.parseExpr()
	if err != nil {
		return Node{}, err
	}
	if p.cur().kind != tokEOF {
		return Node{}, p.errorf("unexpected %s", p.cur().describe())
	}
	if err := TypeCheck(n); err != nil {
		var tcErr *TypeCheckError
		if errors.As(err, &tcErr) {
			return Node{}, &ParseError{Pos: tcErr.Pos, Msg: tcErr.Msg}
		}
		return Node{}, err
	}
	return n, nil
}

// parser walks a fully lexed token slice with an index. Tokenizing the
// whole source upfront (rather than lexing on demand) buys the predicate
// production's lookahead — telling a label from a comparison needs to see
// past the identifier without consuming it — for free: peek is just an
// index offset.
type parser struct {
	toks []token
	i    int
}

func (p *parser) cur() token { return p.toks[p.i] }

// peek looks n tokens ahead of cur without consuming anything, clamped to
// the trailing tokEOF so a lookahead past the end of input never panics.
func (p *parser) peek(n int) token {
	idx := p.i + n
	if idx >= len(p.toks) {
		idx = len(p.toks) - 1
	}
	return p.toks[idx]
}

// advance returns the current token and moves past it, except at tokEOF,
// which stays current forever — every caller that reaches end of input
// keeps reporting against the same position instead of indexing out of
// bounds.
func (p *parser) advance() token {
	t := p.toks[p.i]
	if p.i < len(p.toks)-1 {
		p.i++
	}
	return t
}

func (p *parser) errorf(format string, args ...any) error {
	return &ParseError{Pos: p.cur().pos, Msg: fmt.Sprintf(format, args...)}
}

// expect consumes cur() if it has kind k, else reports a parse error
// naming what was expected against what was actually found.
func (p *parser) expect(k tokKind, want string) (token, error) {
	if p.cur().kind != k {
		return token{}, p.errorf("expected %s, got %s", want, p.cur().describe())
	}
	return p.advance(), nil
}

// parseExpr is the grammar's entry point: expr := or.
func (p *parser) parseExpr() (Node, error) {
	return p.parseOr()
}

// parseOr implements: or := and ("or" and)*. A single "and" result passes
// through unwrapped — an expression with no "or" in it is not a KindOr
// node with one argument, it is whatever "and" produced.
func (p *parser) parseOr() (Node, error) {
	first, err := p.parseAnd()
	if err != nil {
		return Node{}, err
	}
	items := []Node{first}
	for p.cur().kind == tokIdent && p.cur().text == "or" {
		p.advance()
		next, err := p.parseAnd()
		if err != nil {
			return Node{}, err
		}
		items = append(items, next)
	}
	if len(items) == 1 {
		return items[0], nil
	}
	return Node{Kind: KindOr, Args: items, Pos: first.Pos}, nil
}

// parseAnd implements: and := unary ("and" unary)*, the same
// single-item-passes-through shape as parseOr.
func (p *parser) parseAnd() (Node, error) {
	first, err := p.parseUnary()
	if err != nil {
		return Node{}, err
	}
	items := []Node{first}
	for p.cur().kind == tokIdent && p.cur().text == "and" {
		p.advance()
		next, err := p.parseUnary()
		if err != nil {
			return Node{}, err
		}
		items = append(items, next)
	}
	if len(items) == 1 {
		return items[0], nil
	}
	return Node{Kind: KindAnd, Args: items, Pos: first.Pos}, nil
}

// parseUnary implements: unary := "not" unary | "(" expr ")" | predicate.
func (p *parser) parseUnary() (Node, error) {
	if p.cur().kind == tokIdent && p.cur().text == "not" {
		pos := p.cur().pos
		p.advance()
		inner, err := p.parseUnary()
		if err != nil {
			return Node{}, err
		}
		return Node{Kind: KindNot, Args: []Node{inner}, Pos: pos}, nil
	}
	if p.cur().kind == tokLParen {
		p.advance()
		inner, err := p.parseExpr()
		if err != nil {
			return Node{}, err
		}
		if _, err := p.expect(tokRParen, "')'"); err != nil {
			return Node{}, err
		}
		return inner, nil
	}
	return p.parsePredicate()
}

// parsePredicate implements: predicate := label | comparison. Both
// productions can start with the same ident, so telling them apart needs
// to look past it without consuming: "in", or "==" / "!=" followed by a
// string literal, commits to label; anything else falls through to
// comparison, which re-reads the identifier itself as an arith operand.
func (p *parser) parsePredicate() (Node, error) {
	if p.cur().kind == tokIdent {
		if p.peek(1).kind == tokIdent && p.peek(1).text == "in" {
			return p.parseLabelIn()
		}
		if (p.peek(1).kind == tokEqEq || p.peek(1).kind == tokNeq) && p.peek(2).kind == tokString {
			return p.parseLabelEq(), nil
		}
	}
	return p.parseComparison()
}

// parseLabelIn implements: ident "in" "(" string ("," string)* ")".
func (p *parser) parseLabelIn() (Node, error) {
	name := p.advance() // ident
	p.advance()         // "in"
	if _, err := p.expect(tokLParen, "'('"); err != nil {
		return Node{}, err
	}
	var values []string
	for {
		str, err := p.expect(tokString, "string")
		if err != nil {
			return Node{}, err
		}
		values = append(values, str.text)
		if p.cur().kind != tokComma {
			break
		}
		p.advance()
	}
	if _, err := p.expect(tokRParen, "')'"); err != nil {
		return Node{}, err
	}
	return Node{Kind: KindLabel, Name: name.text, Op: "in", Values: values, Pos: name.pos}, nil
}

// parseLabelEq implements: ident ("==" | "!=") string. parsePredicate only
// calls this once its own lookahead has already confirmed the token right
// after the operator is a string, so unlike parseLabelIn (whose contents
// are never pre-checked) there is no error path left to cover here.
func (p *parser) parseLabelEq() Node {
	name := p.advance()  // ident
	opTok := p.advance() // "==" or "!="
	str := p.advance()   // string, guaranteed by the caller's lookahead
	op := "=="
	if opTok.kind == tokNeq {
		op = "!="
	}
	return Node{Kind: KindLabel, Name: name.text, Op: op, Str: str.text, Pos: name.pos}
}

// parseComparison implements: comparison := arith (op) arith. The
// operator is not optional — a bare arith expression standing alone is
// not a boolean predicate, so its absence is a parse error here rather
// than a "not a boolean expression" TypeCheck failure discovered later.
func (p *parser) parseComparison() (Node, error) {
	left, err := p.parseArith()
	if err != nil {
		return Node{}, err
	}
	op, ok := compareOp(p.cur())
	if !ok {
		return Node{}, p.errorf("expected a comparison operator, got %s", p.cur().describe())
	}
	p.advance()
	right, err := p.parseArith()
	if err != nil {
		return Node{}, err
	}
	return Node{Kind: KindCompare, Op: op, Args: []Node{left, right}, Pos: left.Pos}, nil
}

func compareOp(t token) (string, bool) {
	switch t.kind {
	case tokLt:
		return "<", true
	case tokLe:
		return "<=", true
	case tokGt:
		return ">", true
	case tokGe:
		return ">=", true
	case tokEqEq:
		return "==", true
	case tokNeq:
		return "!=", true
	default:
		return "", false
	}
}

// parseArith implements: arith := term (("+" | "-") term)*, left-folding
// each further term into the running result — the grammar's flat
// repetition, but KindArith only ever holds two operands, so the tree
// itself is a left-leaning chain.
func (p *parser) parseArith() (Node, error) {
	left, err := p.parseTerm()
	if err != nil {
		return Node{}, err
	}
	for p.cur().kind == tokPlus || p.cur().kind == tokMinus {
		op := "+"
		if p.cur().kind == tokMinus {
			op = "-"
		}
		p.advance()
		right, err := p.parseTerm()
		if err != nil {
			return Node{}, err
		}
		left = Node{Kind: KindArith, Op: op, Args: []Node{left, right}, Pos: left.Pos}
	}
	return left, nil
}

// parseTerm implements: term := factor (("*" | "/") factor)*, the same
// left-fold as parseArith one precedence level up.
func (p *parser) parseTerm() (Node, error) {
	left, err := p.parseFactor()
	if err != nil {
		return Node{}, err
	}
	for p.cur().kind == tokStar || p.cur().kind == tokSlash {
		op := "*"
		if p.cur().kind == tokSlash {
			op = "/"
		}
		p.advance()
		right, err := p.parseFactor()
		if err != nil {
			return Node{}, err
		}
		left = Node{Kind: KindArith, Op: op, Args: []Node{left, right}, Pos: left.Pos}
	}
	return left, nil
}

// parseFactor implements: factor := number | ident | call | "-" factor |
// "(" arith ")". A unary minus has no dedicated node kind (ast.go gives
// KindArith exactly two operands), so it is represented as "0 - factor" —
// Format renders that shape back as "-x", but the tree Parse produces and
// the tree a hand-built "0 - x" produces are the same.
func (p *parser) parseFactor() (Node, error) {
	tok := p.cur()
	switch tok.kind {
	case tokNumber:
		p.advance()
		return Node{Kind: KindNumber, Num: tok.num, Pos: tok.pos}, nil
	case tokMinus:
		p.advance()
		inner, err := p.parseFactor()
		if err != nil {
			return Node{}, err
		}
		zero := Node{Kind: KindNumber, Num: 0, Pos: tok.pos}
		return Node{Kind: KindArith, Op: "-", Args: []Node{zero, inner}, Pos: tok.pos}, nil
	case tokLParen:
		p.advance()
		inner, err := p.parseArith()
		if err != nil {
			return Node{}, err
		}
		if _, err := p.expect(tokRParen, "')'"); err != nil {
			return Node{}, err
		}
		return inner, nil
	case tokIdent:
		if p.peek(1).kind == tokLParen {
			return p.parseCall()
		}
		p.advance()
		return Node{Kind: KindMetric, Name: tok.text, Pos: tok.pos}, nil
	default:
		return Node{}, p.errorf("expected expression, got %s", tok.describe())
	}
}

// parseCall implements: call := ("avg"|"min"|"max"|"rate") "(" ident ","
// duration ")" | "fact" "(" string ")". The function name was only
// tentatively an ident until this point (parseFactor peeked "(" after
// it); an ident that reaches here under any other name is not one of the
// grammar's five functions, and that is a parse error, not a metric
// named like a function call.
func (p *parser) parseCall() (Node, error) {
	name := p.advance() // function name
	if _, err := p.expect(tokLParen, "'('"); err != nil {
		return Node{}, err
	}
	switch name.text {
	case "avg", "min", "max", "rate":
		metric, err := p.expect(tokIdent, "identifier")
		if err != nil {
			return Node{}, err
		}
		if _, err := p.expect(tokComma, "','"); err != nil {
			return Node{}, err
		}
		seconds, err := p.parseDuration()
		if err != nil {
			return Node{}, err
		}
		if _, err := p.expect(tokRParen, "')'"); err != nil {
			return Node{}, err
		}
		return Node{Kind: KindWindow, Name: metric.text, Agg: name.text, Seconds: seconds, Pos: name.pos}, nil
	case "fact":
		str, err := p.expect(tokString, "string")
		if err != nil {
			return Node{}, err
		}
		if _, err := p.expect(tokRParen, "')'"); err != nil {
			return Node{}, err
		}
		return Node{Kind: KindFact, Name: str.text, Pos: name.pos}, nil
	default:
		return Node{}, &ParseError{Pos: name.pos, Msg: fmt.Sprintf("unknown function %q", name.text)}
	}
}

// parseDuration implements: duration := [0-9]+ ("s" | "m" | "h"),
// returning the length in seconds. lex only ever attaches a unit to an
// integer run immediately followed by lowercase letters (lexNumberOrDuration),
// so a plain tokNumber here means the source had digits with no unit at
// all, and a tokDuration with a unit outside {s, m, h} means it had one
// lex could not make sense of — the brief's two duration error cases.
func (p *parser) parseDuration() (int, error) {
	tok := p.cur()
	if tok.kind == tokNumber {
		return 0, &ParseError{Pos: tok.pos, Msg: "duration: missing unit"}
	}
	if tok.kind != tokDuration {
		return 0, p.errorf("expected a duration, got %s", tok.describe())
	}
	p.advance()
	var mult int
	switch tok.durUnit {
	case "s":
		mult = 1
	case "m":
		mult = 60
	case "h":
		mult = 3600
	default:
		return 0, &ParseError{Pos: tok.pos, Msg: fmt.Sprintf("duration: unknown unit %q", tok.durUnit)}
	}
	return tok.durNum * mult, nil
}
