package secrets

import (
	"maps"
	"slices"
	"sort"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// Limits of the checks section (backlog 3.5a).
const (
	maxLocalChecks       = 32
	maxLocalParamsBytes  = 2048 // a check's params as canonical JSON
	defaultLocalInterval = 60 * time.Second
)

// LocalCheck is a check defined in the file's checks section. The agent
// runs it next to the manager's checks, under its own key.
type LocalCheck struct {
	Key, Plugin string
	Params      map[string]any // nil when none
	Interval    time.Duration
	ParamsJSON  string // canonical JSON ("{}" when none), what is reported
}

// clone is a deep copy of c: its params map and any list in it.
func (c LocalCheck) clone() LocalCheck {
	if c.Params != nil {
		params := make(map[string]any, len(c.Params))
		for k, v := range c.Params {
			if list, ok := v.([]any); ok {
				v = slices.Clone(list)
			}
			params[k] = v
		}
		c.Params = params
	}
	return c
}

// The fixed reasons of an entry's refusal. None quotes the file.
const (
	badLocalKey      = "a local check key is local- followed by lowercase letters, digits and dashes, 63 characters in all at most"
	badLocalField    = "unknown entry field; an entry has plugin, interval_seconds and params"
	noLocalPlugin    = "plugin is required"
	badLocalPlugin   = "plugin must be a plugin name"
	unknownPlugin    = "unknown plugin"
	serverRunPlugin  = "the plugin runs on the server, not on the agent"
	badParamName     = "a parameter name is 1 to 64 characters of A-Z a-z 0-9 _ . -"
	unencodableParam = "params cannot be written as JSON"
	tooManyChecks    = "more than 32 local checks"
)

var (
	badLocalInterval = "interval_seconds must be a whole number from " +
		strconv.Itoa(int(checkplugins.MinInterval/time.Second)) + " to " +
		strconv.Itoa(int(checkplugins.MaxInterval/time.Second))
	longLocalParams = "params are longer than " + strconv.Itoa(maxLocalParamsBytes) + " bytes as JSON"
)

// localEntry is one entry of the checks section after the structural walk:
// what it says, not yet judged.
type localEntry struct {
	key          *yaml.Node
	unknownField bool
	plugin       *yaml.Node // nil when absent
	interval     *yaml.Node // nil when absent
	params       map[string]any
}

// parseLocalChecks decodes the mapping under checks; a missing or empty one
// holds none. It works at two levels. A structural fault — the wrong node
// kind where the format needs a mapping (checks, an entry, params), a
// params value that is neither a single value nor a list of single
// values, a null, a duplicate key — is an error, and the whole file is
// refused. Everything an entry says (its key, fields, plugin, interval and
// params) refuses that entry only: it is left out and described in
// problems as "line N: <reason>", N being the entry key's line, in line
// order. Of the entries left, the first maxLocalChecks in key order are
// kept, in key order.
func parseLocalChecks(n *yaml.Node) (checks []LocalCheck, problems []string, err error) {
	if n == nil || isNull(n) {
		return nil, nil, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, nil, errAt(n, "checks must map check keys to their plugin, interval_seconds and params")
	}
	entries := make([]localEntry, 0, len(n.Content)/2)
	lines := map[string]int{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		if key.Kind != yaml.ScalarNode {
			return nil, nil, errAt(key, "a check key must be a single value")
		}
		if line, dup := lines[key.Value]; dup {
			return nil, nil, errAt(key, "this check is already defined at line "+strconv.Itoa(line))
		}
		lines[key.Value] = key.Line
		e, err := parseLocalEntry(key, val)
		if err != nil {
			return nil, nil, err
		}
		entries = append(entries, e)
	}
	type problem struct {
		line   int
		reason string
	}
	var refused []problem
	var valid []LocalCheck
	validLines := map[string]int{}
	for _, e := range entries {
		c, reason := e.judge()
		if reason != "" {
			refused = append(refused, problem{e.key.Line, reason})
			continue
		}
		valid = append(valid, c)
		validLines[c.Key] = e.key.Line
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].Key < valid[j].Key })
	if len(valid) > maxLocalChecks {
		for _, c := range valid[maxLocalChecks:] {
			refused = append(refused, problem{validLines[c.Key], tooManyChecks})
		}
		valid = valid[:maxLocalChecks]
	}
	sort.SliceStable(refused, func(i, j int) bool { return refused[i].line < refused[j].line })
	for _, p := range refused {
		problems = append(problems, "line "+strconv.Itoa(p.line)+": "+p.reason)
	}
	return valid, problems, nil
}

