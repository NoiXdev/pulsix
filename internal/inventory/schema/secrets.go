package schema

import (
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Secret markers inside a vars document. A stored secret is
// {"$sealed": "<base64>"}; a read returns {"$secret": "set"} in its place; a
// write keeps a stored secret with {"$secret": "keep", "from": "<path>"}.
const (
	markerSealed = "$sealed"
	markerSecret = "$secret"
	markerFrom   = "from"
	secretSet    = "set"
	secretKeep   = "keep"
)

// Sealed wraps ciphertext the way it is stored inside a vars document.
func Sealed(ciphertext []byte) map[string]any {
	return map[string]any{markerSealed: base64.StdEncoding.EncodeToString(ciphertext)}
}

// SealedBytes returns the ciphertext when v is a stored sealed value.
func SealedBytes(v any) ([]byte, bool) {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return nil, false
	}
	s, ok := m[markerSealed].(string)
	if !ok {
		return nil, false
	}
	b, err := base64.StdEncoding.DecodeString(s)
	return b, err == nil
}

// Mask is what a read returns in place of a stored secret.
func Mask() map[string]any { return map[string]any{markerSecret: secretSet} }

// KeepFrom returns the path a keep marker points at.
func KeepFrom(v any) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 2 || m[markerSecret] != secretKeep {
		return "", false
	}
	from, ok := m[markerFrom].(string)
	return from, ok
}

// MaskVars returns a deep copy of values with every sealed value, at any
// depth and whether or not a schema still declares it, replaced by Mask().
// Reads use it so no ciphertext ever leaves the manager.
func MaskVars(values map[string]any) map[string]any {
	return ReplaceSealed(values, func() any { return Mask() })
}

// MaskDeclared returns a deep copy of values with every non-empty value
// under a field fields currently declares secret-typed replaced by Mask(),
// whether or not that value is actually sealed (backlog 2.21) — a plain
// string left over from before the field became secret (a string → secret
// schema change reseals nothing; only the next SetVars/Assign does) is
// masked exactly like a real sealed value, so a read never shows it in the
// clear. Every sealed value MaskVars would have caught — undeclared keys,
// and a value sitting under a field whose type changed away from secret —
// is still masked too, by the same ReplaceSealed sweep MaskVars uses,
// applied as a second pass so this is a strict superset of MaskVars's
// guarantee, not a replacement of it.
func MaskDeclared(fields []Field, values map[string]any) map[string]any {
	masked, err := MapSecrets(fields, values, func(_ string, v any) (any, error) {
		if isEmptySecret(v) {
			return v, nil
		}
		return Mask(), nil
	})
	if err != nil {
		// MapSecrets only ever errors through fn, and fn above never
		// returns one; unreachable in practice, but fail toward masking
		// everything rather than panicking or leaking values unmasked.
		return ReplaceSealed(values, func() any { return Mask() })
	}
	return ReplaceSealed(masked, func() any { return Mask() })
}

// isEmptySecret reports whether v is "nothing to hide": absent, or an
// empty string. Every other value — including a non-empty plain string, a
// sealed envelope, or any other shape a secret field should not hold but
// might because of a schema change — counts as set and is masked.
func isEmptySecret(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && s == ""
}

// ReplaceSealed returns a deep copy of values with every sealed value, at
// any depth and whether or not a schema still declares it, replaced by what
// with returns.
func ReplaceSealed(values map[string]any, with func() any) map[string]any {
	out := make(map[string]any, len(values))
	for k, v := range values {
		out[k] = replaceSealed(v, with)
	}
	return out
}

func replaceSealed(v any, with func() any) any {
	switch t := v.(type) {
	case map[string]any:
		if _, ok := SealedBytes(t); ok {
			return with()
		}
		return ReplaceSealed(t, with)
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = replaceSealed(x, with)
		}
		return out
	default:
		return v
	}
}

// StrayValue is one sealed value StraySealed found sitting somewhere it no
// longer belongs (backlog 2.22). Container is false when Path names one
// scalar-typed field's own value directly — "the field is no longer a
// secret field" is an accurate, specific complaint there. Container is
// true when Path names a spot that should hold a whole object or a
// repeater item (the field is Object/Repeater-typed, or the stored value
// simply is not shaped like anything the current schema at that point
// describes) but instead holds a sealed envelope, or has one somewhere
// inside it that no declared sub-field explains — "the field is no longer
// secret" would misdescribe that: there is no longer any well-formed
// field at Path to have a type at all, so the message needs different
// wording (renderer.go's straySealedMessage builds it).
type StrayValue struct {
	Path      string
	Container bool
}

// StraySealed returns, in field order, every sealed value that sits at a
// declared path whose field is not secret-typed — left behind when a
// secret field changed type, or when a field that used to be an object or
// a repeater of secret sub-fields changed shape entirely, leaving a whole
// stored structure (not one sub-field) as a stray sealed envelope. Values
// of secret fields are not strays, and undeclared (orphaned) keys are not
// looked at: rendering drops them.
func StraySealed(fields []Field, values map[string]any) []StrayValue {
	var out []StrayValue
	strayObject(&out, "", fields, values)
	return out
}

