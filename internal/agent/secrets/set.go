// Package secrets reads the agent's local configuration file (agent.yaml):
// the named secrets, the host-defined commands and the local checks an
// operator keeps there. It checks the file's protection, parses the whole
// file strictly and reads it again once a minute. Field values and command
// arguments never leave this package except through Set.Fields and
// Set.Command, local check params only through Set.LocalChecks; every
// error, state and printed form names secrets, commands, check keys and
// file positions only (a refused local check's problem may name a
// parameter, never its value).
package secrets

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/noixdev/pulsix/internal/scriptargs"
)

// Set is an immutable collection of secrets (names mapping to their
// fields), commands (names mapping to their program and arguments) and
// local checks, with the problems of the local checks it refused. The zero
// Set holds none. It is safe for concurrent use.
type Set struct {
	secrets            map[string]map[string]string
	commands           map[string]scriptargs.Template
	localChecks        []LocalCheck // key order
	localCheckProblems []string     // line order
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

// Command returns the named command's command line, whose Bind yields
// its program and arguments; ok is false when the set does not define it.
// A Template is immutable, so it is handed out as it is.
func (s Set) Command(name string) (cmd scriptargs.Template, ok bool) {
	cmd, ok = s.commands[name]
	return cmd, ok
}

// LocalChecks returns copies of the local checks, in key order.
func (s Set) LocalChecks() []LocalCheck {
	if len(s.localChecks) == 0 {
		return nil
	}
	out := make([]LocalCheck, len(s.localChecks))
	for i, c := range s.localChecks {
		out[i] = c.clone()
	}
	return out
}

// LocalCheckProblems returns why each refused local check was refused,
// "line N: <reason>", in line order.
func (s Set) LocalCheckProblems() []string {
	return slices.Clone(s.localCheckProblems)
}

// localCheckKeys are the local checks' keys, in key order.
func (s Set) localCheckKeys() []string {
	keys := make([]string, len(s.localChecks))
	for i, c := range s.localChecks {
		keys[i] = c.Key
	}
	return keys
}

// String names the secrets, commands and local checks and counts the
// refused local checks only, so a Set printed by mistake (%v, %+v, %s, a
// panic) shows no value, no argument and no param.
func (s Set) String() string {
	return fmt.Sprintf("secrets%v commands%v checks%v check_problems=%d",
		s.Names(), s.CommandNames(), s.localCheckKeys(), len(s.localCheckProblems))
}

// GoString is String for %#v.
func (s Set) GoString() string { return s.String() }

// LogValue logs the names only, as a group of secrets, commands, local
// check keys and the number of refused local checks.
func (s Set) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Any("secrets", s.Names()),
		slog.Any("commands", s.CommandNames()),
		slog.Any("checks", s.localCheckKeys()),
		slog.Int("check_problems", len(s.localCheckProblems)),
	)
}
