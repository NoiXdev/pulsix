package varsync

import (
	"encoding/json"
	"reflect"
	"slices"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/varsource"
)

// attribute is one content attribute of a field as a diff shows it: nil
// when unset.
type attribute struct {
	name  string
	value func(schema.Field) any
}

// attributes are a field's content attributes in the order a diff lists
// them; sub-fields and a list's item are one attribute each, never merged.
// scope and overridable come last: a template's fields do not use them,
// but they are content, so a difference in them is listed too.
var attributes = []attribute{
	{"label", func(f schema.Field) any { return str(f.Label) }},
	{"description", func(f schema.Field) any { return str(f.Description) }},
	{"type", func(f schema.Field) any { return str(string(f.Type)) }},
	{"required", func(f schema.Field) any { return f.Required }},
	{"default", func(f schema.Field) any { return f.Default }},
	{"options", func(f schema.Field) any { return nonEmpty(f.Options) }},
	{"min", func(f schema.Field) any { return deref(f.Min) }},
	{"max", func(f schema.Field) any { return deref(f.Max) }},
	{"pattern", func(f schema.Field) any { return str(f.Pattern) }},
	{"min_items", func(f schema.Field) any { return deref(f.MinItems) }},
	{"max_items", func(f schema.Field) any { return deref(f.MaxItems) }},
	{"item", func(f schema.Field) any { return deref(f.Item) }},
	{"fields", func(f schema.Field) any { return nonEmpty(f.Fields) }},
	{"scope", func(f schema.Field) any { return str(string(f.Scope)) }},
	{"overridable", func(f schema.Field) any { return f.Overridable }},
}

func str(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func nonEmpty[T any](s []T) any {
	if len(s) == 0 {
		return nil
	}
	return s
}

// diff lists the content attributes in which from and to differ, compared
// as their values decoded from JSON (see sameJSON).
func diff(from, to schema.Field) []Change {
	out := []Change{}
	for _, a := range attributes {
		o, n := a.value(from), a.value(to)
		if !sameJSON(o, n) {
			out = append(out, Change{Attr: a.name, Old: o, New: n})
		}
	}
	return out
}

// sameJSON reports whether a and b are equal once each went through JSON
// as the database stores and returns them: an int default equals the same
// number as a float, and an integer beyond 2^53 equals the float it reads
// back as, so a stored field never differs from its source only by that
// round trip.
func sameJSON(a, b any) bool {
	x, okX := decoded(a)
	y, okY := decoded(b)
	return okX && okY && reflect.DeepEqual(x, y)
}

func decoded(v any) (any, bool) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false
	}
	return out, true
}

// sameContent reports whether f and g declare the same field, provenance
// left out, compared like sameJSON.
func sameContent(f, g schema.Field) bool { return sameJSON(content(f), content(g)) }

// content is f without provenance: what the source declares.
func content(f schema.Field) schema.Field {
	f.Active, f.Origin, f.Source, f.SetInPlaybook, f.SourceField = nil, "", nil, false, nil
	return f
}

// review says whether automatic mode must hold the change from field from
// to field to for a person, and why: a secret flag that changes anywhere
// in the field, a type change, a stored value that fits from but not to,
// or sub-fields that change while capped says a limit cut fields. A type
// change of a sub-field or a list's item is held only when a stored value
// no longer fits: with nothing stored it changes nothing a run receives.
func review(from, to schema.Field, stored map[string]any, capped bool, changes []Change) (bool, ReviewReason) {
	switch {
	case !slices.Equal(secretPaths(from, ""), secretPaths(to, "")):
		return true, ReasonSecret
	case from.Type != to.Type:
		return true, ReasonType
	}
	if v, ok := stored[to.Name]; ok && v != nil && fits(from, v) && !fits(to, v) {
		return true, ReasonValue
	}
	if capped && slices.ContainsFunc(changes, func(c Change) bool { return c.Attr == "fields" || c.Attr == "item" }) {
		return true, ReasonCut
	}
	return false, ""
}

// fits reports whether v is a valid stored value of f, checked as if f
// were active: an inactive field's value is validated once a person
// switches it on.
func fits(f schema.Field, v any) bool {
	return schema.ValidateStored([]schema.Field{content(f)}, map[string]any{f.Name: v}) == nil
}

// secretPaths lists the dotted paths of f's secret parts, f itself
// included, in a stable order. A list's item is never secret.
func secretPaths(f schema.Field, path string) []string {
	var out []string
	if f.Type == schema.TypeSecret {
		out = append(out, path)
	}
	for _, s := range f.Fields {
		out = append(out, secretPaths(s, path+"."+s.Name)...)
	}
	return out
}

// capNotes are the parser's notes that a limit cut fields out of a source
// (top-level or nested), or that notes themselves were cut, so such a cut
// may have gone unreported.
var capNotes = []string{"too_deep", "too_many_fields", "too_many_options", "too_many_notes"}

// capped reports whether a limit may have cut fields out of pb, so a
// change of sub-fields may come from the cut rather than the source.
func capped(pb varsource.Playbook) bool {
	return pb.FieldsCut || slices.ContainsFunc(pb.Notes, func(n varsource.Note) bool { return slices.Contains(capNotes, n.Code) })
}
