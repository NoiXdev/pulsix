package jobs_test

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/jobs"
)

// TestScopeReachOverlap pins which hosts an inventory change reaches in
// each template's run (decision D6): the managed hosts the changed scope
// selects (a group: its members and its descendant groups' members; a
// host) that the template's scope selects too, by name. A scope naming a
// group that does not exist counts as selecting every changed host (fail
// closed).
func TestScopeReachOverlap(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	zone := uuid.Must(uuid.NewV7())
	h := func(name string, managed bool) hosts.Host {
		return hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Name: name, Managed: managed}
	}
	web1, web2, db1, off := h("web1", true), h("web2", true), h("db1", true), h("off", false)
	parent, child, other, unknown := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	reach := jobs.NewScopeReach(
		fakeGroups{groups: []inventory.GroupSummary{group(parent, nil), group(child, &parent), group(other, nil)}},
		fakeMembers{members: []inventory.Membership{
			{GroupID: parent, HostID: web1.ID}, {GroupID: parent, HostID: off.ID},
			{GroupID: child, HostID: web2.ID}, {GroupID: other, HostID: db1.ID},
		}},
		fakeAllHosts{hosts: []hosts.Host{web1, web2, db1, off}},
	)
	orgWide, ofOther, ofChild, onlyDB, webAndDB, gone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()),
		uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	scopes := map[uuid.UUID]render.Scope{
		orgWide:  {},
		ofOther:  {GroupID: &other},
		ofChild:  {GroupID: &child},
		onlyDB:   {HostIDs: []uuid.UUID{db1.ID}},
		webAndDB: {HostIDs: []uuid.UUID{web2.ID, db1.ID}},
		gone:     {GroupID: &unknown},
	}

	t.Run("a group reaches its members and its descendants' members", func(t *testing.T) {
		got, err := reach.Overlap(ctx, org, render.Scope{GroupID: &parent}, scopes)
		if err != nil {
			t.Fatal(err)
		}
		want := map[uuid.UUID][]string{
			orgWide:  {"web1", "web2"},
			ofChild:  {"web2"},
			webAndDB: {"web2"},
			gone:     {"web1", "web2"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("overlap = %v, want %v", got, want)
		}
	})

	t.Run("a host reaches the scopes selecting it", func(t *testing.T) {
		got, err := reach.Overlap(ctx, org, render.Scope{HostIDs: []uuid.UUID{db1.ID}}, scopes)
		if err != nil {
			t.Fatal(err)
		}
		want := map[uuid.UUID][]string{
			orgWide:  {"db1"},
			ofOther:  {"db1"},
			onlyDB:   {"db1"},
			webAndDB: {"db1"},
			gone:     {"db1"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("overlap = %v, want %v", got, want)
		}
	})

	t.Run("an unmanaged host reaches nothing", func(t *testing.T) {
		got, err := reach.Overlap(ctx, org, render.Scope{HostIDs: []uuid.UUID{off.ID}}, scopes)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("overlap = %v, want none", got)
		}
	})
}
