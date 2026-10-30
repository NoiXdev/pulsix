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

// ReservedKey reports whether k is an object key ansible-core's JSON loader
// would read as a marker of its own (see reservedKeyPrefix).
func ReservedKey(k string) bool {
	return strings.HasPrefix(k, reservedKeyPrefix)
}

// ErrReservedKey refuses an untrusted value holding a reserved object key.
var ErrReservedKey = errors.New(`an object key starting with "__ansible" is reserved`)

// MaxUntrustedDepth caps how deeply an untrusted value may nest, counting
// objects and lists alike ({"a": ["x"]} is 2 deep). From roughly 250
// levels ansible-core's JSON load of the extra-vars file fails on Python's
// recursion limit and — like a reserved key — silently falls back to YAML,
// which drops every unsafe marker; YAML only overflows at roughly 500
// (final re-review 2). 32 is far below either and far above any real
// payload a mapping would pick.
const MaxUntrustedDepth = 32

// ErrTooDeep refuses an untrusted value nested deeper than MaxUntrustedDepth.
var ErrTooDeep = fmt.Errorf("the value is nested more than %d levels deep", MaxUntrustedDepth)

// ErrMalformedMarker refuses an extra-vars document Ansible would not load
// as JSON because of a reserved key that is not a well-formed marker.
var ErrMalformedMarker = errors.New(`an object with an "__ansible" key must be exactly {"__ansible_unsafe": "<text>"} or {"__ansible_vault": "<text>"}`)

// CheckUntrusted reports whether v, a value an unauthenticated sender
// supplied, can travel to Ansible marked by Inert: ErrReservedKey when an
// object inside it, at any depth, has a key starting with "__ansible", and
// ErrTooDeep when it nests deeper than MaxUntrustedDepth. Either would make
// Ansible's JSON load of the extra-vars file fail and drop the markers. The
// error names nothing from v: the caller names the variable.
func CheckUntrusted(v any) error {
	return checkUntrusted(v, 0)
}

func checkUntrusted(v any, depth int) error {
	switch x := v.(type) {
	case map[string]any:
		if depth++; depth > MaxUntrustedDepth {
			return ErrTooDeep
		}
		for k, item := range x {
			if ReservedKey(k) {
				return ErrReservedKey
			}
			if err := checkUntrusted(item, depth); err != nil {
				return err
			}
		}
	case []any:
		if depth++; depth > MaxUntrustedDepth {
			return ErrTooDeep
		}
		for _, item := range x {
			if err := checkUntrusted(item, depth); err != nil {
				return err
			}
		}
	}
	return nil
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
