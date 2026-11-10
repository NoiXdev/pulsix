package varsource

import (
	"strings"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"gopkg.in/yaml.v3"
)

// maxSpecFields bounds the options one argument spec file may map, sub-
// options included. A role's spec file maps every entry point it declares
// when it is parsed (roleSpec.extract), so the bound is shared among them,
// and each entry point costs one too: a file that uses more is given up as
// file_too_complex with nothing read from it (backlog 7.113). A playbook's
// sidecar maps one entry point, and a single entry point past the bound is
// cut (too_many_fields). Anchors and aliases can make a small file describe an
// exponentially large tree; the top-level option that runs out of budget is
// skipped whole, with the rest.
const maxSpecFields = 10_000

// argumentSpecFields maps one entry point of an argument spec file (a role's
// meta/argument_specs.yml, or a playbook's sidecar) onto top-level fields,
// in the order the file declares them. entryPoint "" means "main". readOK
// is false when the file was not read: not valid YAML (one invalid_yaml
// warning), or too complex to read within the budget (one file_too_complex
// warning). entryFound is false when the file lacks the entry point (one
// hint).
func argumentSpecFields(data []byte, file, role, entryPoint string, kind schema.SourceKind) (fields []schema.Field, notes []Note, readOK, entryFound bool) {
	return readArgumentSpec(newYAMLFile(file), data, role, entryPoint, kind)
}

// readArgumentSpec is argumentSpecFields over a given yamlFile, so a test
// can see the work it took.
func readArgumentSpec(y *yamlFile, data []byte, role, entryPoint string, kind schema.SourceKind) (fields []schema.Field, notes []Note, readOK, entryFound bool) {
	r := readSpecData(y, data, role, entryPoint, kind)
	return r.fields, r.notes, r.readOK, r.entryFound
}

// specResult is one entry point read from an argument spec file.
type specResult struct {
	fields     []schema.Field
	notes      []Note
	readOK     bool // false: not valid YAML, or too complex (one note says which)
	entryFound bool
	broken     bool     // the entry point or its options are not a mapping: none of it could be read
	cut        bool     // top-level options were left out by a cap
	unreadable []string // top-level options declared but mapped onto no field
}

func readSpecData(y *yamlFile, data []byte, role, entryPoint string, kind schema.SourceKind) specResult {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return specResult{notes: []Note{invalidYAML(y.file, err)}}
	}
	return readSpecRoot(y, documentRoot(&doc), role, entryPoint, kind)
}

// readSpecRoot is readSpec over a parsed document's root.
func readSpecRoot(y *yamlFile, root *yaml.Node, role, entryPoint string, kind schema.SourceKind) specResult {
	if entryPoint == "" {
		entryPoint = "main"
	}
	r := mapEntryPoint(y, root, role, entryPoint, kind)
	if n, over := y.tooComplex(); over {
		return specResult{notes: []Note{n}}
	}
	r.notes, r.readOK = y.takeNotes(), true
	return r
}

// locateSpecs finds the argument_specs entry of a spec file's root. bad is
// the note why the file or its argument_specs is not a mapping, nil when it
// is (or argument_specs is absent or null).
func locateSpecs(y *yamlFile, root *yaml.Node) (specs pair, hasSpecs bool, bad *Note) {
	if !isNull(root) && root.Kind != yaml.MappingNode {
		n := warning(codeFieldInvalid, y.file, root.Line, "the file is not a mapping; nothing read")
		return pair{}, false, &n
	}
	specs, hasSpecs = y.entry(root, "argument_specs")
	if v := resolve(specs.value); hasSpecs && !isNull(v) && v.Kind != yaml.MappingNode {
		n := warning(codeFieldInvalid, y.file, specs.key.Line, "argument_specs is not a mapping of entry points; nothing read")
		return pair{}, false, &n
	}
	return specs, hasSpecs, nil
}

// missingEntryPoint is the hint for an entry point argument_specs lacks;
// line is the argument_specs key's line, 0 without one.
func missingEntryPoint(file string, line int, entryPoint string) Note {
	return hint(codeEntryPointMissing, file, line, "no entry point %q under argument_specs; no fields read from this file", short(entryPoint))
}

// mapEntryPoint maps the options of one entry point, noting into y.
func mapEntryPoint(y *yamlFile, root *yaml.Node, role, entryPoint string, kind schema.SourceKind) specResult {
	specs, hasSpecs, bad := locateSpecs(y, root)
	if bad != nil {
		y.note(*bad)
		return specResult{broken: true}
	}
	ep, found := y.entry(specs.value, entryPoint)
	if !found {
		line := 0
		if hasSpecs {
			line = specs.key.Line
		}
		y.note(missingEntryPoint(y.file, line, entryPoint))
		return specResult{}
	}
	return mapEntry(y, &specMapper{y: y, budget: maxSpecFields}, ep, role, entryPoint, kind)
}

