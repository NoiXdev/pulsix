package secrets

import (
	"math"
	"regexp"
	"strconv"

	"gopkg.in/yaml.v3"
)

// parseParams decodes a params mapping into the values a JSON-decoded
// manager push would carry: see paramScalar for a single value; a list
// becomes []any of them. An empty mapping is nil. A structural fault is an
// error (the file is refused); a number in a form paramScalar refuses is
// the first such problem, in document order, which refuses the entry only.
func parseParams(n *yaml.Node) (params map[string]any, problem string, err error) {
	if len(n.Content) == 0 {
		return nil, "", nil
	}
	out := make(map[string]any, len(n.Content)/2)
	lines := map[string]int{}
	note := func(p string) {
		if problem == "" {
			problem = p
		}
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		if key.Kind != yaml.ScalarNode {
			return nil, "", errAt(key, "a parameter name must be a single value")
		}
		if line, dup := lines[key.Value]; dup {
			return nil, "", errAt(key, "this parameter is already set at line "+strconv.Itoa(line))
		}
		lines[key.Value] = key.Line
		switch val.Kind {
		case yaml.ScalarNode:
			if isNull(val) {
				return nil, "", errAt(val, `a parameter has no value; write "" for an empty one`)
			}
			v, p := paramScalar(val)
			note(p)
			out[key.Value] = v
		case yaml.SequenceNode:
			list := make([]any, 0, len(val.Content))
			for _, entry := range val.Content {
				switch {
				case entry.Kind != yaml.ScalarNode:
					return nil, "", errAt(entry, "a list entry must be a single value")
				case isNull(entry):
					return nil, "", errAt(entry, `a list entry has no value; write "" for an empty one`)
				}
				v, p := paramScalar(entry)
				note(p)
				list = append(list, v)
			}
			out[key.Value] = list
		default:
			return nil, "", errAt(val, "a parameter value must be a single value or a list of single values")
		}
	}
	return out, problem, nil
}

// paramScalar resolves a non-null single value the way YAML 1.2's core
// schema does: a number is a float64 (as encoding/json decodes one), true
// or false a bool, anything else — a quoted value, one tagged !!str, a
// timestamp — the string as written. A number numberProblem refuses comes
// back with that problem.
func paramScalar(n *yaml.Node) (any, string) {
	switch n.ShortTag() {
	case "!!int", "!!float":
		if problem := numberProblem(n); problem != "" {
			return n.Value, problem
		}
		var v any
		if err := n.Decode(&v); err == nil {
			if f, ok := toFloat(v); ok {
				return f, ""
			}
		}
	case "!!bool":
		var b bool
		if err := n.Decode(&b); err == nil {
			return b, ""
		}
	}
	return n.Value, ""
}

// decimalNumber is a number as YAML 1.2's core schema and JSON agree on
// it: an optional sign, decimal digits without a leading zero, an optional
// fraction and exponent.
var decimalNumber = regexp.MustCompile(`^[-+]?(?:(?:0|[1-9][0-9]*)(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][-+]?[0-9]+)?$`)

// numberProblem judges a plain scalar yaml resolved to !!int or !!float.
// yaml also resolves YAML 1.1 forms — 010 is octal 8, 1_000 and 0b11 are
// numbers — that an operator may well mean otherwise (interval_seconds:
// 0300 would run every 192 s), and hexadecimal and 0o octal that no
// manager push carries; all are refused rather than read. So are .inf and
// .nan, which JSON cannot carry. The reason never quotes the value.
func numberProblem(n *yaml.Node) string {
	var f float64
	if err := n.Decode(&f); err == nil && (math.IsInf(f, 0) || math.IsNaN(f)) {
		return nonFiniteNumber
	}
	if !decimalNumber.MatchString(n.Value) {
		return badNumberForm
	}
	return ""
}

// toFloat converts a number yaml decoded into a float64.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}
