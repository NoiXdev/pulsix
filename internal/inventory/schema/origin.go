package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Origin says where a top-level field came from (spec 7.75 goal 2). Only a
// job template takes fields from a playbook; groups and shared definitions
// hold manual fields only (Schema.CheckManualOnly).
type Origin string

// Origins. An empty origin means manual.
const (
	// OriginManual is a field a person declared.
	OriginManual Origin = "manual"
	// OriginSource is a field read from the playbook, locked to it.
	OriginSource Origin = "source"
	// OriginAdjusted is a source field a person changed; a sync keeps the
	// change and records the source's own version in SourceField.
	OriginAdjusted Origin = "adjusted"
	// OriginGone is a source field the source no longer declares, kept until
	// a person confirms its removal.
	OriginGone Origin = "gone"
)

// SourceKind is the kind of declaration a source field was read from.
type SourceKind string

// Source kinds.
const (
	SourceArgumentSpec SourceKind = "argument_spec"
	SourceVarsPrompt   SourceKind = "vars_prompt"
	SourceMarker       SourceKind = "marker"
	SourceSidecar      SourceKind = "sidecar"
)

// FieldSource locates the declaration a field was read from: the file
// (relative to the repo), its line, and for a role argument spec the role
// and entry point.
type FieldSource struct {
	Kind       SourceKind `json:"kind"`
	Role       string     `json:"role,omitempty"`
	EntryPoint string     `json:"entry_point,omitempty"`
	File       string     `json:"file"`
	Line       int        `json:"line,omitempty"`
}

// IsActive reports whether the field is shown in forms, validated and sent.
// An absent active switch means active.
func (f Field) IsActive() bool { return f.Active == nil || *f.Active }

// EffectiveOrigin is the field's origin, manual when unset.
func (f Field) EffectiveOrigin() Origin {
	if f.Origin == "" {
		return OriginManual
	}
	return f.Origin
}

// SameContent reports whether f and g declare the same field, ignoring
// provenance (active, origin, source, set_in_playbook, source_field). It
// compares canonical JSON, so a default of int 8080 equals one decoded as
// float64 8080.
func (f Field) SameContent(g Field) bool {
	a, errA := json.Marshal(withoutProvenance(f))
	b, errB := json.Marshal(withoutProvenance(g))
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

// CheckManualOnly reports every top-level field that came from a playbook,
// or nil: inventory groups and shared template definitions hold manual
// fields only (spec 7.75 owner decision 1). Run it after Check.
func (s Schema) CheckManualOnly() error {
	const msg = "only job templates take fields from a playbook"
	var p problems
	for i, f := range s.Fields {
		path := fmt.Sprintf("fields[%d]", i)
		if f.EffectiveOrigin() != OriginManual {
			p.add(path+".origin", msg)
		}
		if f.SetInPlaybook {
			p.add(path+".set_in_playbook", msg)
		}
	}
	return p.err()
}

// Sendable returns the values a run receives: those under active top-level
// fields. A value under an inactive field, or under a name the schema no
// longer declares, stays stored but is not sent. A gone field still counts
// while it is active, until a person confirms its removal.
func Sendable(fields []Field, values map[string]any) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		if !f.IsActive() {
			continue
		}
		if v, ok := values[f.Name]; ok {
			out[f.Name] = v
		}
	}
	return out
}

// checkProvenance checks the provenance attributes of one field. Sub-fields
// and list items carry none: a top-level field from a source is one unit.
func checkProvenance(p *problems, path string, f Field, depth int, top bool) {
	if !top {
		for _, attr := range provenanceAttrs(f) {
			p.add(path+"."+attr, "only top-level fields carry "+attr)
		}
		return
	}
	switch f.EffectiveOrigin() {
	case OriginManual:
		if f.Source != nil {
			p.add(path+".source", "a manual field has no source")
		}
	case OriginSource, OriginAdjusted, OriginGone:
		if f.Source == nil {
			p.add(path+".source", "a field from a playbook needs a source")
		}
	default:
		p.add(path+".origin", fmt.Sprintf("unknown origin %q; must be manual, source, adjusted or gone", f.Origin))
	}
	if f.Source != nil {
		checkSource(p, path+".source", *f.Source)
	}
	if f.SourceField != nil {
		checkSourceField(p, path, f, depth)
	}
}

func checkSource(p *problems, path string, s FieldSource) {
	switch s.Kind {
	case SourceArgumentSpec, SourceVarsPrompt, SourceMarker, SourceSidecar:
	default:
		p.add(path+".kind", fmt.Sprintf("unknown source kind %q; must be argument_spec, vars_prompt, marker or sidecar", s.Kind))
	}
	if s.File == "" {
		p.add(path+".file", "a source needs a file")
	}
	if s.Line < 0 {
		p.add(path+".line", "must be ≥ 0")
	}
}

// checkSourceField checks the source's version of an adjusted or gone
// field: the same name, no provenance of its own, and a well-formed field.
func checkSourceField(p *problems, path string, f Field, depth int) {
	sp := path + ".source_field"
	if o := f.EffectiveOrigin(); o != OriginAdjusted && o != OriginGone {
		p.add(sp, "only adjusted and gone fields keep the source's version")
	}
	sf := *f.SourceField
	if sf.Name != f.Name {
		p.add(sp+".name", "must be the field's own name "+f.Name)
	}
	for _, attr := range provenanceAttrs(sf) {
		p.add(sp+"."+attr, "the source's version of a field carries no "+attr)
	}
	checkField(p, sp, withoutProvenance(sf), depth, true)
}

// provenanceAttrs names the provenance attributes f sets, in wire order.
func provenanceAttrs(f Field) []string {
	var out []string
	if f.Active != nil {
		out = append(out, "active")
	}
	if f.Origin != "" {
		out = append(out, "origin")
	}
	if f.Source != nil {
		out = append(out, "source")
	}
	if f.SetInPlaybook {
		out = append(out, "set_in_playbook")
	}
	if f.SourceField != nil {
		out = append(out, "source_field")
	}
	return out
}

func withoutProvenance(f Field) Field {
	f.Active, f.Origin, f.Source, f.SetInPlaybook, f.SourceField = nil, "", nil, false, nil
	return f
}
