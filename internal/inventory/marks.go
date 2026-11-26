package inventory

import (
	"maps"
	"reflect"
	"slices"
	"strconv"

	"github.com/noixdev/pulsix/internal/ansiblevars"
	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// The marks of decision D6 (spec 2026-11-25 assistant API gaps): per
// top-level key, which values of a group or membership and which schema
// defaults of a group an assistant wrote. The renderer emits marked values
// inert (plain text to Ansible); a person's save of the object clears its
// marks.

// ChangedKeys returns the sorted keys of next whose non-null value differs
// from stored's (a key stored lacks counts as changed): what a vars write
// set or changed. Secrets compare sealed, so a keep marker is no change.
func ChangedKeys(stored, next map[string]any) []string {
	var out []string
	for k, v := range next {
		if v == nil {
			continue
		}
		if prev, ok := stored[k]; !ok || !reflect.DeepEqual(prev, v) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// valueMarks is the marks a vars write by actor leaves: none for a person
// (a group-vars or membership save replaces the whole map); for an
// assistant, the earlier marks of keys still holding a value plus the
// keys it changed.
func valueMarks(prev []string, next map[string]any, changed []string, actor assistantchange.Actor) []string {
	if !actor.Assistant {
		return []string{}
	}
	keep := map[string]bool{}
	for _, k := range prev {
		if v, ok := next[k]; ok && v != nil {
			keep[k] = true
		}
	}
	for _, k := range changed {
		keep[k] = true
	}
	return slices.Sorted(maps.Keys(keep))
}

// checkAssistantValues refuses what an assistant may not write under the
// keys it changed (the write-time checks of D6): a name Ansible or Pulsix
// act on (ansiblevars.CheckUntrustedName), and a value holding an object
// key Ansible's JSON loader reads as its own marker or nesting too deep
// (ansiblevars.CheckUntrusted), which would break the inert form.
func checkAssistantValues(next map[string]any, changed []string) error {
	var problems []schema.Problem
	for _, k := range changed {
		if err := ansiblevars.CheckUntrustedName(k); err != nil {
			problems = append(problems, schema.Problem{Path: k, Message: "an assistant may not set this variable: " + err.Error()})
			continue
		}
		if err := ansiblevars.CheckUntrusted(next[k]); err != nil {
			problems = append(problems, schema.Problem{Path: k, Message: untrustedValueMessage})
		}
	}
	if len(problems) > 0 {
		return &schema.ValidationError{Problems: problems}
	}
	return nil
}

var untrustedValueMessage = `an assistant's value may not hold an object key starting with "__ansible" or nest more than ` +
	strconv.Itoa(ansiblevars.MaxUntrustedDepth) + ` levels deep`

// defaultsOf is everything of f a schema default fills in: its own default
// and its sub-fields', at any depth; nil when it has none.
func defaultsOf(f schema.Field) any {
	var subs map[string]any
	for _, s := range f.Fields {
		if d := defaultsOf(s); d != nil {
			if subs == nil {
				subs = map[string]any{}
			}
			subs[s.Name] = d
		}
	}
	if f.Default == nil && subs == nil {
		return nil
	}
	return map[string]any{"default": f.Default, "fields": subs}
}

// ChangedDefaults returns the sorted top-level fields of next whose
// defaults (defaultsOf) are set and differ from the same-named field's in
// old: what a schema save set or changed.
func ChangedDefaults(old, next schema.Schema) []string {
	before := make(map[string]any, len(old.Fields))
	for _, f := range old.Fields {
		before[f.Name] = defaultsOf(f)
	}
	var out []string
	for _, f := range next.Fields {
		if d := defaultsOf(f); d != nil && !reflect.DeepEqual(before[f.Name], d) {
			out = append(out, f.Name)
		}
	}
	slices.Sort(out)
	return out
}

// defaultMarks is the default marks a schema save by actor leaves: none
// for a person (the save carries every field); for an assistant, the
// earlier marks of fields that still have defaults plus the fields whose
// defaults it changed.
func defaultMarks(prev []string, next schema.Schema, changed []string, actor assistantchange.Actor) []string {
	if !actor.Assistant {
		return []string{}
	}
	keep := map[string]bool{}
	for _, f := range next.Fields {
		if slices.Contains(prev, f.Name) && defaultsOf(f) != nil {
			keep[f.Name] = true
		}
	}
	for _, k := range changed {
		keep[k] = true
	}
	return slices.Sorted(maps.Keys(keep))
}

// checkAssistantDefaults is checkAssistantValues for the defaults an
// assistant set or changed: the field's name, every default inside it,
// and no sub-field named like one of Ansible's markers.
func checkAssistantDefaults(next schema.Schema, changed []string) error {
	var problems []schema.Problem
	for i, f := range next.Fields {
		if !slices.Contains(changed, f.Name) {
			continue
		}
		path := "fields[" + strconv.Itoa(i) + "].default"
		if err := ansiblevars.CheckUntrustedName(f.Name); err != nil {
			problems = append(problems, schema.Problem{Path: path, Message: "an assistant may not set a default for this field: " + err.Error()})
			continue
		}
		if !defaultsUntrusted(f) {
			problems = append(problems, schema.Problem{Path: path, Message: untrustedValueMessage})
		}
	}
	if len(problems) > 0 {
		return &schema.ValidationError{Problems: problems}
	}
	return nil
}

// defaultsUntrusted reports whether every default inside f passes
// ansiblevars.CheckUntrusted and no sub-field is named like a marker.
func defaultsUntrusted(f schema.Field) bool {
	if f.Default != nil && ansiblevars.CheckUntrusted(f.Default) != nil {
		return false
	}
	for _, s := range f.Fields {
		if ansiblevars.ReservedKey(s.Name) || !defaultsUntrusted(s) {
			return false
		}
	}
	return true
}
