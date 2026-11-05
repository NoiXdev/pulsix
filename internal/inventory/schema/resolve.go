package schema

import "slices"

// Resolve returns the values Ansible sees for fields: declared keys only at
// every depth, schema defaults filled in for absent or null optional values.
// A value whose shape does not match its field — a map where a scalar or a
// list is expected, a non-list for a list or repeater, and so on, as left
// behind by a schema type change — is treated as absent, so the schema
// default applies; list and repeater entries of the wrong shape are left
// out. Nothing of the wrong shape, a sealed value least of all, is ever
// passed through. An inactive field is left out, its value and its default
// alike.
func Resolve(fields []Field, values map[string]any) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		if !f.IsActive() {
			continue
		}
		v, ok := values[f.Name]
		if ok && v != nil {
			if rv, fits := resolveValue(f, v); fits {
				out[f.Name] = rv
				continue
			}
		}
		if f.Default != nil {
			out[f.Name] = f.Default
		}
	}
	return out
}

// resolveValue returns v resolved against f, and false when v's shape does
// not match f.
func resolveValue(f Field, v any) (any, bool) {
	switch f.Type {
	case TypeObject:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		return Resolve(f.Fields, obj), true
	case TypeRepeater:
		items, ok := v.([]any)
		if !ok {
			return nil, false
		}
		out := make([]any, 0, len(items))
		for _, it := range items {
			if obj, isObj := it.(map[string]any); isObj {
				out = append(out, Resolve(f.Fields, obj))
			}
		}
		return out, true
	case TypeList:
		items, ok := v.([]any)
		if !ok {
			return nil, false
		}
		out := make([]any, 0, len(items))
		for _, it := range items {
			if f.Item != nil && scalarFits(f.Item.Type, it) {
				out = append(out, it)
			}
		}
		return out, true
	}
	return v, scalarFits(f.Type, v)
}

// scalarFits reports whether v has the JSON shape a scalar field of type t
// holds. Secret values reach Resolve already masked or revealed, as strings.
func scalarFits(t Type, v any) bool {
	switch t {
	case TypeString, TypeEnum, TypeSecret:
		_, ok := v.(string)
		return ok
	case TypeInt, TypeFloat:
		_, ok := number(v)
		return ok
	case TypeBool:
		_, ok := v.(bool)
		return ok
	}
	return false
}

// Orphans returns the sorted top-level keys of values that fields does not
// declare, or nil when there are none.
func Orphans(fields []Field, values map[string]any) []string {
	declared := make(map[string]bool, len(fields))
	for _, f := range fields {
		declared[f.Name] = true
	}
	var out []string
	for k := range values {
		if !declared[k] {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}
