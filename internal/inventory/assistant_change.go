package inventory

import (
	"context"
	"maps"
	"reflect"
	"slices"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// SetChangeWatcher wires the watcher an assistant's change of the values a
// run receives is handed to (decision D6, spec 2026-11-25 C2): it pauses
// what runs unattended on the hosts the change reaches until a person
// reviews it. Without one nothing is handed on.
func (s *Service) SetChangeWatcher(w assistantchange.Watcher) { s.watcher = w }

// watches reports whether a write by actor is handed on: only an
// assistant's is.
func (s *Service) watches(actor assistantchange.Actor) bool {
	return actor.Assistant && s.watcher != nil
}

// handOnGroup tells the watcher about an assistant's change of group g
// moving fields; nothing when no field moved. It runs inside the write's
// transaction.
func (s *Service) handOnGroup(ctx context.Context, orgID uuid.UUID, actor assistantchange.Actor, g Group, fields []assistantchange.Field) error {
	if !s.watches(actor) || len(fields) == 0 {
		return nil
	}
	return s.watcher.Changed(ctx, orgID, assistantchange.New(assistantchange.SubjectInventoryGroup, g.ID, g.Name, fields, actor))
}

// handOnMembership is handOnGroup for the values of host's membership in
// g.
func (s *Service) handOnMembership(ctx context.Context, orgID uuid.UUID, actor assistantchange.Actor, g Group, hostID uuid.UUID, hostName string, fields []assistantchange.Field) error {
	if !s.watches(actor) || len(fields) == 0 {
		return nil
	}
	c := assistantchange.New(assistantchange.SubjectInventoryMembership, g.ID, g.Name, fields, actor)
	c.HostID, c.HostName = &hostID, hostName
	return s.watcher.Changed(ctx, orgID, c)
}

// valueChanges lists the top-level keys whose value moved from old to
// next (set, changed or removed), sorted, as "vars.<key>" with the values
// a person reads: secrets masked under fields, nil for none.
func valueChanges(fields []schema.Field, old, next map[string]any) []assistantchange.Field {
	before, after := schema.MaskDeclared(fields, old), schema.MaskDeclared(fields, next)
	var out []assistantchange.Field
	for _, k := range unionKeys(old, next) {
		if !reflect.DeepEqual(old[k], next[k]) {
			out = append(out, assistantchange.Field{Name: "vars." + k, Old: before[k], New: after[k]})
		}
	}
	return out
}

// defaultChanges lists the top-level fields whose defaults (defaultsOf)
// moved from old to next (set, changed or removed, the field's removal
// included), sorted, as "default.<field>". A secret field has no default.
func defaultChanges(old, next schema.Schema) []assistantchange.Field {
	before, after := defaultsByName(old), defaultsByName(next)
	var out []assistantchange.Field
	for _, k := range unionKeys(before, after) {
		if !reflect.DeepEqual(before[k], after[k]) {
			out = append(out, assistantchange.Field{Name: "default." + k, Old: before[k], New: after[k]})
		}
	}
	return out
}

// defaultsByName is the default of each top-level field of sc that has
// one: the field's own default, or with defaults of its sub-fields all of
// them (defaultsOf).
func defaultsByName(sc schema.Schema) map[string]any {
	out := map[string]any{}
	for _, f := range sc.Fields {
		d := defaultsOf(f)
		if d == nil {
			continue
		}
		if m, ok := d.(map[string]any); ok {
			if subs, _ := m["fields"].(map[string]any); len(subs) > 0 {
				out[f.Name] = d
				continue
			}
		}
		out[f.Name] = f.Default
	}
	return out
}

// unionKeys is every key of a and b whose value is not nil, sorted.
func unionKeys(a, b map[string]any) []string {
	seen := map[string]bool{}
	for _, m := range []map[string]any{a, b} {
		for k, v := range m {
			if v != nil {
				seen[k] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(seen))
}
