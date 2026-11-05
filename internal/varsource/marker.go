package varsource

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"gopkg.in/yaml.v3"
)

// marker is a parsed "# pulsix:" comment on a play variable. Grammar (the
// user documentation's reference copies it):
//
//	marker-comment = "#" , { space } , "pulsix:" , [ space1 , body ] , { space } ;
//	body           = keyword-list , [ space1 , label ] | label ;
//	keyword-list   = keyword , { space1 , keyword } ;
//	keyword        = enum | "secret" | "required" ;
//	enum           = "enum[" , option , { "," , option } , "]" ;
//	option         = { space } , option-char , { option-char } , { space } ;   (* trimmed *)
//	option-char    = ? any character except "," , "]" and a line break ? ;
//	label          = ? the rest of the line, trimmed; the first token that is not a keyword starts it ? ;
//	space          = " " | "\t" ;   space1 = space , { space } ;
//
// A repeated keyword, enum together with secret, an "enum[" without "]",
// an empty or duplicate option, or more than schema.MaxOptions options make
// the marker unreadable.
type marker struct {
	Enum     []string
	Secret   bool
	Required bool
	Label    string
}

const markerPrefix = "pulsix:"

// parseMarker reads a YAML line comment (with its "#"). isMarker is true
// when the comment opens with "pulsix:"; err says why such a comment is
// unreadable.
func parseMarker(comment string) (m marker, isMarker bool, err error) {
	body, ok := strings.CutPrefix(comment, "#")
	if !ok {
		return marker{}, false, nil
	}
	body, ok = strings.CutPrefix(strings.TrimLeft(body, " \t"), markerPrefix)
	if !ok {
		return marker{}, false, nil
	}
	if body != "" && !isBlank(body[0]) {
		return marker{}, true, errors.New(`"pulsix:" must be followed by a space`)
	}
	m, err = parseMarkerBody(strings.Trim(body, " \t"))
	return m, true, err
}

func parseMarkerBody(body string) (marker, error) {
	var m marker
	seen := map[string]bool{}
	for body != "" {
		var keyword, rest string
		if after, ok := strings.CutPrefix(body, "enum["); ok {
			list, tail, closed := strings.Cut(after, "]")
			if !closed {
				return marker{}, errors.New(`"enum[" without a closing "]"`)
			}
			options, err := parseEnumOptions(list)
			if err != nil {
				return marker{}, err
			}
			keyword, rest, m.Enum = "enum", tail, options
		} else {
			token, tail := cutToken(body)
			switch token {
			case "secret":
				m.Secret = true
			case "required":
				m.Required = true
			default:
				m.Label = body
				return finishMarker(m)
			}
			keyword, rest = token, tail
		}
		if seen[keyword] {
			return marker{}, fmt.Errorf("%q appears twice", keyword)
		}
		seen[keyword] = true
		if rest != "" && !isBlank(rest[0]) {
			return marker{}, fmt.Errorf("%q must be followed by a space", keyword)
		}
		body = strings.TrimLeft(rest, " \t")
	}
	return finishMarker(m)
}

func finishMarker(m marker) (marker, error) {
	if m.Secret && m.Enum != nil {
		return marker{}, errors.New(`"enum" and "secret" cannot be combined`)
	}
	return m, nil
}

func parseEnumOptions(list string) ([]string, error) {
	parts := strings.Split(list, ",")
	if len(parts) > schema.MaxOptions {
		return nil, fmt.Errorf("more than %d enum options", schema.MaxOptions)
	}
	options := make([]string, 0, len(parts))
	for _, p := range parts {
		o := strings.Trim(p, " \t")
		switch {
		case o == "":
			return nil, errors.New("an empty enum option")
		case slices.Contains(options, o):
			return nil, fmt.Errorf("the enum option %q appears twice", o)
		}
		options = append(options, o)
	}
	return options, nil
}

// cutToken splits body at its first space or tab.
func cutToken(body string) (token, rest string) {
	if i := strings.IndexAny(body, " \t"); i >= 0 {
		return body[:i], body[i:]
	}
	return body, ""
}

func isBlank(b byte) bool { return b == ' ' || b == '\t' }

// lineComment is the comment a marker is read from: the value's line
// comment ("name: value  # …"), else the key's ("name:  # …" before a
// block value or nothing).
func lineComment(key, value *yaml.Node) string {
	if value != nil && value.LineComment != "" {
		return value.LineComment
	}
	return key.LineComment
}

