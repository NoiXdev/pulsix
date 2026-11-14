// Package secrets reads the agent's local configuration file (agent.yaml):
// the named secrets and the host-defined commands an operator keeps there.
// It checks the file's protection, parses the whole file strictly and
// reads it again once a minute. Field values and command arguments never
// leave this package except through Set.Fields and Set.Command; every
// error, state and printed form names secrets, commands and file positions
// only.
package secrets

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
)

// Set is an immutable collection of secrets (names mapping to their
// fields) and commands (names mapping to their program and arguments). The
// zero Set holds none. It is safe for concurrent use.
type Set struct {
	secrets  map[string]map[string]string
	commands map[string][]string
}

// Len is the number of secrets; commands are not counted.
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

// CommandNames are the commands' names, sorted.
func (s Set) CommandNames() []string {
	return slices.Sorted(maps.Keys(s.commands))
}

// Command returns a copy of the named command's program and arguments; ok
// is false when the set does not define it.
func (s Set) Command(name string) (argv []string, ok bool) {
	a, ok := s.commands[name]
	if !ok {
		return nil, false
	}
	return slices.Clone(a), true
}

// String names the secrets and commands only, so a Set printed by mistake
// (%v, %+v, %s, a panic) shows no value and no argument.
func (s Set) String() string {
	return fmt.Sprintf("secrets%v commands%v", s.Names(), s.CommandNames())
}

// GoString is String for %#v.
func (s Set) GoString() string { return s.String() }

// LogValue logs the names only, as a group of secrets and commands.
func (s Set) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Any("secrets", s.Names()),
		slog.Any("commands", s.CommandNames()),
	)
}
