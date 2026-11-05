package varsource

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"gopkg.in/yaml.v3"
)

// Text limits. Every scalar a reader reads is charged by its length against
// the file's byte budget (maxReadBytes, 16 times the 512 KiB file cap): an
// alias repeats a scalar's bytes without repeating them in the file.
const (
	// maxReadBytes is the byte budget of one file; past it the file is
	// given up (file_too_complex).
	maxReadBytes = 8 << 20
	// maxNumberLen bounds a number's text: a longer one is never decoded.
	maxNumberLen = 64
	// maxDefaultBytes bounds the text of one default, a list's items
	// summed; a larger default is refused once the cap is passed.
	maxDefaultBytes = 64 << 10
	// maxLabelBytes and maxDescriptionBytes cut a label and a description,
	// with an ellipsis.
	maxLabelBytes       = 256
	maxDescriptionBytes = 4096
	// maxMessageBytes cuts a note's message, with an ellipsis.
	maxMessageBytes = 512
	// maxQuotedBytes cuts each string from a file a message quotes.
	maxQuotedBytes = 64
	// maxNameBytes is the longest name a field may have (the schema's name
	// rule); a longer key is refused before it is used in any path.
	maxNameBytes = 64
)

const ellipsis = "…"

// clip cuts s to at most limit bytes at a rune boundary, ending in an
// ellipsis when it cut.
func clip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}

// clipWords cuts s like clip, but at the last space before the cut when
// there is one in the second half, so a label does not end in half a word.
func clipWords(s string, limit int) string {
	c := clip(s, limit)
	if c == s {
		return s
	}
	body := strings.TrimSuffix(c, ellipsis)
	if i := strings.LastIndexAny(body, " \t"); i > len(body)/2 {
		body = strings.TrimRight(body[:i], " \t")
	}
	return body + ellipsis
}

// spendBytes charges n bytes read against the file's byte budget; false
// once it is spent, after which every reader returns nothing.
func (y *yamlFile) spendBytes(n int) bool {
	if y.over {
		return false
	}
	if n > y.bytesLeft {
		y.bytesLeft, y.over = 0, true
		return false
	}
	y.bytesLeft -= n
	return true
}

func (y *yamlFile) bytesRead() int { return maxReadBytes - y.bytesLeft }

// scalarText reads a scalar's text, charging its bytes; ok is false for a
// missing, null or non-scalar node.
func (y *yamlFile) scalarText(n *yaml.Node) (string, bool) {
	n = resolve(n)
	if n == nil || n.Kind != yaml.ScalarNode || isNull(n) || !y.spendBytes(scalarCost(n)) {
		return "", false
	}
	return n.Value, true
}

// boolValue reads a boolean attribute (YAML 1.1 spellings such as "yes"
// included, see scalarOf); anything else is false.
func (y *yamlFile) boolValue(n *yaml.Node) bool {
	if n = resolve(n); n == nil || n.Kind != yaml.ScalarNode || !y.spendBytes(scalarCost(n)) {
		return false
	}
	_, v, _ := scalarOf(n)
	b, _ := v.(bool)
	return b
}

// scalarCost is what reading a scalar charges: its text and its tag (a
// verbatim tag may be long).
func scalarCost(n *yaml.Node) int { return len(n.Value) + len(n.Tag) }

// readScalar reads one scalar of a default (or of a marked value), charging
// its bytes and adding them to *size. asText takes a number as its text
// (a long one included) for a field that holds text. why says what keeps it
// from being a default: the default cap passed, a number too long to
// decode, or a tag with no field counterpart.
func (y *yamlFile) readScalar(s *yaml.Node, size *int, asText bool) (typ schema.Type, v any, why string) {
	if !y.spendBytes(scalarCost(s)) {
		return "", nil, "could not be read within the file's budget"
	}
	if *size += len(s.Value); *size > maxDefaultBytes {
		return "", nil, fmt.Sprintf("is larger than %d bytes", maxDefaultBytes)
	}
	if asText && longNumber(s) {
		return schema.TypeString, s.Value, ""
	}
	t, v, ok := scalarOf(s)
	switch {
	case ok:
		return t, v, ""
	case longNumber(s):
		return "", nil, fmt.Sprintf("holds a number longer than %d characters", maxNumberLen)
	}
	return "", nil, fmt.Sprintf("holds a value tagged %s, which has no form field", short(s.ShortTag()))
}

// defaultValue reads a default: a scalar, or a list of scalars. asText
// takes scalars as their text as written (a str option's default 8080 is
// "8080", like Ansible's conversion; an enum compares as text). why says
// why the default cannot be used.
func (y *yamlFile) defaultValue(n *yaml.Node, asText bool) (any, string) {
	n = resolve(n)
	size := 0
	one := func(s *yaml.Node) (any, string) {
		s = resolve(s)
		if s.Kind != yaml.ScalarNode {
			return nil, "is not a single value or a list of single values"
		}
		t, v, why := y.readScalar(s, &size, asText)
		if why == "" && asText && t != "" {
			v = s.Value
		}
		return v, why
	}
	if n.Kind != yaml.SequenceNode {
		return one(n)
	}
	items := make([]any, 0, len(n.Content))
	for _, it := range y.items(n) {
		v, why := one(it)
		if why != "" {
			return nil, why
		}
		items = append(items, v)
	}
	return items, ""
}

// yaml11Bools are the plain scalars PyYAML (and so Ansible) reads as
// booleans beyond YAML 1.2's true/false, which yaml.v3 reads as strings.
var yaml11Bools = map[string]bool{
	"yes": true, "Yes": true, "YES": true, "on": true, "On": true, "ON": true,
	"no": false, "No": false, "NO": false, "off": false, "Off": false, "OFF": false,
}

// scalarOf maps a scalar to the field type its YAML tag implies and its
// value, reading plain yes/no/on/off as booleans the way Ansible does. A
// null has no type and no value; ok is false for a tag with no field
// counterpart (a custom tag such as !vault, or !!binary) and for a number
// longer than maxNumberLen. It charges nothing: callers read through the
// yamlFile.
func scalarOf(n *yaml.Node) (schema.Type, any, bool) {
	switch n.ShortTag() {
	case "!!null":
		return "", nil, true
	case "!!str":
		// Plain and untagged only: a quoted or !!str-tagged "yes" is text.
		if b, ok := yaml11Bools[n.Value]; ok && n.Style == 0 {
			return schema.TypeBool, b, true
		}
		return schema.TypeString, n.Value, true
	case "!!timestamp", "!unsafe":
		return schema.TypeString, n.Value, true
	case "!!int":
		return numberOf(n, schema.TypeInt)
	case "!!float":
		return numberOf(n, schema.TypeFloat)
	case "!!bool":
		var b bool
		if err := n.Decode(&b); err != nil {
			return "", nil, false
		}
		return schema.TypeBool, b, true
	}
	return "", nil, false
}

func longNumber(n *yaml.Node) bool {
	t := n.ShortTag()
	return (t == "!!int" || t == "!!float") && len(n.Value) > maxNumberLen
}

// numberOf decodes an !!int or !!float scalar of at most maxNumberLen
// characters. An integer too large for int64 becomes a float64, which Check
// then refuses as a whole number.
func numberOf(n *yaml.Node, typ schema.Type) (schema.Type, any, bool) {
	if longNumber(n) {
		return "", nil, false
	}
	var v any
	if err := n.Decode(&v); err != nil {
		return "", nil, false
	}
	switch x := v.(type) {
	case int, int64, float64:
	case uint64:
		v = float64(x)
	default:
		return "", nil, false
	}
	return typ, v, true
}
