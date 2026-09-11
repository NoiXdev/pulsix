package api_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/*.golden.json")

func TestInventoryPreviewGolden(t *testing.T) {
	f := newInvFixture(t, "inv-golden")

	var cred credentialBody
	decodeInto(t, postJSON(t, f.c, f.base+"/credentials", map[string]any{"name": "deploy", "kind": "ssh_key", "username": "ansible", "generate": true}), &cred)
	var jump bastionBody
	decodeInto(t, postJSON(t, f.c, f.base+"/bastions", map[string]any{"name": "jump", "address": "bastion.fra.example", "port": 2222, "username": "ops", "credential_id": cred.Credential.ID}), &jump)

	prod := f.group(t, "production", nil)
	if resp := requestWithHeaders(t, f.c, http.MethodPatch, f.base+"/inventory/groups/"+prod.Group.ID,
		map[string]any{"name": "production", "bastion_id": jump.Bastion.ID}, map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	web := f.group(t, "webserver", &prod.Group.ID)
	webURL := f.base + "/inventory/groups/" + web.Group.ID
	if resp := requestWithHeaders(t, f.c, http.MethodPut, webURL+"/schema", webSchemaBody, map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	if resp := requestWithHeaders(t, f.c, http.MethodPut, webURL+"/vars", map[string]any{"vars": map[string]any{"env": "prod", "db_password": "golden-hunter2"}}, map[string]string{"If-Match": `"2"`}); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	host := decodeHost(t, postJSON(t, f.c, f.base+"/hosts", map[string]any{"name": "web-01", "address": "10.0.0.11", "zone_id": f.zone, "managed": true})).ID
	if resp := requestJSON(t, f.c, http.MethodPut, f.base+"/hosts/"+host+"/ssh", map[string]any{"port": 2200, "credential_id": cred.Credential.ID}); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	if resp := requestJSON(t, f.c, http.MethodPut, webURL+"/members/"+host, map[string]any{"vars": map[string]any{"vhosts": []any{
		map[string]any{"domain": "acme.io", "basic_auth": map[string]any{"user": "u", "password": "golden-member"}},
		map[string]any{"domain": "shop.acme.io"},
	}}}); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	// An invalid assignment is rejected and changes nothing.
	if resp := requestJSON(t, f.c, http.MethodPut, webURL+"/members/"+host, map[string]any{"vars": map[string]any{"vhosts": []any{}}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("empty repeater must be rejected: %d", resp.StatusCode)
	}

	resp := requestJSON(t, f.c, http.MethodGet, f.base+"/inventory/preview?group="+prod.Group.ID, nil)
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusOK || strings.Contains(body, "golden-hunter2") || strings.Contains(body, "golden-member") {
		t.Fatalf("preview: %d %s", resp.StatusCode, body)
	}
	var preview struct {
		Inventory json.RawMessage `json:"inventory"`
	}
	if err := json.Unmarshal([]byte(body), &preview); err != nil {
		t.Fatal(err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, preview.Inventory, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	const path = "testdata/inventory_preview.golden.json"
	if *updateGolden {
		if err := os.WriteFile(path, pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden file (run with -update once, then review it): %v", err)
	}
	if pretty.String() != string(want) {
		t.Errorf("preview differs from %s:\n%s", path, pretty.String())
	}
}
