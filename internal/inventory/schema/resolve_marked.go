package schema

// Marks names the top-level fields of one group or membership whose stored
// value (Values) or schema default (Defaults) an assistant wrote (decision
// D6, spec 2026-11-25 assistant API gaps): such values reach a run as plain
// text until a person saves them.
type Marks struct {
	Values   map[string]bool
	Defaults map[string]bool
}

// ResolveMarked is Resolve with the values and defaults marks names passed
// through wrap (the renderer's inert form): a marked value whole, nested
// defaults filled into it included; a marked field's schema default
// wherever it fills in, at the top or inside a person's value. Every other
// part resolves exactly as Resolve does. The second result lists, in field
// order, the top-level fields whose resolved value holds a wrapped part.
func ResolveMarked(fields []Field, values map[string]any, marks Marks, wrap func(any) any) (map[string]any, []string) {
	out := make(map[string]any, len(fields))
	var marked []string
	for _, f := range fields {
		if !f.IsActive() {
			continue
		}
		valueMarked, defaultMarked := marks.Values[f.Name], marks.Defaults[f.Name]
		if v, ok := values[f.Name]; ok && v != nil {
			used := false
			var fill func(any) any
			// A marked value is wrapped whole below; wrapping its defaults
			// first would nest the inert form inside itself.
			if defaultMarked && !valueMarked {
				fill = func(d any) any {
					used = true
					return wrap(d)
				}
			}
			if rv, fits := resolveValue(f, v, fill); fits {
				if valueMarked {
					rv, used = wrap(rv), true
				}
				out[f.Name] = rv
				if used {
					marked = append(marked, f.Name)
				}
				continue
			}
		}
		if f.Default == nil {
			continue
		}
		if defaultMarked {
			out[f.Name] = wrap(f.Default)
			marked = append(marked, f.Name)
		} else {
			out[f.Name] = f.Default
		}
	}
	return out, marked
}
