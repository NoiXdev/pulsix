package varsource

import (
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"gopkg.in/yaml.v3"
)

// maxMergeSources bounds how many merged mappings (YAML "<<" keys, nested
// ones included) one mapping may pull in, so a file of anchors merging each
// other twice per level cannot make the walk exponential.
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

// pairs returns the entries of a mapping in order, nil for anything else.
// Merge keys ("<<: *anchor" or "<<: [*a, *b]") are expanded in place; a key
// the mapping sets itself wins over a merged one, an earlier merge over a
// later one.
func pairs(n *yaml.Node) []pair {
	budget := maxMergeSources
	return mergedPairs(n, &budget)
}

func mergedPairs(n *yaml.Node, budget *int) []pair {
	n = resolve(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	own := make(map[string]bool, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		if !isMergeKey(n.Content[i]) {
			own[n.Content[i].Value] = true
		}
	}
	out := make([]pair, 0, len(n.Content)/2)
	merged := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if !isMergeKey(k) {
			out = append(out, pair{k, v})
			continue
		}
		for _, src := range mergeSources(v) {
			if *budget <= 0 {
				break
			}
			*budget--
			for _, p := range mergedPairs(src, budget) {
				if own[p.key.Value] || merged[p.key.Value] {
					continue
				}
				merged[p.key.Value] = true
				out = append(out, p)
			}
		}
	}
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

// lookup returns the value under key in a mapping (aliases resolved), nil
// when n is no mapping or lacks the key.
func lookup(n *yaml.Node, key string) *yaml.Node {
	for _, p := range pairs(n) {
		if p.key.Value == key {
			return resolve(p.value)
		}
	}
	return nil
}

// attrs indexes a mapping's entries by key, aliases resolved; a repeated
// key keeps its last value, as Ansible's loader does.
func attrs(n *yaml.Node) map[string]*yaml.Node {
	ps := pairs(n)
	out := make(map[string]*yaml.Node, len(ps))
	for _, p := range ps {
		out[p.key.Value] = resolve(p.value)
	}
	return out
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

// boolValue reads a YAML boolean (YAML 1.1 spellings such as "yes"
// included); anything else is false.
func boolValue(n *yaml.Node) bool {
	var b bool
	if n = resolve(n); n == nil || n.Kind != yaml.ScalarNode || n.Decode(&b) != nil {
		return false
	}
	return b
}

// scalarOf maps a scalar to the field type its YAML tag implies and its
// value. A null has no type and no value; ok is false for a tag with no
// field counterpart (a custom tag such as !vault, or !!binary).
func scalarOf(n *yaml.Node) (schema.Type, any, bool) {
	switch n.ShortTag() {
	case "!!null":
		return "", nil, true
	case "!!str", "!!timestamp", "!unsafe":
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
func defaultValue(n *yaml.Node) (any, bool) {
	n = resolve(n)
	switch n.Kind {
	case yaml.ScalarNode:
		_, v, ok := scalarOf(n)
		return v, ok
	case yaml.SequenceNode:
		items := make([]any, 0, len(n.Content))
		for _, it := range n.Content {
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
