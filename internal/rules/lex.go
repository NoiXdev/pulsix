package rules

import (
	"fmt"
	"strconv"
	"strings"
)

// tokKind names one lexical category the expression grammar's terminals
// fall into. Keywords ("and", "or", "not", "in", the window aggregates,
// "fact") are not their own kinds: they lex as tokIdent like any other
// identifier, and the parser tells them apart by their text where the
// grammar calls for it.
type tokKind int

const (
	tokEOF tokKind = iota
	tokIdent
	tokNumber
	tokDuration
	tokString
	tokLParen
	tokRParen
	tokComma
	tokPlus
	tokMinus
	tokStar
	tokSlash
	tokLt
	tokLe
	tokGt
	tokGe
	tokEqEq
	tokNeq
)

// token is one lexed terminal: its kind, its byte offset into the source
// (Pos, in Node terms), and whatever value that kind carries. Only the
// fields a given kind uses are set; text carries an identifier's or a
// string literal's content, num a number literal's value, durNum and
// durUnit a duration's amount and unit letters.
type token struct {
	kind    tokKind
	pos     int
	text    string
	num     float64
	durNum  int
	durUnit string
}

// describe renders t for an error message an editor's caret can sit under
// alongside — "expected ')', got end of input", not a raw token kind
// number.
func (t token) describe() string {
	switch t.kind {
	case tokEOF:
		return "end of input"
	case tokIdent:
		return fmt.Sprintf("identifier %q", t.text)
	case tokNumber:
		return "number"
	case tokDuration:
		return "duration"
	case tokString:
		return "string"
	case tokLParen:
		return "'('"
	case tokRParen:
		return "')'"
	case tokComma:
		return "','"
	case tokPlus:
		return "'+'"
	case tokMinus:
		return "'-'"
	case tokStar:
		return "'*'"
	case tokSlash:
		return "'/'"
	case tokLt:
		return "'<'"
	case tokLe:
		return "'<='"
	case tokGt:
		return "'>'"
	case tokGe:
		return "'>='"
	case tokEqEq:
		return "'=='"
	case tokNeq:
		return "'!='"
	default:
		return "token"
	}
}

// lex tokenizes src in full, returning a *ParseError at the first lexical
// problem: an unterminated string or a character the grammar never
// produces. A trailing tokEOF (positioned at len(src)) always terminates
// the result, so the parser never needs a separate end-of-input check.
func lex(src string) ([]token, error) {
	var toks []token
	i, n := 0, len(src)
	for i < n {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(':
			toks = append(toks, token{kind: tokLParen, pos: i})
			i++
		case c == ')':
			toks = append(toks, token{kind: tokRParen, pos: i})
			i++
		case c == ',':
			toks = append(toks, token{kind: tokComma, pos: i})
			i++
		case c == '+':
			toks = append(toks, token{kind: tokPlus, pos: i})
			i++
		case c == '-':
			toks = append(toks, token{kind: tokMinus, pos: i})
			i++
		case c == '*':
			toks = append(toks, token{kind: tokStar, pos: i})
			i++
		case c == '/':
			toks = append(toks, token{kind: tokSlash, pos: i})
			i++
		case c == '<':
			toks = append(toks, lexMaybeEq(&i, src, tokLt, tokLe))
		case c == '>':
			toks = append(toks, lexMaybeEq(&i, src, tokGt, tokGe))
		case c == '=':
			tok, ok := lexTwoChar(&i, src, '=', tokEqEq)
			if !ok {
				return nil, &ParseError{Pos: i, Msg: fmt.Sprintf("unexpected character %q", string(c))}
			}
			toks = append(toks, tok)
		case c == '!':
			tok, ok := lexTwoChar(&i, src, '=', tokNeq)
			if !ok {
				return nil, &ParseError{Pos: i, Msg: fmt.Sprintf("unexpected character %q", string(c))}
			}
			toks = append(toks, tok)
		case c == '"':
			tok, err := lexString(&i, src)
			if err != nil {
				return nil, err
			}
			toks = append(toks, tok)
		case c >= '0' && c <= '9':
			toks = append(toks, lexNumberOrDuration(&i, src))
		case c >= 'a' && c <= 'z':
			start := i
			for i < n && isIdentCont(src[i]) {
				i++
			}
			toks = append(toks, token{kind: tokIdent, pos: start, text: src[start:i]})
		default:
			return nil, &ParseError{Pos: i, Msg: fmt.Sprintf("unexpected character %q", string(c))}
		}
	}
	toks = append(toks, token{kind: tokEOF, pos: n})
	return toks, nil
}