// markedVar reads the marker of one top-level entry of a play's vars:.
// isMarker is false when the entry carries no "# pulsix:" comment; an
// unreadable marker is a marker_unreadable warning and no field.
// y is the playbook file's read state; its owner checks y.tooComplex when
// done with the file.
func markedVar(y *yamlFile, key, value *yaml.Node) (f schema.Field, notes []Note, isMarker, ok bool) {
	file := y.file
	m, isMarker, err := parseMarker(lineComment(key, value))
	if !isMarker {
		return schema.Field{}, nil, false, false
	}
	if err != nil {
		return schema.Field{}, []Note{warning(codeMarkerUnreadable, file, key.Line, "%s: unreadable marker (%v); the variable is not a form field", key.Value, err)}, true, false
	}
	f, notes, ok = markerField(y, key, value, m)
	return f, notes, true, ok
}

// markerField maps a marked play variable onto a top-level field: its value
// is the default and its YAML type sets the field type (string, int, float,
// bool; a list of scalars of one type is a list). enum makes an enum the
// value must be an option of; secret makes a secret without default.
func markerField(y *yamlFile, key, value *yaml.Node, m marker) (schema.Field, []Note, bool) {
	file := y.file
	line := key.Line
	v := resolve(value)
	if v == nil {
		v = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}
	}
	valueType := func(format string, args ...any) (schema.Field, []Note, bool) {
		msg := fmt.Sprintf(format, args...)
		return schema.Field{}, []Note{warning(codeMarkerValueType, file, line, "%s: %s; the variable is not a form field", key.Value, msg)}, false
	}
	f := schema.Field{Name: key.Value, Label: m.Label, Required: m.Required}
	switch {
	case m.Secret:
		if v.Kind != yaml.ScalarNode {
			return valueType("a secret takes a single value")
		}
		f.Type = schema.TypeSecret
	case m.Enum != nil:
		if v.Kind != yaml.ScalarNode {
			return valueType("an enum takes a single value")
		}
		f.Type, f.Options = schema.TypeEnum, m.Enum
		if !isNull(v) {
			size := 0
			if _, _, why := y.readScalar(v, &size); why != "" && !longNumber(v) {
				return valueType("the value %s", why)
			}
			if !slices.Contains(m.Enum, v.Value) {
				return schema.Field{}, []Note{warning(codeMarkerEnumValue, file, line, "%s: the value %q is not one of %s; the variable is not a form field", key.Value, v.Value, strings.Join(m.Enum, ", "))}, false
			}
			f.Default = v.Value
		}
	default:
		typ, item, def, why := inferField(y, v)
		if why != "" {
			return valueType("%s", why)
		}
		f.Type, f.Item, f.Default = typ, item, def
	}
	f.Origin = schema.OriginSource
	f.Source = &schema.FieldSource{Kind: schema.SourceMarker, File: file, Line: line}
	return finishField(f, file, line)
}

// inferField derives a field type from a value: a scalar's tag, or a list
// whose items are scalars of one type. Its bytes count against the file's
// budget and against maxDefaultBytes. why says what does not fit.
func inferField(y *yamlFile, v *yaml.Node) (typ schema.Type, item *schema.Field, def any, why string) {
	size := 0
	switch v.Kind {
	case yaml.ScalarNode:
		t, val, why := y.readScalar(v, &size)
		if why != "" {
			return "", nil, nil, "the value " + why
		}
		if t == "" {
			t = schema.TypeString
		}
		return t, nil, val, ""
	case yaml.SequenceNode:
		itemType := schema.TypeString
		items := make([]any, 0, len(v.Content))
		for i, it := range y.items(v) {
			it = resolve(it)
			if it.Kind != yaml.ScalarNode || isNull(it) {
				return "", nil, nil, "a list must hold single values that are not empty"
			}
			t, val, why := y.readScalar(it, &size)
			if why != "" {
				return "", nil, nil, "the list " + why
			}
			if i > 0 && t != itemType {
				return "", nil, nil, "a list must hold values of one type"
			}
			itemType = t
			items = append(items, val)
		}
		return schema.TypeList, &schema.Field{Type: itemType}, items, ""
	}
	return "", nil, nil, "a mapping has no form field"
}
