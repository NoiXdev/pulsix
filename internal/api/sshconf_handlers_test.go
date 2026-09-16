package api_test

import (
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type bastionBody struct {
	Bastion struct {
		ID           string  `json:"id"`
		Name         string  `json:"name"`
		Port         int     `json:"port"`
		ViaBastionID *string `json:"via_bastion_id"`
	} `json:"bastion"`
}

type hostSSHBody struct {
	SSH struct {
		HostID       string  `json:"host_id"`
		Username     string  `json:"username"`
		Port         int     `json:"port"`
		CredentialID *string `json:"credential_id"`
		BastionMode  string  `json:"bastion_mode"`
		BastionID    *string `json:"bastion_id"`
	} `json:"ssh"`
}

// sshFixture creates an org with an owner, the default zone's id, a managed
// host and an SSH key credential.
type sshFixture struct {
	s       *apitest.Server
	c       *http.Client
	org     string
	zoneID  string
	hostID  string
	keyID   string
	tokenID string
}

func newSSHFixture(t *testing.T, slug string) sshFixture {
	t.Helper()
	s := apitest.New(t)
	owner := s.SeedUser(slug+"-owner@example.test", "correct horse battery")
	s.SeedOrg(slug, owner)
	f := sshFixture{s: s, c: s.LoginAs(owner), org: s.HTTP.URL + "/api/v1/orgs/" + slug}
	resp := postJSON(t, f.c, f.org+"/zones", map[string]any{"slug": "fra", "name": "fra"})
	f.zoneID = decodeZone(t, resp).ID
	resp = postJSON(t, f.c, f.org+"/hosts", map[string]any{"name": "web-01", "zone_id": f.zoneID, "managed": true})
	f.hostID = decodeHost(t, resp).ID
	var cred credentialBody
	decodeInto(t, postJSON(t, f.c, f.org+"/credentials", map[string]any{"name": "key", "kind": "ssh_key", "generate": true}), &cred)
	f.keyID = cred.Credential.ID
	decodeInto(t, postJSON(t, f.c, f.org+"/credentials", map[string]any{"name": "tok", "kind": "git_token", "token": "x"}), &cred)
	f.tokenID = cred.Credential.ID
	return f
}

func TestBastionLifecycle(t *testing.T) {
	f := newSSHFixture(t, "bastion-org")
	var a, b bastionBody
	resp := postJSON(t, f.c, f.org+"/bastions", map[string]any{"name": "a", "address": "a.example", "username": "ops", "credential_id": f.keyID})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create a: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	decodeInto(t, resp, &a)
	if a.Bastion.Port != 22 {
		t.Fatalf("port default = %d", a.Bastion.Port)
	}
	resp = postJSON(t, f.c, f.org+"/bastions", map[string]any{"name": "b", "address": "b.example", "port": 2222, "username": "ops", "via_bastion_id": a.Bastion.ID})
	decodeInto(t, resp, &b)
	if b.Bastion.ViaBastionID == nil || *b.Bastion.ViaBastionID != a.Bastion.ID {
		t.Fatalf("b = %+v", b)
	}
	resp = requestJSON(t, f.c, http.MethodPatch, f.org+"/bastions/"+a.Bastion.ID, map[string]any{"name": "a", "address": "a.example", "username": "ops", "via_bastion_id": b.Bastion.ID})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("cycle: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, f.c, http.MethodDelete, f.org+"/bastions/"+a.Bastion.ID, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete a bastion another one goes through: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, f.c, http.MethodDelete, f.org+"/credentials/"+f.keyID, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete a credential a bastion uses: %d", resp.StatusCode)
	}

	resp = requestJSON(t, f.c, http.MethodPut, f.org+"/zones/"+f.zoneID+"/bastion", map[string]any{"bastion_id": b.Bastion.ID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("zone default: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp = requestJSON(t, f.c, http.MethodGet, f.org+"/zones", nil)
	var zones struct {
		Zones []struct {
			ID        string  `json:"id"`
			BastionID *string `json:"bastion_id"`
		} `json:"zones"`
	}
	decodeInto(t, resp, &zones)
	found := false
	for _, z := range zones.Zones {
		if z.ID == f.zoneID && z.BastionID != nil && *z.BastionID == b.Bastion.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("zone default not listed: %+v", zones)
	}
	if resp := requestJSON(t, f.c, http.MethodDelete, f.org+"/bastions/"+b.Bastion.ID, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete a zone default: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, f.c, http.MethodPut, f.org+"/zones/"+f.zoneID+"/bastion", map[string]any{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("clear zone default: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, f.c, http.MethodDelete, f.org+"/bastions/"+b.Bastion.ID, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete b: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, f.c, http.MethodPut, f.org+"/zones/"+f.zoneID+"/bastion", map[string]any{"bastion_id": b.Bastion.ID}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("zone default to a deleted bastion: %d", resp.StatusCode)
	}
}

func TestHostSSHSettings(t *testing.T) {
	f := newSSHFixture(t, "hostssh-org")
	url := f.org + "/hosts/" + f.hostID + "/ssh"
	var got hostSSHBody
	resp := requestJSON(t, f.c, http.MethodGet, url, nil)
	decodeInto(t, resp, &got)
	if got.SSH.Port != 22 || got.SSH.BastionMode != "inherit" || got.SSH.HostID != f.hostID {
		t.Fatalf("defaults = %+v", got)
	}
	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"explicit without bastion", map[string]any{"bastion_mode": "explicit"}, http.StatusBadRequest},
		{"git credential", map[string]any{"credential_id": f.tokenID}, http.StatusBadRequest},
		{"unknown mode", map[string]any{"bastion_mode": "maybe"}, http.StatusUnprocessableEntity},
		{"malformed credential id", map[string]any{"credential_id": "nope"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if resp := requestJSON(t, f.c, http.MethodPut, url, tc.body); resp.StatusCode != tc.want {
				t.Fatalf("status %d, want %d: %s", resp.StatusCode, tc.want, mustReadBody(t, resp))
			}
		})
	}
	resp = requestJSON(t, f.c, http.MethodPut, url, map[string]any{"username": "deploy", "port": 2222, "credential_id": f.keyID, "bastion_mode": "none"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	decodeInto(t, requestJSON(t, f.c, http.MethodGet, url, nil), &got)
	if got.SSH.Username != "deploy" || got.SSH.Port != 2222 || got.SSH.BastionMode != "none" || got.SSH.CredentialID == nil {
		t.Fatalf("stored = %+v", got)
	}
	var cred credentialBody
	decodeInto(t, requestJSON(t, f.c, http.MethodGet, f.org+"/credentials/"+f.keyID, nil), &cred)
	if cred.Credential.UsedBy.Hosts != 1 {
		t.Fatalf("used_by = %+v", cred.Credential.UsedBy)
	}
	if resp := requestJSON(t, f.c, http.MethodGet, f.org+"/hosts/00000000-0000-7000-8000-000000000000/ssh", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown host: %d", resp.StatusCode)
	}
}

// TestSSHWriteRoutesRejectBadIDs is backlog 2.19: bastion update/delete,
// the zone default jump host and a host's SSH settings must each 404 a
// foreign-organization id and 400 (or 404, never 500) a malformed one.
// Bastion create has no id path parameter and so is not part of this
// table.
func TestSSHWriteRoutesRejectBadIDs(t *testing.T) {
	f := newSSHFixture(t, "sshbadid-org")

	other := f.s.SeedUser("sshbadid-other@example.test", "correct horse battery")
	f.s.SeedOrg("sshbadid-other", other)
	otherClient := f.s.LoginAs(other)
	otherOrg := f.s.HTTP.URL + "/api/v1/orgs/sshbadid-other"
	var foreignBastion bastionBody
	decodeInto(t, postJSON(t, otherClient, otherOrg+"/bastions", map[string]any{"name": "foreign", "address": "f.example", "username": "ops"}), &foreignBastion)
	foreignZone := decodeZone(t, postJSON(t, otherClient, otherOrg+"/zones", map[string]any{"slug": "fra", "name": "fra"}))
	foreignHost := decodeHost(t, postJSON(t, otherClient, otherOrg+"/hosts", map[string]any{"name": "foreign-host", "zone_id": foreignZone.ID, "managed": true}))

	bastionReqBody := map[string]any{"name": "a", "address": "a.example", "username": "ops"}
	cases := []badIDCase{
		{"update bastion", http.MethodPatch, func(id string) string { return f.org + "/bastions/" + id }, bastionReqBody, nil, foreignBastion.Bastion.ID},
		{"delete bastion", http.MethodDelete, func(id string) string { return f.org + "/bastions/" + id }, nil, nil, foreignBastion.Bastion.ID},
		{"zone default jump host", http.MethodPut, func(id string) string { return f.org + "/zones/" + id + "/bastion" }, map[string]any{}, nil, foreignZone.ID},
		{"host ssh settings", http.MethodPut, func(id string) string { return f.org + "/hosts/" + id + "/ssh" }, map[string]any{"port": 22}, nil, foreignHost.ID},
	}
	for _, tc := range cases {
		assertRejectsBadIDs(t, f.c, tc)
	}
}

func TestSSHPermissionsAndIsolation(t *testing.T) {
	f := newSSHFixture(t, "sshperm-org")

	// A second organization on the same server, with an operator and a viewer.
	rolesOwner := f.s.SeedUser("sshperm-roles-owner@example.test", "correct horse battery")
	rolesOrg := f.s.SeedOrg("sshperm-roles", rolesOwner)
	operator := f.s.SeedUser("sshperm-op@example.test", "correct horse battery")
	viewer := f.s.SeedUser("sshperm-viewer@example.test", "correct horse battery")
	f.s.AddMember(rolesOrg, operator, tenancy.RoleOperator)
	f.s.AddMember(rolesOrg, viewer, tenancy.RoleViewer)
	roles := f.s.HTTP.URL + "/api/v1/orgs/sshperm-roles"

	if resp := postJSON(t, f.s.LoginAs(operator), roles+"/bastions", map[string]any{"name": "a", "address": "a.example", "username": "ops"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator creates a bastion: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, f.s.LoginAs(viewer), http.MethodGet, roles+"/bastions", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer lists bastions: %d", resp.StatusCode)
	}

	// A credential of the second organization cannot be bound to a host of the first.
	var foreign credentialBody
	decodeInto(t, postJSON(t, f.s.LoginAs(rolesOwner), roles+"/credentials", map[string]any{"name": "foreign", "kind": "ssh_key", "generate": true}), &foreign)
	resp := requestJSON(t, f.c, http.MethodPut, f.org+"/hosts/"+f.hostID+"/ssh", map[string]any{"credential_id": foreign.Credential.ID})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("cross-org credential: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
}
