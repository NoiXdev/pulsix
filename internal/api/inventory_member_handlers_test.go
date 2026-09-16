package api_test

import (
	"net/http"
	"testing"
)

// TestInventoryMemberWriteRoutesRejectBadIDs is backlog 2.19: assign and
// unassign each take two ids (the group and the host); a foreign or
// malformed value in either slot must be rejected the same way a
// single-id route is, with the other slot held at a real id of the
// caller's own organization so only the slot under test is exercised.
func TestInventoryMemberWriteRoutesRejectBadIDs(t *testing.T) {
	f := newInvFixture(t, "inv-member-badid")
	own := f.group(t, "webserver", nil)
	ownHost := f.host(t, "web-01", true)

	other := f.s.SeedUser("inv-member-badid-other@example.test", "correct horse battery")
	f.s.SeedOrg("inv-member-badid-other", other)
	otherClient := f.s.LoginAs(other)
	otherBase := f.s.HTTP.URL + "/api/v1/orgs/inv-member-badid-other"
	var foreignGroup groupBody
	decodeInto(t, postJSON(t, otherClient, otherBase+"/inventory/groups", map[string]any{"name": "foreign"}), &foreignGroup)
	foreignZone := decodeZone(t, postJSON(t, otherClient, otherBase+"/zones", map[string]any{"slug": "fra", "name": "fra"}))
	foreignHost := decodeHost(t, postJSON(t, otherClient, otherBase+"/hosts", map[string]any{"name": "foreign-host", "zone_id": foreignZone.ID, "managed": true}))

	memberURL := func(group, host string) string { return f.base + "/inventory/groups/" + group + "/members/" + host }
	body := map[string]any{"vars": map[string]any{}}
	cases := []badIDCase{
		{"assign, group id", http.MethodPut, func(id string) string { return memberURL(id, ownHost) }, body, nil, foreignGroup.Group.ID},
		{"assign, host id", http.MethodPut, func(id string) string { return memberURL(own.Group.ID, id) }, body, nil, foreignHost.ID},
		{"unassign, group id", http.MethodDelete, func(id string) string { return memberURL(id, ownHost) }, nil, nil, foreignGroup.Group.ID},
		{"unassign, host id", http.MethodDelete, func(id string) string { return memberURL(own.Group.ID, id) }, nil, nil, foreignHost.ID},
	}
	for _, tc := range cases {
		assertRejectsBadIDs(t, f.c, tc)
	}
}
