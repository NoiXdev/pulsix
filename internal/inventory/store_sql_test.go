package inventory_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/sshconf"
)

func TestSQLStore(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := inventory.NewSQLStore(d)
	hostStore := hosts.NewSQLStore(d)
	sshStore := sshconf.NewSQLStore(d)

	var org, other uuid.UUID
	for i, slug := range []string{"inv-org", "inv-other"} {
		o, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			org = o.ID
		} else {
			other = o.ID
		}
	}
	zone := uuid.Must(uuid.NewV7())
	if err := hostStore.CreateZone(ctx, hosts.Zone{ID: zone, OrgID: org, Slug: "fra", Name: "fra"}); err != nil {
		t.Fatal(err)
	}
	newHost := func(name string) uuid.UUID {
		id := uuid.Must(uuid.NewV7())
		if err := hostStore.CreateHost(ctx, hosts.Host{ID: id, OrgID: org, ZoneID: zone, Name: name, Managed: true, Facts: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// Created out of name order on purpose: member lists must sort by host
	// name, not by insertion.
	webA, webB := newHost("web-b"), newHost("web-a")

	sc := schema.Schema{Fields: []schema.Field{{Name: "env", Type: schema.TypeString}}}
	prod, err := store.CreateGroup(ctx, inventory.Group{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "production", Schema: sc, Vars: map[string]any{"env": "prod"}})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if prod.Revision != 1 || prod.Vars["env"] != "prod" || len(prod.Schema.Fields) != 1 {
		t.Fatalf("created = %+v", prod)
	}
	if _, err := store.CreateGroup(ctx, inventory.Group{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "production", Schema: sc, Vars: map[string]any{}}); !errors.Is(err, inventory.ErrNameTaken) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := store.CreateGroup(ctx, inventory.Group{ID: uuid.Must(uuid.NewV7()), OrgID: other, Name: "x", ParentID: &prod.ID, Schema: sc, Vars: map[string]any{}}); !errors.Is(err, inventory.ErrInvalidInput) {
		t.Fatalf("parent in another org: %v", err)
	}
	web, err := store.CreateGroup(ctx, inventory.Group{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "webserver", ParentID: &prod.ID, Schema: sc, Vars: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}

	// Revision handling.
	prod.Vars = map[string]any{"env": "production"}
	updated, err := store.UpdateGroup(ctx, prod)
	if err != nil || updated.Revision != 2 || updated.Vars["env"] != "production" {
		t.Fatalf("UpdateGroup = %+v, %v", updated, err)
	}
	if _, err := store.UpdateGroup(ctx, prod); !errors.Is(err, inventory.ErrRevisionMismatch) {
		t.Fatalf("stale revision: %v", err)
	}
	ghost := prod
	ghost.ID = uuid.Must(uuid.NewV7())
	if _, err := store.UpdateGroup(ctx, ghost); !errors.Is(err, inventory.ErrNotFound) {
		t.Fatalf("unknown group: %v", err)
	}
	if got, err := store.GetGroup(ctx, other, prod.ID); !errors.Is(err, inventory.ErrNotFound) {
		t.Fatalf("other org Get = %+v, %v", got, err)
	}
	if list, err := store.ListGroups(ctx, org); err != nil || len(list) != 2 || list[0].Name != "production" {
		t.Fatalf("ListGroups = %v, %v", list, err)
	}

	// Memberships.
	m, err := store.PutMembership(ctx, inventory.Membership{GroupID: web.ID, HostID: webA, OrgID: org, Vars: map[string]any{"env": "a"}})
	if err != nil || m.Revision != 1 {
		t.Fatalf("PutMembership = %+v, %v", m, err)
	}
	m2, err := store.PutMembership(ctx, inventory.Membership{GroupID: web.ID, HostID: webA, OrgID: org, Vars: map[string]any{"env": "b"}})
	if err != nil || m2.Revision != 2 || m2.Vars["env"] != "b" {
		t.Fatalf("replace = %+v, %v", m2, err)
	}
	m2.Vars = map[string]any{"env": "c"}
	if m3, err := store.UpdateMembership(ctx, m2); err != nil || m3.Revision != 3 {
		t.Fatalf("UpdateMembership = %+v, %v", m3, err)
	}
	if _, err := store.UpdateMembership(ctx, m2); !errors.Is(err, inventory.ErrRevisionMismatch) {
		t.Fatalf("stale membership: %v", err)
	}
	missing := inventory.Membership{GroupID: prod.ID, HostID: webA, OrgID: org, Revision: 1}
	if _, err := store.UpdateMembership(ctx, missing); !errors.Is(err, inventory.ErrNotFound) {
		t.Fatalf("unknown membership: %v", err)
	}
	if _, err := store.PutMembership(ctx, inventory.Membership{GroupID: web.ID, HostID: webB, OrgID: org, Vars: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutMembership(ctx, inventory.Membership{GroupID: prod.ID, HostID: webA, OrgID: org, Vars: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	members, err := store.ListGroupMembers(ctx, org, web.ID)
	if err != nil || len(members) != 2 || members[0].HostName != "web-a" || members[1].HostName != "web-b" {
		t.Fatalf("ListGroupMembers = %+v, %v", members, err)
	}
	hostGroups, err := store.ListHostMemberships(ctx, org, webA)
	if err != nil || len(hostGroups) != 2 || hostGroups[0].GroupName != "production" {
		t.Fatalf("ListHostMemberships = %+v, %v", hostGroups, err)
	}
	counts, err := store.MemberCounts(ctx, org)
	if err != nil || counts[web.ID] != 2 || counts[prod.ID] != 1 {
		t.Fatalf("MemberCounts = %v, %v", counts, err)
	}
	if all, err := store.ListMemberships(ctx, org); err != nil || len(all) != 3 {
		t.Fatalf("ListMemberships = %v, %v", all, err)
	}
	if got, err := store.GetMembership(ctx, org, web.ID, webA); err != nil || got.Vars["env"] != "c" {
		t.Fatalf("GetMembership = %+v, %v", got, err)
	}

	// A group's jump host blocks deleting the jump host.
	b, err := sshStore.CreateBastion(ctx, sshconf.Bastion{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "bastion", Address: "b.example", Port: 22, Username: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := store.GetGroup(ctx, org, web.ID)
	cur.BastionID = &b.ID
	if _, err := store.UpdateGroup(ctx, cur); err != nil {
		t.Fatal(err)
	}
	if err := sshStore.DeleteBastion(ctx, org, b.ID); !errors.Is(err, sshconf.ErrInUse) {
		t.Fatalf("delete referenced bastion: %v", err)
	}

	// Deletes.
	if err := store.DeleteGroup(ctx, org, prod.ID); !errors.Is(err, inventory.ErrHasChildren) {
		t.Fatalf("delete parent: %v", err)
	}
	if err := hostStore.DeleteHost(ctx, org, webB); err != nil {
		t.Fatal(err)
	}
	if members, _ := store.ListGroupMembers(ctx, org, web.ID); len(members) != 1 {
		t.Fatalf("deleting a host must remove its memberships: %v", members)
	}
	if err := store.DeleteMembership(ctx, org, prod.ID, webA); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteMembership(ctx, org, prod.ID, webA); !errors.Is(err, inventory.ErrNotFound) {
		t.Fatalf("second DeleteMembership: %v", err)
	}
	if err := store.DeleteGroup(ctx, org, web.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ := store.ListMemberships(ctx, org); len(all) != 0 {
		t.Fatalf("deleting a group must remove its memberships: %v", all)
	}
	if err := store.DeleteGroup(ctx, org, web.ID); !errors.Is(err, inventory.ErrNotFound) {
		t.Fatalf("second DeleteGroup: %v", err)
	}
}
