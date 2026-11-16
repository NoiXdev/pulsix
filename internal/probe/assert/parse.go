package assert

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/andybalholm/cascadia"
	"github.com/noixdev/pulsix/internal/jpath"
	"golang.org/x/net/http/httpguts"
)

// namePattern is the rule of an assertion's name: json_values' (and a
// secret's) name rule, FormatSecretName in internal/checkplugins, which a
// test there keeps equal.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// numberPattern is a decimal number: an optional sign, digits, an optional
// fraction.
var numberPattern = regexp.MustCompile(`^[+-]?[0-9]+(\.[0-9]+)?$`)

// Parse parses one entry, name=expression with an optional @warn or
// @crit. Its error says what is wrong, quoting only the operator's own
// entry.
func Parse(entry string) (Assertion, error) {
	switch {
	case len(entry) > MaxEntryLen:
		return Assertion{}, fmt.Errorf("an assertion is at most %d bytes", MaxEntryLen)
	case !utf8.ValidString(entry):
		return Assertion{}, errors.New("an assertion must be valid UTF-8")
	case strings.ContainsFunc(entry, unicode.IsControl):
		return Assertion{}, errors.New("an assertion must not contain control characters (write \\t or \\n inside a string)")
	}
	name, expr, ok := strings.Cut(entry, "=")
	if !ok || !namePattern.MatchString(name) {
		return Assertion{}, errors.New("the name before = must be 1 to 64 letters, digits, '_', '.' or '-'")
	}
	p := &parser{s: expr}
	a, err := p.assertion()
	if err != nil {
		return Assertion{}, err
	}
	a.name = name
	return a, nil
}

// parser reads an expression left to right; i is the next byte.
type parser struct {
	s string
	i int
}

func (p *parser) spaces() {
	for p.i < len(p.s) && p.s[p.i] == ' ' {
		p.i++
	}
}

func (p *parser) done() bool { return p.i >= len(p.s) }

// word reads a run of the bytes in set.
func (p *parser) word(in func(c byte) bool) string {
	start := p.i
	for p.i < len(p.s) && in(p.s[p.i]) {
		p.i++
	}
	return p.s[start:p.i]
}

func isLower(c byte) bool  { return c >= 'a' && c <= 'z' || c == '_' }
func isLetter(c byte) bool { return isLower(c) || c >= 'A' && c <= 'Z' }
func isSymbol(c byte) bool { return c == '<' || c == '>' || c == '=' || c == '!' }

// rest reads the next space-separated token.
func (p *parser) rest() string { return p.word(func(c byte) bool { return c != ' ' }) }

func (p *parser) assertion() (Assertion, error) {
	var a Assertion
	if err := p.subject(&a); err != nil {
		return Assertion{}, err
	}
	if err := p.op(&a); err != nil {
		return Assertion{}, err
	}
	if err := p.operand(&a); err != nil {
		return Assertion{}, err
	}
	a.severity = Crit
	p.spaces()
	if !p.done() && p.s[p.i] == '@' {
		start := p.i
		switch p.rest() {
		case "@warn":
			a.severity = Warn
		case "@crit":
		default:
			p.i = start
		}
		p.spaces()
	}
	if !p.done() {
		return Assertion{}, fmt.Errorf("unexpected %q after the operand (only @warn or @crit may follow it)", p.rest())
	}
	return a, nil
}

// subject reads the subject and, for header, json and html, its quoted
// argument (compiled for json and html) and html's count.
func (p *parser) subject(a *Assertion) error {
	p.spaces()
	if p.done() {
		return errors.New("the expression is empty: it starts with a subject such as status")
	}
	word := p.word(isLetter)
	s := Subject(word)
	known := []Subject{SubjectStatus, SubjectResponseMS, SubjectTTFBMS, SubjectSize, SubjectRedirects,
		SubjectCertDays, SubjectCertIssuer, SubjectBody, SubjectHeader, SubjectJSON, SubjectHTML}
	if !slices.Contains(known, s) {
		if word == "" {
			word = p.rest()
		}
		return fmt.Errorf("unknown subject %q", word)
	}
	hasArg := !p.done() && p.s[p.i] == '('
	switch {
	case s.takesArg() && !hasArg:
		return fmt.Errorf("%s takes a quoted argument: %s(\"…\")", s, s)
	case !s.takesArg() && hasArg:
		return fmt.Errorf("%s takes no argument", s)
	case !hasArg:
		a.subject = s
		return nil
	}
	p.i++
	arg, err := p.str()
	if err != nil {
		return fmt.Errorf("%s(…) takes a JSON string: %w", s, err)
	}
	if p.done() || p.s[p.i] != ')' {
		return fmt.Errorf("%s(\"…\") needs a closing )", s)
	}
	p.i++
	if err := compileArg(a, s, arg); err != nil {
		return err
	}
	a.subject, a.arg = s, arg
	if s == SubjectHTML {
		save := p.i
		p.spaces()
		if p.word(isLetter) == "count" && (p.done() || p.s[p.i] == ' ') {
			a.subject = SubjectHTMLCount
		} else {
			p.i = save
		}
	}
	return nil
}

