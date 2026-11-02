package selector

import (
	"fmt"
	"strings"
)

// A selector's bounds (backlog 7.41). Every condition compiles into its
// own correlated sub-query of one statement on the shared pool (see
// Selector.SQL), and selectors are stored and re-evaluated on every
// resolve, so an unbounded one was a way to make every later resolve of
// the organization expensive. MaxConditions is far past any selector a
// person writes; MaxLen leaves room for a long "in" list.
const (
	MaxConditions = 32
	MaxLen        = 4096
)

// Parse parses s against the grammar in this package's own doc comment
// (selector.go). Any input that is not exactly one valid expression is
// rejected with an error naming where in s parsing stopped making sense —
// never a panic, and never a partially-parsed result. A selector longer
// than MaxLen bytes or with more than MaxConditions conditions is
// rejected too.
func Parse(s string) (Selector, error) {
	if len(s) > MaxLen {
		return Selector{}, fmt.Errorf("selector: longer than %d bytes", MaxLen)
	}
	p := &parser{lex: newLexer(s)}
	if err := p.advance(); err != nil {
		return Selector{}, err
	}
	sel, err := p.parseExpr()
	if err != nil {
		return Selector{}, err
	}
	if p.tok.kind != tokEOF {
		return Selector{}, p.errorf("unexpected input after a complete expression")
	}
	return sel, nil
}

// parser is a recursive-descent parser over the token stream lexer
// produces, holding exactly one token of lookahead (p.tok).
type parser struct {
	lex *lexer
	tok token
}

func (p *parser) advance() error {
	tok, err := p.lex.next()
	if err != nil {
		return err
	}
	p.tok = tok
	return nil
}

func (p *parser) errorf(format string, args ...any) error {
	return fmt.Errorf("selector: %s at position %d", fmt.Sprintf(format, args...), p.tok.pos)
}

// isKeyword reports whether the current token is the bare word kw, matched
// case-insensitively — the grammar's "and"/"in"/"exists" are keywords only
// in this positional sense; the lexer itself never distinguishes them from
// any other word.
func (p *parser) isKeyword(kw string) bool {
	return p.tok.kind == tokWord && strings.EqualFold(p.tok.text, kw)
}

// parseExpr parses `cond ("and" cond)*`.
func (p *parser) parseExpr() (Selector, error) {
	first, err := p.parseCond()
	if err != nil {
		return Selector{}, err
	}
	conds := []condition{first}
	for p.isKeyword("and") {
		if len(conds) == MaxConditions {
			return Selector{}, p.errorf("more than %d conditions", MaxConditions)
		}
		if err := p.advance(); err != nil {
			return Selector{}, err
		}
		c, err := p.parseCond()
		if err != nil {
			return Selector{}, err
		}
		conds = append(conds, c)
	}
	return Selector{conds: conds}, nil
}

// parseCond parses one `key <op> ...` term.
func (p *parser) parseCond() (condition, error) {
	if p.tok.kind != tokWord {
		return condition{}, p.errorf("expected a tag key")
	}
	key := p.tok.text
	if !keyPattern.MatchString(key) {
		return condition{}, p.errorf("invalid key %q: must match %s", key, keyPattern.String())
	}
	if err := p.advance(); err != nil {
		return condition{}, err
	}

	switch {
	case p.tok.kind == tokEQ:
		if err := p.advance(); err != nil {
			return condition{}, err
		}
		val, err := p.parseValue()
		if err != nil {
			return condition{}, err
		}
		return condition{key: key, op: opEq, values: []string{val}}, nil

	case p.tok.kind == tokNEQ:
		if err := p.advance(); err != nil {
			return condition{}, err
		}
		val, err := p.parseValue()
		if err != nil {
			return condition{}, err
		}
		return condition{key: key, op: opNeq, values: []string{val}}, nil

	case p.isKeyword("in"):
		if err := p.advance(); err != nil {
			return condition{}, err
		}
		if p.tok.kind != tokLParen {
			return condition{}, p.errorf("expected '(' after 'in'")
		}
		if err := p.advance(); err != nil {
			return condition{}, err
		}
		values, err := p.parseValueList()
		if err != nil {
			return condition{}, err
		}
		if p.tok.kind != tokRParen {
			return condition{}, p.errorf("expected ')'")
		}
		if err := p.advance(); err != nil {
			return condition{}, err
		}
		return condition{key: key, op: opIn, values: values}, nil

	case p.isKeyword("exists"):
		if err := p.advance(); err != nil {
			return condition{}, err
		}
		return condition{key: key, op: opExists}, nil

	default:
		return condition{}, p.errorf("expected '=', '!=', 'in' or 'exists' after key %q", key)
	}
}

func (p *parser) parseValue() (string, error) {
	switch p.tok.kind {
	case tokWord, tokString:
		v := p.tok.text
		if err := p.advance(); err != nil {
			return "", err
		}
		return v, nil
	default:
		return "", p.errorf("expected a value")
	}
}

func (p *parser) parseValueList() ([]string, error) {
	first, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	values := []string{first}
	for p.tok.kind == tokComma {
		if err := p.advance(); err != nil {
			return nil, err
		}
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, nil
}
