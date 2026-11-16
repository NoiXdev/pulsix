package mcp

import (
	"fmt"
	"strings"

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

// newSecretPaths lists the fields secret-typed in updated but not in old,
// in updated's order.
func newSecretPaths(old, updated []schema.Field) []string {
	was := map[string]bool{}
	for _, p := range secretPaths("", old) {
		was[p] = true
	}
	var out []string
	for _, p := range secretPaths("", updated) {
		if !was[p] {
			out = append(out, p)
		}
	}
	return out
}

// checkNewSecretsEmpty refuses to make a field secret while any of stored
// holds a value at it: the value was written as a plain one, so keeping
// it under the secret type would let the assistant choose a secret (spec
// 2026-11-16 assistant access, "Secrets never go in"). A field that holds
// nothing may become secret; a person fills it in.
func checkNewSecretsEmpty(old, updated []schema.Field, stored ...map[string]any) error {
	for _, p := range newSecretPaths(old, updated) {
		for _, doc := range stored {
			for _, v := range valuesAt(doc, p) {
				if v != nil {
					return fmt.Errorf("a field becomes secret only while it holds no value; clear %s first, or let a person change it in Pulsix", p)
				}
			}
		}
	}
	return nil
}

// valuesAt returns the values a secretPaths path names in doc: "a.b"
// inside objects, "a[]" for every item of a list.
func valuesAt(doc any, path string) []any {
	nodes := []any{doc}
	for _, seg := range strings.Split(path, ".") {
		nodes = step(nodes, seg)
	}
	return nodes
}

// step follows one path segment from every node: the object key, and for
// "name[]" every item of the list under it.
func step(nodes []any, seg string) []any {
	name, each := strings.CutSuffix(seg, "[]")
	var next []any
	for _, n := range nodes {
		m, ok := n.(map[string]any)
		if !ok {
			continue
		}
		v, ok := m[name]
		if !ok {
			continue
		}
		if !each {
			next = append(next, v)
			continue
		}
		if items, ok := v.([]any); ok {
			next = append(next, items...)
		}
	}
	return next
}

// withoutPaths returns a copy of doc with the values at paths removed (a
// list of secret items goes as a whole); doc itself is left as it is.
func withoutPaths(doc map[string]any, paths []string) map[string]any {
	out, _ := deepCopy(doc).(map[string]any)
	for _, p := range paths {
		segs := strings.Split(p, ".")
		parents := []any{out}
		for _, seg := range segs[:len(segs)-1] {
			parents = step(parents, seg)
		}
		last := strings.TrimSuffix(segs[len(segs)-1], "[]")
		for _, n := range parents {
			if m, ok := n.(map[string]any); ok {
				delete(m, last)
			}
		}
	}
	return out
}

func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = deepCopy(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = deepCopy(x)
		}
		return out
	default:
		return v
	}
}
