// Package scriptargs holds the rules of the values a script check passes
// to a command the host defines in its agent.yaml (backlog 3.6j): the
// shape of a placeholder's name and of a value, the manager's
// "command:placeholder=value" entry, the rules a host attaches to a
// placeholder and the Template that binds values into a command line.
//
// The host's administrator decides in agent.yaml which arguments of a
// command are placeholders and which values each accepts; the manager
// only supplies values. The manager checks the shape alone (it does not
// know the host's rules); the agent checks the shape again and then the
// rule, and passes each value as one whole argument, never through a
// shell. No error here quotes a value.
//
// It is a leaf package: the manager's descriptors (internal/checkplugins)
// and the agent (internal/agent/secrets, the script plugin) both import it.
package scriptargs

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Bounds shared by the manager and the agent.
const (
	// MaxValueBytes bounds one value.
	MaxValueBytes = 512
	// MaxPlaceholders bounds the placeholders of one command, and so the
	// values a check passes to it.
	MaxPlaceholders = 16
	// MaxPatternBytes bounds a pattern rule's expression.
	MaxPatternBytes = 512
	// MaxEnumValues bounds a values rule's list.
	MaxEnumValues = 64
	// MaxEntryBytes bounds one "command:placeholder=value" entry: a
	// 64-byte command name, a 32-byte placeholder name, a value and the
	// two separators fit.
	MaxEntryBytes = 640
)

// The value shape problems, phrased to follow `argument "<name>"`.
const (
	ProblemEmpty   = "must not be empty"
	ProblemLong    = "is longer than 512 bytes"
	ProblemUTF8    = "must be valid UTF-8"
	ProblemControl = "must not contain control, format or separator characters"
)

// The entry problems, phrased to follow the parameter's name ("args …").
const (
	ProblemEntryShape   = "must be command:placeholder=value"
	ProblemEntryCommand = "must start with a command name of 1 to 64 letters, digits, '_', '.' or '-'"
	ProblemEntryName    = "must name a placeholder of a lower-case letter followed by up to 31 lower-case letters, digits or '_'"
	ProblemEntryValue   = "must have a value of 1 to 512 bytes without control, format or separator characters"
)

var (
	namePattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	commandPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	// embedded finds a placeholder anywhere in an argument.
	embedded = regexp.MustCompile(`\{[a-z][a-z0-9_]{0,31}\}`)
)

// ValidName reports whether s is a placeholder name: a lower-case letter
// followed by up to 31 lower-case letters, digits or '_'.
func ValidName(s string) bool { return namePattern.MatchString(s) }

// ValueProblem says why v cannot be a value whatever the host's rule, or
// returns "": a value is 1 to MaxValueBytes bytes of valid UTF-8 without
// an invisible character (invisible). The text never quotes v.
func ValueProblem(v string) string {
	switch {
	case v == "":
		return ProblemEmpty
	case len(v) > MaxValueBytes:
		return ProblemLong
	case !utf8.ValidString(v):
		return ProblemUTF8
	case strings.ContainsFunc(v, invisible):
		return ProblemControl
	}
	return ""
}

// ParseEntry splits a "command:placeholder=value" entry at its first ':'
// and the first '=' after it; ok is false when either is missing. The
// parts are not checked (EntryProblem does).
func ParseEntry(s string) (command, name, value string, ok bool) {
	command, rest, ok := strings.Cut(s, ":")
	if !ok {
		return "", "", "", false
	}
	name, value, ok = strings.Cut(rest, "=")
	if !ok {
		return "", "", "", false
	}
	return command, name, value, true
}

// EntryProblem says why s is not a valid "command:placeholder=value"
// entry, or returns "". It never quotes the value.
func EntryProblem(s string) string {
	command, name, value, ok := ParseEntry(s)
	switch {
	case !ok:
		return ProblemEntryShape
	case !commandPattern.MatchString(command):
		return ProblemEntryCommand
	case !ValidName(name):
		return ProblemEntryName
	case ValueProblem(value) != "":
		return ProblemEntryValue
	}
	return ""
}

// Placeholder reports whether arg is exactly one placeholder, "{name}",
// and its name.
func Placeholder(arg string) (string, bool) {
	if len(arg) < 3 || arg[0] != '{' || arg[len(arg)-1] != '}' {
		return "", false
	}
	name := arg[1 : len(arg)-1]
	return name, ValidName(name)
}

// EmbedsPlaceholder reports whether arg holds a placeholder among other
// text ("--port={port}"), which a command line may not.
func EmbedsPlaceholder(arg string) bool {
	if _, ok := Placeholder(arg); ok {
		return false
	}
	return embedded.MatchString(arg)
}

// invisible reports a character no value may hold on either side: a
// control character (C0, DEL, C1), a format character (Cf: soft hyphen,
// bidi overrides and isolates, zero-width characters, the byte order
// mark), a line or paragraph separator, or a non-character (U+FDD0 to
// U+FDEF and the last two code points of every plane). Each either breaks
// a line, hides or reorders text in output, or is no character at all.
func invisible(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) ||
		(r >= 0xfdd0 && r <= 0xfdef) || r&0xfffe == 0xfffe
}

// printableASCII reports whether v holds only the characters from space
// to '~'.
func printableASCII(v string) bool {
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] > 0x7e {
			return false
		}
	}
	return true
}
