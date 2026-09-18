package api_test

import (
	"net/http"
	"strings"
	"testing"
)

func (f runFixture) triggersURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/triggers"
}

type triggerCreated struct {
	Trigger struct {
		ID              string `json:"id"`
		Preset          string `json:"preset"`
		SignatureHeader string `json:"signature_header"`
		URL             string `json:"url"`
		Revision        int64  `json:"revision"`
	} `json:"trigger"`
	Secret string `json:"secret"`
	URL    string `json:"url"`
}

func TestTriggerLifecycle(t *testing.T) {
	f := newRunFixture(t, "trig-life")
	c := f.operatorC
	resp := postJSON(t, c, f.triggersURL(), map[string]any{
		"kind": "template_run", "template_id": f.templateID.String(), "name": "deploy on push", "preset": "github",
		"mapping": map[string]any{"extra_vars": map[string]string{"deploy_tag": "$.ref"}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	var created triggerCreated
	decodeInto(t, resp, &created)
	if created.Secret == "" || created.Trigger.SignatureHeader != "X-Hub-Signature-256" || created.URL != created.Trigger.URL {
		t.Fatalf("created = %+v", created)
	}
	id := created.Trigger.ID
	// The secret is never read back.
	for _, url := range []string{f.triggersURL() + "?template_id=" + f.templateID.String(), f.triggersURL() + "/" + id} {
		if body := mustReadBody(t, requestJSON(t, f.viewerC, http.MethodGet, url, nil)); strings.Contains(body, created.Secret) || strings.Contains(body, `"secret"`) {
			t.Fatalf("%s leaks the secret: %s", url, body)
		}
	}
	// Test call: signed in-process, recorded as a delivery.
	resp = postJSON(t, c, f.triggersURL()+"/"+id+"/test", map[string]any{"payload": map[string]any{"ref": "v9"}})
	var test struct {
		Outcome string `json:"outcome"`
		RunID   string `json:"run_id"`
	}
	decodeInto(t, resp, &test)
	if test.Outcome != "triggered" || test.RunID == "" {
		t.Fatalf("test call = %+v", test)
	}
	var deliveries struct {
		Deliveries []struct {
			Outcome string `json:"outcome"`
			Reason  string `json:"reason"`
		} `json:"deliveries"`
	}
	decodeInto(t, requestJSON(t, c, http.MethodGet, f.triggersURL()+"/"+id+"/deliveries", nil), &deliveries)
	if len(deliveries.Deliveries) != 1 || deliveries.Deliveries[0].Reason != "test call" {
		t.Fatalf("deliveries = %+v", deliveries)
	}
	// Rotation: the old secret stops working at the hook.
	var rotated triggerCreated
	decodeInto(t, postJSON(t, c, f.triggersURL()+"/"+id+"/rotate-secret", nil), &rotated)
	if rotated.Secret == "" || rotated.Secret == created.Secret {
		t.Fatalf("rotated = %+v", rotated)
	}
	// Update needs If-Match.
	up := map[string]any{"kind": "template_run", "template_id": f.templateID.String(), "name": "deploy on push", "preset": "github", "enabled": false}
	if resp := requestJSON(t, c, http.MethodPut, f.triggersURL()+"/"+id, up); resp.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match: %d", resp.StatusCode)
	}
	resp = requestWithHeaders(t, c, http.MethodPut, f.triggersURL()+"/"+id, up, map[string]string{"If-Match": `"2"`})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, c, http.MethodDelete, f.triggersURL()+"/"+id, nil); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	for _, action := range []string{"POST createTrigger", "POST testTrigger", "POST rotateTriggerSecret", "PUT updateTrigger", "DELETE deleteTrigger"} {
		assertActivityContains(t, f, action)
	}
}

func TestTriggerPermissionsAndValidation(t *testing.T) {
	f := newRunFixture(t, "trig-perm")
	body := map[string]any{"kind": "template_run", "template_id": f.templateID.String(), "name": "t", "preset": "pulsix"}
	if resp := postJSON(t, f.viewerC, f.triggersURL(), body); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, f.viewerC, http.MethodGet, f.triggersURL(), nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("list without a target filter: %d", resp.StatusCode)
	}
	bad := map[string]any{"kind": "template_run", "template_id": f.templateID.String(), "name": "t2", "preset": "pulsix",
		"mapping": map[string]any{"extra_vars": map[string]string{"x": "$..ref"}}}
	if resp := postJSON(t, f.operatorC, f.triggersURL(), bad); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("descendant path: %d", resp.StatusCode)
	}
	var created triggerCreated
	decodeInto(t, postJSON(t, f.operatorC, f.triggersURL(), body), &created)
	if resp := postJSON(t, f.viewerC, f.triggersURL()+"/"+created.Trigger.ID+"/rotate-secret", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer rotate: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, f.viewerC, http.MethodGet, f.triggersURL()+"/"+created.Trigger.ID+"/deliveries", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer deliveries: %d", resp.StatusCode)
	}
}

// TestTriggerSecretIsNeverLeaked is the brief's "the webhook secret is
// returned only in the create and rotate responses, never in get or list"
// rule (task-11-brief.md), checked the same way
// TestScheduleExtraVarsSecretsAreNeverLeaked (schedule_handlers_test.go)
// checks schedule overrides: the secret value itself, and the "secret"
// JSON key that would carry it, must be absent from get, list and the
// org's audit trail — only the create and rotate-secret responses may ever
// carry it, and only once each.
func TestTriggerSecretIsNeverLeaked(t *testing.T) {
	f := newRunFixture(t, "trig-secret")
	c := f.operatorC
	var created triggerCreated
	decodeInto(t, postJSON(t, c, f.triggersURL(), map[string]any{
		"kind": "template_run", "template_id": f.templateID.String(), "name": "deploy on push", "preset": "github",
	}), &created)
	id := created.Trigger.ID
	if created.Secret == "" {
		t.Fatalf("create did not return a secret: %+v", created)
	}

	var rotated triggerCreated
	decodeInto(t, postJSON(t, c, f.triggersURL()+"/"+id+"/rotate-secret", nil), &rotated)
	if rotated.Secret == "" || rotated.Secret == created.Secret {
		t.Fatalf("rotate did not return a fresh secret: %+v", rotated)
	}

	assertNoLeak := func(t *testing.T, label, raw string) {
		t.Helper()
		for _, marker := range []string{created.Secret, rotated.Secret, `"secret"`} {
			if strings.Contains(raw, marker) {
				t.Errorf("%s leaks %q: %s", label, marker, raw)
			}
		}
	}

	assertNoLeak(t, "get", mustReadBody(t, requestJSON(t, c, http.MethodGet, f.triggersURL()+"/"+id, nil)))
	assertNoLeak(t, "list", mustReadBody(t, requestJSON(t, c, http.MethodGet, f.triggersURL()+"?template_id="+f.templateID.String(), nil)))

	activity := mustReadBody(t, requestJSON(t, f.adminC, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/activity?limit=100", nil))
	for _, marker := range []string{created.Secret, rotated.Secret} {
		if strings.Contains(activity, marker) {
			t.Errorf("activity feed leaks %q: %s", marker, activity)
		}
	}
}
