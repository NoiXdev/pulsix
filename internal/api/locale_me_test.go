package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// meLocale is the language part of GET /auth/me and PUT /auth/me/locale.
type meLocale struct {
	Locale          *string `json:"locale"`
	EffectiveLocale string  `json:"effective_locale"`
}

func decodeMeLocale(t *testing.T, resp *http.Response) meLocale {
	t.Helper()
	var body meLocale
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestUserLocale(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("locale-user@example.test", "correct horse battery")
	c := s.LoginAs(uid)
	meURL := s.HTTP.URL + "/api/v1/auth/me"
	putURL := s.HTTP.URL + "/api/v1/auth/me/locale"

	// me reads the user's locale and the language that applies.
	me := func() meLocale {
		t.Helper()
		resp := requestJSON(t, c, http.MethodGet, meURL, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET me: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
		}
		return decodeMeLocale(t, resp)
	}
	put := func(body any) *http.Response {
		t.Helper()
		return requestJSON(t, c, http.MethodPut, putURL, body)
	}
	want := func(got meLocale, locale *string, effective string) {
		t.Helper()
		if (got.Locale == nil) != (locale == nil) || (locale != nil && *got.Locale != *locale) || got.EffectiveLocale != effective {
			gl := "<null>"
			if got.Locale != nil {
				gl = *got.Locale
			}
			wl := "<null>"
			if locale != nil {
				wl = *locale
			}
			t.Fatalf("locale = %s, effective = %q; want %s, %q", gl, got.EffectiveLocale, wl, effective)
		}
	}
	de, en := "de", "en"

	// Before any choice: no own language, English applies.
	want(me(), nil, "en")

	// The instance default now applies to a user without a choice.
	if err := s.Deps.Setup.SetDefaultLocale(t.Context(), "de"); err != nil {
		t.Fatal(err)
	}
	want(me(), nil, "de")

	// An own choice wins over the instance default.
	resp := put(map[string]any{"locale": "en"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT en: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	want(decodeMeLocale(t, resp), &en, "en")
	want(me(), &en, "en")

	// Clearing it falls back to the instance default, not to English.
	resp = put(map[string]any{"locale": nil})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT null: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	want(decodeMeLocale(t, resp), nil, "de")
	want(me(), nil, "de")

	// The user's own German with an English instance default.
	if err := s.Deps.Setup.SetDefaultLocale(t.Context(), "en"); err != nil {
		t.Fatal(err)
	}
	if resp := put(map[string]any{"locale": "de"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT de: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	want(me(), &de, "de")

	// An unsupported or missing value is refused and changes nothing.
	for name, body := range map[string]any{
		"unsupported": map[string]any{"locale": "fr"},
		"empty":       map[string]any{"locale": ""},
		"missing":     map[string]any{},
	} {
		if resp := put(body); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("PUT %s: status %d, want 422; body %s", name, resp.StatusCode, mustReadBody(t, resp))
		}
	}
	want(me(), &de, "de")

	// The change is an account-level event on the user's own activity trail.
	ev := findEvent(t, myActivity(t, s, c), "PUT setLocale")
	if ev.OrgID != nil {
		t.Fatalf("OrgID = %v, want nil (an own-account setting has no organization)", *ev.OrgID)
	}
	if ev.ActorID == nil || *ev.ActorID != uid.String() {
		t.Fatalf("ActorID = %v, want %s", ev.ActorID, uid)
	}
	if ev.ObjectID != uid.String() {
		t.Fatalf("ObjectID = %q, want the user id %s", ev.ObjectID, uid)
	}
}

// TestUserLocaleNeedsAnInteractiveSession: a half-authenticated session and
// an API token cannot change the account's language.
func TestUserLocaleNeedsAnInteractiveSession(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("locale-pending@example.test", "correct horse battery")
	putURL := s.HTTP.URL + "/api/v1/auth/me/locale"
	body := map[string]any{"locale": "de"}

	if resp := requestJSON(t, s.LoginAsPending(uid), http.MethodPut, putURL, body); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("PUT from an MFA-pending session: status %d, want 403", resp.StatusCode)
	}
	if resp := requestJSON(t, s.Client(), http.MethodPut, putURL, body); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("PUT without a session: status %d, want 401", resp.StatusCode)
	}

	// A bearer token carries no cookie session on this route, so it never
	// authenticates here at all; the 403 for a token principal is pinned
	// in TestInteractiveSessionRejectsTokenPrincipal.
	_, tok := s.OrgWithToken(t)
	req, err := http.NewRequest(http.MethodPut, putURL, strings.NewReader(`{"locale":"de"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("a bearer token changed the language: status %d", resp.StatusCode)
	}
}
