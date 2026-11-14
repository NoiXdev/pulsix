package secrets

import (
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Limits of the commands section.
const (
	maxCommands    = 256
	maxCommandArgs = 64 // the program and its arguments
)

const badCommandName = "a command name is 1 to 64 characters of A-Z a-z 0-9 _ . -"

// parseCommands decodes the mapping under commands; a missing or empty one
// holds none. A command is a name matching namePattern that maps to a
// sequence of 1 to maxCommandArgs single values, each at most
// maxValueBytes long and without a NUL character (no process can receive
// one); the first is the program and must be an absolute path on the
// agent's OS without control characters. An argument may hold a password,
// so like a secret's value it is never quoted in an error.
func parseCommands(n *yaml.Node) (map[string][]string, error) {
	if n == nil || isNull(n) {
		return nil, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, errAt(n, "commands must map command names to their program and arguments")
	}
	out := make(map[string][]string, len(n.Content)/2)
	lines := map[string]int{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		if err := checkKey(key, badCommandName); err != nil {
			return nil, err
		}
		if line, dup := lines[key.Value]; dup {
			return nil, errAt(key, "this command is already defined at line "+strconv.Itoa(line))
		}
		lines[key.Value] = key.Line
		if len(out) == maxCommands {
			return nil, errAt(key, "more than "+strconv.Itoa(maxCommands)+" commands")
		}
		argv, err := parseArgv(key, val)
		if err != nil {
			return nil, err
		}
		out[key.Value] = argv
	}
	return out, nil
}

// parseArgv decodes one command's sequence; name is the command's name
// node, where a missing sequence is reported.
func parseArgv(name, n *yaml.Node) ([]string, error) {
	if n.Kind != yaml.SequenceNode {
		at := n
		if isNull(n) {
			at = name // an empty value sits after the key; point at the key
		}
		return nil, errAt(at, "a command must be a list of its program and arguments")
	}
	if len(n.Content) == 0 {
		return nil, errAt(n, "a command needs at least its program")
	}
	argv := make([]string, 0, min(len(n.Content), maxCommandArgs))
	for i, entry := range n.Content {
		switch {
		case i == maxCommandArgs:
			return nil, errAt(entry, "a command has more than "+strconv.Itoa(maxCommandArgs)+" entries")
		case entry.Kind != yaml.ScalarNode:
			return nil, errAt(entry, "a command entry must be a single value, not a list or mapping")
		case isNull(entry):
			return nil, errAt(entry, `a command entry has no value; write "" for an empty one`)
		case len(entry.Value) > maxValueBytes:
			return nil, errAt(entry, "a command entry is longer than "+strconv.Itoa(maxValueBytes)+" bytes")
		case strings.IndexByte(entry.Value, 0) >= 0:
			return nil, errAt(entry, "a command entry must not contain a NUL character")
		case i == 0 && strings.ContainsFunc(entry.Value, isControl):
			return nil, errAt(entry, "the program path must not contain control characters")
		case i == 0 && !filepath.IsAbs(entry.Value):
			return nil, errAt(entry, "the program must be an absolute path")
		}
		argv = append(argv, entry.Value)
	}
	return argv, nil
}

// isControl reports a C0 control character or DEL. The program path may
// appear in a check's output, where such a character would break a line or
// a terminal.
func isControl(r rune) bool { return r < 0x20 || r == 0x7f }
