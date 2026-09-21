package jobs_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/grouptree"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/jobs"
)

type fakeGroups struct{ groups []inventory.GroupSummary }

func (f fakeGroups) ListGroups(context.Context, uuid.UUID) ([]inventory.GroupSummary, error) {
	return f.groups, nil
}

type fakeMembers struct{ members []inventory.Membership }

func (f fakeMembers) ListMemberships(context.Context, uuid.UUID) ([]inventory.Membership, error) {
	return f.members, nil
}

type fakeAllHosts struct{ hosts []hosts.Host }

func (f fakeAllHosts) ListAllHosts(context.Context, uuid.UUID) ([]hosts.Host, error) {
	return f.hosts, nil
}

func group(id uuid.UUID, parent *uuid.UUID) inventory.GroupSummary {
	return inventory.GroupSummary{GroupView: inventory.GroupView{Group: inventory.Group{
		ID: id, ParentID: parent, Name: "g-" + id.String()[:8],
	}}}
}

func TestScopeResolverGroupsSelectedHostsByZone(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	z1, z2 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	h := func(z uuid.UUID, managed bool) hosts.Host {
		return hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: z, Managed: managed}
	}
	h1, h2, h3, unmanaged := h(z1, true), h(z2, true), h(z1, true), h(z1, false)
	g := uuid.Must(uuid.NewV7())
	members := []inventory.Membership{
		{GroupID: g, HostID: h1.ID},
		{GroupID: g, HostID: unmanaged.ID},
	}
	allHosts := fakeAllHosts{hosts: []hosts.Host{h1, h2, h3, unmanaged}}
	res := jobs.NewScopeResolver(
		fakeGroups{groups: []inventory.GroupSummary{group(g, nil)}},
		fakeMembers{members: members},
		allHosts,
	)

	t.Run("whole org", func(t *testing.T) {
		targets, err := res.Resolve(ctx, org, render.Scope{}, "")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(targets) != 2 {
			t.Fatalf("targets = %+v, want one per zone", targets)
		}
		if targets[0].ZoneID.String() >= targets[1].ZoneID.String() {
			t.Fatalf("zones must be sorted: %+v", targets)
		}
		// z1 carries h1 and h3, z2 carries h2; the unmanaged host never
		// appears.
		counts := map[uuid.UUID]int{z1: 2, z2: 1}
		for _, tg := range targets {
			if len(tg.HostIDs) != counts[tg.ZoneID] {
				t.Fatalf("zone %v hosts = %v", tg.ZoneID, tg.HostIDs)
			}
		}
	})

	t.Run("group subtree", func(t *testing.T) {
		targets, err := res.Resolve(ctx, org, render.Scope{GroupID: &g}, "")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(targets) != 1 || targets[0].ZoneID != z1 || targets[0].HostIDs[0] != h1.ID {
			t.Fatalf("targets = %+v, want only h1 in z1", targets)
		}
	})

	t.Run("host intersection", func(t *testing.T) {
		targets, err := res.Resolve(ctx, org, render.Scope{GroupID: &g, HostIDs: []uuid.UUID{h1.ID, h2.ID}}, "")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(targets) != 1 || len(targets[0].HostIDs) != 1 || targets[0].HostIDs[0] != h1.ID {
			t.Fatalf("targets = %+v, want the intersection", targets)
		}
	})

	t.Run("unknown group is invisible", func(t *testing.T) {
		missing := uuid.Must(uuid.NewV7())
		if _, err := res.Resolve(ctx, org, render.Scope{GroupID: &missing}, ""); err != jobs.ErrNotFound {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("empty resolution", func(t *testing.T) {
		leaf := uuid.Must(uuid.NewV7())
		res2 := jobs.NewScopeResolver(
			fakeGroups{groups: []inventory.GroupSummary{group(leaf, nil)}},
			fakeMembers{},
			fakeAllHosts{},
		)
		if _, err := res2.Resolve(ctx, org, render.Scope{GroupID: &leaf}, ""); err != jobs.ErrEmptyScope {
			t.Fatalf("err = %v, want ErrEmptyScope", err)
		}
	})
}

// TestScopeResolverAppliesLimitPerZone pins the per-zone limit gate
// (backlog 2.45): the limit decides whether a zone gets a job at all, and
// never narrows a zone's host list — the surviving zone keeps its full set
// and the limit string still ships on the job payload for ansible to apply
// (defense in depth). A zone whose hosts all fail the match is dropped; a
// limit that matches nothing anywhere is ErrEmptyScope; an undecidable
// pattern keeps every zone (fail-open).
func TestScopeResolverAppliesLimitPerZone(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	z1, z2 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	h := func(z uuid.UUID, name string) hosts.Host {
		return hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: z, Name: name, Managed: true}
	}
	web1, web2, db1 := h(z1, "web1"), h(z1, "web2"), h(z2, "db1")
	res := jobs.NewScopeResolver(fakeGroups{}, fakeMembers{}, fakeAllHosts{hosts: []hosts.Host{web1, web2, db1}})

	t.Run("limit keeps the matching zone whole", func(t *testing.T) {
		targets, err := res.Resolve(ctx, org, render.Scope{}, "web*")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(targets) != 1 || targets[0].ZoneID != z1 {
			t.Fatalf("targets = %+v, want only z1", targets)
		}
		// The zone keeps BOTH of its hosts: the limit gates the zone's job,
		// it never narrows the host list.
		if len(targets[0].HostIDs) != 2 {
			t.Fatalf("z1 hosts = %v, want the zone's full set", targets[0].HostIDs)
		}
	})

	t.Run("zone with no matches gets no job", func(t *testing.T) {
		targets, err := res.Resolve(ctx, org, render.Scope{}, "db1")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(targets) != 1 || targets[0].ZoneID != z2 {
			t.Fatalf("targets = %+v, want only z2", targets)
		}
	})

	t.Run("all zones empty is ErrEmptyScope", func(t *testing.T) {
		if _, err := res.Resolve(ctx, org, render.Scope{}, "nomatch*"); err != jobs.ErrEmptyScope {
			t.Fatalf("err = %v, want ErrEmptyScope", err)
		}
	})

	t.Run("exclusion keeps the surviving zone", func(t *testing.T) {
		targets, err := res.Resolve(ctx, org, render.Scope{}, "web*:!web1")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(targets) != 1 || targets[0].ZoneID != z1 || len(targets[0].HostIDs) != 2 {
			t.Fatalf("targets = %+v, want z1 with its full host list", targets)
		}
	})

	t.Run("undecidable pattern keeps every zone", func(t *testing.T) {
		targets, err := res.Resolve(ctx, org, render.Scope{}, "~web.*")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(targets) != 2 {
			t.Fatalf("targets = %+v, want both zones", targets)
		}
	})
}

// TestGroupTreeDescendants pins the subtree walk the resolver relies on:
// a scope group selects its members and its descendants' members.
func TestGroupTreeDescendants(t *testing.T) {
	parent, child := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	tree := grouptree.New([]grouptree.Node{
		{ID: parent, Name: "parent"}, {ID: child, ParentID: &parent, Name: "child"},
	})
	if !tree.Has(child) || len(tree.Descendants(parent)) != 1 {
		t.Fatal("subtree walk broken")
	}
}