// parseLocalEntry walks one entry's mapping for structural faults; key is
// the entry's key node, where a missing mapping is reported. The value of
// an unknown field is not looked at: the entry is refused anyway.
func parseLocalEntry(key, n *yaml.Node) (localEntry, error) {
	e := localEntry{key: key}
	if n.Kind != yaml.MappingNode {
		at := n
		if isNull(n) {
			at = key // an empty value sits after the key; point at the key
		}
		return e, errAt(at, "a check must map plugin, interval_seconds and params")
	}
	lines := map[string]int{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		field, val := n.Content[i], n.Content[i+1]
		if field.Kind != yaml.ScalarNode {
			e.unknownField = true
			continue
		}
		if line, dup := lines[field.Value]; dup {
			return e, errAt(field, "this field is already set at line "+strconv.Itoa(line))
		}
		lines[field.Value] = field.Line
		switch field.Value {
		case "plugin", "interval_seconds":
			if isNull(val) {
				return e, errAt(val, "an entry field has no value")
			}
			if field.Value == "plugin" {
				e.plugin = val
			} else {
				e.interval = val
			}
		case "params":
			if val.Kind != yaml.MappingNode {
				at := val
				if isNull(val) {
					at = field
				}
				return e, errAt(at, "params must map parameter names to values; write {} for none")
			}
			params, err := parseParams(val)
			if err != nil {
				return e, err
			}
			e.params = params
		default:
			e.unknownField = true
		}
	}
	return e, nil
}

// parseParams decodes a params mapping into the values a JSON-decoded
// manager push would carry: see paramScalar for a single value; a list
// becomes []any of them. An empty mapping is nil.
func parseParams(n *yaml.Node) (map[string]any, error) {
	if len(n.Content) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(n.Content)/2)
	lines := map[string]int{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		if key.Kind != yaml.ScalarNode {
			return nil, errAt(key, "a parameter name must be a single value")
		}
		if line, dup := lines[key.Value]; dup {
			return nil, errAt(key, "this parameter is already set at line "+strconv.Itoa(line))
		}
		lines[key.Value] = key.Line
		switch val.Kind {
		case yaml.ScalarNode:
			if isNull(val) {
				return nil, errAt(val, `a parameter has no value; write "" for an empty one`)
			}
			out[key.Value] = paramScalar(val)
		case yaml.SequenceNode:
			list := make([]any, 0, len(val.Content))
			for _, entry := range val.Content {
				switch {
				case entry.Kind != yaml.ScalarNode:
					return nil, errAt(entry, "a list entry must be a single value")
				case isNull(entry):
					return nil, errAt(entry, `a list entry has no value; write "" for an empty one`)
				}
				list = append(list, paramScalar(entry))
			}
			out[key.Value] = list
		default:
			return nil, errAt(val, "a parameter value must be a single value or a list of single values")
		}
	}
	return out, nil
}

// paramScalar resolves a non-null single value the way YAML's core schema
// does: an integer or float is a float64 (as encoding/json decodes a
// number), true or false a bool, anything else — a quoted value, one
// tagged !!str, a timestamp — the string as written. A value that is not
// finite (.inf, .nan) stays a float64 for the plugin's validation to
// refuse.
func paramScalar(n *yaml.Node) any {
	switch n.ShortTag() {
	case "!!int", "!!float":
		var v any
		if err := n.Decode(&v); err == nil {
			if f, ok := toFloat(v); ok {
				return f
			}
		}
	case "!!bool":
		var b bool
		if err := n.Decode(&b); err == nil {
			return b
		}
	}
	return n.Value
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

// judge validates what the entry says, in order: key, fields, plugin,
// interval, the plugin's own params validation, the params' JSON size. It
// returns the check or the first reason to refuse it.
func (e localEntry) judge() (LocalCheck, string) {
	c := LocalCheck{Key: e.key.Value, Interval: defaultLocalInterval, Params: e.params}
	switch {
	case !checkplugins.ValidLocalCheckKey(c.Key):
		return LocalCheck{}, badLocalKey
	case e.unknownField:
		return LocalCheck{}, badLocalField
	case e.plugin == nil:
		return LocalCheck{}, noLocalPlugin
	case e.plugin.Kind != yaml.ScalarNode:
		return LocalCheck{}, badLocalPlugin
	}
	c.Plugin = e.plugin.Value
	d, ok := checkplugins.Get(c.Plugin)
	switch {
	case !ok:
		return LocalCheck{}, unknownPlugin
	case d.Runs != checkplugins.Agent:
		return LocalCheck{}, serverRunPlugin
	}
	if e.interval != nil {
		interval, ok := localInterval(e.interval)
		if !ok {
			return LocalCheck{}, badLocalInterval
		}
		c.Interval = interval
	}
	for _, name := range slices.Sorted(maps.Keys(c.Params)) {
		if !namePattern.MatchString(name) {
			return LocalCheck{}, badParamName // Validate would quote it
		}
	}
	if err := checkplugins.Validate(c.Plugin, c.Params); err != nil {
		return LocalCheck{}, err.Error()
	}
	raw, err := checkplugins.CanonicalParams(c.Params)
	switch {
	case err != nil:
		return LocalCheck{}, unencodableParam
	case len(raw) > maxLocalParamsBytes:
		return LocalCheck{}, longLocalParams
	}
	c.ParamsJSON = string(raw)
	return c, ""
}

// localInterval reads interval_seconds: a plain integer within the
// manager's bounds.
func localInterval(n *yaml.Node) (time.Duration, bool) {
	if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!int" {
		return 0, false
	}
	var secs int64
	if err := n.Decode(&secs); err != nil {
		return 0, false
	}
	d := time.Duration(secs) * time.Second
	if secs < 0 || secs > int64(checkplugins.MaxInterval/time.Second) || d < checkplugins.MinInterval {
		return 0, false
	}
	return d, true
}
