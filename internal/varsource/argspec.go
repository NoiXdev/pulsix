package varsource

import (
	"strings"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"gopkg.in/yaml.v3"
)

// maxSpecFields bounds the options one argument spec file may map, sub-
// options included. Anchors and aliases can make a small file describe an
// exponentially large tree; the top-level option that runs out of budget is
// skipped whole, with the rest (too_many_fields).
const maxSpecFields = 10_000

// argumentSpecFields maps one entry point of an argument spec file (a role's
// meta/argument_specs.yml, or a playbook's sidecar) onto top-level fields,
// in the order the file declares them. entryPoint "" means "main". readOK
// is false when the file is not valid YAML (one invalid_yaml warning);
// entryFound is false when the file lacks the entry point (one hint).
func argumentSpecFields(data []byte, file, role, entryPoint string, kind schema.SourceKind) (fields []schema.Field, notes []Note, readOK, entryFound bool) {
	if entryPoint == "" {
		entryPoint = "main"
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, []Note{invalidYAML(file, err)}, false, false
	}
	entry := lookup(lookup(documentRoot(&doc), "argument_specs"), entryPoint)
	if entry == nil {
		return nil, []Note{hint(codeEntryPointMissing, file, 0, "no entry point %q under argument_specs; no fields read from this file", entryPoint)}, true, false
	}
	m := specMapper{file: file, budget: maxSpecFields}
	options := lookup(entry, "options")
	if !isNull(options) && options.Kind != yaml.MappingNode {
		m.warn(codeFieldInvalid, options.Line, "the options of entry point %s are not a mapping; nothing read", entryPoint)
	}
	for _, p := range pairs(options) {
		if len(fields) == schema.MaxFields {
			m.warn(codeTooManyFields, p.key.Line, "entry point %s declares more than %d options; the rest are skipped", entryPoint, schema.MaxFields)
			break
		}
		f, ok := m.option(p, p.key.Value, 0)
		if m.exhausted {
			m.warn(codeTooManyFields, p.key.Line, "%s: this file declares more than %d options in all (aliases expanded); this option and the rest are skipped", p.key.Value, maxSpecFields)
			break
		}
		if !ok {
			continue
		}
		f.Origin = schema.OriginSource
		f.Source = &schema.FieldSource{Kind: kind, Role: role, EntryPoint: entryPoint, File: file, Line: p.key.Line}
		f, n, ok := finishField(f, file, p.key.Line)
		m.notes = append(m.notes, n...)
		if ok {
			fields = append(fields, f)
		}
	}
	return fields, m.notes, true, true
}

// specMapper maps argument spec options onto fields, recording notes.
type specMapper struct {
	file      string
	budget    int
	exhausted bool // the budget ran out; the option being mapped is incomplete
	notes     []Note
}

func (m *specMapper) warn(code string, line int, format string, args ...any) {
	m.notes = append(m.notes, warning(code, m.file, line, format, args...))
}

// scalarTypes are the argument spec types with a scalar field counterpart.
var scalarTypes = map[string]schema.Type{
	"str":   schema.TypeString,
	"path":  schema.TypeString,
	"int":   schema.TypeInt,
	"float": schema.TypeFloat,
	"bool":  schema.TypeBool,
}

// option maps one option; path is its dotted name, depth 0 the top level.
// Sub-options are mapped down to schema.MaxDepth; finishField cuts there.
func (m *specMapper) option(p pair, path string, depth int) (schema.Field, bool) {
	line := p.key.Line
	if m.budget <= 0 {
		m.exhausted = true
		return schema.Field{}, false
	}
	m.budget--
	spec := resolve(p.value)
	if spec == nil || spec.Kind != yaml.MappingNode {
		m.warn(codeFieldInvalid, line, "%s: an option's spec must be a mapping; skipped", path)
		return schema.Field{}, false
	}
	a := attrs(spec)
	f := schema.Field{Name: p.key.Value}
	if !m.mapType(&f, a, path, line) {
		return schema.Field{}, false
	}
	if boolValue(a["no_log"]) {
		if !m.makeSecret(&f, path, line) {
			return schema.Field{}, false
		}
	} else if !m.applyChoices(&f, a["choices"], path, line) {
		return schema.Field{}, false
	}
	f.Required = boolValue(a["required"])
	f.Label, f.Description = labelAndDescription(a["description"])
	m.applyDefault(&f, a["default"], path, line)
	if (f.Type == schema.TypeRepeater || f.Type == schema.TypeObject) && depth+1 <= schema.MaxDepth {
		for _, sp := range pairs(a["options"]) {
			if sub, ok := m.option(sp, path+"."+sp.key.Value, depth+1); ok {
				f.Fields = append(f.Fields, sub)
			}
		}
	}
	return f, true
}

