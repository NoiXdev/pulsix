package requirements

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// errNotPlainYAML: the file uses a YAML feature that galaxy's loader (PyYAML)
// and yaml.v3 read differently, so the shape check could pass a file galaxy
// reads another way: anchors, aliases, merge keys (PyYAML merges any key
// tagged as a merge, an alias of a "<<" key too; yaml.v3 only a plain "<<")
// and explicit tags.
var errNotPlainYAML = errors.New("anchors, aliases, merge keys and tags are not supported")

// errSecondDocument: galaxy's loader refuses a stream of several documents;
// yaml.v3 would read the first one only.
var errSecondDocument = errors.New("only one YAML document is allowed")

// errOddText: the file is not plain UTF-8 text. The tag check
// (startsWithTag) finds a node's text by yaml.v3's line and column, and the
// loaders must see the same characters, so anything they treat differently
// is refused:
//   - UTF-16 and UTF-32 (yaml.v3 decodes them) and invalid UTF-8;
//   - line breaks other than \n and \r\n (yaml.v3 also breaks at a lone \r,
//     U+0085, U+2028 and U+2029);
//   - a U+FEFF after the first character: galaxy's loader (libyaml) skips
//     one at the start of any line, yaml.v3 only at the start of the file,
//     so a name: or include: key behind one reads differently;
//   - every other invisible character: control characters except tab, LF and
//     the CR of a CRLF (C0, DEL, C1) and format characters (Unicode category
//     Cf: zero-width characters, bidi controls, the soft hyphen, …).
var errOddText = errors.New("only UTF-8 text with ordinary line breaks and no invisible characters is supported")

// checkText refuses data that is not plain UTF-8 text (errOddText). One
// leading UTF-8 byte order mark is fine.
func checkText(data []byte) error {
	text := bytes.TrimPrefix(data, []byte("\uFEFF"))
	if !utf8.Valid(text) {
		return errOddText
	}
	for i, r := range string(text) {
		switch {
		case r == '\r':
			if i+1 == len(text) || text[i+1] != '\n' {
				return errOddText
			}
		case invisible(r):
			return errOddText
		}
	}
	return nil
}

// checkValues refuses a scalar below n, key or value, whose decoded text
// holds a character checkText refuses in the raw text (errOddText): a
// double-quoted escape ("\x1c", "\u200b", "\N", …) must not bring one back.
// Galaxy's str.strip() trims U+001C-U+001F where Go would not, for one.
// Tab, LF and CR are fine in a value ("\t", "\n", "\r").
func checkValues(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		if !utf8.ValidString(n.Value) {
			return errOddText
		}
		for _, r := range n.Value {
			if r != '\r' && invisible(r) {
				return errOddText
			}
		}
	}
	for _, c := range n.Content {
		if err := checkValues(c); err != nil {
			return err
		}
	}
	return nil
}

// invisible reports a character refused in a requirements file: a control
// character other than tab and LF (C0, DEL, C1; the callers decide on CR),
// U+2028, U+2029 and the format characters (Unicode category Cf, U+FEFF
// among them).
func invisible(r rune) bool {
	if r == '\t' || r == '\n' {
		return false
	}
	return r == '\u2028' || r == '\u2029' || unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}

// decodePlain parses data as one plain YAML document. (nil, nil) for an empty
// document (nothing but comments or whitespace). A parse error comes back as
// it is; errOddText (raw text or a decoded scalar), errSecondDocument and
// errNotPlainYAML name the refusals.
func decodePlain(data []byte) (*yaml.Node, error) {
	if err := checkText(data); err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var next yaml.Node
	if err := dec.Decode(&next); err == nil {
		return nil, errSecondDocument
	} else if !errors.Is(err, io.EOF) {
		return nil, err
	}
	lines := strings.Split(strings.TrimPrefix(string(data), "\uFEFF"), "\n")
	if !isPlain(&doc, lines) {
		return nil, errNotPlainYAML
	}
	if err := checkValues(&doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

// isPlain reports that no node below n carries an anchor, is an alias, is a
// "<<" mapping key (quoted too) or has an explicit tag.
//
// yaml.v3 marks a tag as TaggedStyle except the non-specific tag "!", which
// it drops. A node's position is where its properties start, so a node whose
// source text starts with '!' carries a tag (or its first child does), which
// catches "!" as well.
func isPlain(n *yaml.Node, lines []string) bool {
	if n.Anchor != "" || n.Kind == yaml.AliasNode || n.Style&yaml.TaggedStyle != 0 || startsWithTag(n, lines) {
		return false
	}
	for i, c := range n.Content {
		if n.Kind == yaml.MappingNode && i%2 == 0 && c.Kind == yaml.ScalarNode && c.Value == "<<" {
			return false
		}
		if !isPlain(c, lines) {
			return false
		}
	}
	return true
}

// startsWithTag reports that the source text at n's position (1-based line,
// 1-based column in characters) starts with '!'.
func startsWithTag(n *yaml.Node, lines []string) bool {
	if n.Line < 1 || n.Line > len(lines) || n.Column < 1 {
		return false
	}
	line := []rune(lines[n.Line-1])
	return n.Column <= len(line) && line[n.Column-1] == '!'
}
