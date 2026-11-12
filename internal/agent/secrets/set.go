// Package secrets reads the named secrets an operator keeps in the agent's
// local configuration file (agent.yaml): it checks the file's protection,
// parses it strictly and reads it again once a minute. Field values never
// leave this package except through Set.Fields; every error, state and
// printed form names secrets and file positions only.
package secrets

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
)

// Set is an immutable collection of secrets: names mapping to their
// fields. The zero Set holds none. It is safe for concurrent use.
type Set struct {
	secrets map[string]map[string]string
}

// Len is the number of secrets.
func (s Set) Len() int { return len(s.secrets) }

// Names are the secrets' names, sorted.
func (s Set) Names() []string {
	return slices.Sorted(maps.Keys(s.secrets))
}

// Fields returns a copy of the named secret's fields; ok is false when the
// set does not define it.
func (s Set) Fields(name string) (fields map[string]string, ok bool) {
	f, ok := s.secrets[name]
	if !ok {
		return nil, false
	}
	return maps.Clone(f), true
}

// String names the secrets only, so a Set printed by mistake (%v, %+v, %s,
// a panic) shows no value.
func (s Set) String() string { return fmt.Sprintf("secrets%v", s.Names()) }

// GoString is String for %#v.
func (s Set) GoString() string { return s.String() }

// LogValue logs the names only.
func (s Set) LogValue() slog.Value { return slog.AnyValue(s.Names()) }
