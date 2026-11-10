package varsource

import (
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
	file      string
	entries   map[*yaml.Node][]pair
	byKey     map[*yaml.Node]map[string]*yaml.Node
	left      int // steps: mapping entries and sequence items visited
	bytesLeft int // bytes of scalars read (scalars.go)
	over      bool
	notes     []Note // up to maxNotesPerFile (note)
	dropped   int    // notes past the cap

	// Set while a role's spec file is mapped entry point by entry point
	// (roleSpec.extract): a mapping is read once, but every entry point that
	// reads it raises its duplicate_key notes, so an entry point nobody uses
	// cannot swallow them. dups holds a mapping's notes, seen the mappings the
	// running capture has read.
	trackDups bool
	dups      map[*yaml.Node][]Note       // a mapping's own duplicate_key notes
	merged    map[*yaml.Node][]*yaml.Node // the mappings a mapping merges in
	seen      map[*yaml.Node]bool
	refs      []dupRef // the notes the running capture re-raised
}

// dupRef is a mapping's duplicate_key notes a capture re-raised, at the
// place among the capture's own notes where they were: it points at the
// notes kept once, it does not copy them.
type dupRef struct {
	at    int
	notes []Note
}

func newYAMLFile(file string) *yamlFile {
	return &yamlFile{
		file:      file,
		entries:   map[*yaml.Node][]pair{},
		byKey:     map[*yaml.Node]map[string]*yaml.Node{},
		left:      maxVisits,
		bytesLeft: maxReadBytes,
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
	return warning(codeFileTooComplex, y.file, 0, "reading this file took more than %d steps, %d bytes or %d options (anchors, aliases and merge keys expanded); nothing read from it", maxVisits, maxReadBytes, maxSpecFields), true
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
		y.raiseDups(n)
		return ps
	}
	y.entries[n] = nil // an anchor merged into itself reads as empty
	y.markSeen(n)
	if !y.spend(len(n.Content) / 2) {
		return nil
	}
	keys := make([]*yaml.Node, len(n.Content)/2)
	size := 0
	for i := range keys {
		keys[i] = keyNode(n.Content[2*i])
		size += len(keys[i].Value)
	}
	if !y.spendBytes(size) {
		return nil
	}
	last := make(map[string]int, len(keys)) // key → index of its last entry
	for i, k := range keys {
		if isMergeKey(k) {
			continue
		}
		if j, dup := last[k.Value]; dup {
			y.noteDuplicate(n, warning(codeDuplicateKey, y.file, k.Line,
				"%q is set twice in one mapping (lines %d and %d); the later one counts", short(k.Value), keys[j].Line, k.Line))
		}
		last[k.Value] = i
	}
	out := make([]pair, 0, len(last))
	placed := make(map[string]bool, len(last))
	sources := 0
	for i, k := range keys {
		if !isMergeKey(k) {
			if !placed[k.Value] {
				placed[k.Value] = true
				j := last[k.Value]
				out = append(out, pair{keys[j], n.Content[2*j+1]})
			}
			continue
		}
		for _, src := range mergeSources(n.Content[2*i+1]) {
			if sources == maxMergeSources {
				break
			}
			sources++
			if y.trackDups {
				y.merged[n] = append(y.merged[n], resolve(src))
			}
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

// keyNode is a mapping key as read: an alias key reads as the scalar its
// anchor names, at the alias's own line.
func keyNode(k *yaml.Node) *yaml.Node {
	if k.Kind != yaml.AliasNode {
		return k
	}
	r := resolve(k)
	if r == nil || r.Kind != yaml.ScalarNode {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Line: k.Line, Column: k.Column}
	}
	c := *r
	c.Line, c.Column, c.Anchor = k.Line, k.Column, ""
	return &c
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
		y.raiseDups(n)
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

// trackDuplicates starts keeping the duplicate_key notes of each mapping.
func (y *yamlFile) trackDuplicates() {
	y.trackDups, y.dups, y.merged = true, map[*yaml.Node][]Note{}, map[*yaml.Node][]*yaml.Node{}
}

// noteDuplicate raises a duplicate_key note of the mapping n, and keeps it
// for the entry points that read n later when dups are tracked.
func (y *yamlFile) noteDuplicate(n *yaml.Node, note Note) {
	if y.trackDups {
		y.dups[n] = append(y.dups[n], note)
	}
	y.note(note)
}

// markSeen records that the running capture has read the mapping n.
func (y *yamlFile) markSeen(n *yaml.Node) {
	if !y.trackDups {
		return
	}
	if y.seen == nil {
		y.seen = map[*yaml.Node]bool{}
	}
	y.seen[n] = true
}

// raiseDups re-raises the duplicate_key notes of the mapping n and of the
// mappings it merges in, once per capture, as references to the notes kept
// (see dupRef). Each is charged to the file, so entry points sharing a
// mapping with many duplicate keys cannot multiply them for free.
func (y *yamlFile) raiseDups(n *yaml.Node) {
	if !y.trackDups || n == nil || y.seen[n] {
		return
	}
	y.markSeen(n)
	own, merged := y.dups[n], y.merged[n]
	if len(own)+len(merged) == 0 || !y.spend(len(own)+len(merged)) {
		return
	}
	if len(own) > 0 {
		y.refs = append(y.refs, dupRef{at: len(y.notes), notes: own})
	}
	for _, m := range merged {
		y.raiseDups(m)
	}
}

// release drops what the file's readers cached about its parsed nodes, so
// the tree can be collected once its owner lets go of it. What stays is the
// budget and the notes: the file is still accounted, and its notes taken at
// the end of the walk. A released file must not be read again (backlog 7.113).
func (y *yamlFile) release() {
	y.entries = map[*yaml.Node][]pair{}
	y.byKey = map[*yaml.Node]map[string]*yaml.Node{}
	y.trackDups, y.dups, y.merged, y.seen, y.refs = false, nil, nil, nil, nil
}
