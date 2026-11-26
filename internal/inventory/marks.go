package inventory

import (
	"errors"
	"fmt"
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
// inert (plain text to Ansible) until a person changes them or accepts
// them (marks_accept.go).

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

// valueMarks is the marks a vars write by actor leaves. The earlier mark
// of a key still holding a value stays, unless a person changed that
// value: a person's save makes live only what they changed (controller's
// refinement of D6) — a value resent unchanged, by any form or client,
// keeps its mark, so a save that never showed it (an inactive field, a
// script resending what it read) cannot make it live. AcceptVars and
// AcceptMemberVars make an unchanged value live on purpose. An assistant's
// write adds the keys it changed.
func valueMarks(prev []string, next map[string]any, changed []string, actor assistantchange.Actor) []string {
	keep := map[string]bool{}
	for _, k := range prev {
		if v, ok := next[k]; ok && v != nil && (actor.Assistant || !slices.Contains(changed, k)) {
			keep[k] = true
		}
	}
	if actor.Assistant {
		for _, k := range changed {
			keep[k] = true
		}
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

// defaultMarks is the default marks a schema save by actor leaves: the
// earlier marks of fields that still have defaults, unless a person
// changed those defaults (as valueMarks: an unchanged default keeps its
// mark, AcceptDefaults makes it live on purpose); an assistant's save adds
// the fields whose defaults it changed.
func defaultMarks(prev []string, next schema.Schema, changed []string, actor assistantchange.Actor) []string {
	keep := map[string]bool{}
	for _, f := range next.Fields {
		if slices.Contains(prev, f.Name) && defaultsOf(f) != nil && (actor.Assistant || !slices.Contains(changed, f.Name)) {
			keep[f.Name] = true
		}
	}
	if actor.Assistant {
		for _, k := range changed {
			keep[k] = true
		}
	}
	return slices.Sorted(maps.Keys(keep))
}

// acceptMarks returns marks without keys, each of which must be marked: a
// person accepts an assistant's value (or default) as live unchanged.
func acceptMarks(marks, keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: name at least one key to accept", ErrInvalidInput)
	}
	for _, k := range keys {
		if !slices.Contains(marks, k) {
			return nil, fmt.Errorf("%w: %s holds no value an assistant wrote", ErrInvalidInput, k)
		}
	}
	out := []string{}
	for _, k := range marks {
		if !slices.Contains(keys, k) {
			out = append(out, k)
		}
	}
	return out, nil
}

// checkAssistantFieldNames refuses the field names an assistant's schema
// save adds, at any depth, under the execution-level name rule
// (ansiblevars.CheckUntrustedName: ansible_…, pulsix_…, __ansible…, magic
// names): a top-level one would steer a person's later value into a
// variable Ansible or Pulsix act on, a sub-field named like one of
// Ansible's markers would break the inventory's JSON load. Names already
// in old at the same place are a person's and stay.
func checkAssistantFieldNames(old, next schema.Schema) error {
	var problems []schema.Problem
	var walk func(prefix string, before, after []schema.Field)
	walk = func(prefix string, before, after []schema.Field) {
		known := make(map[string]schema.Field, len(before))
		for _, f := range before {
			known[f.Name] = f
		}
		for i, f := range after {
			path := prefix + "fields[" + strconv.Itoa(i) + "]"
			prev, existed := known[f.Name]
			if !existed {
				if err := ansiblevars.CheckUntrustedName(f.Name); err != nil {
					problems = append(problems, schema.Problem{Path: path + ".name", Message: "an assistant may not add this field: " + err.Error()})
				}
			}
			walk(path+".", prev.Fields, f.Fields)
		}
	}
	walk("", old.Fields, next.Fields)
	if len(problems) > 0 {
		return &schema.ValidationError{Problems: problems}
	}
	return nil
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

// joinProblems merges validation errors into one; a nil error adds
// nothing.
func joinProblems(errs ...error) error {
	var problems []schema.Problem
	for _, err := range errs {
		var ve *schema.ValidationError
		if errors.As(err, &ve) {
			problems = append(problems, ve.Problems...)
		} else if err != nil {
			return err
		}
	}
	if len(problems) > 0 {
		return &schema.ValidationError{Problems: problems}
	}
	return nil
}
