package requirements

import (
	"bytes"
	"errors"
	"io"
	"strings"
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

// errOddText: the file is not UTF-8 text whose lines break at \n or \r\n.
// yaml.v3 also reads UTF-16 and breaks lines at a lone \r, U+0085, U+2028 and
// U+2029; the tag check (startsWithTag) finds a node's text by yaml.v3's line
// and column, so the lines it splits must be the lines yaml.v3 counts.
var errOddText = errors.New("only UTF-8 text with ordinary line breaks is supported")

// checkText refuses data that is not valid UTF-8 (UTF-16 and UTF-32 among
// it: invalid bytes or NULs), holds a NUL, a \r not followed by \n, U+0085,
// U+2028 or U+2029. A leading UTF-8 byte order mark is fine.
func checkText(data []byte) error {
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 ||
		bytes.ContainsAny(data, "\u0085\u2028\u2029") {
		return errOddText
	}
	for i, b := range data {
		if b == '\r' && (i+1 == len(data) || data[i+1] != '\n') {
			return errOddText
		}
	}
	return nil
}

// decodePlain parses data as one plain YAML document. (nil, nil) for an empty
// document (nothing but comments or whitespace). A parse error comes back as
// it is; errOddText, errSecondDocument and errNotPlainYAML name the refusals.
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