// mapEntry maps the options of the entry point ep of argument_specs with
// m's budget, which a caller mapping several entry points of one file
// shares among them (see roleSpec.extract).
func mapEntry(y *yamlFile, m *specMapper, ep pair, role, entryPoint string, kind schema.SourceKind) specResult {
	r := specResult{entryFound: true}
	entry := resolve(ep.value)
	if !isNull(entry) && entry.Kind != yaml.MappingNode {
		m.warn(codeFieldInvalid, ep.key.Line, "entry point %s is not a mapping; nothing read", short(entryPoint))
		r.broken = true
		return r
	}
	options := y.lookup(entry, "options")
	if !isNull(options) && options.Kind != yaml.MappingNode {
		m.warn(codeFieldInvalid, options.Line, "the options of entry point %s are not a mapping; nothing read", short(entryPoint))
		r.broken = true
	}
	for _, p := range y.pairs(options) {
		if len(r.fields) == schema.MaxFields {
			m.warn(codeTooManyFields, p.key.Line, "entry point %s declares more than %d options; the rest are skipped", short(entryPoint), schema.MaxFields)
			r.cut = true
			break
		}
		f, ok := m.option(p, "", 0)
		if m.exhausted {
			m.warn(codeTooManyFields, p.key.Line, "%s: this file declares more than %d options in all (aliases expanded); this option and the rest are skipped", short(p.key.Value), maxSpecFields)
			r.cut = true
			break
		}
		if ok {
			f.Origin = schema.OriginSource
			f.Source = &schema.FieldSource{Kind: kind, Role: role, EntryPoint: entryPoint, File: y.file, Line: p.key.Line}
			var n []Note
			f, n, ok = finishField(f, y.file, p.key.Line)
			y.note(n...)
		}
		if !ok {
			r.unreadable = append(r.unreadable, p.key.Value)
			continue
		}
		r.fields = append(r.fields, f)
	}
	return r
}

// specMapper maps argument spec options onto fields; its notes go to the
// file's.
type specMapper struct {
	y         *yamlFile
	budget    int
	exhausted bool // the budget ran out; the option being mapped is incomplete
}

func (m *specMapper) warn(code string, line int, format string, args ...any) {
	m.y.note(warning(code, m.y.file, line, format, args...))
}

// scalarTypes are the argument spec types with a scalar field counterpart.
var scalarTypes = map[string]schema.Type{
	"str":   schema.TypeString,
	"path":  schema.TypeString,
	"int":   schema.TypeInt,
	"float": schema.TypeFloat,
	"bool":  schema.TypeBool,
}

