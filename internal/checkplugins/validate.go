package checkplugins

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
)

// ErrInvalidParams is what every refusal of Validate wraps. The error's own
// text does not repeat it ("disk: unknown parameter \"x\""), so a caller can
// wrap the error into its own sentinel without the message saying "invalid"
// twice.
var ErrInvalidParams = errors.New("invalid check parameters")

type paramsError struct{ msg string }

func (e *paramsError) Error() string { return e.msg }
func (e *paramsError) Unwrap() error { return ErrInvalidParams }

func refuse(format string, args ...any) error {
	return &paramsError{msg: fmt.Sprintf(format, args...)}
}

// Validate checks params against plugin's descriptor, in this order: no
// unknown key, every required key present, every value valid for its spec,
// and then the plugin's Check hook. Rejecting unknown keys rather than ignoring them is
// deliberate — a typo'd parameter must fail at write time, not be dropped
// and discovered missing at the agent weeks later. Keys are visited in
// sorted order so the same bad input always names the same parameter.
func Validate(plugin string, params map[string]any) error {
	d, ok := Get(plugin)
	if !ok {
		return refuse("unsupported plugin %q", plugin)
	}
	return d.validate(params)
}

func (d Descriptor) validate(params map[string]any) error {
	if len(d.Params) == 0 && len(params) > 0 {
		return refuse("plugin %q takes no parameters", d.Name)
	}
	specs := make(map[string]ParamSpec, len(d.Params))
	for _, p := range d.Params {
		specs[p.Name] = p
	}
	keys := slices.Sorted(maps.Keys(params))
	// Unknown keys first: a typo of a required key ("unit" for "units")
	// names the typo rather than only the key it left missing.
	for _, k := range keys {
		if _, ok := specs[k]; !ok {
			return refuse("%s: unknown parameter %q", d.Name, k)
		}
	}
	for _, p := range d.Params {
		if _, ok := params[p.Name]; p.Required && !ok {
			return refuse("%s: %s is required", d.Name, p.Name)
		}
	}
	for _, k := range keys {
		if problem := specs[k].problem(params[k]); problem != "" {
			return refuse("%s: %s %s", d.Name, k, problem)
		}
	}
	if d.Check != nil {
		if err := d.Check(params); err != nil {
			return refuse("%s: %s", d.Name, err)
		}
	}
	return nil
}

// problem describes why v is not a valid value for p ("must be …"), or
// returns "" when it is.
func (p ParamSpec) problem(v any) string {
	switch p.Type {
	case TypeInteger:
		if n, ok := wholeNumber(v); !ok || (p.Min != nil && n < *p.Min) || (p.Max != nil && n > *p.Max) {
			return "must be " + p.describeInteger()
		}
	case TypeBool:
		if _, ok := v.(bool); !ok {
			return "must be true or false"
		}
	case TypeEnum:
		if s, ok := v.(string); !ok || !slices.Contains(p.Options, s) {
			return "must be one of " + strings.Join(p.Options, ", ")
		}
	case TypeString:
		s, ok := v.(string)
		if !ok {
			return "must be a string"
		}
		return p.stringProblem(s, "")
	case TypeList:
		return p.listProblem(v)
	default:
		return fmt.Sprintf("has a parameter type %q no validator knows", p.Type)
	}
	return ""
}

func (p ParamSpec) describeInteger() string {
	switch {
	case p.Min != nil && p.Max != nil:
		return fmt.Sprintf("a whole number between %d and %d", *p.Min, *p.Max)
	case p.Min != nil:
		return fmt.Sprintf("a whole number of at least %d", *p.Min)
	case p.Max != nil:
		return fmt.Sprintf("a whole number of at most %d", *p.Max)
	}
	return "a whole number"
}

// stringProblem checks a string value, or one list entry when subject is
// "entries ": its length, then its format. NonEmpty is a string's own rule
// (a list's NonEmpty bounds the list, see listProblem).
func (p ParamSpec) stringProblem(s, subject string) string {
	if subject == "" && p.NonEmpty && s == "" {
		return "must not be empty"
	}
	if p.MaxBytes > 0 && len(s) > p.MaxBytes {
		return fmt.Sprintf("%smust be at most %d bytes", subject, p.MaxBytes)
	}
	if p.Format != "" {
		if problem := p.Format.problem(s); problem != "" {
			return subject + problem
		}
	}
	return ""
}

func (p ParamSpec) listProblem(v any) string {
	list, ok := stringList(v)
	if !ok {
		return "must be a list of strings"
	}
	if p.NonEmpty && len(list) == 0 {
		return "must have at least one entry"
	}
	if p.MaxEntries > 0 && len(list) > p.MaxEntries {
		return fmt.Sprintf("must have at most %d entries", p.MaxEntries)
	}
	for _, e := range list {
		if e == "" {
			return "must not contain an empty entry"
		}
		if problem := p.stringProblem(e, "entries "); problem != "" {
			return problem
		}
	}
	return ""
}

// stringList reads a []string out of a value that may be either — a
// literal []string (a Go caller, or a seed definition's literal) or a []any
// of strings (what encoding/json decodes a JSON array into, which is what
// every params value coming off the wire actually is).
func stringList(v any) ([]string, bool) {
	switch vv := v.(type) {
	case []string:
		return vv, true
	case []any:
		out := make([]string, len(vv))
		for i, e := range vv {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out[i] = s
		}
		return out, true
	default:
		return nil, false
	}
}

// wholeNumber reads a whole number out of a value a JSON body decodes as
// float64 (or a Go caller passes as an int or int64).
func wholeNumber(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || math.Abs(n) > 1<<53 {
			return 0, false
		}
		return int64(n), true
	}
	return 0, false
}
