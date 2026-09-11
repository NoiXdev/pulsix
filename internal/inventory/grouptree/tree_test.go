package grouptree_test

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/grouptree"
)

// build returns a tree and a name->id map for:
//
//	production
//	├── dbserver
//	└── webserver
//	    └── php
//	staging
func build() (grouptree.Tree, map[string]uuid.UUID) {
	ids := map[string]uuid.UUID{}
	for _, n := range []string{"production", "webserver", "php", "dbserver", "staging"} {
		ids[n] = uuid.Must(uuid.NewV7())
	}
	p := func(n string) *uuid.UUID { id := ids[n]; return &id }
	return grouptree.New([]grouptree.Node{
		{ID: ids["php"], ParentID: p("webserver"), Name: "php"},
		{ID: ids["webserver"], ParentID: p("production"), Name: "webserver"},
		{ID: ids["production"], Name: "production"},
		{ID: ids["dbserver"], ParentID: p("production"), Name: "dbserver"},
		{ID: ids["staging"], Name: "staging"},
	}), ids
}

func names(t grouptree.Tree, ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		n, _ := t.Node(id)
		out[i] = n.Name
	}
	return out
}

func TestTreeStructure(t *testing.T) {
	tr, ids := build()
	if got := names(tr, tr.Roots()); !slices.Equal(got, []string{"production", "staging"}) {
		t.Errorf("Roots = %v", got)
	}
	if got := names(tr, tr.Children(ids["production"])); !slices.Equal(got, []string{"dbserver", "webserver"}) {
		t.Errorf("Children = %v", got)
	}
	if got := names(tr, tr.Ancestors(ids["php"])); !slices.Equal(got, []string{"webserver", "production"}) {
		t.Errorf("Ancestors = %v", got)
	}
	if got := names(tr, tr.Descendants(ids["production"])); !slices.Equal(got, []string{"dbserver", "webserver", "php"}) {
		t.Errorf("Descendants = %v", got)
	}
	for name, depth := range map[string]int{"production": 0, "webserver": 1, "php": 2, "staging": 0} {
		if got := tr.Depth(ids[name]); got != depth {
			t.Errorf("Depth(%s) = %d, want %d", name, got, depth)
		}
	}
	for name, height := range map[string]int{"production": 2, "webserver": 1, "php": 0, "staging": 0} {
		if got := tr.Height(ids[name]); got != height {
			t.Errorf("Height(%s) = %d, want %d", name, got, height)
		}
	}
	if !tr.IsDescendant(ids["php"], ids["production"]) || tr.IsDescendant(ids["production"], ids["php"]) || tr.IsDescendant(ids["php"], ids["php"]) {
		t.Error("IsDescendant")
	}
	if tr.IsDescendant(ids["staging"], ids["production"]) {
		t.Error("siblings of different roots are not descendants")
	}
	if !tr.Has(ids["php"]) || tr.Has(uuid.Must(uuid.NewV7())) {
		t.Error("Has")
	}
	if _, ok := tr.Node(uuid.Must(uuid.NewV7())); ok {
		t.Error("Node of an unknown id")
	}
}

func TestTreeToleratesDanglingParentsAndCycles(t *testing.T) {
	a, b, orphan := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	missing := uuid.Must(uuid.NewV7())
	tr := grouptree.New([]grouptree.Node{
		{ID: a, ParentID: &b, Name: "a"},
		{ID: b, ParentID: &a, Name: "b"},
		{ID: orphan, ParentID: &missing, Name: "orphan"},
	})
	if got := names(tr, tr.Roots()); !slices.Equal(got, []string{"orphan"}) {
		t.Errorf("a node whose parent is missing is a root: %v", got)
	}
	if got := len(tr.Ancestors(a)); got != 1 {
		t.Errorf("Ancestors must stop at a cycle, got %d", got)
	}
	if tr.Depth(a) != 1 {
		t.Errorf("Depth in a cycle = %d", tr.Depth(a))
	}
	if tr.Height(a) != 1 {
		t.Errorf("Height in a cycle = %d", tr.Height(a))
	}
}