// option maps one option; parent is the dotted name of the option it sits
// in ("" at the top level, depth 0). Sub-options are mapped down to
// schema.MaxDepth; finishField cuts there.
func (m *specMapper) option(p pair, parent string, depth int) (schema.Field, bool) {
	line := p.key.Line
	if m.budget <= 0 {
		m.exhausted = true
		return schema.Field{}, false
	}
	m.budget--
	if !m.y.spend(1) { // an option costs a visit even where an alias makes its spec free to read (backlog 7.113)
		m.exhausted = true
		return schema.Field{}, false
	}
	if len(p.key.Value) > maxNameBytes {
		m.warn(codeInvalidName, line, "%q is longer than %d characters, not a usable variable name; skipped", short(p.key.Value), maxNameBytes)
		return schema.Field{}, false
	}
	path := p.key.Value
	if parent != "" {
		path = parent + "." + path
	}
	spec := resolve(p.value)
	if spec == nil || spec.Kind != yaml.MappingNode {
		m.warn(codeFieldInvalid, line, "%s: an option's spec must be a mapping; skipped", path)
		return schema.Field{}, false
	}
	a := m.y.attrs(spec)
	f := schema.Field{Name: p.key.Value}
	if !m.mapType(&f, a, path, line) {
		return schema.Field{}, false
	}
	if m.y.boolValue(a["no_log"]) {
		if !m.makeSecret(&f, path, line) {
			return schema.Field{}, false
		}
	} else if !m.applyChoices(&f, a["choices"], path, line) {
		return schema.Field{}, false
	}
	f.Required = m.y.boolValue(a["required"])
	f.Label, f.Description = m.labelAndDescription(a["description"])
	m.applyDefault(&f, a["default"], path, line)
	if (f.Type == schema.TypeRepeater || f.Type == schema.TypeObject) && depth+1 <= schema.MaxDepth {
		for _, sp := range m.y.pairs(a["options"]) {
			if sub, ok := m.option(sp, path, depth+1); ok {
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
		typ, _ = m.y.scalarText(n)
	}
	hasOptions := len(m.y.pairs(a["options"])) > 0
	if t, ok := scalarTypes[typ]; ok {
		f.Type = t
		return true
	}
	switch typ {
	case "list":
		elements, _ := m.y.scalarText(a["elements"])
		if t, ok := scalarTypes[elements]; ok {
			f.Type, f.Item = schema.TypeList, &schema.Field{Type: t}
			return true
		}
		if elements == "dict" && hasOptions {
			f.Type = schema.TypeRepeater
			return true
		}
		m.warn(codeUnsupportedType, line, "%s: a list needs scalar elements, or elements: dict with options (elements %q); skipped", path, short(elements))
	case "dict":
		if hasOptions {
			f.Type = schema.TypeObject
			return true
		}
		m.warn(codeUnsupportedType, line, "%s: a dict without options has no form field; skipped", path)
	default:
		m.warn(codeUnsupportedType, line, "%s: type %q has no form field; skipped", path, short(typ))
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
	if !choicesApply(target.Type) {
		return true
	}
	if choices.Kind == yaml.SequenceNode && len(choices.Content) == 0 {
		return true // an empty choices list restricts nothing
	}
	if choices.Kind != yaml.SequenceNode {
		m.warn(codeFieldInvalid, line, "%s: choices must be a list; skipped", path)
		return false
	}
	if len(choices.Content) > schema.MaxOptions {
		m.warn(codeTooManyOptions, line, "%s: more than %d choices; skipped", path, schema.MaxOptions)
		return false
	}
	options := make([]string, 0, len(choices.Content))
	converted := false
	for _, c := range m.y.items(choices) {
		c = resolve(c)
		if c.Kind != yaml.ScalarNode {
			m.warn(codeFieldInvalid, line, "%s: choices must be single values; skipped", path)
			return false
		}
		text, _ := m.y.scalarText(c)
		if t, _, _ := scalarOf(c); t != schema.TypeString {
			converted = true // a long number is not decoded; it stays its text
		}
		options = append(options, text)
	}
	if converted {
		m.warn(codeChoicesConverted, line, "%s: choices that are not strings become strings as written", path)
	}
	target.Type, target.Options = schema.TypeEnum, options
	return true
}

// choicesApply reports whether choices turn a field of type t into an
// enum. A bool already offers exactly its two values, and an enum of
// strings would send text where the role expects a boolean, so choices on
// a bool are ignored.
func choicesApply(t schema.Type) bool {
	return t == schema.TypeString || t == schema.TypeInt || t == schema.TypeFloat
}

// textual reports whether a field of type t holds text, so a scalar default
// is taken as written ("8080" for a str option, like Ansible's conversion).
func textual(t schema.Type) bool {
	return t == schema.TypeString || t == schema.TypeEnum
}

// applyDefault sets f's default from the spec. A scalar default of a
// string or enum field (or of a list of them) is taken as its text: enums
// compare as text, like the choices. schema.Check (in finishField) decides whether
// the field accepts the value.
func (m *specMapper) applyDefault(f *schema.Field, d *yaml.Node, path string, line int) {
	if isNull(d) {
		return
	}
	switch f.Type {
	case schema.TypeSecret:
		m.warn(codeSecretDefaultDropped, line, "%s: a secret carries no default; the default is dropped", path)
		return
	case schema.TypeRepeater, schema.TypeObject:
		m.warn(codeDefaultDropped, line, "%s: a %s field carries no default; the default is dropped", path, f.Type)
		return
	}
	target := f.Type
	if f.Type == schema.TypeList {
		target = f.Item.Type
	}
	v, why := m.y.defaultValue(d, textual(target))
	if why != "" {
		m.warn(codeDefaultInvalid, line, "%s: the default %s; dropped", path, why)
		return
	}
	f.Default = v
}

// labelAndDescription splits an argument spec description (a string, or a
// list of lines) into its first line, the label, and the rest. A first
// line too long for a label is kept whole in the description (finishField
// then cuts the label at a word). A list is read only until the
// description passes its cap.
func (m *specMapper) labelAndDescription(n *yaml.Node) (string, string) {
	n = resolve(n)
	if n == nil {
		return "", ""
	}
	parts := []*yaml.Node{n}
	if n.Kind == yaml.SequenceNode {
		parts = m.y.items(n)
	}
	var b strings.Builder
	firstNL := -1 // where the first line ends
	for _, it := range parts {
		descStart := 0 // the first line counts toward the description when it is too long for a label
		if firstNL >= 0 && firstNL <= maxLabelBytes {
			descStart = firstNL
		}
		if (firstNL >= 0 || b.Len() > maxLabelBytes) && b.Len()-descStart > maxDescriptionBytes+1 {
			break // the description's cap is reached: the rest would be cut anyway
		}
		text, ok := m.y.scalarText(it)
		if !ok {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
		if firstNL < 0 {
			firstNL = strings.IndexByte(b.String(), '\n')
		}
	}
	text := strings.TrimSpace(b.String())
	label, rest, _ := strings.Cut(text, "\n")
	if label = strings.TrimSpace(label); len(label) > maxLabelBytes {
		return label, text
	}
	return label, strings.TrimSpace(rest)
}
