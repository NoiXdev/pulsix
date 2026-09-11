package selector

import (
	"fmt"
	"strings"
)

// tokenKind names one lexical class the tokenizer can produce.
type tokenKind int

const (
	tokEOF tokenKind = iota
	// tokWord is a bare, unquoted run of characters — a key, a keyword
	// (and/in/exists, matched case-insensitively by the parser), or a bare
	// value. The lexer does not distinguish between these; the parser
	// decides what a word means from where it appears.
	tokWord
	// tokString is a double-quoted value with the quotes stripped. Only a
	// value may be quoted — the grammar has no quoted keys or keywords.
	tokString
	tokEQ     // =
	tokNEQ    // !=
	tokLParen // (
	tokRParen // )
	tokComma  // ,
)

// token is one lexical unit, carrying the rune offset it started at so a
// syntax error can say where the input went wrong.
type token struct {
	kind tokenKind
	text string
	pos  int
}

// isWordChar reports whether r may appear inside a bare word (a key,
// keyword or unquoted value). Every character that is not whitespace or one
// of the grammar's own punctuation marks is fair game — this is
// deliberately more permissive than the key shape hosts.tagKeyPattern
// enforces (e.g. it allows uppercase), since the same scanning rule also
// produces keyword tokens ("AND", "In", ...) and bare-word values, and it
// is the parser's job — not the lexer's — to reject a key that does not
// match the grammar's lowercase-only shape.
func isWordChar(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '(', ')', ',', '"', '=', '!':
		return false
	default:
		return true
	}
}

// lexer tokenizes a selector expression by hand, one rune array over the
// whole input with no backtracking — the brief asks for a hand-written
// tokenizer rather than a parser-generator dependency, and the grammar is
// small enough that this needs no more machinery than that.
type lexer struct {
	input []rune
	pos   int
}

func newLexer(s string) *lexer { return &lexer{input: []rune(s)} }

func (l *lexer) errorf(pos int, format string, args ...any) error {
	return fmt.Errorf("selector: %s at position %d", fmt.Sprintf(format, args...), pos)
}

func (l *lexer) skipSpace() {
	for l.pos < len(l.input) {
		switch l.input[l.pos] {
		case ' ', '\t', '\n', '\r':
			l.pos++
		default:
			return
		}
	}
}

// next returns the next token, or a tokEOF token once the input is
// exhausted. It never returns both a token and a non-nil error.
func (l *lexer) next() (token, error) {
	l.skipSpace()
	if l.pos >= len(l.input) {
		return token{kind: tokEOF, pos: l.pos}, nil
	}
	start := l.pos
	switch l.input[l.pos] {
	case '(':
		l.pos++
		return token{kind: tokLParen, text: "(", pos: start}, nil
	case ')':
		l.pos++
		return token{kind: tokRParen, text: ")", pos: start}, nil
	case ',':
		l.pos++
		return token{kind: tokComma, text: ",", pos: start}, nil
	case '=':
		l.pos++
		return token{kind: tokEQ, text: "=", pos: start}, nil
	case '!':
		l.pos++
		if l.pos >= len(l.input) || l.input[l.pos] != '=' {
			return token{}, l.errorf(start, "expected '=' after '!'")
		}
		l.pos++
		return token{kind: tokNEQ, text: "!=", pos: start}, nil
	case '"':
		return l.lexString(start)
	default:
		return l.lexWord(start), nil
	}
}

func (l *lexer) lexWord(start int) token {
	for l.pos < len(l.input) && isWordChar(l.input[l.pos]) {
		l.pos++
	}
	return token{kind: tokWord, text: string(l.input[start:l.pos]), pos: start}
}

// lexString reads a double-quoted value. There is no escape syntax — the
// grammar is deliberately minimal (see selector.go's own doc comment), so a
// value containing a literal '"' simply cannot be expressed.
func (l *lexer) lexString(start int) (token, error) {
	l.pos++ // consume the opening quote
	var b strings.Builder
	for {
		if l.pos >= len(l.input) {
			return token{}, l.errorf(start, "unterminated quoted string")
		}
		r := l.input[l.pos]
		if r == '"' {
			l.pos++
			return token{kind: tokString, text: b.String(), pos: start}, nil
		}
		b.WriteRune(r)
		l.pos++
	}
}
