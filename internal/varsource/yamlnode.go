package varsource

import (
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"gopkg.in/yaml.v3"
)

// maxMergeSources bounds how many mappings one mapping may merge in
// through YAML "<<" keys.
const maxMergeSources = 64

// pair is one key/value entry of a mapping.
type pair struct{ key, value *yaml.Node }

// resolve follows an alias to the node its anchor names.
func resolve(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

// documentRoot is the top node of a parsed document, nil for an empty one.
func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	return resolve(doc.Content[0])
}

func isNull(n *yaml.Node) bool {
	n = resolve(n)
	return n == nil || (n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null")
}

// maxVisits is the budget of mapping entries and sequence items one file's
// readers may visit. A file of 512 KiB without anchors has well under 100 000
// entries; anchors, aliases and merge keys can make a small file describe
// far more, and past the budget the file is given up (file_too_complex).
const maxVisits = 1_000_000

// yamlFile is the read state of one parsed YAML file, shared by every
// mapper of that file: the entries of each mapping, computed once per node
// (aliases let one node appear in many places), the budget of visits, and
// the notes the readers raise (duplicate_key). Whoever creates it owns the
// file: when done, it checks tooComplex (the file then counts as not read)
// and takes the notes.
type yamlFile struct {
	file    string
	entries map[*yaml.Node][]pair
	byKey   map[*yaml.Node]map[string]*yaml.Node
	left    int
	over    bool
	notes   []Note
}

func newYAMLFile(file string) *yamlFile {
	return &yamlFile{
		file:    file,
		entries: map[*yaml.Node][]pair{},
		byKey:   map[*yaml.Node]map[string]*yaml.Node{},
		left:    maxVisits,
	}
}

// spend takes n visits from the budget; false once it is spent, after
// which every reader returns nothing.
func (y *yamlFile) spend(n int) bool {
	if y.over {
		return false
	}
	if n > y.left {
		y.left, y.over = 0, true
		return false
	}
	y.left -= n
	return true
}

func (y *yamlFile) visits() int    { return maxVisits - y.left }
func (y *yamlFile) exceeded() bool { return y.over }

// tooComplex is the one warning for a file whose budget ran out; ok is
// false while the budget holds. A file given up counts as not read.
func (y *yamlFile) tooComplex() (Note, bool) {
	if !y.over {
		return Note{}, false
	}
	return warning(codeFileTooComplex, y.file, 0, "reading this file took more than %d steps (anchors, aliases and merge keys expanded); nothing read from it", maxVisits), true
}

// pairs returns the entries of a mapping in order, nil for anything else.
// A key set twice keeps its first place and its last value, as Ansible's
// loader does (duplicate_key). Merge keys ("<<: *anchor" or "<<: [*a, *b]")
// are expanded in place; a key the mapping sets itself wins over a merged
// one, an earlier merge over a later one.
func (y *yamlFile) pairs(n *yaml.Node) []pair {
	n = resolve(n)
	if n == nil || n.Kind != yaml.MappingNode || y.over {
		return nil
	}
	if ps, ok := y.entries[n]; ok {
		return ps
	}
	y.entries[n] = nil // an anchor merged into itself reads as empty
	if !y.spend(len(n.Content) / 2) {
		return nil
	}
	last := make(map[string]int, len(n.Content)/2) // key → index of its last entry
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		if isMergeKey(k) {
			continue
		}
		if j, dup := last[k.Value]; dup {
			y.notes = append(y.notes, warning(codeDuplicateKey, y.file, k.Line,
				"%q is set twice in one mapping (lines %d and %d); the later one counts", k.Value, n.Content[j].Line, k.Line))
		}
		last[k.Value] = i
	}
	out := make([]pair, 0, len(last))
	placed := make(map[string]bool, len(last))
	sources := 0
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		if !isMergeKey(k) {
			if !placed[k.Value] {
				placed[k.Value] = true
				j := last[k.Value]
				out = append(out, pair{n.Content[j], n.Content[j+1]})
			}
			continue
		}
		for _, src := range mergeSources(n.Content[i+1]) {
			if sources == maxMergeSources {
				break
			}
			sources++
			ps := y.pairs(src)
			if !y.spend(len(ps)) {
				return nil
			}
			for _, p := range ps {
				if _, own := last[p.key.Value]; own || placed[p.key.Value] {
					continue
				}
				placed[p.key.Value] = true
				out = append(out, p)
			}
		}
	}
	y.entries[n] = out
	return out
}

