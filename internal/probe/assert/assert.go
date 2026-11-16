// Package assert parses the http check's assertions (backlog 3.13b, spec
// docs/superpowers/specs/2026-11-16-web-assertions-design.md, section 3):
// entries "name=expression" with an optional " @warn" (or " @crit", the
// default), each stating one thing a good response is.
//
//	assertion = subject SP op [SP operand]
//	subject   = status | response_ms | ttfb_ms | size | redirects
//	          | cert_days | cert_issuer | body
//	          | header("Name") | json("$.path") | html("selector")
//	          | html("selector") count
//
// Numeric subjects take <, <=, >, >=, ==, != and in (a..b, inclusive)
// with a decimal number; text subjects take ==, !=, contains, !contains,
// matches and !matches (RE2) with a JSON string literal; header, json and
// html also take exists and !exists, without an operand.
//
// The descriptor's validation (internal/checkplugins), the probe
// (internal/probe/http) and the alert detail all parse with this package,
// so it imports nothing of theirs. Parsing compiles the regular
// expression, the CSS selector (Pulsix's own subset, selector.go) and the
// JSONPath, so an entry that parses can be evaluated.
//
// The probe matches html selectors against a tree it builds in one
// linear pass over the page's tokens, not with the HTML parsing
// algorithm: void, self-closing and common omitted end tags (p, li,
// dt/dd, table rows and cells, option) are handled, but no html or body
// is implied and other malformed markup can group differently than in a
// browser.
package assert

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/noixdev/pulsix/internal/jpath"
)

// Bounds of the assertions parameter; the descriptor declares them and
// Parse refuses a longer entry, regular expression or selector itself.
const (
	MaxEntries   = 32
	MaxEntryLen  = 512
	MaxRegexLen  = 256
	MaxSelectLen = 256
)

// Subject is what an assertion looks at.
type Subject string

// The subjects. SubjectHTMLCount is spelled html("selector") count.
const (
	SubjectStatus     Subject = "status"
	SubjectResponseMS Subject = "response_ms"
	SubjectTTFBMS     Subject = "ttfb_ms"
	SubjectSize       Subject = "size"
	SubjectRedirects  Subject = "redirects"
	SubjectCertDays   Subject = "cert_days"
	SubjectCertIssuer Subject = "cert_issuer"
	SubjectBody       Subject = "body"
	SubjectHeader     Subject = "header"
	SubjectJSON       Subject = "json"
	SubjectHTML       Subject = "html"
	SubjectHTMLCount  Subject = "html count"
)

// Numeric reports whether s compares a number.
func (s Subject) Numeric() bool {
	switch s {
	case SubjectStatus, SubjectResponseMS, SubjectTTFBMS, SubjectSize, SubjectRedirects, SubjectCertDays, SubjectHTMLCount:
		return true
	}
	return false
}

// ReadsBody reports whether s needs the response body at all; a HEAD
// request fetches none, so the descriptor refuses these with HEAD.
func (s Subject) ReadsBody() bool {
	return s == SubjectSize || s.KeepsBody()
}

// KeepsBody reports whether s reads the kept first MiB of the body.
func (s Subject) KeepsBody() bool {
	switch s {
	case SubjectBody, SubjectJSON, SubjectHTML, SubjectHTMLCount:
		return true
	}
	return false
}

// takesArg reports whether s is written with a quoted argument.
func (s Subject) takesArg() bool {
	return s == SubjectHeader || s == SubjectJSON || s == SubjectHTML || s == SubjectHTMLCount
}

// Op is an assertion's operator.
type Op string

// The operators.
const (
	OpLt          Op = "<"
	OpLe          Op = "<="
	OpGt          Op = ">"
	OpGe          Op = ">="
	OpEq          Op = "=="
	OpNe          Op = "!="
	OpIn          Op = "in"
	OpContains    Op = "contains"
	OpNotContains Op = "!contains"
	OpMatches     Op = "matches"
	OpNotMatches  Op = "!matches"
	OpExists      Op = "exists"
	OpNotExists   Op = "!exists"
)

// Negative reports whether op states that something is not there, which
// only the whole body can prove.
func (op Op) Negative() bool {
	return op == OpNotContains || op == OpNotMatches || op == OpNotExists
}

