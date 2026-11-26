package inventory_test

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// TestSQLStoreKeepsAssistantMarks: the marks of decision D6 round-trip
// through every write and read of the SQL store, and a row written without
// any (a group created, a membership put by a person) reads back empty,
// never nil-violating the NOT NULL columns.
func TestSQLStoreKeepsAssistantMarks(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := inventory.NewSQLStore(d)
	hostStore := hosts.NewSQLStore(d)
	o, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: "inv-marks", Name: "inv-marks"})
	if err != nil {
		t.Fatal(err)
	}
	org, zone, host := o.ID, uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if err := hostStore.CreateZone(ctx, hosts.Zone{ID: zone, OrgID: org, Slug: "fra", Name: "fra"}); err != nil {
		t.Fatal(err)
	}
	if err := hostStore.CreateHost(ctx, hosts.Host{ID: host, OrgID: org, ZoneID: zone, Name: "web-01", Managed: true, Facts: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	sc := schema.Schema{Fields: []schema.Field{{Name: "a", Type: schema.TypeString, Default: "x"}, {Name: "b", Type: schema.TypeString}}}
	g, err := store.CreateGroup(ctx, inventory.Group{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "web", Schema: sc, Vars: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if g.AssistantKeys == nil || len(g.AssistantKeys) != 0 || g.AssistantDefaultKeys == nil || len(g.AssistantDefaultKeys) != 0 {
		t.Fatalf("new group marks = %#v / %#v", g.AssistantKeys, g.AssistantDefaultKeys)
	}
	g.Vars, g.AssistantKeys, g.AssistantDefaultKeys = map[string]any{"b": "v"}, []string{"b"}, []string{"a"}
	if g, err = store.UpdateGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetGroup(ctx, org, g.ID)
	if err != nil || !slices.Equal(got.AssistantKeys, []string{"b"}) || !slices.Equal(got.AssistantDefaultKeys, []string{"a"}) {
		t.Fatalf("GetGroup marks = %v / %v, %v", got.AssistantKeys, got.AssistantDefaultKeys, err)
	}
	// A write that carries no marks (nil) stores none.
	got.AssistantKeys, got.AssistantDefaultKeys = nil, nil
	if got, err = store.UpdateGroup(ctx, got); err != nil || len(got.AssistantKeys) != 0 || len(got.AssistantDefaultKeys) != 0 {
		t.Fatalf("UpdateGroup without marks = %+v, %v", got, err)
	}

	m, err := store.PutMembership(ctx, inventory.Membership{GroupID: g.ID, HostID: host, OrgID: org, Vars: map[string]any{"b": "v"}, AssistantKeys: []string{"b"}})
	if err != nil || !slices.Equal(m.AssistantKeys, []string{"b"}) {
		t.Fatalf("PutMembership = %+v, %v", m, err)
	}
	m.AssistantKeys = nil
	if m, err = store.UpdateMembership(ctx, m); err != nil || m.AssistantKeys == nil || len(m.AssistantKeys) != 0 {
		t.Fatalf("UpdateMembership without marks = %+v, %v", m, err)
	}
	m.AssistantKeys = []string{"b"}
	if m, err = store.UpdateMembership(ctx, m); err != nil {
		t.Fatal(err)
	}
	for name, list := range map[string]func() ([]inventory.Membership, error){
		"ListMemberships":     func() ([]inventory.Membership, error) { return store.ListMemberships(ctx, org) },
		"ListGroupMembers":    func() ([]inventory.Membership, error) { return store.ListGroupMembers(ctx, org, g.ID) },
		"ListHostMemberships": func() ([]inventory.Membership, error) { return store.ListHostMemberships(ctx, org, host) },
	} {
		ms, err := list()
		if err != nil || len(ms) != 1 || !slices.Equal(ms[0].AssistantKeys, []string{"b"}) {
			t.Fatalf("%s = %+v, %v", name, ms, err)
		}
	}
	if one, err := store.GetMembership(ctx, org, g.ID, host); err != nil || !slices.Equal(one.AssistantKeys, []string{"b"}) {
		t.Fatalf("GetMembership = %+v, %v", one, err)
	}
}
