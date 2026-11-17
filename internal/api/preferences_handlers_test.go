package api

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// TestPreferencesRefuseBearerToken: preferences belong to the person at the
// browser, so a bearer-token principal gets 403 on both routes.
func TestPreferencesRefuseBearerToken(t *testing.T) {
	api := newInteractiveSessionTestAPI(t)
	ctx := ctxWithTokenPrincipal(uuid.Must(uuid.NewV7()))
	body := map[string]any{"density": "standard", "page_size": 25, "lists": map[string]any{}}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		var b any
		if method == http.MethodPut {
			b = body
		}
		rec := doOptional(api, ctx, method, "/api/v1/auth/me/preferences", b)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s with a token principal: status %d, want 403, body %s", method, rec.Code, rec.Body.String())
		}
	}
}
