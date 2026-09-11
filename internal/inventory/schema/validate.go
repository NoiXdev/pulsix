package schema

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// maxSafeInt is the largest integer a JSON number carries without loss.
const maxSafeInt = 1 << 53

// Problem is one validation finding, located by path (vhosts[1].domain for
// values, fields[0].name for a schema).
type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ValidationError carries every problem found. Services return it unchanged
// and the API turns it into 422 with one detail per problem.
type ValidationError struct {
	Problems []Problem
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, pr := range e.Problems {
		path := pr.Path
		if path == "" {
			path = "(root)"
		}
		parts[i] = path + ": " + pr.Message
	}
	return "invalid: " + strings.Join(parts, "; ")
}

type problems struct{ list []Problem }

func (p *problems) add(path, msg string) { p.list = append(p.list, Problem{Path: path, Message: msg}) }

func (p *problems) err() error {
	if len(p.list) == 0 {
		return nil
	}
	return &ValidationError{Problems: p.list}
}

// Validate checks a write strictly: an undeclared key at any depth is a
// problem, a missing or null value is a problem for a required field without
// a default, and every present value must satisfy its field. Secret fields
// accept a non-empty plaintext string or a sealed value; keep markers must be
// resolved before Validate runs.
func Validate(fields []Field, values map[string]any) error {
	var p problems
	validateObject(&p, "", fields, values, true)
	return p.err()
}

// ValidateStored checks stored values like Validate but ignores keys the
// schema no longer declares: a removed field keeps its old value until the
// next save, and that must not make a membership look incomplete.
func ValidateStored(fields []Field, values map[string]any) error {
	var p problems
	validateObject(&p, "", fields, values, false)
	return p.err()
}

func validateObject(p *problems, prefix string, fields []Field, values map[string]any, strict bool) {
	if strict {
		declared := make(map[string]bool, len(fields))
		for _, f := range fields {
			declared[f.Name] = true
		}
		var unknown []string
		for k := range values {
			if !declared[k] {
				unknown = append(unknown, k)
			}
		}
		slices.Sort(unknown)
		for _, k := range unknown {
			p.add(join(prefix, k), "unknown field")
		}
	}
	for _, f := range fields {
		v, ok := values[f.Name]
		if !ok || v == nil {
			if f.Required && f.Default == nil {
				p.add(join(prefix, f.Name), "is required")
			}
			continue
		}
		validateValue(p, join(prefix, f.Name), f, v, strict)
	}
}

func validateValue(p *problems, path string, f Field, v any, strict bool) {
	switch f.Type {
	case TypeString:
		s, ok := v.(string)
		if !ok {
			p.add(path, "must be a string")
			return
		}
		n := float64(utf8.RuneCountInString(s))
		if f.Min != nil && n < *f.Min {
			p.add(path, "must be at least "+num(*f.Min)+" characters")
		}
		if f.Max != nil && n > *f.Max {
			p.add(path, "must be at most "+num(*f.Max)+" characters")
		}
		if f.Pattern != "" && !matchPattern(f.Pattern, s) {
			p.add(path, "must match "+f.Pattern)
		}
	case TypeInt:
		n, ok := number(v)
		if !ok || n != math.Trunc(n) || math.Abs(n) > maxSafeInt {
			p.add(path, "must be a whole number")
			return
		}
		checkRange(p, path, f, n)
	case TypeFloat:
		n, ok := number(v)
		if !ok {
			p.add(path, "must be a number")
			return
		}
		checkRange(p, path, f, n)
	case TypeBool:
		if _, ok := v.(bool); !ok {
			p.add(path, "must be true or false")
		}
	case TypeEnum:
		s, ok := v.(string)
		if !ok || !slices.Contains(f.Options, s) {
			p.add(path, "must be one of "+strings.Join(f.Options, ", "))
		}
	case TypeSecret:
		if _, ok := SealedBytes(v); ok {
			return
		}
		s, ok := v.(string)
		if !ok {
			p.add(path, "must be a string")
			return
		}
		if s == "" {
			p.add(path, "must not be empty")
		}
	case TypeList:
		items, ok := v.([]any)
		if !ok {
			p.add(path, "must be a list")
			return
		}
		checkCount(p, path, f, len(items))
		if f.Item == nil {
			return
		}
		for i, it := range items {
			ip := fmt.Sprintf("%s[%d]", path, i)
			if it == nil {
				p.add(ip, "must not be empty")
				continue
			}
			validateValue(p, ip, *f.Item, it, strict)
		}
	case TypeRepeater:
		items, ok := v.([]any)
		if !ok {
			p.add(path, "must be a list of entries")
			return
		}
		checkCount(p, path, f, len(items))
		for i, it := range items {
			ip := fmt.Sprintf("%s[%d]", path, i)
			obj, ok := it.(map[string]any)
			if !ok {
				p.add(ip, "must be an entry with fields")
				continue
			}
			validateObject(p, ip, f.Fields, obj, strict)
		}
	case TypeObject:
		obj, ok := v.(map[string]any)
		if !ok {
			p.add(path, "must be an object")
			return
		}
		validateObject(p, path, f.Fields, obj, strict)
	}
}

func checkRange(p *problems, path string, f Field, n float64) {
	if f.Min != nil && n < *f.Min {
		p.add(path, "must be at least "+num(*f.Min))
	}
	if f.Max != nil && n > *f.Max {
		p.add(path, "must be at most "+num(*f.Max))
	}
}

func checkCount(p *problems, path string, f Field, n int) {
	if f.MinItems != nil && n < *f.MinItems {
		p.add(path, fmt.Sprintf("needs at least %d entries", *f.MinItems))
	}
	if f.MaxItems != nil && n > *f.MaxItems {
		p.add(path, fmt.Sprintf("allows at most %d entries", *f.MaxItems))
	}
}

// number accepts the numeric shapes a decoded JSON document or a Go caller
// can produce.
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// matchPattern reports whether s matches pattern. A pattern that does not
// compile never fails a value: Check reports the pattern itself.
func matchPattern(pattern, s string) bool {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return true
	}
	return re.MatchString(s)
}