// StrayTopLevel returns the top-level variable names under which
// StraySealed finds a value, in field order and each once: a run leaves
// those variables out until a person enters their value again (backlog
// 7.87). Empty, never nil, when there is none.
func StrayTopLevel(fields []Field, values map[string]any) []string {
	out := []string{}
	for _, s := range StraySealed(fields, values) {
		if name := topLevel(s.Path); !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// topLevel is the top-level variable name of a value path
// ("vhosts[1].password" is under vhosts).
func topLevel(path string) string {
	if i := strings.IndexAny(path, ".["); i >= 0 {
		return path[:i]
	}
	return path
}

func strayObject(out *[]StrayValue, prefix string, fields []Field, values map[string]any) {
	for _, f := range fields {
		if v, ok := values[f.Name]; ok && v != nil {
			strayValue(out, join(prefix, f.Name), f, v)
		}
	}
}

func strayValue(out *[]StrayValue, path string, f Field, v any) {
	if f.Type == TypeSecret {
		return
	}
	if _, ok := SealedBytes(v); ok {
		*out = append(*out, StrayValue{Path: path, Container: f.Type == TypeObject || f.Type == TypeRepeater})
		return
	}
	switch t := v.(type) {
	case map[string]any:
		if f.Type == TypeObject {
			strayObject(out, path, f.Fields, t)
			return
		}
		sealedWithin(out, path, t)
	case []any:
		for i, it := range t {
			ip := fmt.Sprintf("%s[%d]", path, i)
			if obj, ok := it.(map[string]any); ok && f.Type == TypeRepeater {
				if _, sealed := SealedBytes(obj); !sealed {
					strayObject(out, ip, f.Fields, obj)
					continue
				}
			}
			sealedWithin(out, ip, it)
		}
	}
}

// sealedWithin appends the path of every sealed value inside v, which a
// field of the wrong shape holds. Every hit here is, by construction, a
// container-shaped mismatch (StrayValue.Container = true): sealedWithin
// only ever runs once strayValue has already established that v's own
// shape does not match what f declares, so nothing found underneath it is
// "one field that changed type" — it is inside a structure the current
// schema no longer describes at all.
func sealedWithin(out *[]StrayValue, path string, v any) {
	switch t := v.(type) {
	case map[string]any:
		if _, ok := SealedBytes(t); ok {
			*out = append(*out, StrayValue{Path: path, Container: true})
			return
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			sealedWithin(out, join(path, k), t[k])
		}
	case []any:
		for i, x := range t {
			sealedWithin(out, fmt.Sprintf("%s[%d]", path, i), x)
		}
	}
}

// SecretFunc transforms the value of one secret-typed field found at path.
type SecretFunc func(path string, v any) (any, error)

// MapSecrets returns a deep copy of values in which fn replaced every
// non-null value of a secret-typed field, at any depth. Undeclared keys and
// values whose shape does not match their field are copied unchanged;
// Validate reports those.
func MapSecrets(fields []Field, values map[string]any, fn SecretFunc) (map[string]any, error) {
	return mapObject("", fields, values, fn)
}

func mapObject(prefix string, fields []Field, values map[string]any, fn SecretFunc) (map[string]any, error) {
	byName := make(map[string]Field, len(fields))
	for _, f := range fields {
		byName[f.Name] = f
	}
	out := make(map[string]any, len(values))
	for k, v := range values {
		f, declared := byName[k]
		if !declared || v == nil {
			out[k] = copyValue(v)
			continue
		}
		mv, err := mapValue(join(prefix, k), f, v, fn)
		if err != nil {
			return nil, err
		}
		out[k] = mv
	}
	return out, nil
}

func mapValue(path string, f Field, v any, fn SecretFunc) (any, error) {
	switch f.Type {
	case TypeSecret:
		return fn(path, v)
	case TypeObject:
		if obj, ok := v.(map[string]any); ok {
			return mapObject(path, f.Fields, obj, fn)
		}
	case TypeRepeater:
		if items, ok := v.([]any); ok {
			out := make([]any, len(items))
			for i, it := range items {
				obj, isObj := it.(map[string]any)
				if !isObj {
					out[i] = copyValue(it)
					continue
				}
				m, err := mapObject(fmt.Sprintf("%s[%d]", path, i), f.Fields, obj, fn)
				if err != nil {
					return nil, err
				}
				out[i] = m
			}
			return out, nil
		}
	}
	return copyValue(v), nil
}

func copyValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = copyValue(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = copyValue(x)
		}
		return out
	default:
		return v
	}
}

// Lookup returns the value at path ("vhosts[1].basic_auth.password").
func Lookup(values map[string]any, path string) (any, bool) {
	var cur any = values
	for _, seg := range strings.Split(path, ".") {
		name, idxs, ok := splitSegment(seg)
		if !ok {
			return nil, false
		}
		m, isMap := cur.(map[string]any)
		if !isMap {
			return nil, false
		}
		if cur, ok = m[name]; !ok {
			return nil, false
		}
		for _, i := range idxs {
			arr, isArr := cur.([]any)
			if !isArr || i >= len(arr) {
				return nil, false
			}
			cur = arr[i]
		}
	}
	return cur, true
}

// splitSegment splits "vhosts[2][0]" into "vhosts" and [2 0].
func splitSegment(seg string) (string, []int, bool) {
	name, rest, hasIndex := strings.Cut(seg, "[")
	if name == "" {
		return "", nil, false
	}
	if !hasIndex {
		return name, nil, true
	}
	rest = "[" + rest
	var idxs []int
	for rest != "" {
		if !strings.HasPrefix(rest, "[") {
			return "", nil, false
		}
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			return "", nil, false
		}
		i, err := strconv.Atoi(rest[1:end])
		if err != nil || i < 0 {
			return "", nil, false
		}
		idxs = append(idxs, i)
		rest = rest[end+1:]
	}
	return name, idxs, true
}
