package varsource

import (
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"gopkg.in/yaml.v3"
)

// promptFields maps a play's vars_prompt list onto top-level fields. A
// prompt has no type, so every field is a string, or a secret when the
// prompt is private (an absent private means true, as in Ansible). A secret
// carries no default; a prompt is required when it has no default, and a
// private prompt always is. At most schema.MaxFields entries are looked
// at, kept or not.
//
// y is the playbook file's read state: notes y raises (duplicate_key) stay
// in y, and its owner checks y.tooComplex when done with the file.
func promptFields(y *yamlFile, prompts *yaml.Node) ([]schema.Field, []Note) {
	file := y.file
	prompts = resolve(prompts)
	if isNull(prompts) {
		return nil, nil
	}
	if prompts.Kind != yaml.SequenceNode {
		return nil, []Note{warning(codeFieldInvalid, file, prompts.Line, "vars_prompt is not a list of prompts; nothing read from it")}
	}
	var fields []schema.Field
	var notes []Note
	for i, item := range y.items(prompts) {
		if i == schema.MaxFields {
			notes = append(notes, warning(codeTooManyFields, file, item.Line, "more than %d prompts; the rest are skipped", schema.MaxFields))
			break
		}
		f, n, ok := promptField(y, resolve(item), item.Line)
		notes = append(notes, n...)
		if ok {
			fields = append(fields, f)
		}
	}
	return fields, notes
}

func promptField(y *yamlFile, entry *yaml.Node, line int) (schema.Field, []Note, bool) {
	file := y.file
	if entry.Kind != yaml.MappingNode {
		return schema.Field{}, []Note{warning(codeFieldInvalid, file, line, "a vars_prompt entry must be a mapping; skipped")}, false
	}
	a := y.attrs(entry)
	name, ok := y.scalarText(a["name"])
	if !ok {
		return schema.Field{}, []Note{warning(codeInvalidName, file, line, "a vars_prompt entry without a name; skipped")}, false
	}
	private := true
	if p := a["private"]; !isNull(p) {
		private = y.boolValue(p)
	}
	label, _ := y.scalarText(a["prompt"])
	f := schema.Field{Name: name, Label: label, Type: schema.TypeString}
	if private {
		f.Type = schema.TypeSecret
	}
	var notes []Note
	d := a["default"]
	switch {
	case isNull(d):
	case private:
		notes = append(notes, warning(codeSecretDefaultDropped, file, line, "%s: a private prompt becomes a secret, which carries no default; the default is dropped", name))
	case d.Kind == yaml.ScalarNode:
		v, why := y.defaultValue(d, true) // a prompt answers with text
		if why != "" {
			notes = append(notes, warning(codeDefaultInvalid, file, line, "%s: the default %s; dropped", name, why))
		}
		f.Default = v
	default:
		notes = append(notes, warning(codeDefaultInvalid, file, line, "%s: a prompt's default must be a single value; dropped", name))
	}
	f.Required = private || isNull(d)
	f.Origin = schema.OriginSource
	f.Source = &schema.FieldSource{Kind: schema.SourceVarsPrompt, File: file, Line: line}
	f, n, ok := finishField(f, file, line)
	return f, append(notes, n...), ok
}
