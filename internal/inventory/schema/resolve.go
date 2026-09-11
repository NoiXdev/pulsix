package schema

import "slices"

// Resolve returns the values Ansible sees for fields: declared keys only at
// every depth, schema defaults filled in for absent or null optional values.
// Values whose shape does not match their field are passed through; stored
// values were validated when they were written.
func Resolve(fields []Field, values map[string]any) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		v, ok := values[f.Name]
		if !ok || v == nil {
			if f.Default != nil {
				out[f.Name] = f.Default
			}
			continue
		}
		out[f.Name] = resolveValue(f, v)
	}
	return out
}

func resolveValue(f Field, v any) any {
	switch f.Type {
	case TypeObject:
		if obj, ok := v.(map[string]any); ok {
			return Resolve(f.Fields, obj)
		}
	case TypeRepeater:
		if items, ok := v.([]any); ok {
			out := make([]any, len(items))
			for i, it := range items {
				if obj, isObj := it.(map[string]any); isObj {
					out[i] = Resolve(f.Fields, obj)
				} else {
					out[i] = it
				}
			}
			return out
		}
	}
	return v
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
