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
	kind      ruleKind
	re        *regexp.Regexp
	allowDash bool
	min, max  int64
	values    []string
}

// The fixed reasons a rule refuses a value, phrased to follow
// `argument "<name>"`. None quotes the value or the rule's own text
// (agent.yaml may hold more than the user should read); a range names
// its bounds, which help and hold nothing.
const (
	reasonLeadingDash = "must not start with '-'"
	reasonPattern     = "does not match the pattern this host allows"
	reasonEnum        = "is not one of the values this host allows"
	reasonNoRule      = "has no rule on this host"
)

// PatternRule accepts a value the RE2 expression matches as a whole (it
// is anchored here as ^(?:expr)$). A value starting with '-' is refused
// unless allowDash is set: such a value could be read as an option.
func PatternRule(expr string, allowDash bool) (Rule, error) {
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
	return Rule{kind: kindPattern, re: re, allowDash: allowDash}, nil
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
// distinct values that each pass ValueProblem. A listed value may start
// with '-': the host's administrator wrote it.
func EnumRule(values []string) (Rule, error) {
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
		if slices.Contains(values[:i], v) {
			return Rule{}, errors.New("a values list holds a value twice")
		}
	}
	return Rule{kind: kindEnum, values: slices.Clone(values)}, nil
}

// check returns the argument to pass for v, which passed ValueProblem,
// or a reason the rule refuses it.
func (r Rule) check(v string) (arg, reason string) {
	switch r.kind {
	case kindPattern:
		if strings.HasPrefix(v, "-") && !r.allowDash {
			return "", reasonLeadingDash
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
