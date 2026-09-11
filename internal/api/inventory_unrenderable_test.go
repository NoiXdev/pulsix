package api_test

import (
	"net/http"
	"strings"
	"testing"
)

// TestInventoryPreviewUnrenderableIs409 proves a render failure reaches the
// client as 409 with the reason, not as an opaque 500. The API itself
// rejects cyclic jump host chains, so the cycle is written behind the
// service's back, as a concurrent write could leave it.
func TestInventoryPreviewUnrenderableIs409(t *testing.T) {
	f := newInvFixture(t, "inv-unrenderable")
	var a, b bastionBody
	decodeInto(t, postJSON(t, f.c, f.base+"/bastions", map[string]any{"name": "a", "address": "a.example", "username": "ops"}), &a)
	decodeInto(t, postJSON(t, f.c, f.base+"/bastions", map[string]any{"name": "b", "address": "b.example", "username": "ops", "via_bastion_id": a.Bastion.ID}), &b)

	g := f.group(t, "webserver", nil)
	url := f.base + "/inventory/groups/" + g.Group.ID
	if resp := requestWithHeaders(t, f.c, http.MethodPut, url+"/schema", webSchemaBody, map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	if resp := requestWithHeaders(t, f.c, http.MethodPut, url+"/vars", map[string]any{"vars": map[string]any{"env": "prod", "db_password": "unrenderable-hunter2"}}, map[string]string{"If-Match": `"2"`}); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	host := f.host(t, "web-01", true)
	if resp := requestJSON(t, f.c, http.MethodPut, url+"/members/"+host, map[string]any{"vars": map[string]any{"vhosts": []any{map[string]any{"domain": "acme.io"}}}}); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	if resp := requestJSON(t, f.c, http.MethodPut, f.base+"/hosts/"+host+"/ssh", map[string]any{"bastion_mode": "explicit", "bastion_id": b.Bastion.ID}); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	if _, err := f.s.DB.Pool().Exec(t.Context(), "UPDATE bastions SET via_bastion_id = $1 WHERE id = $2", b.Bastion.ID, a.Bastion.ID); err != nil {
		t.Fatal(err)
	}

	for _, u := range []string{f.base + "/inventory/preview", f.base + "/inventory/preview?group=" + g.Group.ID, f.base + "/hosts/" + host + "/inventory/preview"} {
		resp := requestJSON(t, f.c, http.MethodGet, u, nil)
		body := mustReadBody(t, resp)
		if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "web-01") || !strings.Contains(body, "cycle") {
			t.Errorf("GET %s: %d %s", u, resp.StatusCode, body)
		}
		if strings.Contains(body, "$sealed") || strings.Contains(body, "hunter2") {
			t.Errorf("GET %s leaks a secret: %s", u, body)
		}
	}
}
