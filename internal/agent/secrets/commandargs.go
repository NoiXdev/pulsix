package secrets

import (
	"errors"
	"strconv"

	"github.com/noixdev/pulsix/internal/scriptargs"
	"gopkg.in/yaml.v3"
)

// parseCommandMapping decodes a command's mapping form (backlog 3.6j):
//
//	<name>:
//	  run: [<program>, <argument or {placeholder}>, …]
//	  args:
//	    <placeholder>: <rule>
//
// run is a command sequence as parseArgv reads it; an entry that is
// exactly {placeholder} takes a value a check supplies, under the rule
// args gives it (parseRule). Placeholders are whole arguments, every one
// needs a rule and every rule must be used (scriptargs.NewTemplate). name
// is the command's name node, where a missing run is reported. Errors name
// a position and a fixed reason, never an argument, a pattern or a value.
func parseCommandMapping(name, n *yaml.Node) (scriptargs.Template, error) {
	var run, args *yaml.Node
	lines := map[string]int{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		if key.Kind != yaml.ScalarNode || (key.Value != "run" && key.Value != "args") {
			return scriptargs.Template{}, errAt(key, "a command mapping takes only run and args")
		}
		if line, dup := lines[key.Value]; dup {
			return scriptargs.Template{}, errAt(key, key.Value+" is already set at line "+strconv.Itoa(line))
		}
		lines[key.Value] = key.Line
		if key.Value == "run" {
			run = val
		} else {
			args = val
		}
	}
	if run == nil {
		return scriptargs.Template{}, errAt(name, "a command mapping needs run, the list of its program and arguments")
	}
	argv, err := parseArgv(name, run)
	if err != nil {
		return scriptargs.Template{}, err
	}
	params, keys, err := parseArgs(args)
	if err != nil {
		return scriptargs.Template{}, err
	}
	tmpl, err := scriptargs.NewTemplate(argv, params)
	var te *scriptargs.TemplateError
	switch {
	case err == nil:
		return tmpl, nil
	case !errors.As(err, &te):
		return scriptargs.Template{}, errAt(name, "the command is not valid")
	case te.Arg >= 0 && te.Arg < len(run.Content):
		return scriptargs.Template{}, errAt(run.Content[te.Arg], te.Reason)
	case keys[te.Param] != nil:
		return scriptargs.Template{}, errAt(keys[te.Param], te.Reason)
	case args != nil:
		return scriptargs.Template{}, errAt(args, te.Reason)
	default:
		return scriptargs.Template{}, errAt(run, te.Reason)
	}
}

// parseArgs decodes the args mapping, placeholder names to rules, and
// returns each name's key node for errors found later. A missing or empty
// args holds none.
func parseArgs(n *yaml.Node) (map[string]scriptargs.Param, map[string]*yaml.Node, error) {
	if n == nil || isNull(n) {
		return nil, nil, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, nil, errAt(n, "args must map placeholder names to their rules")
	}
	params := make(map[string]scriptargs.Param, len(n.Content)/2)
	keys := make(map[string]*yaml.Node, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		if key.Kind != yaml.ScalarNode || !scriptargs.ValidName(key.Value) {
			return nil, nil, errAt(key, "a placeholder name is a lower-case letter followed by up to 31 lower-case letters, digits or _")
		}
		if prev, dup := keys[key.Value]; dup {
			return nil, nil, errAt(key, "this placeholder is already set at line "+strconv.Itoa(prev.Line))
		}
		keys[key.Value] = key
		if len(keys) > scriptargs.MaxPlaceholders {
			return nil, nil, errAt(key, "a command has more than "+strconv.Itoa(scriptargs.MaxPlaceholders)+" placeholders")
		}
		p, err := parseRule(key, val)
		if err != nil {
			return nil, nil, err
		}
		params[key.Value] = p
	}
	return params, keys, nil
}

// ruleKeys are the keys a placeholder's rule mapping may hold.
var ruleKeys = map[string]bool{
	"pattern": true, "allow_leading_dash": true, "allow_dotdot": true, "unicode": true,
	"min": true, "max": true, "values": true, "default": true,
}

