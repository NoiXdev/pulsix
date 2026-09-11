// Package grouptree answers structural questions about an organization's
// inventory group tree: roots, children, ancestors, depth and subtree
// height. It is pure and shared by the inventory service (moves, depth
// limits) and the renderer. It never loops forever: a cycle or a dangling
// parent in its input (which the service's own checks prevent) is
// tolerated, not trusted.
package grouptree

import (
	"cmp"
	"slices"

	"github.com/google/uuid"
)

// Node is one group as the tree sees it.
type Node struct {
	ID       uuid.UUID
	ParentID *uuid.UUID
	Name     string
}

// Tree indexes nodes by id and by parent.
type Tree struct {
	nodes    map[uuid.UUID]Node
	children map[uuid.UUID][]uuid.UUID
	roots    []uuid.UUID
}

// New builds a tree. A node whose parent is not among nodes is a root.
func New(nodes []Node) Tree {
	t := Tree{nodes: make(map[uuid.UUID]Node, len(nodes)), children: map[uuid.UUID][]uuid.UUID{}}
	for _, n := range nodes {
		t.nodes[n.ID] = n
	}
	for _, n := range nodes {
		if n.ParentID != nil {
			if _, ok := t.nodes[*n.ParentID]; ok {
				t.children[*n.ParentID] = append(t.children[*n.ParentID], n.ID)
				continue
			}
		}
		t.roots = append(t.roots, n.ID)
	}
	byName := func(a, b uuid.UUID) int { return cmp.Compare(t.nodes[a].Name, t.nodes[b].Name) }
	slices.SortFunc(t.roots, byName)
	for _, c := range t.children {
		slices.SortFunc(c, byName)
	}
	return t
}

// Has reports whether id is in the tree.
func (t Tree) Has(id uuid.UUID) bool { _, ok := t.nodes[id]; return ok }

// Node returns the node with id.
func (t Tree) Node(id uuid.UUID) (Node, bool) { n, ok := t.nodes[id]; return n, ok }

// Roots returns the root ids ordered by name.
func (t Tree) Roots() []uuid.UUID { return slices.Clone(t.roots) }

// Children returns the direct children of id ordered by name.
func (t Tree) Children(id uuid.UUID) []uuid.UUID { return slices.Clone(t.children[id]) }

// Ancestors returns the ancestors of id, nearest first, without id itself.
func (t Tree) Ancestors(id uuid.UUID) []uuid.UUID {
	var out []uuid.UUID
	seen := map[uuid.UUID]bool{id: true}
	n, ok := t.nodes[id]
	for ok && n.ParentID != nil {
		p := *n.ParentID
		if seen[p] || !t.Has(p) {
			break
		}
		seen[p] = true
		out = append(out, p)
		n, ok = t.nodes[p]
	}
	return out
}

// Depth is the number of ancestors: 0 for a root.
func (t Tree) Depth(id uuid.UUID) int { return len(t.Ancestors(id)) }

// Descendants returns every node below id, depth first, children by name.
func (t Tree) Descendants(id uuid.UUID) []uuid.UUID {
	var out []uuid.UUID
	seen := map[uuid.UUID]bool{id: true}
	var walk func(uuid.UUID)
	walk = func(n uuid.UUID) {
		for _, c := range t.children[n] {
			if seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
			walk(c)
		}
	}
	walk(id)
	return out
}

// IsDescendant reports whether id lies strictly below of.
func (t Tree) IsDescendant(id, of uuid.UUID) bool {
	return slices.Contains(t.Ancestors(id), of)
}

// Height is the length of the longest path from id down to a leaf: 0 for a
// leaf.
func (t Tree) Height(id uuid.UUID) int {
	seen := map[uuid.UUID]bool{}
	var h func(uuid.UUID) int
	h = func(n uuid.UUID) int {
		if seen[n] {
			return -1
		}
		seen[n] = true
		best := 0
		for _, c := range t.children[n] {
			if d := h(c) + 1; d > best {
				best = d
			}
		}
		return best
	}
	return h(id)
}
