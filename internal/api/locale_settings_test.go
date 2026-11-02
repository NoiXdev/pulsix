package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// statusDefaultLocale reads default_locale from the public status route.
func statusDefaultLocale(t *testing.T, c *http.Client, base string) string {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, base+"/api/v1/setup/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET setup/status: status %d", resp.StatusCode)
	}
	var body struct {
		DefaultLocale string `json:"default_locale"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.DefaultLocale
}

func TestDefaultLocaleSettings(t *testing.T) {
	s := apitest.New(t)
	anon := s.Client()

	// Public, and English on a fresh instance.
	if got := statusDefaultLocale(t, anon, s.HTTP.URL); got != "en" {
		t.Fatalf("status default_locale on a fresh instance = %q, want en", got)
	}

	wizard := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/setup/complete", completeSetupBody("locale-admin@example.test", "locale"))
	if wizard.StatusCode != http.StatusCreated {
		t.Fatalf("wizard complete: %d, body %s", wizard.StatusCode, mustReadBody(t, wizard))
	}
	var out struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(wizard.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	admin := s.LoginAs(uuid.MustParse(out.UserID))
	url := s.HTTP.URL + "/api/v1/admin/settings/locale"

	get := func(c *http.Client) string {
		t.Helper()
		resp := requestJSON(t, c, http.MethodGet, url, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET locale settings: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var body struct {
			DefaultLocale string `json:"default_locale"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body.DefaultLocale
	}

	if got := get(admin); got != "en" {
		t.Fatalf("GET before any PUT = %q, want en", got)
	}
	if resp := requestJSON(t, admin, http.MethodPut, url, map[string]any{"default_locale": "de"}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT de: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if got := get(admin); got != "de" {
		t.Fatalf("GET after PUT de = %q, want de", got)
	}
	if got := statusDefaultLocale(t, anon, s.HTTP.URL); got != "de" {
		t.Fatalf("status default_locale after PUT de = %q, want de", got)
	}

	// An unsupported language is refused and changes nothing.
	if resp := requestJSON(t, admin, http.MethodPut, url, map[string]any{"default_locale": "fr"}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("PUT fr: status %d, want 422; body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if got := get(admin); got != "de" {
		t.Fatalf("GET after a refused PUT = %q, want de", got)
	}

	// A plain member is not an instance admin, for reading or writing.
	member := s.SeedUser("locale-member@example.test", "correct horse battery")
	s.SeedOrg("locale-members-org", member)
	memberClient := s.LoginAs(member)
	if resp := requestJSON(t, memberClient, http.MethodGet, url, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET as a non-admin: status %d, want 403", resp.StatusCode)
	}
	if resp := requestJSON(t, memberClient, http.MethodPut, url, map[string]any{"default_locale": "en"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("PUT as a non-admin: status %d, want 403", resp.StatusCode)
	}
	if got := get(admin); got != "de" {
		t.Fatalf("a refused non-admin PUT changed the language to %q", got)
	}
}

func TestSetupCompleteStoresTheDefaultLocale(t *testing.T) {
	s := apitest.New(t)
	body := completeSetupBody("de-admin@example.test", "de-first")
	body["default_locale"] = "de"
	resp := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/setup/complete", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("complete with de: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if got := statusDefaultLocale(t, s.Client(), s.HTTP.URL); got != "de" {
		t.Fatalf("status default_locale after setup = %q, want de", got)
	}
}

func TestSetupCompleteRefusesAnUnsupportedLocale(t *testing.T) {
	s := apitest.New(t)
	body := completeSetupBody("fr-admin@example.test", "fr-first")
	body["default_locale"] = "fr"
	resp := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/setup/complete", body)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("complete with fr: status %d, want 422; body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if st := requestJSON(t, s.Client(), http.MethodGet, s.HTTP.URL+"/api/v1/setup/status", nil); st.StatusCode != http.StatusOK {
		t.Fatal("status unreachable")
	}
}
