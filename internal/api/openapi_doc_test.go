package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDocument verifies Document produces a well-formed OpenAPI document
// that actually describes the API surface — not just any valid JSON — so a
// regression that silently stopped registering routes (or broke the
// marshal) would fail this test rather than only surfacing much later, as
// a confusing diff in the generated TypeScript client.
func TestDocument(t *testing.T) {
	doc, err := Document()
	if err != nil {
		t.Fatalf("Document() error = %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatalf("Document() did not produce valid JSON: %v", err)
	}

	if parsed["openapi"] == nil {
		t.Error("Document() output missing top-level \"openapi\" version field")
	}

	paths, ok := parsed["paths"].(map[string]any)
	if !ok {
		t.Fatal("Document() output missing a \"paths\" object")
	}

	for _, want := range []string{
		"/api/v1/auth/login",
		"/api/v1/setup/status",
		"/api/v1/orgs/{org}/hosts",
		"/api/v1/orgs/{org}/hosts/{id}/tags",
		"/api/v1/orgs/{org}/members",
		"/api/v1/orgs/{org}/activity",
		"/api/v1/orgs/{org}/tokens",
		"/api/v1/auth/passkeys/login/finish",
		"/api/v1/orgs/{org}/repos",
		"/api/v1/orgs/{org}/repos/{id}",
		"/api/v1/orgs/{org}/repos/{id}/sync",
		"/api/v1/orgs/{org}/playbooks",
		"/api/v1/orgs/{org}/playbooks/{id}",
		"/api/v1/orgs/{org}/job-templates",
		"/api/v1/orgs/{org}/job-templates/{id}",
		"/api/v1/orgs/{org}/schedules",
		"/api/v1/orgs/{org}/triggers",
		"/api/v1/orgs/{org}/triggers/{id}",
		"/api/v1/orgs/{org}/triggers/{id}/rotate-secret",
		"/api/v1/orgs/{org}/triggers/{id}/test",
		"/api/v1/orgs/{org}/triggers/{id}/deliveries",
		"/api/v1/hooks/jobs/{id}",
		"/api/v1/hooks/repos/{id}",
	} {
		if _, ok := paths[want]; !ok {
			t.Errorf("Document() paths missing %q", want)
		}
	}

	// Called twice: Document must not depend on package-level mutable state
	// that only survives one call (a real risk given registerRoutes builds
	// on a fresh huma.API every time, but a subtle bug could still leak
	// state through a shared registry).
	doc2, err := Document()
	if err != nil {
		t.Fatalf("Document() second call error = %v", err)
	}
	if !strings.Contains(string(doc2), `"openapi"`) {
		t.Error("Document() second call did not produce a document")
	}
}

// TestAlertDetailDTOSchemaCarriesEveryAlertField guards a gap a JSON
// round-trip test cannot see: AlertDetailDTO (alert_handlers.go) embeds
// AlertDTO anonymously to share one field set between the list and detail
// routes. huma's own schema builder (schema.go's getFields) skips an
// anonymous field whose NAME — which, for an embedded field, is the
// embedded type's own name — is unexported, the same rule normal struct
// fields follow. encoding/json does not share that rule: it promotes an
// unexported-TYPE embed's exported fields regardless, so the wire shape
// was always correct and only the generated OpenAPI document — and with
// it, the TypeScript client Task 10 depends on — silently lost every
// field but timeline. Keeping AlertDTO exported is what fixes this; this
// test is what stops it coming back quietly.
func TestAlertDetailDTOSchemaCarriesEveryAlertField(t *testing.T) {
	doc, err := Document()
	if err != nil {
		t.Fatalf("Document() error = %v", err)
	}
	var parsed struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]any `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatalf("Document() did not produce valid JSON: %v", err)
	}
	detail, ok := parsed.Components.Schemas["AlertDetailDTO"]
	if !ok {
		t.Fatal(`Document() components.schemas missing "AlertDetailDTO"`)
	}
	for _, field := range []string{"id", "host_id", "host_name", "scope", "opened_at", "severity", "peak_severity", "timeline"} {
		if _, ok := detail.Properties[field]; !ok {
			t.Errorf("AlertDetailDTO schema missing %q — an embedded DTO's own fields were dropped from the OpenAPI document (properties: %+v)", field, detail.Properties)
		}
	}
}

// TestAdminUserDetailDTOSchemaCarriesEveryUserField is the same guard for
// the account page (backlog 7.74): AdminUserDetailDTO embeds AdminUserDTO,
// and the SPA reads the embedded fields from the generated client.
func TestAdminUserDetailDTOSchemaCarriesEveryUserField(t *testing.T) {
	doc, err := Document()
	if err != nil {
		t.Fatalf("Document() error = %v", err)
	}
	var parsed struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]any `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatalf("Document() did not produce valid JSON: %v", err)
	}
	detail, ok := parsed.Components.Schemas["AdminUserDetailDTO"]
	if !ok {
		t.Fatal(`Document() components.schemas missing "AdminUserDetailDTO"`)
	}
	for _, field := range []string{"id", "email", "display_name", "instance_admin", "state", "org_count", "last_sign_in_at", "memberships", "factors"} {
		if _, ok := detail.Properties[field]; !ok {
			t.Errorf("AdminUserDetailDTO schema missing %q (properties: %+v)", field, detail.Properties)
		}
	}
}
