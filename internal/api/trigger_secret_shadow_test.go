package api_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// inventorySecret gives the fixture's template host (hostA) a group whose
// schema declares name a secret.
func (f runFixture) inventorySecret(t *testing.T, name string) {
	t.Helper()
	scoped := db.WithOrgScope(context.Background(), f.orgID)
	g, err := f.s.Deps.Inventory.CreateGroup(scoped, f.orgID, inventory.GroupInput{Name: "secrets_" + name})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.Deps.Inventory.SetSchema(scoped, f.orgID, g.ID, g.Revision,
		schema.Schema{Fields: []schema.Field{{Name: name, Type: schema.TypeSecret}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Deps.Inventory.Assign(scoped, f.orgID, g.ID, f.hostA, nil, nil); err != nil {
		t.Fatal(err)
	}
}

// TestTriggerMappingOntoASecretIsRefused (backlog 2.139): creating or
// updating a trigger whose mapping targets a secret of the template's
// hosts answers 400 naming the variable and the kind of secret.
func TestTriggerMappingOntoASecretIsRefused(t *testing.T) {
	f := newRunFixture(t, "trig-shadow-save")
	plain := map[string]any{"kind": "template_run", "template_id": f.templateID.String(), "name": "deploy", "preset": "pulsix"}
	resp := postJSON(t, f.operatorC, f.triggersURL(), plain)
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	var created triggerCreated
	decodeInto(t, resp, &created)
	f.inventorySecret(t, "deploy_tag")

	mapped := map[string]any{"kind": "template_run", "template_id": f.templateID.String(), "name": "deploy-mapped", "preset": "pulsix",
		"mapping": map[string]any{"extra_vars": map[string]string{"deploy_tag": "$.ref"}}}
	const want = "mapping target deploy_tag: the inventory holds a secret of that name for a target host"
	resp = postJSON(t, f.operatorC, f.triggersURL(), mapped)
	if b := mustReadBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(b, want) {
		t.Fatalf("create = %d %s, want 400 naming the inventory secret", resp.StatusCode, b)
	}
	mapped["name"] = "deploy"
	resp = requestWithHeaders(t, f.operatorC, http.MethodPut, f.triggersURL()+"/"+created.Trigger.ID, mapped,
		map[string]string{"If-Match": `"` + strconv.FormatInt(created.Trigger.Revision, 10) + `"`})
	if b := mustReadBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(b, want) {
		t.Fatalf("update = %d %s, want 400 naming the inventory secret", resp.StatusCode, b)
	}
}

// TestHookRefusesAMappingOntoALaterSecret (backlog 2.139, R4): a secret
// that appears after the trigger was saved fails the delivery with 422 at
// the mapped field; no run starts and the recorded reason names the
// variable, never the value.
func TestHookRefusesAMappingOntoALaterSecret(t *testing.T) {
	f := newRunFixture(t, "hook-shadow")
	c := f.createTrigger(t, nil)
	f.inventorySecret(t, "deploy_tag")
	body := []byte(`{"ref":"s3cret-value-9f"}`)
	resp := hook(t, c.URL, body, signedHeaders(c, body))
	b := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(b, "extra_vars.deploy_tag") || strings.Contains(b, "s3cret-value-9f") {
		t.Fatalf("hook = %d %s, want 422 at extra_vars.deploy_tag", resp.StatusCode, b)
	}
	var list struct {
		Jobs []jobDTO `json:"jobs"`
	}
	decodeInto(t, requestJSON(t, f.operatorC, http.MethodGet, f.baseURL()+"?trigger=webhook", nil), &list)
	if len(list.Jobs) != 0 {
		t.Fatalf("a refused delivery started %d jobs", len(list.Jobs))
	}
	var deliveries struct {
		Deliveries []struct {
			Outcome string `json:"outcome"`
			Reason  string `json:"reason"`
		} `json:"deliveries"`
	}
	decodeInto(t, requestJSON(t, f.operatorC, http.MethodGet, f.triggersURL()+"/"+c.Trigger.ID.String()+"/deliveries", nil), &deliveries)
	if len(deliveries.Deliveries) != 1 {
		t.Fatalf("deliveries = %+v", deliveries)
	}
	d := deliveries.Deliveries[0]
	if d.Outcome != "invalid_payload" || !strings.Contains(d.Reason, "deploy_tag") || !strings.Contains(d.Reason, "inventory holds a secret") || strings.Contains(d.Reason, "s3cret-value-9f") {
		t.Fatalf("delivery = %+v", d)
	}
}