// mapType sets f's type from the spec's type, elements and options, or
// reports the type as unsupported.
func (m *specMapper) mapType(f *schema.Field, a map[string]*yaml.Node, path string, line int) bool {
	typ := "str"
	if n := a["type"]; !isNull(n) {
		typ, _ = scalarText(n)
	}
	hasOptions := len(pairs(a["options"])) > 0
	if t, ok := scalarTypes[typ]; ok {
		f.Type = t
		return true
	}
	switch typ {
	case "list":
		elements, _ := scalarText(a["elements"])
		if t, ok := scalarTypes[elements]; ok {
			f.Type, f.Item = schema.TypeList, &schema.Field{Type: t}
			return true
		}
		if elements == "dict" && hasOptions {
			f.Type = schema.TypeRepeater
			return true
		}
		m.warn(codeUnsupportedType, line, "%s: a list needs scalar elements, or elements: dict with options (elements %q); skipped", path, elements)
	case "dict":
		if hasOptions {
			f.Type = schema.TypeObject
			return true
		}
		m.warn(codeUnsupportedType, line, "%s: a dict without options has no form field; skipped", path)
	default:
		m.warn(codeUnsupportedType, line, "%s: type %q has no form field; skipped", path, typ)
	}
	return false
}

// makeSecret turns a no_log option into a secret; a collection cannot be one.
func (m *specMapper) makeSecret(f *schema.Field, path string, line int) bool {
	switch f.Type {
	case schema.TypeList, schema.TypeRepeater, schema.TypeObject:
		m.warn(codeNoLogCollection, line, "%s: no_log on a %s has no form field (secrets are single values); skipped", path, f.Type)
		return false
	}
	f.Type, f.Item = schema.TypeSecret, nil
	return true
}

// applyChoices turns a scalar field (or a list's items) into an enum whose
// options are the choices as strings.
func (m *specMapper) applyChoices(f *schema.Field, choices *yaml.Node, path string, line int) bool {
	if isNull(choices) {
		return true
	}
	target := f
	if f.Type == schema.TypeList {
		target = f.Item
	}
	if !scalarField(target.Type) {
		return true
	}
	if choices.Kind != yaml.SequenceNode {
		m.warn(codeFieldInvalid, line, "%s: choices must be a list; skipped", path)
		return false
	}
	options := make([]string, 0, len(choices.Content))
	converted := false
	for _, c := range choices.Content {
		c = resolve(c)
		if c.Kind != yaml.ScalarNode {
			m.warn(codeFieldInvalid, line, "%s: choices must be single values; skipped", path)
			return false
		}
		if c.ShortTag() != "!!str" {
			converted = true
		}
		options = append(options, c.Value)
	}
	if converted {
		m.warn(codeChoicesConverted, line, "%s: choices that are not strings become the strings %s", path, strings.Join(options, ", "))
	}
	target.Type, target.Options = schema.TypeEnum, options
	return true
}

// scalarField reports whether t is a scalar type choices can turn into an
// enum.
func scalarField(t schema.Type) bool {
	switch t {
	case schema.TypeString, schema.TypeInt, schema.TypeFloat, schema.TypeBool:
		return true
	}
	return false
}

// applyDefault sets f's default from the spec. Enum defaults are compared
// as text, like the choices. schema.Check (in finishField) decides whether
// the field accepts the value.
func (m *specMapper) applyDefault(f *schema.Field, d *yaml.Node, path string, line int) {
	if isNull(d) {
		return
	}
	switch {
	case f.Type == schema.TypeSecret:
		m.warn(codeSecretDefaultDropped, line, "%s: a secret carries no default; the default is dropped", path)
		return
	case f.Type == schema.TypeRepeater || f.Type == schema.TypeObject:
		m.warn(codeDefaultDropped, line, "%s: a %s field carries no default; the default is dropped", path, f.Type)
		return
	case f.Type == schema.TypeEnum && d.Kind == yaml.ScalarNode:
		f.Default = d.Value
		return
	case f.Type == schema.TypeList && f.Item.Type == schema.TypeEnum && d.Kind == yaml.SequenceNode:
		if texts, ok := scalarTexts(d); ok {
			f.Default = texts
			return
		}
	}
	v, ok := defaultValue(d)
	if !ok {
		m.warn(codeDefaultInvalid, line, "%s: the default is not a value this field can hold; dropped", path)
		return
	}
	f.Default = v
}

// scalarTexts is the text of each item of a list of scalars.
func scalarTexts(n *yaml.Node) ([]any, bool) {
	out := make([]any, 0, len(n.Content))
	for _, it := range n.Content {
		s, ok := scalarText(it)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// labelAndDescription splits an argument spec description (a string, or a
// list of lines) into its first line, the label, and the rest.
func labelAndDescription(n *yaml.Node) (string, string) {
	var text string
	switch n = resolve(n); {
	case n == nil:
		return "", ""
	case n.Kind == yaml.ScalarNode && !isNull(n):
		text = n.Value
	case n.Kind == yaml.SequenceNode:
		lines := make([]string, 0, len(n.Content))
		for _, it := range n.Content {
			if s, ok := scalarText(it); ok {
				lines = append(lines, s)
			}
		}
		text = strings.Join(lines, "\n")
	}
	label, rest, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(label), strings.TrimSpace(rest)
}
