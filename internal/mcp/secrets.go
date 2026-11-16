package mcp

import (
	"fmt"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// The secret rules of the write tools (spec 2026-11-16 assistant access,
// "Secrets never go in"). In a vars document a plain value under a
// secret-typed field sets the secret, so the rules are about fields, not
// about value shapes: a secret-typed field may only be kept or left out.

// keepForm turns "keep the stored secret at path" into the route's own
// keep marker.
type keepForm func(path string) any

// keepGroup is the keep marker of group and membership vars.
func keepGroup(path string) any { return map[string]any{"$secret": "keep", "from": path} }

// keepTemplate is the keep marker of template extra vars.
func keepTemplate(string) any { return map[string]any{"$secret": "set"} }

// isKeep reports whether v asks to keep the stored secret: the tools'
// {"$secret":"keep"}, the read-back {"$secret":"set"}, or a route's own
// keep marker.
func isKeep(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	marker, _ := m["$secret"].(string)
	if marker != "keep" && marker != "set" {
		return false
	}
	for k := range m {
		if k != "$secret" && k != "from" {
			return false
		}
	}
	return true
}

// checkValues returns values with every secret-typed field's keep request
// translated to keep, and refuses any other value under a field that is
// secret-typed in any of fieldSets (the stored and the submitted schema).
func checkValues(fieldSets [][]schema.Field, values map[string]any, keep keepForm) (map[string]any, error) {
	out := values
	for _, fields := range fieldSets {
		var err error
		out, err = schema.MapSecrets(fields, out, func(path string, v any) (any, error) {
			if isKeep(v) {
				return keep(path), nil
			}
			return nil, fmt.Errorf("secrets are set by a person in Pulsix (%s); send {\"$secret\": \"keep\"} or leave the field out", path)
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// checkSchemaChange refuses a schema change that would drop the secret type
// of a field, or a secret field: either could make a stored secret readable
// or lose it. Adding fields, secret ones included, is fine.
func checkSchemaChange(old, updated []schema.Field) error {
	newSecret := map[string]bool{}
	for _, p := range secretPaths("", updated) {
		newSecret[p] = true
	}
	for _, p := range secretPaths("", old) {
		if !newSecret[p] {
			return fmt.Errorf("secret fields keep their type; change them in Pulsix (%s)", p)
		}
	}
	return nil
}

// secretPaths lists the secret-typed fields: "a.b" inside objects, "a[]"
// for items of lists and repeaters.
func secretPaths(prefix string, fields []schema.Field) []string {
	var out []string
	for _, f := range fields {
		path := f.Name
		if prefix != "" {
			path = prefix + "." + f.Name
		}
		switch f.Type {
		case schema.TypeSecret:
			out = append(out, path)
		case schema.TypeObject:
			out = append(out, secretPaths(path, f.Fields)...)
		case schema.TypeRepeater:
			out = append(out, secretPaths(path+"[]", f.Fields)...)
		case schema.TypeList:
			switch {
			case f.Item == nil:
			case f.Item.Type == schema.TypeSecret:
				out = append(out, path+"[]")
			default:
				out = append(out, secretPaths(path+"[]", f.Item.Fields)...)
			}
		}
	}
	return out
}
