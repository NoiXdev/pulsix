package assert

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// PseudoKind is a supported pseudo-class.
type PseudoKind int

// The pseudo-classes. The nth kinds carry A and B (An+B; odd is 2n+1,
// even 2n, a number N is 0n+N), PseudoNot the compounds it excludes.
const (
	PseudoFirst PseudoKind = iota
	PseudoLast
	PseudoNth
	PseudoOnly
	PseudoNthLast
	PseudoFirstOfType
	PseudoLastOfType
	PseudoOnlyOfType
	PseudoNthOfType
	PseudoNthLastOfType
	PseudoEmpty
	PseudoRoot
	PseudoNot
)

// Pseudo is one pseudo-class of a compound.
type Pseudo struct {
	Kind PseudoKind
	A, B int
	Not  []Compound
}

// plainPseudos are the pseudo-classes without an argument.
var plainPseudos = map[string]PseudoKind{
	"first-child": PseudoFirst, "last-child": PseudoLast, "only-child": PseudoOnly,
	"first-of-type": PseudoFirstOfType, "last-of-type": PseudoLastOfType, "only-of-type": PseudoOnlyOfType,
	"empty": PseudoEmpty, "root": PseudoRoot,
}

// nthPseudos are the pseudo-classes taking An+B.
var nthPseudos = map[string]PseudoKind{
	"nth-child": PseudoNth, "nth-last-child": PseudoNthLast,
	"nth-of-type": PseudoNthOfType, "nth-last-of-type": PseudoNthLastOfType,
}

const supportedPseudos = ":first-child, :last-child, :only-child, :nth-child(), :nth-last-child(), " +
	":first-of-type, :last-of-type, :only-of-type, :nth-of-type(), :nth-last-of-type(), :empty, :root and :not()"

// pseudo reads one of the supported pseudo-classes and the number of
// simple selectors it counts as. Inside a :not (inNot) another :not is
// refused.
func (p *selParser) pseudo(inNot bool) (Pseudo, int, error) {
	p.i++
	if !p.done() && p.peek() == ':' {
		return Pseudo{}, 0, errors.New("a CSS selector of html(…) may not use pseudo-elements (::)")
	}
	name := strings.ToLower(p.ident())
	if name == "" {
		return Pseudo{}, 0, errors.New("a CSS selector of html(…) needs a pseudo-class name after the colon")
	}
	if kind, ok := plainPseudos[name]; ok {
		return Pseudo{Kind: kind}, 1, nil
	}
	kind, nth := nthPseudos[name]
	if !nth && name != "not" {
		return Pseudo{}, 0, fmt.Errorf("a CSS selector of html(…) may not use :%s (only %s are supported)", name, supportedPseudos)
	}
	if p.done() || p.peek() != '(' {
		return Pseudo{}, 0, fmt.Errorf("a CSS selector of html(…) needs ( after :%s", name)
	}
	p.i++
	if !nth {
		if inNot {
			return Pseudo{}, 0, errors.New("a CSS selector of html(…) may not nest :not(…) inside :not(…)")
		}
		return p.notList()
	}
	end := strings.IndexByte(p.s[p.i:], ')')
	if end < 0 {
		return Pseudo{}, 0, fmt.Errorf("a CSS selector of html(…) needs ) to close :%s(…)", name)
	}
	arg := p.s[p.i : p.i+end]
	p.i += end + 1
	a, b, err := parseNth(arg, name)
	if err != nil {
		return Pseudo{}, 0, err
	}
	return Pseudo{Kind: kind, A: a, B: b}, 1, nil
}

// notList reads the compounds of a :not after its "(", through ")".
func (p *selParser) notList() (Pseudo, int, error) {
	ps := Pseudo{Kind: PseudoNot}
	weight := 1
	for {
		p.ws()
		if p.done() || p.peek() == ',' || p.peek() == ')' {
			return Pseudo{}, 0, errors.New("a CSS selector of html(…) needs a selector inside :not(…) before each , and )")
		}
		c, n, err := p.weighedCompound(true)
		if err != nil {
			return Pseudo{}, 0, err
		}
		ps.Not = append(ps.Not, c)
		weight += n
		p.ws()
		switch {
		case p.done():
			return Pseudo{}, 0, errors.New("a CSS selector of html(…) needs ) to close :not(…)")
		case p.peek() == ')':
			p.i++
			return ps, weight, nil
		case p.peek() == ',':
			p.i++
		default:
			return Pseudo{}, 0, errors.New("a CSS selector of html(…) may not use combinators inside :not(…), only compound selectors separated by commas")
		}
	}
}

// parseNth reads An+B as CSS writes it: odd, even, an integer, or
// [+-][A]n with an optional [+-]B, spaces allowed only around that sign.
func parseNth(arg, name string) (a, b int, err error) {
	refuse := fmt.Errorf("a CSS selector of html(…) takes An+B (such as 3, 2n+1 or -n+3), odd or even in :%s(…), with A and B up to %d, not %q", name, maxNth, arg)
	s := strings.ToLower(strings.Trim(arg, " \t\n\r\f"))
	if fields := strings.Fields(s); len(fields) > 1 && slices.Contains(fields[1:], "of") {
		return 0, 0, fmt.Errorf("a CSS selector of html(…) may not use :%s(An+B of S)", name)
	}
	switch s {
	case "odd":
		return 2, 1, nil
	case "even":
		return 2, 0, nil
	}
	head, tail, hasN := strings.Cut(s, "n")
	if !hasN {
		b, ok := nthInt(s, true)
		if !ok {
			return 0, 0, refuse
		}
		return 0, b, nil
	}
	switch head {
	case "", "+":
		a = 1
	case "-":
		a = -1
	default:
		var ok bool
		if a, ok = nthInt(head, true); !ok {
			return 0, 0, refuse
		}
	}
	tail = strings.TrimLeft(tail, " \t\n\r\f")
	if tail == "" {
		return a, 0, nil
	}
	sign := 1
	switch tail[0] {
	case '+':
	case '-':
		sign = -1
	default:
		return 0, 0, refuse
	}
	b, ok := nthInt(strings.TrimLeft(tail[1:], " \t\n\r\f"), false)
	if !ok {
		return 0, 0, refuse
	}
	return a, sign * b, nil
}

// nthInt reads a decimal integer of at most maxNth, with a leading sign
// when signed allows it. Leading zeros are allowed in any number, as
// 3.13b's whole numbers allowed them; only the digits after them are
// bounded.
func nthInt(s string, signed bool) (int, bool) {
	neg, digits := false, s
	if signed && s != "" && (s[0] == '+' || s[0] == '-') {
		neg, digits = s[0] == '-', s[1:]
	}
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return 0, false
	}
	significant := strings.TrimLeft(digits, "0")
	if len(significant) > 7 {
		return 0, false
	}
	n := 0
	if significant != "" {
		var err error
		if n, err = strconv.Atoi(significant); err != nil {
			return 0, false
		}
	}
	if n > maxNth {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}
