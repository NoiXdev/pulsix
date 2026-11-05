// Package schema is the typed variable schema an inventory group owns (spec
// §6): the field model, the check that a schema is well-formed, validation of
// values against it, schema defaults, and the secret markers that let sealed
// values travel through the API without ever being revealed. It is pure: no
// I/O, no database, no HTTP.
package schema

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// Type is a field type.
type Type string

// Field types (spec §6). list holds scalars, repeater holds objects with a
// sub-schema, object is a single sub-schema.
const (
	TypeString   Type = "string"
	TypeInt      Type = "int"
	TypeFloat    Type = "float"
	TypeBool     Type = "bool"
	TypeEnum     Type = "enum"
	TypeSecret   Type = "secret"
	TypeList     Type = "list"
	TypeRepeater Type = "repeater"
	TypeObject   Type = "object"
)

// Scope says where a top-level field's value lives: on the group, set once,
// or on each membership, asked when a host is assigned.
type Scope string

// Scopes.
const (
	ScopeGroup Scope = "group"
	ScopeHost  Scope = "host"
)

// Limits that keep a schema small enough to render as a form.
const (
	MaxDepth      = 4
	MaxFields     = 100
	MaxOptions    = 100
	MaxPatternLen = 512
)

var fieldNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,63}$`)

// ValidFieldName reports whether name is usable as an Ansible variable
// name: a lowercase letter or underscore, then lowercase letters,
// digits and underscores, up to 64 characters.
//
// Exported so a caller that sets extra vars WITHOUT a schema to check
// them against can apply the same rule rather than keep a second
// definition of what a variable may be called — a webhook trigger's
// mapping targets, where a template with no schema used to accept any
// string at all including "" and "a b" (backlog 2.62).
func ValidFieldName(name string) bool { return fieldNamePattern.MatchString(name) }

// reservedPrefixes protect Ansible's connection variables and the facts
// Pulsix renders under pulsix_facts from being shadowed by a schema field.
var reservedPrefixes = []string{"ansible_", "pulsix_"}

// Field is one schema field. The JSON tags are the wire contract of spec §6:
// the SPA renders its forms from exactly this JSON.
type Field struct {
	Name        string   `json:"name"`
	Label       string   `json:"label,omitempty"`
	Description string   `json:"description,omitempty"`
	Type        Type     `json:"type"`
	Scope       Scope    `json:"scope,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Overridable bool     `json:"overridable,omitempty"`
	Default     any      `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
	MinItems    *int     `json:"min_items,omitempty"`
	MaxItems    *int     `json:"max_items,omitempty"`
	Item        *Field   `json:"item,omitempty"`
	Fields      []Field  `json:"fields,omitempty"`

	// Provenance (origin.go): top-level fields only. An absent active means
	// active and an absent origin means manual, so a schema stored before
	// these attributes existed keeps its behaviour.
	Active        *bool        `json:"active,omitempty"`
	Origin        Origin       `json:"origin,omitempty"`
	Source        *FieldSource `json:"source,omitempty"`
	SetInPlaybook bool         `json:"set_in_playbook,omitempty"`
	SourceField   *Field       `json:"source_field,omitempty"`
}

// Schema is the ordered field list a group owns.
type Schema struct {
	Fields []Field `json:"fields"`
}

// EffectiveScope is the field's scope, group when unset.
func (f Field) EffectiveScope() Scope {
	if f.Scope == "" {
		return ScopeGroup
	}
	return f.Scope
}

// GroupFields returns the top-level fields whose values live on the group.
func (s Schema) GroupFields() []Field {
	var out []Field
	for _, f := range s.Fields {
		if f.EffectiveScope() == ScopeGroup {
			out = append(out, f)
		}
	}
	return out
}

// HostFields returns the top-level fields asked when a host is assigned.
func (s Schema) HostFields() []Field {
	var out []Field
	for _, f := range s.Fields {
		if f.EffectiveScope() == ScopeHost {
			out = append(out, f)
		}
	}
	return out
}

// MembershipFields returns what a membership may carry: every host-scoped
// field, plus every overridable group-scoped field as an optional override
// without a schema default (an absent override means "use the group value").
func (s Schema) MembershipFields() []Field {
	var out []Field
	for _, f := range s.Fields {
		switch {
		case f.EffectiveScope() == ScopeHost:
			out = append(out, f)
		case f.Overridable:
			o := f
			o.Required = false
			o.Default = nil
			out = append(out, o)
		}
	}
	return out
}

// Check reports every problem with the schema itself, or nil. Problem paths
// index into the field list: fields[0].fields[2].name.
func (s Schema) Check() error {
	var p problems
	checkFields(&p, "", s.Fields, 0, true)
	return p.err()
}

func checkFields(p *problems, prefix string, fields []Field, depth int, top bool) {
	if len(fields) > MaxFields {
		p.add(prefix, fmt.Sprintf("at most %d fields are allowed", MaxFields))
	}
	seen := make(map[string]bool, len(fields))
	for i, f := range fields {
		path := join(prefix, fmt.Sprintf("fields[%d]", i))
		switch {
		case !fieldNamePattern.MatchString(f.Name):
			p.add(path+".name", "must start with a lowercase letter or underscore and contain only lowercase letters, digits and underscores (at most 64)")
		case top && ReservedName(f.Name):
			p.add(path+".name", "must not start with ansible_ or pulsix_")
		case seen[f.Name]:
			p.add(path+".name", "duplicate field name "+f.Name)
		}
		seen[f.Name] = true
		checkField(p, path, f, depth, top)
	}
}

func checkField(p *problems, path string, f Field, depth int, top bool) {
	if !knownType(f.Type) {
		p.add(path+".type", "unknown type "+string(f.Type))
		return
	}
	checkScope(p, path, f, top)
	checkAttributes(p, path, f)
	checkProvenance(p, path, f, depth, top)
	switch f.Type {
	case TypeEnum:
		checkOptions(p, path, f.Options)
	case TypeList:
		checkItem(p, path, f.Item)
	case TypeRepeater, TypeObject:
		switch {
		case len(f.Fields) == 0:
			p.add(path+".fields", "a "+string(f.Type)+" needs at least one sub-field")
		case depth+1 > MaxDepth:
			p.add(path+".fields", fmt.Sprintf("schemas nest at most %d levels deep", MaxDepth))
		default:
			checkFields(p, path, f.Fields, depth+1, false)
		}
	}
	checkDefault(p, path, f)
}

func checkScope(p *problems, path string, f Field, top bool) {
	if !top {
		if f.Scope != "" {
			p.add(path+".scope", "only top-level fields have a scope")
		}
		if f.Overridable {
			p.add(path+".overridable", "only top-level fields can be overridable")
		}
		return
	}
	if f.Scope != "" && f.Scope != ScopeGroup && f.Scope != ScopeHost {
		p.add(path+".scope", "must be group or host")
	}
	if f.Overridable && f.EffectiveScope() != ScopeGroup {
		p.add(path+".overridable", "only group-scoped fields can be overridden per host")
	}
}

func checkAttributes(p *problems, path string, f Field) {
	numeric := f.Type == TypeInt || f.Type == TypeFloat
	if (f.Min != nil || f.Max != nil) && !numeric && f.Type != TypeString {
		p.add(path+".min", "min and max apply to int, float and string fields; use min_items and max_items for lists")
	}
	if f.Min != nil && f.Max != nil && *f.Min > *f.Max {
		p.add(path+".max", "must not be less than min")
	}
	if f.Type == TypeString {
		for _, bound := range []struct {
			attr string
			v    *float64
		}{{".min", f.Min}, {".max", f.Max}} {
			if b := bound.v; b != nil && (*b < 0 || *b != math.Trunc(*b)) {
				p.add(path+bound.attr, "string lengths must be whole numbers ≥ 0")
			}
		}
	}
	if f.Pattern != "" {
		switch {
		case f.Type != TypeString:
			p.add(path+".pattern", "pattern applies to string fields only")
		case len(f.Pattern) > MaxPatternLen:
			p.add(path+".pattern", fmt.Sprintf("must be at most %d characters", MaxPatternLen))
		default:
			if _, err := regexp.Compile(f.Pattern); err != nil {
				p.add(path+".pattern", "invalid regular expression: "+err.Error())
			}
		}
	}
	if (f.MinItems != nil || f.MaxItems != nil) && f.Type != TypeList && f.Type != TypeRepeater {
		p.add(path+".min_items", "min_items and max_items apply to list and repeater fields only")
	}
	if f.MinItems != nil && *f.MinItems < 0 {
		p.add(path+".min_items", "must be ≥ 0")
	}
	if f.MinItems != nil && f.MaxItems != nil && *f.MinItems > *f.MaxItems {
		p.add(path+".max_items", "must not be less than min_items")
	}
	if len(f.Options) > 0 && f.Type != TypeEnum {
		p.add(path+".options", "options apply to enum fields only")
	}
	if f.Item != nil && f.Type != TypeList {
		p.add(path+".item", "item applies to list fields only")
	}
	if len(f.Fields) > 0 && f.Type != TypeRepeater && f.Type != TypeObject {
		p.add(path+".fields", "sub-fields apply to repeater and object fields only")
	}
}

func checkOptions(p *problems, path string, options []string) {
	if len(options) == 0 {
		p.add(path+".options", "an enum needs at least one option")
		return
	}
	if len(options) > MaxOptions {
		p.add(path+".options", fmt.Sprintf("at most %d options are allowed", MaxOptions))
	}
	seen := make(map[string]bool, len(options))
	for _, o := range options {
		switch {
		case o == "":
			p.add(path+".options", "options must not be empty")
		case seen[o]:
			p.add(path+".options", "duplicate option "+o)
		}
		seen[o] = true
	}
}

func checkItem(p *problems, path string, item *Field) {
	if item == nil {
		p.add(path+".item", "a list needs an item type")
		return
	}
	ip := path + ".item"
	if item.Name != "" {
		p.add(ip+".name", "list items have no name")
	}
	if !isScalar(item.Type) {
		p.add(ip+".type", "list items must be string, int, float, bool or enum")
		return
	}
	if item.Scope != "" || item.Overridable || item.Required || item.Default != nil {
		p.add(ip, "list items carry only type and validation attributes")
	}
	checkProvenance(p, ip, *item, 0, false)
	checkAttributes(p, ip, *item)
	if item.Type == TypeEnum {
		checkOptions(p, ip, item.Options)
	}
}

func checkDefault(p *problems, path string, f Field) {
	if f.Default == nil {
		return
	}
	switch f.Type {
	case TypeSecret, TypeRepeater, TypeObject:
		p.add(path+".default", string(f.Type)+" fields cannot have a default")
		return
	}
	var vp problems
	validateValue(&vp, "", f, f.Default, true)
	if len(vp.list) > 0 {
		p.add(path+".default", "does not satisfy the field: "+vp.list[0].Message)
	}
}

func knownType(t Type) bool {
	switch t {
	case TypeString, TypeInt, TypeFloat, TypeBool, TypeEnum, TypeSecret, TypeList, TypeRepeater, TypeObject:
		return true
	}
	return false
}

func isScalar(t Type) bool {
	switch t {
	case TypeString, TypeInt, TypeFloat, TypeBool, TypeEnum:
		return true
	}
	return false
}

// ReservedName reports whether a top-level field may not take name: it
// would shadow Ansible's connection variables (ansible_*) or the facts
// Pulsix renders (pulsix_*). Sub-fields are not bound by it. Exported so
// the playbook source parser applies the same rule rather than a copy.
func ReservedName(name string) bool {
	for _, prefix := range reservedPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// join builds a dotted path; an empty prefix means the root.
func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}