// lexMaybeEq lexes '<' or '>', extending to the "<=" / ">=" pair when the
// next byte is '='. Unlike '=' and '!', a bare '<' or '>' is itself a
// valid, complete token, so there is no error case here.
func lexMaybeEq(i *int, src string, bare, withEq tokKind) token {
	pos := *i
	if *i+1 < len(src) && src[*i+1] == '=' {
		*i += 2
		return token{kind: withEq, pos: pos}
	}
	*i++
	return token{kind: bare, pos: pos}
}

// lexTwoChar lexes a two-character operator ("==" or "!=") that has no
// one-character form: ok is false when the second character does not
// follow, which the caller turns into a lexical error.
func lexTwoChar(i *int, src string, second byte, kind tokKind) (token, bool) {
	pos := *i
	if *i+1 < len(src) && src[*i+1] == second {
		*i += 2
		return token{kind: kind, pos: pos}, true
	}
	return token{}, false
}

// lexString consumes a double-quoted literal starting at src[*i], with no
// escape sequences (the rule language's strings hold metric labels and
// fact keys, never quotes themselves). Reaching the end of src before a
// closing quote is the "unterminated string" error the brief asks for,
// positioned at the opening quote.
func lexString(i *int, src string) (token, error) {
	start := *i
	*i++
	var sb strings.Builder
	for *i < len(src) {
		if src[*i] == '"' {
			*i++
			return token{kind: tokString, pos: start, text: sb.String()}, nil
		}
		sb.WriteByte(src[*i])
		*i++
	}
	return token{}, &ParseError{Pos: start, Msg: "unterminated string"}
}

// lexNumberOrDuration consumes a digit run, optionally followed by a
// decimal fraction, starting at src[*i]. An integer run immediately
// followed by lowercase letters (no space — a metric name can never
// itself begin with a digit, so this sequence is unambiguous) is a
// duration; the letters become its unit for the parser to validate. A
// decimal number is never a duration, whatever follows it.
func lexNumberOrDuration(i *int, src string) token {
	start := *i
	n := len(src)
	for *i < n && src[*i] >= '0' && src[*i] <= '9' {
		*i++
	}
	isFloat := false
	if *i < n && src[*i] == '.' && *i+1 < n && src[*i+1] >= '0' && src[*i+1] <= '9' {
		isFloat = true
		*i++
		for *i < n && src[*i] >= '0' && src[*i] <= '9' {
			*i++
		}
	}
	digits := src[start:*i]
	if !isFloat && *i < n && src[*i] >= 'a' && src[*i] <= 'z' {
		unitStart := *i
		for *i < n && src[*i] >= 'a' && src[*i] <= 'z' {
			*i++
		}
		amount, _ := strconv.Atoi(digits) // digits is a just-scanned run of '0'-'9', always valid
		return token{kind: tokDuration, pos: start, durNum: amount, durUnit: src[unitStart:*i]}
	}
	f, _ := strconv.ParseFloat(digits, 64) // digits is a just-scanned, well-formed numeral
	return token{kind: tokNumber, pos: start, num: f}
}

// isIdentCont reports whether c continues an identifier once its first
// (necessarily [a-z]) character has been consumed. Metric names carry
// dots ("cpu.used_pct"), so '.' continues an identifier the same as a
// letter, digit or underscore does.
func isIdentCont(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '.'
}