// compileArg checks and compiles a subject's argument.
func compileArg(a *Assertion, s Subject, arg string) error {
	switch s {
	case SubjectHeader:
		if !httpguts.ValidHeaderFieldName(arg) {
			return fmt.Errorf("%q is not a header name", arg)
		}
	case SubjectJSON:
		if !strings.HasPrefix(arg, "$") {
			return errors.New("the JSONPath of json(…) must start with $")
		}
		path, err := jpath.Parse(arg)
		if err != nil {
			return fmt.Errorf("the JSONPath of json(…) is not in Pulsix's supported subset (see the docs): %w", err)
		}
		a.path = path
	case SubjectHTML:
		switch {
		case strings.TrimSpace(arg) == "":
			return errors.New("the CSS selector of html(…) must not be empty")
		case len(arg) > MaxSelectLen:
			return fmt.Errorf("the CSS selector of html(…) must be at most %d bytes", MaxSelectLen)
		}
		sel, err := cascadia.ParseGroup(arg)
		if err != nil {
			return fmt.Errorf("the CSS selector of html(…) does not parse: %w", err)
		}
		costs, err := selectorCosts(arg, sel)
		if err != nil {
			return err
		}
		a.sel, a.costs = sel, costs
	}
	return nil
}

// op reads the operator and checks the subject takes it.
func (p *parser) op(a *Assertion) error {
	p.spaces()
	if p.done() {
		return fmt.Errorf("%s needs an operator", a.subject)
	}
	var word string
	switch {
	case p.s[p.i] == '!' && p.i+1 < len(p.s) && isLetter(p.s[p.i+1]):
		p.i++
		word = "!" + p.word(isLetter)
	case isLetter(p.s[p.i]):
		word = p.word(isLetter)
	default:
		word = p.word(isSymbol)
	}
	if word == "" {
		word = p.rest()
	}
	op := Op(word)
	if !slices.Contains(opsOf(a.subject), op) {
		return fmt.Errorf("%q is not an operator of %s", word, a.subject)
	}
	a.op = op
	return nil
}

// operand reads what the operator compares with: none for exists, a
// range for in, a number for a numeric subject, a string otherwise.
func (p *parser) operand(a *Assertion) error {
	p.spaces()
	switch {
	case a.op == OpExists || a.op == OpNotExists:
		if !p.done() && p.s[p.i] != '@' {
			return fmt.Errorf("%s takes no operand", a.op)
		}
		return nil
	case a.op == OpIn:
		lo, hi, ok := strings.Cut(p.rest(), "..")
		var errLo, errHi error
		a.lo, errLo = number(lo)
		a.hi, errHi = number(hi)
		if !ok || errLo != nil || errHi != nil || a.lo > a.hi {
			return errors.New(`"in" takes a range a..b with a <= b, such as 200..299`)
		}
		return nil
	case a.subject.Numeric():
		n, err := number(p.rest())
		if err != nil {
			return fmt.Errorf("%s takes a decimal number after %q, such as 200", a.subject, a.op)
		}
		a.num = n
		return nil
	}
	text, err := p.str()
	if err != nil {
		return fmt.Errorf("%s takes a JSON string after %q, such as \"text\": %w", a.subject, a.op, err)
	}
	a.text = text
	if a.op == OpMatches || a.op == OpNotMatches {
		if len(text) > MaxRegexLen {
			return fmt.Errorf("the regular expression must be at most %d bytes", MaxRegexLen)
		}
		re, err := regexp.Compile(text)
		if err != nil {
			return fmt.Errorf("the regular expression does not compile: %w", err)
		}
		a.re = re
	}
	return nil
}

// number reads a decimal number a float64 holds.
func number(s string) (float64, error) {
	if !numberPattern.MatchString(s) {
		return 0, errors.New("not a decimal number")
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(n, 0) {
		return 0, errors.New("the number is out of range")
	}
	return n, nil
}

// str reads a JSON string literal and returns its value.
func (p *parser) str() (string, error) {
	if p.done() || p.s[p.i] != '"' {
		return "", errors.New(`a string is written in double quotes`)
	}
	end := p.i + 1
	for ; end < len(p.s); end++ {
		if p.s[end] == '\\' {
			end++
			continue
		}
		if p.s[end] == '"' {
			break
		}
	}
	if end >= len(p.s) {
		return "", errors.New("the string has no closing quote")
	}
	var v string
	if err := json.Unmarshal([]byte(p.s[p.i:end+1]), &v); err != nil {
		return "", errors.New("the string has an escape JSON does not know")
	}
	p.i = end + 1
	return v, nil
}

// Quote spells s as a JSON string literal, without HTML escaping.
func Quote(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}
