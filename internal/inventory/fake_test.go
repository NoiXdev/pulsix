package inventory_test

import (
	"context"
	"sort"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/sshconf"
)

var ctx = context.Background()

type memberKey struct{ group, host uuid.UUID }

// fakeStore mirrors the SQL store's contract: revision checks, name
// uniqueness, the children guard on delete, and cascading memberships.
type fakeStore struct {
	groups  map[uuid.UUID]inventory.Group
	members map[memberKey]inventory.Membership
	hosts   fakeHosts
}

func newFakeStore(h fakeHosts) *fakeStore {
	return &fakeStore{groups: map[uuid.UUID]inventory.Group{}, members: map[memberKey]inventory.Membership{}, hosts: h}
}

func (f *fakeStore) CreateGroup(_ context.Context, g inventory.Group) (inventory.Group, error) {
	for _, x := range f.groups {
		if x.OrgID == g.OrgID && x.Name == g.Name {
			return inventory.Group{}, inventory.ErrNameTaken
		}
	}
	g.Revision = 1
	f.groups[g.ID] = g
	return g, nil
}

func (f *fakeStore) GetGroup(_ context.Context, orgID, id uuid.UUID) (inventory.Group, error) {
	g, ok := f.groups[id]
	if !ok || g.OrgID != orgID {
		return inventory.Group{}, inventory.ErrNotFound
	}
	return g, nil
}

func (f *fakeStore) ListGroups(_ context.Context, orgID uuid.UUID) ([]inventory.Group, error) {
	var out []inventory.Group
	for _, g := range f.groups {
		if g.OrgID == orgID {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeStore) UpdateGroup(_ context.Context, g inventory.Group) (inventory.Group, error) {
	cur, ok := f.groups[g.ID]
	if !ok || cur.OrgID != g.OrgID {
		return inventory.Group{}, inventory.ErrNotFound
	}
	if cur.Revision != g.Revision {
		return inventory.Group{}, inventory.ErrRevisionMismatch
	}
	for _, x := range f.groups {
		if x.ID != g.ID && x.OrgID == g.OrgID && x.Name == g.Name {
			return inventory.Group{}, inventory.ErrNameTaken
		}
	}
	g.Revision++
	f.groups[g.ID] = g
	return g, nil
}

func (f *fakeStore) DeleteGroup(_ context.Context, orgID, id uuid.UUID) error {
	g, ok := f.groups[id]
	if !ok || g.OrgID != orgID {
		return inventory.ErrNotFound
	}
	for _, x := range f.groups {
		if x.ParentID != nil && *x.ParentID == id {
			return inventory.ErrHasChildren
		}
	}
	delete(f.groups, id)
	for k := range f.members {
		if k.group == id {
			delete(f.members, k)
		}
	}
	return nil
}

func (f *fakeStore) MemberCounts(_ context.Context, orgID uuid.UUID) (map[uuid.UUID]int, error) {
	out := map[uuid.UUID]int{}
	for _, m := range f.members {
		if m.OrgID == orgID {
			out[m.GroupID]++
		}
	}
	return out, nil
}

func (f *fakeStore) PutMembership(_ context.Context, m inventory.Membership) (inventory.Membership, error) {
	k := memberKey{m.GroupID, m.HostID}
	m.Revision = f.members[k].Revision + 1
	f.members[k] = m
	return m, nil
}

func (f *fakeStore) UpdateMembership(_ context.Context, m inventory.Membership) (inventory.Membership, error) {
	k := memberKey{m.GroupID, m.HostID}
	cur, ok := f.members[k]
	if !ok || cur.OrgID != m.OrgID {
		return inventory.Membership{}, inventory.ErrNotFound
	}
	if cur.Revision != m.Revision {
		return inventory.Membership{}, inventory.ErrRevisionMismatch
	}
	m.Revision++
	f.members[k] = m
	return m, nil
}

func (f *fakeStore) GetMembership(_ context.Context, orgID, groupID, hostID uuid.UUID) (inventory.Membership, error) {
	m, ok := f.members[memberKey{groupID, hostID}]
	if !ok || m.OrgID != orgID {
		return inventory.Membership{}, inventory.ErrNotFound
	}
	return m, nil
}

func (f *fakeStore) DeleteMembership(_ context.Context, orgID, groupID, hostID uuid.UUID) error {
	k := memberKey{groupID, hostID}
	if m, ok := f.members[k]; !ok || m.OrgID != orgID {
		return inventory.ErrNotFound
	}
	delete(f.members, k)
	return nil
}

func (f *fakeStore) ListMemberships(_ context.Context, orgID uuid.UUID) ([]inventory.Membership, error) {
	var out []inventory.Membership
	for _, m := range f.members {
		if m.OrgID == orgID {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeStore) ListGroupMembers(_ context.Context, orgID, groupID uuid.UUID) ([]inventory.Membership, error) {
	var out []inventory.Membership
	for _, m := range f.members {
		if m.OrgID == orgID && m.GroupID == groupID {
			m.HostName = f.hosts[m.HostID].Name
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HostName < out[j].HostName })
	return out, nil
}

func (f *fakeStore) ListHostMemberships(_ context.Context, orgID, hostID uuid.UUID) ([]inventory.Membership, error) {
	var out []inventory.Membership
	for _, m := range f.members {
		if m.OrgID == orgID && m.HostID == hostID {
			m.GroupName = f.groups[m.GroupID].Name
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GroupName < out[j].GroupName })
	return out, nil
}

type fakeHosts map[uuid.UUID]hosts.Host

func (f fakeHosts) GetHost(_ context.Context, orgID, id uuid.UUID) (hosts.Host, error) {
	h, ok := f[id]
	if !ok || h.OrgID != orgID {
		return hosts.Host{}, hosts.ErrNotFound
	}
	return h, nil
}

type fakeBastions map[uuid.UUID]sshconf.Bastion

func (f fakeBastions) GetBastion(_ context.Context, orgID, id uuid.UUID) (sshconf.Bastion, error) {
	b, ok := f[id]
	if !ok || b.OrgID != orgID {
		return sshconf.Bastion{}, sshconf.ErrNotFound
	}
	return b, nil
}

func (f fakeHosts) ListAllHosts(_ context.Context, orgID uuid.UUID) ([]hosts.Host, error) {
	var out []hosts.Host
	for _, h := range f {
		if h.OrgID == orgID {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

type fakeSSH struct {
	hostSSH  []sshconf.HostSSH
	bastions []sshconf.Bastion
	zones    map[uuid.UUID]uuid.UUID
}

func (f fakeSSH) ListHostSSH(context.Context, uuid.UUID) ([]sshconf.HostSSH, error) { return f.hostSSH, nil }
func (f fakeSSH) ListBastions(context.Context, uuid.UUID) ([]sshconf.Bastion, error) { return f.bastions, nil }
func (f fakeSSH) ZoneBastions(context.Context, uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
	return f.zones, nil
}

type fakeCreds []credentials.Credential

func (f fakeCreds) List(context.Context, uuid.UUID) ([]credentials.Credential, error) { return f, nil }
