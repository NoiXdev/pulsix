package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Limits of the file format.
const (
	maxFileBytes  = 1 << 20 // the whole file, refused before parsing
	maxSecrets    = 256
	maxValueBytes = 4096 // one field value
)

// namePattern is what a secret name and a field name may look like.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

const (
	badSecretName = "a secret name is 1 to 64 characters of A-Z a-z 0-9 _ . -"
	badFieldName  = "a field name is 1 to 64 characters of A-Z a-z 0-9 _ . -"
)

// positionError is a problem at a place in the file. Its text names the
// line and column and a fixed reason — never anything read from the file.
type positionError struct {
	line, column int
	reason       string
}

func (e *positionError) Error() string {
	return fmt.Sprintf("line %d, column %d: %s", e.line, e.column, e.reason)
}

func errAt(n *yaml.Node, reason string) error {
	return &positionError{line: n.Line, column: n.Column, reason: reason}
}

// parse decodes the file format:
//
//	secrets:
//	  <name>:
//	    <field>: <value>
//	commands:
//	  <name>: [<program>, <argument>, …]
//	  <name>:
//	    run: [<program>, <argument or {placeholder}>, …]
//	    args:
//	      <placeholder>: {pattern: <RE2> | min: <n>, max: <n> | values: [<value>, …], default: <value>}
//	checks:
//	  <key>:
//	    plugin: <plugin>
//	    interval_seconds: <seconds>
//	    params:
//	      <name>: <value or list of values>
//	probes:
//	  enabled: <true | false>
//	  deny_cidrs: [<CIDR>, …]
//
// strictly: the only top-level keys are secrets, commands, checks and
// probes, each
// optional and an empty one meaning none; names and field names match
// namePattern; duplicate keys, anchors (and so aliases), nulls, nested
// values and a second document are refused. A scalar value is taken as
// written, so an unquoted number or boolean (port: 5432, tls: true) is the
// text "5432" or "true". parseCommands has the rules of a command,
// parseLocalChecks those of a local check — whose params, unlike a secret's
// fields, are typed the way YAML's core schema types them — and
// parseProbes those of the probe policy (backlog 3.82).
//
// Explicit YAML tags are refused, wherever they stand, with the fixed
// reason badTagReason — except !!str on a single value (a key or a field
// value), which only says what every value already is. A tag such as
// !!binary, !!int or a custom !vault would otherwise suggest a decoding
// this format never does.
//
// Anchors are refused wherever they stand, with the fixed reason
// badAnchorReason, whether or not an alias uses them. An alias needs an
// anchor before it, so no alias reaches the decoding below.
func parse(data []byte) (Set, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return Set{}, nil // empty, or comments only
		}
		return Set{}, syntaxError(err)
	}
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case errors.Is(err, io.EOF):
	case err != nil:
		return Set{}, syntaxError(err)
	default:
		return Set{}, errAt(&extra, "the file holds more than one YAML document")
	}
	root := &doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return Set{}, nil
		}
		root = root.Content[0]
	}
	if err := checkNodes(root); err != nil {
		return Set{}, err
	}
	if isNull(root) {
		return Set{}, nil
	}
	if root.Kind != yaml.MappingNode {
		return Set{}, errAt(root, "the file must be a mapping with a secrets, commands, checks or probes key")
	}
	sections := map[string]*yaml.Node{}
	seen := map[string]int{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i]
		if key.Kind != yaml.ScalarNode || !slices.Contains(topLevelKeys, key.Value) {
			return Set{}, errAt(key, "unknown top-level key; only secrets, commands, checks and probes are allowed")
		}
		if line, dup := seen[key.Value]; dup {
			return Set{}, errAt(key, key.Value+" is already set at line "+strconv.Itoa(line))
		}
		seen[key.Value] = key.Line
		sections[key.Value] = root.Content[i+1]
	}
	secrets, err := parseSecrets(sections["secrets"])
	if err != nil {
		return Set{}, err
	}
	commands, err := parseCommands(sections["commands"])
	if err != nil {
		return Set{}, err
	}
	checks, problems, err := parseLocalChecks(sections["checks"])
	if err != nil {
		return Set{}, err
	}
	probes, err := parseProbes(sections["probes"])
	if err != nil {
		return Set{}, err
	}
	return Set{secrets: secrets, commands: commands, localChecks: checks, localCheckProblems: problems, probes: probes}, nil
}

// topLevelKeys are the file's sections.
var topLevelKeys = []string{"secrets", "commands", "checks", "probes"}

