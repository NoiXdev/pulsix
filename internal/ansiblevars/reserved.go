package ansiblevars

import (
	"errors"
	"fmt"
	"strings"
)

// reservedKeyPrefix starts every object key ansible-core's JSON loader
// gives a meaning to: "__ansible_unsafe", "__ansible_vault" and
// "__ansible_type" (ansible-core 2.21.4). An object holding one that does
// not decode — a non-string marker, an unknown type — fails the whole JSON
// load of "-e @file", and Ansible then silently re-reads the file as YAML,
// where every {"__ansible_unsafe": …} marker is an ordinary dict whose
// strings are templated again (final re-review New Breakage 1). The prefix
// is reserved as a whole, so a key a later ansible-core adds is covered.
const reservedKeyPrefix = "__ansible"

// ErrReservedKey refuses an untrusted value holding a reserved object key.
var ErrReservedKey = errors.New(`object keys starting with "__ansible" are reserved`)

// ErrMalformedMarker refuses an extra-vars document Ansible would not load
// as JSON because of a reserved key that is not a well-formed marker.
var ErrMalformedMarker = errors.New(`an object with an "__ansible" key must be exactly {"__ansible_unsafe": "<text>"} or {"__ansible_vault": "<text>"}`)

// CheckUntrustedKeys reports ErrReservedKey when any object inside v, at
// any depth of lists and objects, has a key starting with "__ansible". It
// runs on a value an unauthenticated sender supplied, before Inert marks
// it; the error names where the key sits, never the value.
func CheckUntrustedKeys(v any) error {
	return walkObjects(v, "", func(path string, m map[string]any) error {
		for k := range m {
			if strings.HasPrefix(k, reservedKeyPrefix) {
				return fmt.Errorf("%w (at %s)", ErrReservedKey, orRoot(path))
			}
		}
		return nil
	})
}

// CheckMarkers reports ErrMalformedMarker when an object inside v has a key
// starting with "__ansible" but is not a well-formed marker — exactly one
// key, "__ansible_unsafe" or "__ansible_vault", holding a string. Such an
// object is what makes Ansible's JSON load fail; every other document
// passes. Inert's output always passes.
func CheckMarkers(v any) error {
	return walkObjects(v, "", func(path string, m map[string]any) error {
		reserved := false
		for k := range m {
			if strings.HasPrefix(k, reservedKeyPrefix) {
				reserved = true
				break
			}
		}
		if !reserved {
			return nil
		}
		if len(m) == 1 {
			for _, k := range []string{unsafeKey, vaultKey} {
				if _, ok := m[k].(string); ok {
					return nil
				}
			}
		}
		return fmt.Errorf("%w (at %s)", ErrMalformedMarker, orRoot(path))
	})
}

// vaultKey is the JSON form of Ansible's !vault tag.
const vaultKey = "__ansible_vault"

// walkObjects calls fn for every object inside v, depth first, stopping at
// the first error.
func walkObjects(v any, path string, fn func(path string, m map[string]any) error) error {
	switch x := v.(type) {
	case map[string]any:
		if err := fn(path, x); err != nil {
			return err
		}
		for k, item := range x {
			if err := walkObjects(item, path+"."+k, fn); err != nil {
				return err
			}
		}
	case []any:
		for i, item := range x {
			if err := walkObjects(item, fmt.Sprintf("%s[%d]", path, i), fn); err != nil {
				return err
			}
		}
	}
	return nil
}

func orRoot(path string) string {
	if path == "" {
		return "the top level"
	}
	return strings.TrimPrefix(path, ".")
}
