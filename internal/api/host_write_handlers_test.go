package api_test

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/noixdev/pulsix/internal/tenancy"
)

type hostTagsBody struct {
	Tags []struct {
		Key    string `json:"key"`
		Value  string `json:"value"`
		Source string `json:"source"`
	} `json:"tags"`
}

// createBastion creates a jump host in the fixture's organization through
// the API and returns its id.
func createBastion(t *testing.T, f sshFixture, name string) string {
	t.Helper()
	var b bastionBody
	resp := postJSON(t, f.c, f.org+"/bastions", map[string]any{"name": name, "address": name + ".example", "username": "ops"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create bastion: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	decodeInto(t, resp, &b)
	return b.Bastion.ID
}

func readHostSSH(t *testing.T, c *http.Client, url string) hostSSHBody {
	t.Helper()
	var out hostSSHBody
	resp := requestJSON(t, c, http.MethodGet, url, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET ssh: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	decodeInto(t, resp, &out)
	return out
}

func readHostTags(t *testing.T, c *http.Client, url string) map[string]string {
	t.Helper()
	var out hostTagsBody
	resp := requestJSON(t, c, http.MethodGet, url, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET tags: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	decodeInto(t, resp, &out)
	m := map[string]string{}
	for _, tg := range out.Tags {
		m[tg.Key] = tg.Value
	}
	return m
}

// TestHostCreateWithJumpAndTags: one POST sets the host, its jump host and
// its tags, and the dedicated SSH and tag reads show both.
func TestHostCreateWithJumpAndTags(t *testing.T) {
	f := newSSHFixture(t, "hostwrite-create-org")
	bastion := createBastion(t, f, "edge")

	resp := postJSON(t, f.c, f.org+"/hosts", map[string]any{
		"name": "web-02", "zone_id": f.zoneID, "managed": true,
		"jump": map[string]any{"mode": "explicit", "bastion_id": bastion},
		"tags": []map[string]any{{"key": "role", "value": "web"}, {"key": "env", "value": "prod"}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create host: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	h := decodeHost(t, resp)

	ssh := readHostSSH(t, f.c, f.org+"/hosts/"+h.ID+"/ssh")
	if ssh.SSH.BastionMode != "explicit" || ssh.SSH.BastionID == nil || *ssh.SSH.BastionID != bastion {
		t.Fatalf("ssh = %+v, want explicit %s", ssh.SSH, bastion)
	}
	if tags := readHostTags(t, f.c, f.org+"/hosts/"+h.ID+"/tags"); len(tags) != 2 || tags["role"] != "web" || tags["env"] != "prod" {
		t.Fatalf("tags = %v", tags)
	}

	// A jump host that does not exist is not found, and creates nothing.
	resp = postJSON(t, f.c, f.org+"/hosts", map[string]any{
		"name": "web-03", "zone_id": f.zoneID,
		"jump": map[string]any{"mode": "explicit", "bastion_id": "0190a8f0-0000-7000-8000-000000000000"},
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown bastion: %d %s, want 404", resp.StatusCode, mustReadBody(t, resp))
	}
	// A mode that disagrees with the id is the caller's mistake.
	resp = postJSON(t, f.c, f.org+"/hosts", map[string]any{
		"name": "web-03", "zone_id": f.zoneID, "jump": map[string]any{"mode": "explicit"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("explicit without id: %d %s, want 400", resp.StatusCode, mustReadBody(t, resp))
	}
	resp = postJSON(t, f.c, f.org+"/hosts", map[string]any{
		"name": "web-03", "zone_id": f.zoneID, "jump": map[string]any{"mode": "none", "bastion_id": "not-a-uuid"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed bastion id: %d %s, want 400", resp.StatusCode, mustReadBody(t, resp))
	}
}

// TestHostUpdateWithJumpAndTags: PATCH sets the jump host and replaces the
// manual tags in the same write; leaving both out leaves them alone.
func TestHostUpdateWithJumpAndTags(t *testing.T) {
	f := newSSHFixture(t, "hostwrite-update-org")
	url := f.org + "/hosts/" + f.hostID

	// Settings the jump host write must not touch.
	if resp := requestJSON(t, f.c, http.MethodPut, url+"/ssh", map[string]any{"username": "deploy", "port": 2222}); resp.StatusCode != http.StatusOK {
		t.Fatalf("put ssh: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	etag := requestJSON(t, f.c, http.MethodGet, url, nil).Header.Get("ETag")
	resp := requestWithHeaders(t, f.c, http.MethodPatch, url, map[string]any{
		"name": "web-01", "zone_id": f.zoneID, "managed": true,
		"jump": map[string]any{"mode": "none"},
		"tags": []map[string]any{{"key": "role", "value": "db"}},
	}, map[string]string{"If-Match": etag})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	ssh := readHostSSH(t, f.c, url+"/ssh")
	if ssh.SSH.BastionMode != "none" || ssh.SSH.Username != "deploy" || ssh.SSH.Port != 2222 {
		t.Fatalf("ssh = %+v, want none with deploy:2222 kept", ssh.SSH)
	}
	if tags := readHostTags(t, f.c, url+"/tags"); len(tags) != 1 || tags["role"] != "db" {
		t.Fatalf("tags = %v", tags)
	}

	etag = resp.Header.Get("ETag")
	resp = requestWithHeaders(t, f.c, http.MethodPatch, url, map[string]any{
		"name": "web-01", "zone_id": f.zoneID, "managed": true,
	}, map[string]string{"If-Match": etag})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch without jump and tags: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if ssh := readHostSSH(t, f.c, url+"/ssh"); ssh.SSH.BastionMode != "none" {
		t.Fatalf("jump host after a patch without it = %s, want none", ssh.SSH.BastionMode)
	}
	if tags := readHostTags(t, f.c, url+"/tags"); len(tags) != 1 || tags["role"] != "db" {
		t.Fatalf("tags after a patch without them = %v", tags)
	}
}

// TestHostCreateJumpNeedsInventoryWrite: a host write that carries a jump
// host also needs inventory.write, the permission of PUT /hosts/{id}/ssh.
//
// No role in tenancy's matrix holds hosts.write without inventory.write
// (both start at operator), and a token can only be scoped to a role, so
// that split cannot be produced here. The test therefore pins what can be
// observed: a viewer is refused, an operator may send a jump host, and the
// gate itself is the tenancy.Allows check in the handler.
func TestHostCreateJumpNeedsInventoryWrite(t *testing.T) {
	if tenancy.Allows(tenancy.RoleViewer, tenancy.PermHostsWrite) || !tenancy.Allows(tenancy.RoleOperator, tenancy.PermInventoryWrite) {
		t.Fatal("the role matrix changed: revisit this test, a hosts.write-only role may now exist")
	}
	f := newSSHFixture(t, "hostwrite-perm-org")
	bastion := createBastion(t, f, "edge")
	orgID := orgIDBySlug(t, f.s, "hostwrite-perm-org")
	operator := f.s.SeedUser("hostwrite-op@example.test", "correct horse battery")
	viewer := f.s.SeedUser("hostwrite-viewer@example.test", "correct horse battery")
	f.s.AddMember(orgID, operator, tenancy.RoleOperator)
	f.s.AddMember(orgID, viewer, tenancy.RoleViewer)

	body := map[string]any{
		"name": "web-op", "zone_id": f.zoneID,
		"jump": map[string]any{"mode": "explicit", "bastion_id": bastion},
	}
	if resp := postJSON(t, f.s.LoginAs(viewer), f.org+"/hosts", body); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer with jump: %d, want 403", resp.StatusCode)
	}
	if resp := postJSON(t, f.s.LoginAs(operator), f.org+"/hosts", body); resp.StatusCode != http.StatusCreated {
		t.Fatalf("operator with jump: %d %s, want 201", resp.StatusCode, mustReadBody(t, resp))
	}
}

// TestHostDTOCarriesRevision: every host read carries the revision the SPA
// sends back as If-Match, and it matches the ETag.
func TestHostDTOCarriesRevision(t *testing.T) {
	f := newSSHFixture(t, "hostwrite-rev-org")
	url := f.org + "/hosts/" + f.hostID

	resp := requestJSON(t, f.c, http.MethodGet, url, nil)
	h := decodeHost(t, resp)
	if h.Revision < 1 || resp.Header.Get("ETag") != `"`+strconv.FormatInt(h.Revision, 10)+`"` {
		t.Fatalf("revision = %d, ETag %q", h.Revision, resp.Header.Get("ETag"))
	}

	resp = requestWithHeaders(t, f.c, http.MethodPatch, url, map[string]any{"name": "web-01", "zone_id": f.zoneID},
		map[string]string{"If-Match": resp.Header.Get("ETag")})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if got := decodeHost(t, resp); got.Revision != h.Revision+1 {
		t.Fatalf("revision after patch = %d, want %d", got.Revision, h.Revision+1)
	}

	var list struct {
		Hosts []hostDTO `json:"hosts"`
	}
	decodeInto(t, requestJSON(t, f.c, http.MethodGet, f.org+"/hosts", nil), &list)
	if len(list.Hosts) != 1 || list.Hosts[0].Revision != h.Revision+1 {
		t.Fatalf("listed hosts = %+v, want the current revision", list.Hosts)
	}

	// The selector query reads hosts with its own column list; it carries
	// the revision too. The tag write moves it once more (backlog 7.79).
	if resp := requestJSON(t, f.c, http.MethodPut, url+"/tags", []map[string]any{{"key": "role", "value": "web"}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("put tags: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	list.Hosts = nil
	decodeInto(t, requestJSON(t, f.c, http.MethodPost, f.org+"/hosts/query", map[string]any{"selector": "role = web"}), &list)
	if len(list.Hosts) != 1 || list.Hosts[0].Revision != h.Revision+2 {
		t.Fatalf("queried hosts = %+v, want the current revision", list.Hosts)
	}
}
