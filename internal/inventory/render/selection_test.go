package render_test

import (
	"errors"
	"maps"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// markedFixture is fixture() with every group var and every membership var
// replaced by one marker value unique to its row, under a field name unique
// to its group, so no merge hides a membership's value: a row's vars reach
// the document exactly when its marker does.
func markedFixture() (render.Input, map[string]uuid.UUID, map[string]render.MembershipKey) {
	in := fixture()
	groupMarks := map[string]uuid.UUID{}
	memberMarks := map[string]render.MembershipKey{}
	names := map[uuid.UUID]string{}
	for i := range in.Groups {
		g := &in.Groups[i]
		names[g.ID] = g.Name
		g.Schema = schema.Schema{Fields: []schema.Field{
			{Name: "mark", Type: schema.TypeString},
			{Name: "mark_" + g.Name, Type: schema.TypeString, Scope: schema.ScopeHost},
		}}
		mark := "group:" + g.Name
		g.Vars = map[string]any{"mark": mark}
		groupMarks[mark] = g.ID
	}
	for i := range in.Memberships {
		m := &in.Memberships[i]
		mark := "member:" + names[m.GroupID] + ":" + m.HostID.String()
		m.Vars = map[string]any{"mark_" + names[m.GroupID]: mark}
		memberMarks[mark] = render.MembershipKey{GroupID: m.GroupID, HostID: m.HostID}
	}
	return in, groupMarks, memberMarks
}

func stringsIn(v any, into map[string]bool) {
	switch t := v.(type) {
	case string:
		into[t] = true
	case map[string]any:
		for _, e := range t {
			stringsIn(e, into)
		}
	case []any:
		for _, e := range t {
			stringsIn(e, into)
		}
	}
}

// TestCarriesNamesExactlyTheRowsTheDocumentCarries pins Carries to
// Render: for every shape of scope, the groups and memberships it names
// are exactly those whose vars the rendered document holds.
func TestCarriesNamesExactlyTheRowsTheDocumentCarries(t *testing.T) {
	scopes := map[string]render.Scope{
		"whole org":                 {},
		"root group with children":  {GroupID: ptr(gProduction)},
		"child group":               {GroupID: ptr(gWebserver)},
		"group with unmanaged host": {GroupID: ptr(gDbserver)},
		"host in several groups":    {HostIDs: []uuid.UUID{hDb02}},
		"hosts across groups":       {HostIDs: []uuid.UUID{hWeb01, hWeb03}},
		"group and hosts":           {GroupID: ptr(gEdge), HostIDs: []uuid.UUID{hDb02, hWeb01}},
		"ungrouped host":            {HostIDs: []uuid.UUID{hLonely}},
		"unmanaged host":            {HostIDs: []uuid.UUID{hSwitch}},
	}
	for name, scope := range scopes {
		t.Run(name, func(t *testing.T) {
			in, groupMarks, memberMarks := markedFixture()
			in.Scope = scope
			inv, err := render.Render(in)
			if err != nil {
				t.Fatal(err)
			}
			got, err := render.Carries(in)
			if err != nil {
				t.Fatal(err)
			}
			present := map[string]bool{}
			stringsIn(inv.Doc, present)
			wantGroups, wantMembers := map[uuid.UUID]bool{}, map[render.MembershipKey]bool{}
			for mark, id := range groupMarks {
				if present[mark] {
					wantGroups[id] = true
				}
			}
			for mark, k := range memberMarks {
				if present[mark] {
					wantMembers[k] = true
				}
			}
			if !maps.Equal(got.Groups, wantGroups) {
				t.Errorf("groups = %v, want %v", got.Groups, wantGroups)
			}
			if !maps.Equal(got.Memberships, wantMembers) {
				t.Errorf("memberships = %v, want %v", got.Memberships, wantMembers)
			}
		})
	}
}

func TestCarriesFailsAsRenderDoes(t *testing.T) {
	in := fixture()
	in.Scope = render.Scope{GroupID: ptr(id(99))}
	if _, err := render.Carries(in); !errors.Is(err, render.ErrUnknownGroup) {
		t.Fatalf("unknown scope group: %v", err)
	}
	in = fixture()
	in.Groups[0].ParentID = ptr(gWebserver) // production <-> webserver
	if _, err := render.Carries(in); !errors.Is(err, render.ErrUnrenderable) {
		t.Fatalf("group tree with a cycle: %v", err)
	}
}