// parseSecrets decodes the mapping under secrets; a missing or empty one
// holds none.
func parseSecrets(n *yaml.Node) (map[string]map[string]string, error) {
	if n == nil || isNull(n) {
		return nil, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, errAt(n, "secrets must map secret names to their fields")
	}
	out := make(map[string]map[string]string, len(n.Content)/2)
	lines := map[string]int{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		if err := checkKey(key, badSecretName); err != nil {
			return nil, err
		}
		if line, dup := lines[key.Value]; dup {
			return nil, errAt(key, "this secret is already defined at line "+strconv.Itoa(line))
		}
		lines[key.Value] = key.Line
		if len(out) == maxSecrets {
			return nil, errAt(key, "more than "+strconv.Itoa(maxSecrets)+" secrets")
		}
		fields, err := parseFields(key, val)
		if err != nil {
			return nil, err
		}
		out[key.Value] = fields
	}
	return out, nil
}

// parseFields decodes one secret's field mapping; name is the secret's name
// node, where a missing mapping is reported.
func parseFields(name, n *yaml.Node) (map[string]string, error) {
	if n.Kind != yaml.MappingNode {
		at := n
		if isNull(n) {
			at = name // an empty value sits after the key; point at the key
		}
		return nil, errAt(at, "a secret must map field names to values")
	}
	out := make(map[string]string, len(n.Content)/2)
	lines := map[string]int{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		if err := checkKey(key, badFieldName); err != nil {
			return nil, err
		}
		if line, dup := lines[key.Value]; dup {
			return nil, errAt(key, "this field is already set at line "+strconv.Itoa(line))
		}
		lines[key.Value] = key.Line
		switch {
		case val.Kind != yaml.ScalarNode:
			return nil, errAt(val, "a field value must be a single value, not a list or mapping")
		case isNull(val):
			return nil, errAt(val, `a field has no value; write "" for an empty one`)
		case len(val.Value) > maxValueBytes:
			return nil, errAt(val, "a field value is longer than "+strconv.Itoa(maxValueBytes)+" bytes")
		}
		out[key.Value] = val.Value
	}
	return out, nil
}

// The fixed reasons for the node properties parse refuses.
const (
	badTagReason    = "YAML tags are not supported, except !!str on a value"
	badAnchorReason = "YAML anchors and aliases are not supported"
)

// checkNodes refuses the first node, in document order, that carries an
// anchor or an explicit tag other than !!str on a scalar. Neither reason
// quotes the anchor's name or the tag.
func checkNodes(n *yaml.Node) error {
	if n.Anchor != "" {
		return errAt(n, badAnchorReason)
	}
	if n.Style&yaml.TaggedStyle != 0 && (n.Kind != yaml.ScalarNode || n.ShortTag() != "!!str") {
		return errAt(n, badTagReason)
	}
	for _, c := range n.Content {
		if err := checkNodes(c); err != nil {
			return err
		}
	}
	return nil
}

// checkKey refuses a key that is not a plain name.
func checkKey(key *yaml.Node, reason string) error {
	if key.Kind != yaml.ScalarNode || !namePattern.MatchString(key.Value) {
		return errAt(key, reason)
	}
	return nil
}

// isNull reports an empty or explicit null scalar ("", ~, null) that was
// not quoted.
func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null"
}

// yamlLine matches yaml's syntax error text: "yaml: line N: <problem>".
var yamlLine = regexp.MustCompile(`^yaml: (?:line (\d+): )?(.*)$`)

// knownProblems are yaml syntax problems kept in the reduced error: fixed
// strings from the scanner and parser that quote nothing from the file.
// Every other problem is dropped, because some (an unknown anchor's) quote
// the file.
var knownProblems = map[string]bool{
	"mapping values are not allowed in this context":               true,
	"did not find expected key":                                    true,
	"did not find expected node content":                           true,
	"did not find expected '-' indicator":                          true,
	"did not find expected ',' or ']'":                             true,
	"did not find expected ',' or '}'":                             true,
	"found character that cannot start any token":                  true,
	"found unexpected end of stream":                               true,
	"found a tab character that violates indentation":              true,
	"found a tab character where an indentation space is expected": true,
	"could not find expected ':'":                                  true,
	"block sequence entries are not allowed in this context":       true,
	"found unknown escape character":                               true,
	"found unexpected document indicator":                          true,
	"control characters are not allowed":                           true,
	"invalid leading UTF-8 octet":                                  true,
	"incomplete UTF-8 octet sequence":                              true,
	"invalid trailing UTF-8 octet":                                 true,
	"invalid Unicode character":                                    true,
}

// syntaxError reduces a yaml syntax error to its line and, when it is one
// of knownProblems, its reason.
func syntaxError(err error) error {
	m := yamlLine.FindStringSubmatch(err.Error())
	msg := "not valid YAML"
	switch {
	case m == nil:
	case knownProblems[m[2]]:
		msg += " (" + m[2] + ")"
	case strings.HasPrefix(m[2], "unknown anchor "):
		// "unknown anchor '<name>' referenced" quotes the file.
		msg += " (an alias refers to an unknown anchor)"
	}
	if m != nil && m[1] != "" {
		return errors.New("line " + m[1] + ": " + msg)
	}
	return errors.New(msg)
}
