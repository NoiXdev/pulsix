package scriptargs

import (
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type ruleKind int

const (
	kindNone ruleKind = iota
	kindPattern
	kindRange
	kindEnum
)

// Rule is what a host accepts for one placeholder: a pattern, a range of
// whole numbers or a list of values. The zero Rule accepts nothing.
type Rule struct {
	kind        ruleKind
	re          *regexp.Regexp
	allowDash   bool // pattern: a leading '-' (Windows programs: '/') is fine
	allowDotDot bool // pattern: a ".." path segment is fine
	unicode     bool // pattern, values: characters beyond printable ASCII are fine
	min, max    int64
	values      []string
}

// PatternOptions are the permissions a pattern rule may grant beyond its
// expression; each defaults to the safe refusal.
type PatternOptions struct {
	// AllowLeadingDash admits a value starting with '-', and for a Windows
	// program one starting with '/': either can be read as an option.
	AllowLeadingDash bool
	// AllowDotDot admits a value holding a ".." path segment ('/' or '\'
	// separated), which could lead a path out of the place a pattern such
	// as /var/log/.* means.
	AllowDotDot bool
	// Unicode admits characters beyond printable ASCII (never the
	// invisible ones ValueProblem refuses). On Windows a program that
	// reads its command line in the ANSI code page may see some of them
	// as other characters (best fit: a fullwidth quote as '"').
	Unicode bool
}

// The fixed reasons a rule refuses a value, phrased to follow
// `argument "<name>"`. None quotes the value or the rule's own text
// (agent.yaml may hold more than the user should read); a range names
// its bounds, which help and hold nothing.
const (
	reasonLeadingDash  = "must not start with '-'"
	reasonLeadingSlash = "must not start with '/' (a switch to Windows programs)"
	reasonDotDot       = `must not hold a ".." path segment`
	reasonPattern      = "does not match the pattern this host allows"
	reasonEnum         = "is not one of the values this host allows"
	reasonNoRule       = "has no rule on this host"
)

// PatternRule accepts a value the RE2 expression matches as a whole (it
// is anchored here as ^(?:expr)$). A value starting with '-' (for a
// Windows program also '/'), one holding a ".." path segment and one
// beyond printable ASCII are refused unless o allows them.
func PatternRule(expr string, o PatternOptions) (Rule, error) {
	switch {
	case expr == "":
		return Rule{}, errors.New("a pattern must not be empty")
	case len(expr) > MaxPatternBytes:
		return Rule{}, errors.New("a pattern is longer than " + strconv.Itoa(MaxPatternBytes) + " bytes")
	}
	// expr must compile on its own: "a)|(b" would otherwise close the
	// anchoring group early and match any value starting with a.
	if _, err := regexp.Compile(expr); err != nil {
		return Rule{}, errors.New("a pattern is not a valid regular expression")
	}
	re, err := regexp.Compile(`^(?:` + expr + `)$`)
	if err != nil {
		return Rule{}, errors.New("a pattern is not a valid regular expression")
	}
	return Rule{kind: kindPattern, re: re, allowDash: o.AllowLeadingDash, allowDotDot: o.AllowDotDot, unicode: o.Unicode}, nil
}

// RangeRule accepts a whole number from lo to hi, both inclusive, and
// passes it in canonical decimal. A negative value is possible only when
// lo is negative.
func RangeRule(lo, hi int64) (Rule, error) {
	if lo > hi {
		return Rule{}, errors.New("a range's min is greater than its max")
	}
	return Rule{kind: kindRange, min: lo, max: hi}, nil
}

// EnumRule accepts exactly one of values, which are 1 to MaxEnumValues
// distinct values that each pass ValueProblem and, unless unicode is set,
// are printable ASCII. A listed value may start with '-' or hold "..":
// the host's administrator wrote it.
func EnumRule(values []string, unicode bool) (Rule, error) {
	switch {
	case len(values) == 0:
		return Rule{}, errors.New("a values list must not be empty")
	case len(values) > MaxEnumValues:
		return Rule{}, errors.New("a values list has more than " + strconv.Itoa(MaxEnumValues) + " entries")
	}
	for i, v := range values {
		if p := ValueProblem(v); p != "" {
			return Rule{}, errors.New("a listed value " + p)
		}
		if !unicode && !printableASCII(v) {
			return Rule{}, errors.New("a listed value has characters beyond printable ASCII; set unicode: true to allow them")
		}
		if slices.Contains(values[:i], v) {
			return Rule{}, errors.New("a values list holds a value twice")
		}
	}
	return Rule{kind: kindEnum, values: slices.Clone(values), unicode: unicode}, nil
}

// check returns the argument to pass for v, which passed ValueProblem,
// or a reason the rule refuses it. windows says the program is a Windows
// program, to which a leading '/' is a switch.
func (r Rule) check(v string, windows bool) (arg, reason string) {
	switch r.kind {
	case kindPattern:
		// A program's own parsing may skip leading blanks.
		lead := strings.TrimLeft(v, " \t")
		switch {
		case strings.HasPrefix(lead, "-") && !r.allowDash:
			return "", reasonLeadingDash
		case windows && strings.HasPrefix(lead, "/") && !r.allowDash:
			return "", reasonLeadingSlash
		case !r.allowDotDot && hasDotDot(v):
			return "", reasonDotDot
		}
		if !r.re.MatchString(v) {
			return "", reasonPattern
		}
		return v, ""
	case kindRange:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < r.min || n > r.max {
			return "", "must be a whole number from " + strconv.FormatInt(r.min, 10) + " to " + strconv.FormatInt(r.max, 10)
		}
		return strconv.FormatInt(n, 10), ""
	case kindEnum:
		if !slices.Contains(r.values, v) {
			return "", reasonEnum
		}
		return v, ""
	}
	return "", reasonNoRule
}

// hasDotDot reports a segment of v, split at '/', '\\' and ':' (on every
// OS: a refusal too many on Unix costs nothing), that consists of dots and
// spaces only with at least two dots: "..", and the forms Windows trims to
// it (".. ", "...") or reaches through a drive or stream (C:..\x,
// ..:stream).
func hasDotDot(v string) bool {
	for _, seg := range strings.FieldsFunc(v, func(r rune) bool { return r == '/' || r == '\\' || r == ':' }) {
		if strings.Count(seg, ".") >= 2 && strings.Trim(seg, ". ") == "" {
			return true
		}
	}
	return false
}
