package playbooks

import (
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Node and tag helpers of the outline walk.

func resolveAlias(n *yaml.Node) *yaml.Node {
	for i := 0; n.Kind == yaml.AliasNode && n.Alias != nil && i < 8; i++ {
		n = n.Alias
	}
	return n
}

func lookup(n *yaml.Node, key string) (*yaml.Node, bool) {
	if n.Kind != yaml.MappingNode {
		return nil, false
	}
	v, ok := mappingValue(n, key)
	if !ok {
		return nil, false
	}
	return resolveAlias(v), true
}

func lookupOrNil(n *yaml.Node, key string) *yaml.Node {
	v, _ := lookup(n, key)
	return v
}

func scalarText(n *yaml.Node) string {
	if n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		return ""
	}
	return n.Value
}

// hostsText reads hosts as written: a string, or a list joined by commas.
func hostsText(n *yaml.Node) string {
	if n.Kind != yaml.SequenceNode {
		return scalarText(n)
	}
	var parts []string
	size := 0
	for _, c := range n.Content {
		if s := scalarText(resolveAlias(c)); s != "" {
			parts = append(parts, s)
			if size += len(s) + 1; size > maxOutlineText {
				break // the caller clips the result anyway
			}
		}
	}
	return strings.Join(parts, ",")
}

func isTrue(n *yaml.Node) bool {
	if n.Kind != yaml.ScalarNode || len(n.Value) > 8 {
		return false
	}
	switch strings.ToLower(n.Value) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

// isFalse reports key set to a false value (YAML bool or the yes/no style
// strings Ansible's own boolean parsing accepts).
func isFalse(n *yaml.Node, key string) bool {
	v, ok := lookup(n, key)
	if !ok || v.Kind != yaml.ScalarNode || len(v.Value) > 8 {
		return false
	}
	switch strings.ToLower(v.Value) {
	case "false", "no", "off", "0":
		return true
	}
	return false
}

// tagsOf reads a tags key: a comma-separated string or a list of strings.
// Only the first maxOutlineTagsPerList list items (or comma-separated parts)
// are read; the rest is ignored and the outline marked truncated, so the
// cost never depends on the size of the written list.
func (w *outlineWalker) tagsOf(n *yaml.Node) []string {
	v, ok := lookup(n, "tags")
	if !ok {
		return nil
	}
	var raw []string
	switch v.Kind {
	case yaml.ScalarNode:
		raw = strings.SplitN(scalarText(v), ",", maxOutlineTagsPerList+1)
	case yaml.SequenceNode:
		items := v.Content
		if len(items) > maxOutlineTagsPerList {
			items, w.out.Truncated = items[:maxOutlineTagsPerList], true
		}
		for _, c := range items {
			raw = append(raw, strings.SplitN(scalarText(resolveAlias(c)), ",", maxOutlineTagsPerList+1)...)
			if len(raw) > maxOutlineTagsPerList {
				break
			}
		}
	}
	out := make([]string, 0, min(len(raw), maxOutlineTagsPerList))
	for _, t := range raw {
		if len(t) > maxOutlineText { // checked before trimming: never trim a huge part
			w.out.Truncated = true
			continue
		}
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		if len(out) == maxOutlineTagsPerList {
			w.out.Truncated = true
			break
		}
		out = append(out, t)
	}
	return out
}

// effectiveTags is the sorted unique union of inherited and the node's own
// tags, cut to maxOutlineTagsPerList (outline truncated when it cuts).
func (w *outlineWalker) effectiveTags(inherited []string, n *yaml.Node) []string {
	tags := sortedUnique(inherited, w.tagsOf(n))
	if len(tags) > maxOutlineTagsPerList {
		tags, w.out.Truncated = tags[:maxOutlineTagsPerList], true
	}
	return tags
}

// noteTags adds tags to the outline's top-level union, which holds at most
// maxOutlineTags (outline truncated when it would grow beyond).
func (w *outlineWalker) noteTags(tags []string) {
	for _, t := range tags {
		if w.tagSet[t] {
			continue
		}
		if len(w.tagSet) >= maxOutlineTags {
			w.out.Truncated = true
			return
		}
		w.tagSet[t] = true
	}
}

// sortedUnique merges tag lists into a new sorted, duplicate-free, non-nil
// slice.
func sortedUnique(lists ...[]string) []string {
	set := map[string]bool{}
	for _, l := range lists {
		for _, t := range l {
			set[t] = true
		}
	}
	return sortedKeys(set)
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// clip cuts s to maxOutlineText bytes on a rune boundary, ending in an
// ellipsis, and marks the outline truncated when it cuts.
func (w *outlineWalker) clip(s string) string {
	const ellipsis = "…"
	if len(s) <= maxOutlineText {
		return s
	}
	w.out.Truncated = true
	cut := maxOutlineText - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}