func isMergeKey(k *yaml.Node) bool {
	return k.Kind == yaml.ScalarNode && k.ShortTag() == "!!merge"
}

func mergeSources(v *yaml.Node) []*yaml.Node {
	v = resolve(v)
	if v == nil {
		return nil
	}
	if v.Kind == yaml.SequenceNode {
		return v.Content
	}
	return []*yaml.Node{v}
}

// attrs indexes a mapping's entries by key, aliases resolved; a repeated
// key keeps its last value, as Ansible's loader does.
func (y *yamlFile) attrs(n *yaml.Node) map[string]*yaml.Node {
	n = resolve(n)
	if m, ok := y.byKey[n]; ok {
		return m
	}
	ps := y.pairs(n)
	out := make(map[string]*yaml.Node, len(ps))
	for _, p := range ps {
		out[p.key.Value] = resolve(p.value)
	}
	if n != nil {
		y.byKey[n] = out
	}
	return out
}

// entry returns the entry under key in a mapping, ok false when n is no
// mapping or lacks the key.
func (y *yamlFile) entry(n *yaml.Node, key string) (pair, bool) {
	for _, p := range y.pairs(n) {
		if p.key.Value == key {
			return p, true
		}
	}
	return pair{}, false
}

// lookup returns the value under key in a mapping (aliases resolved), nil
// when n is no mapping or lacks the key.
func (y *yamlFile) lookup(n *yaml.Node, key string) *yaml.Node {
	return y.attrs(n)[key]
}

// items is a sequence's entries, nil for anything else. Every call spends
// the sequence's length: one sequence may be aliased in many places.
func (y *yamlFile) items(n *yaml.Node) []*yaml.Node {
	if n = resolve(n); n == nil || n.Kind != yaml.SequenceNode || !y.spend(len(n.Content)) {
		return nil
	}
	return n.Content
}

// scalarText is the text of a scalar, ok false for a missing, null or
// non-scalar node.
func scalarText(n *yaml.Node) (string, bool) {
	n = resolve(n)
	if n == nil || n.Kind != yaml.ScalarNode || isNull(n) {
		return "", false
	}
	return n.Value, true
}

// boolValue reads a boolean attribute (YAML 1.1 spellings such as "yes"
// included, see scalarOf); anything else is false.
func boolValue(n *yaml.Node) bool {
	if n = resolve(n); n == nil || n.Kind != yaml.ScalarNode {
		return false
	}
	_, v, _ := scalarOf(n)
	b, _ := v.(bool)
	return b
}

// yaml11Bools are the plain scalars PyYAML (and so Ansible) reads as
// booleans beyond YAML 1.2's true/false, which yaml.v3 reads as strings.
var yaml11Bools = map[string]bool{
	"yes": true, "Yes": true, "YES": true, "on": true, "On": true, "ON": true,
	"no": false, "No": false, "NO": false, "off": false, "Off": false, "OFF": false,
}

// scalarOf maps a scalar to the field type its YAML tag implies and its
// value, reading plain yes/no/on/off as booleans the way Ansible does. A null has no type and no value; ok is false for a tag with no
// field counterpart (a custom tag such as !vault, or !!binary).
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

// numberOf decodes an !!int or !!float scalar. An integer too large for
// int64 becomes a float64, which Check then refuses as a whole number.
func numberOf(n *yaml.Node, typ schema.Type) (schema.Type, any, bool) {
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

// defaultValue decodes a default: a scalar, or a list of scalars. ok is
// false for anything a field default cannot hold.
func (y *yamlFile) defaultValue(n *yaml.Node) (any, bool) {
	n = resolve(n)
	switch n.Kind {
	case yaml.ScalarNode:
		_, v, ok := scalarOf(n)
		return v, ok
	case yaml.SequenceNode:
		items := make([]any, 0, len(n.Content))
		for _, it := range y.items(n) {
			it = resolve(it)
			if it.Kind != yaml.ScalarNode {
				return nil, false
			}
			_, v, ok := scalarOf(it)
			if !ok {
				return nil, false
			}
			items = append(items, v)
		}
		return items, true
	}
	return nil, false
}
