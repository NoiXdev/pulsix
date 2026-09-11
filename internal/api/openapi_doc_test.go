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