// opsOf are the operators s takes.
func opsOf(s Subject) []Op {
	switch {
	case s.Numeric():
		return []Op{OpLt, OpLe, OpGt, OpGe, OpEq, OpNe, OpIn}
	case s == SubjectCertIssuer || s == SubjectBody:
		return []Op{OpEq, OpNe, OpContains, OpNotContains, OpMatches, OpNotMatches}
	}
	return []Op{OpEq, OpNe, OpContains, OpNotContains, OpMatches, OpNotMatches, OpExists, OpNotExists}
}

// Severity is what a failed assertion reports.
type Severity string

// The severities; Crit is the default.
const (
	Warn Severity = "warn"
	Crit Severity = "crit"
)

// Assertion is one parsed entry.
type Assertion struct {
	name     string
	subject  Subject
	arg      string
	op       Op
	severity Severity
	// The operand: num for a numeric op, lo and hi for in, text for a text
	// op; re compiled from text for matches.
	num, lo, hi float64
	text        string
	re          *regexp.Regexp
	// program is re's instruction count (ProgramSize).
	program int
	// The compiled argument of json and html.
	path *jpath.Path
	sel  *Selector
}

// Name is the item the assertion reports on.
func (a Assertion) Name() string { return a.name }

// Subject is what the assertion looks at.
func (a Assertion) Subject() Subject { return a.subject }

// Arg is the subject's argument: the header name, the JSONPath or the CSS
// selector; "" for the other subjects.
func (a Assertion) Arg() string { return a.arg }

// Op is the operator.
func (a Assertion) Op() Op { return a.op }

// Severity is what the assertion reports when it fails.
func (a Assertion) Severity() Severity { return a.severity }

// Path is json's compiled JSONPath, nil for any other subject.
func (a Assertion) Path() *jpath.Path { return a.path }

// Selector is html's parsed selector, nil for any other subject.
func (a Assertion) Selector() *Selector { return a.sel }

// ProgramSize is the instruction count of matches' and !matches'
// compiled regular expression, 0 for any other operator: matching it
// costs up to the program size times the input's length.
func (a Assertion) ProgramSize() int { return a.program }

// Operand is the operand as the canonical expression spells it: a
// number, a range a..b or a JSON string literal; "" for exists and
// !exists.
func (a Assertion) Operand() string {
	switch {
	case a.op == OpExists || a.op == OpNotExists:
		return ""
	case a.op == OpIn:
		return formatNumber(a.lo) + ".." + formatNumber(a.hi)
	case a.subject.Numeric():
		return formatNumber(a.num)
	}
	return Quote(a.text)
}

// Expression is the canonical expression, without the name and the
// severity: single spaces, numbers as Go prints them, strings as JSON
// literals without HTML escaping.
func (a Assertion) Expression() string {
	var b strings.Builder
	b.WriteString(subjectText(a.subject, a.arg))
	b.WriteString(" ")
	b.WriteString(string(a.op))
	if operand := a.Operand(); operand != "" {
		b.WriteString(" ")
		b.WriteString(operand)
	}
	return b.String()
}

// Entry is the canonical entry: name=Expression, " @warn" when the
// severity is warn (crit is the default and never written).
func (a Assertion) Entry() string {
	entry := a.name + "=" + a.Expression()
	if a.severity == Warn {
		entry += " @warn"
	}
	return entry
}

// EntryName is the item an entry names, before its first '=' (a name
// never holds one), whether the rest parses or not.
func EntryName(entry string) string {
	name, _, _ := strings.Cut(entry, "=")
	return name
}

func subjectText(s Subject, arg string) string {
	switch s {
	case SubjectHTMLCount:
		return "html(" + Quote(arg) + ") count"
	case SubjectHeader, SubjectJSON, SubjectHTML:
		return string(s) + "(" + Quote(arg) + ")"
	}
	return string(s)
}

// formatNumber prints n the shortest way that reads back exactly, without
// an exponent; zero has no sign.
func formatNumber(n float64) string {
	if n == 0 {
		n = 0
	}
	return strconv.FormatFloat(n, 'f', -1, 64)
}
