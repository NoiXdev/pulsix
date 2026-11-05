package varsource

import (
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// finishField makes a mapped top-level field fit the schema's rules, or
// skips it: a name outside the field name rule skips it (invalid_name);
// sub-fields nested deeper than schema.MaxDepth are cut (too_deep), more
// than schema.MaxFields per level are cut after the first ones
// (too_many_fields), and a field with more than schema.MaxOptions options is
// skipped (too_many_options). A templated default is dropped
// (templated_default). Finally the field goes through schema.Check: a
// default the field does not accept is dropped (default_invalid), any other
// problem skips the field (field_invalid). Notes about nested fields carry
// the top-level field's line and name the field by its dotted path.
//
// The caller sets Origin and Source before; finishField keeps them.
func finishField(f schema.Field, file string, line int) (schema.Field, []Note, bool) {
	fin := finisher{file: file, line: line}
	if !schema.ValidFieldName(f.Name) || schema.ReservedName(f.Name) {
		fin.warn(codeInvalidName, "%q is not a usable variable name (a lowercase letter or underscore, then lowercase letters, digits and underscores, at most 64; not ansible_* or pulsix_*); skipped", f.Name)
		return schema.Field{}, fin.notes, false
	}
	f, ok := fin.prune(f, f.Name, 0)
	if !ok {
		return schema.Field{}, fin.notes, false
	}
	f, ok = fin.check(f)
	if !ok {
		return schema.Field{}, fin.notes, false
	}
	return f, fin.notes, true
}

type finisher struct {
	file  string
	line  int
	notes []Note
}

func (fin *finisher) warn(code, format string, args ...any) {
	fin.notes = append(fin.notes, warning(code, fin.file, fin.line, format, args...))
}

// prune applies the limits to f and its sub-fields; path is f's dotted name.
func (fin *finisher) prune(f schema.Field, path string, depth int) (schema.Field, bool) {
	if depth > 0 && !schema.ValidFieldName(f.Name) {
		fin.warn(codeInvalidName, "%s: %q is not a usable field name; skipped", path, f.Name)
		return f, false
	}
	if len(f.Options) > schema.MaxOptions || (f.Item != nil && len(f.Item.Options) > schema.MaxOptions) {
		fin.warn(codeTooManyOptions, "%s: more than %d choices; skipped", path, schema.MaxOptions)
		return f, false
	}
	f.Default = fin.literalDefault(f.Default, path)
	if f.Type != schema.TypeRepeater && f.Type != schema.TypeObject {
		return f, true
	}
	if depth+1 > schema.MaxDepth {
		fin.warn(codeTooDeep, "%s: nested deeper than %d levels; cut here", path, schema.MaxDepth)
		return f, false
	}
	kept := make([]schema.Field, 0, len(f.Fields))
	for _, sub := range f.Fields {
		if s, ok := fin.prune(sub, path+"."+sub.Name, depth+1); ok {
			kept = append(kept, s)
		}
	}
	if len(kept) > schema.MaxFields {
		fin.warn(codeTooManyFields, "%s: more than %d sub-fields; the first %d are kept", path, schema.MaxFields, schema.MaxFields)
		kept = kept[:schema.MaxFields]
	}
	if len(kept) == 0 {
		fin.warn(codeFieldInvalid, "%s: none of its sub-fields could be read; skipped", path)
		return f, false
	}
	f.Fields = kept
	return f, true
}

// literalDefault drops a default Ansible would template at run time, and
// one no JSON document can carry (NaN, ±Inf).
func (fin *finisher) literalDefault(v any, path string) any {
	switch {
	case v == nil:
		return nil
	case templated(v):
		fin.notes = append(fin.notes, hint(codeTemplatedDefault, fin.file, fin.line,
			"%s: the default is a template the run evaluates; the form shows no default", path))
		return nil
	case !finite(v):
		fin.warn(codeDefaultInvalid, "%s: the default %v is not a finite number; dropped", path, v)
		return nil
	}
	return v
}

func templated(v any) bool {
	switch x := v.(type) {
	case string:
		return strings.Contains(x, "{{") || strings.Contains(x, "{%")
	case []any:
		for _, it := range x {
			if templated(it) {
				return true
			}
		}
	}
	return false
}

func finite(v any) bool {
	switch x := v.(type) {
	case float64:
		return !math.IsNaN(x) && !math.IsInf(x, 0)
	case []any:
		for _, it := range x {
			if !finite(it) {
				return false
			}
		}
	}
	return true
}

// check runs schema.Check over f alone. Defaults the check refuses are
// dropped; any other problem skips the field.
func (fin *finisher) check(f schema.Field) (schema.Field, bool) {
	err := schema.Schema{Fields: []schema.Field{f}}.Check()
	if err == nil {
		return f, true
	}
	var ve *schema.ValidationError
	if !errors.As(err, &ve) {
		fin.warn(codeFieldInvalid, "%s: %v; skipped", f.Name, err)
		return f, false
	}
	for _, p := range ve.Problems {
		if owner, _ := defaultOwner(&f, p.Path); owner == nil || owner.Default == nil {
			fin.warn(codeFieldInvalid, "%s: %s; skipped", f.Name, p.Message)
			return f, false
		}
	}
	for _, p := range ve.Problems {
		owner, path := defaultOwner(&f, p.Path)
		fin.warn(codeDefaultInvalid, "%s: the default %v %s; dropped", path, owner.Default, p.Message)
		owner.Default = nil
	}
	if err := (schema.Schema{Fields: []schema.Field{f}}).Check(); err != nil {
		fin.warn(codeFieldInvalid, "%s: %v; skipped", f.Name, err)
		return f, false
	}
	return f, true
}

// defaultOwner finds the field a Check problem path such as
// "fields[0].fields[2].default" is about, with its dotted name; nil when
// the path is not about a default.
func defaultOwner(top *schema.Field, problemPath string) (*schema.Field, string) {
	rest, ok := strings.CutSuffix(problemPath, ".default")
	if !ok {
		return nil, ""
	}
	segs := strings.Split(rest, ".")
	if len(segs) == 0 || segs[0] != "fields[0]" {
		return nil, ""
	}
	f, name := top, top.Name
	for _, seg := range segs[1:] {
		idx, ok := fieldIndex(seg)
		if !ok || idx >= len(f.Fields) {
			return nil, ""
		}
		f = &f.Fields[idx]
		name += "." + f.Name
	}
	return f, name
}

// fieldIndex parses "fields[3]".
func fieldIndex(seg string) (int, bool) {
	inner, ok := strings.CutPrefix(seg, "fields[")
	if !ok {
		return 0, false
	}
	inner, ok = strings.CutSuffix(inner, "]")
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(inner)
	return i, err == nil && i >= 0
}