// parseRule decodes one placeholder's rule mapping: exactly one of
// pattern (with optional allow_leading_dash and allow_dotdot, true or
// false), min and max together, or values; unicode (true or false) with a
// pattern or values; and an optional default. name is the
// placeholder's key node, where a fault of the whole rule is reported. The
// default is checked against the rule by scriptargs.NewTemplate.
func parseRule(name, n *yaml.Node) (scriptargs.Param, error) {
	if n.Kind != yaml.MappingNode {
		at := n
		if isNull(n) {
			at = name
		}
		return scriptargs.Param{}, errAt(at, "a placeholder's rule must be a mapping of pattern, min and max, or values")
	}
	fields := map[string]*yaml.Node{}
	lines := map[string]int{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		if key.Kind != yaml.ScalarNode || !ruleKeys[key.Value] {
			return scriptargs.Param{}, errAt(key, "a rule takes only pattern, allow_leading_dash, allow_dotdot, unicode, min, max, values and default")
		}
		if line, dup := lines[key.Value]; dup {
			return scriptargs.Param{}, errAt(key, key.Value+" is already set at line "+strconv.Itoa(line))
		}
		lines[key.Value] = key.Line
		if key.Value != "values" && (val.Kind != yaml.ScalarNode || isNull(val)) {
			return scriptargs.Param{}, errAt(val, key.Value+" must be a single value")
		}
		fields[key.Value] = val
	}
	kinds := 0
	for _, k := range []string{"pattern", "min", "values"} {
		if fields[k] != nil || (k == "min" && fields["max"] != nil) {
			kinds++
		}
	}
	switch {
	case kinds != 1:
		return scriptargs.Param{}, errAt(name, "a placeholder needs exactly one rule: pattern, min and max, or values")
	case (fields["min"] == nil) != (fields["max"] == nil):
		return scriptargs.Param{}, errAt(name, "a range needs both min and max")
	case fields["allow_leading_dash"] != nil && fields["pattern"] == nil:
		return scriptargs.Param{}, errAt(name, "allow_leading_dash goes only with a pattern")
	case fields["allow_dotdot"] != nil && fields["pattern"] == nil:
		return scriptargs.Param{}, errAt(name, "allow_dotdot goes only with a pattern")
	case fields["unicode"] != nil && fields["min"] != nil:
		return scriptargs.Param{}, errAt(name, "unicode goes only with a pattern or values")
	}
	rule, err := ruleOf(n, fields)
	if err != nil {
		return scriptargs.Param{}, err
	}
	p := scriptargs.Param{Rule: rule}
	if d := fields["default"]; d != nil {
		p.Default, p.HasDefault = d.Value, true
	}
	return p, nil
}

// ruleOf builds the one rule fields hold; n is the rule mapping, where a
// fault of a range as a whole is reported.
func ruleOf(n *yaml.Node, fields map[string]*yaml.Node) (scriptargs.Rule, error) {
	switch {
	case fields["pattern"] != nil:
		var o scriptargs.PatternOptions
		var err error
		if o.AllowLeadingDash, err = flag(fields, "allow_leading_dash"); err != nil {
			return scriptargs.Rule{}, err
		}
		if o.AllowDotDot, err = flag(fields, "allow_dotdot"); err != nil {
			return scriptargs.Rule{}, err
		}
		if o.Unicode, err = flag(fields, "unicode"); err != nil {
			return scriptargs.Rule{}, err
		}
		r, err := scriptargs.PatternRule(fields["pattern"].Value, o)
		if err != nil {
			return scriptargs.Rule{}, errAt(fields["pattern"], err.Error())
		}
		return r, nil
	case fields["values"] != nil:
		list := fields["values"]
		if list.Kind != yaml.SequenceNode {
			return scriptargs.Rule{}, errAt(list, "values must be a list")
		}
		values := make([]string, 0, len(list.Content))
		for _, e := range list.Content {
			if e.Kind != yaml.ScalarNode || isNull(e) {
				return scriptargs.Rule{}, errAt(e, "a listed value must be a single value")
			}
			values = append(values, e.Value)
		}
		unicode, err := flag(fields, "unicode")
		if err != nil {
			return scriptargs.Rule{}, err
		}
		r, err := scriptargs.EnumRule(values, unicode)
		if err != nil {
			return scriptargs.Rule{}, errAt(list, err.Error())
		}
		return r, nil
	default:
		lo, err := wholeNumber(fields["min"], "min")
		if err != nil {
			return scriptargs.Rule{}, err
		}
		hi, err := wholeNumber(fields["max"], "max")
		if err != nil {
			return scriptargs.Rule{}, err
		}
		r, err := scriptargs.RangeRule(lo, hi)
		if err != nil {
			return scriptargs.Rule{}, errAt(n, err.Error())
		}
		return r, nil
	}
}

// wholeNumber reads a range bound written in decimal.
func wholeNumber(n *yaml.Node, key string) (int64, error) {
	v, err := strconv.ParseInt(n.Value, 10, 64)
	if err != nil {
		return 0, errAt(n, key+" must be a whole number")
	}
	return v, nil
}

// flag reads an optional true or false; absent is false.
func flag(fields map[string]*yaml.Node, key string) (bool, error) {
	n := fields[key]
	if n == nil {
		return false, nil
	}
	switch n.Value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, errAt(n, key+" must be true or false")
}
