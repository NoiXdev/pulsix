package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

const preferencesPath = "/api/v1/auth/me/preferences"

type prefsBody struct {
	Density  string                         `json:"density"`
	PageSize int                            `json:"page_size"`
	Lists    map[string]map[string][]string `json:"lists"`
}

func decodePrefs(t *testing.T, raw string) prefsBody {
	t.Helper()
	var b prefsBody
	if err := json.NewDecoder(strings.NewReader(raw)).Decode(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPreferencesDefaults(t *testing.T) {
	s := apitest.New(t)
	c := s.LoginAs(s.SeedUser("prefs-default@example.test", "correct horse battery"))
	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+preferencesPath, nil)
	raw := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET: status %d, body %s", resp.StatusCode, raw)
	}
	b := decodePrefs(t, raw)
	if b.Density != "standard" || b.PageSize != 25 || b.Lists == nil || len(b.Lists) != 0 {
		t.Fatalf("defaults = %+v (%s)", b, raw)
	}
	if !strings.Contains(raw, `"lists":{}`) {
		t.Fatalf("lists must be an empty object, got %s", raw)
	}
}

func TestPutPreferencesRoundTrip(t *testing.T) {
	s := apitest.New(t)
	c := s.LoginAs(s.SeedUser("prefs-rt@example.test", "correct horse battery"))
	in := map[string]any{"density": "compact", "page_size": 50,
		"lists": map[string]any{"hosts": map[string]any{"columns": []string{"name", "zone"}}}}
	resp := requestJSON(t, c, http.MethodPut, s.HTTP.URL+preferencesPath, in)
	raw := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status %d, body %s", resp.StatusCode, raw)
	}
	for _, got := range []prefsBody{decodePrefs(t, raw), func() prefsBody {
		r := requestJSON(t, c, http.MethodGet, s.HTTP.URL+preferencesPath, nil)
		return decodePrefs(t, mustReadBody(t, r))
	}()} {
		cols := got.Lists["hosts"]["columns"]
		if got.Density != "compact" || got.PageSize != 50 || len(cols) != 2 || cols[0] != "name" || cols[1] != "zone" {
			t.Fatalf("stored = %+v", got)
		}
	}
	// Users do not see each other's preferences.
	other := s.LoginAs(s.SeedUser("prefs-other@example.test", "correct horse battery"))
	r := requestJSON(t, other, http.MethodGet, s.HTTP.URL+preferencesPath, nil)
	if b := decodePrefs(t, mustReadBody(t, r)); b.Density != "standard" {
		t.Fatalf("another user sees %+v", b)
	}
}

func TestPutPreferencesKnownRoundTrip(t *testing.T) {
	s := apitest.New(t)
	c := s.LoginAs(s.SeedUser("prefs-known@example.test", "correct horse battery"))
	in := map[string]any{"density": "standard", "page_size": 25, "lists": map[string]any{
		"hosts": map[string]any{"columns": []string{"name"}, "known": []string{"name", "zone"}},
		"runs":  map[string]any{"columns": []string{"run"}},
	}}
	resp := requestJSON(t, c, http.MethodPut, s.HTTP.URL+preferencesPath, in)
	if raw := mustReadBody(t, resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status %d, body %s", resp.StatusCode, raw)
	}
	r := requestJSON(t, c, http.MethodGet, s.HTTP.URL+preferencesPath, nil)
	got := decodePrefs(t, mustReadBody(t, r))
	if k := got.Lists["hosts"]["known"]; len(k) != 2 || k[0] != "name" || k[1] != "zone" {
		t.Fatalf("hosts known = %v", k)
	}
	if _, ok := got.Lists["runs"]["known"]; ok {
		t.Fatalf("an entry saved without known came back with one: %+v", got.Lists["runs"])
	}
	bad := map[string]any{"density": "standard", "page_size": 25, "lists": map[string]any{
		"hosts": map[string]any{"columns": []string{"name", "zone"}, "known": []string{"name"}}}}
	if resp := requestJSON(t, c, http.MethodPut, s.HTTP.URL+preferencesPath, bad); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("column outside known: status %d, want 422", resp.StatusCode)
	}
}

func TestPutPreferencesInvalidIs422(t *testing.T) {
	s := apitest.New(t)
	c := s.LoginAs(s.SeedUser("prefs-422@example.test", "correct horse battery"))
	for name, in := range map[string]map[string]any{
		"density":    {"density": "dense", "page_size": 25, "lists": map[string]any{}},
		"page size":  {"density": "standard", "page_size": 20, "lists": map[string]any{}},
		"list id":    {"density": "standard", "page_size": 25, "lists": map[string]any{"Hosts": map[string]any{"columns": []string{"name"}}}},
		"duplicates": {"density": "standard", "page_size": 25, "lists": map[string]any{"hosts": map[string]any{"columns": []string{"name", "name"}}}},
	} {
		if resp := requestJSON(t, c, http.MethodPut, s.HTTP.URL+preferencesPath, in); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422; body %s", name, resp.StatusCode, mustReadBody(t, resp))
		}
	}
	r := requestJSON(t, c, http.MethodGet, s.HTTP.URL+preferencesPath, nil)
	if b := decodePrefs(t, mustReadBody(t, r)); b.Density != "standard" || b.PageSize != 25 {
		t.Fatalf("an invalid PUT changed the stored document: %+v", b)
	}
}

func TestPreferencesRefuseLockedSession(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("prefs-locked@example.test", "correct horse battery")
	c, _ := lockedSession(t, s, uid)
	in := map[string]any{"density": "standard", "page_size": 25, "lists": map[string]any{}}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		var b any
		if method == http.MethodPut {
			b = in
		}
		resp := requestJSON(t, c, method, s.HTTP.URL+preferencesPath, b)
		if resp.StatusCode != http.StatusForbidden || problemDetail(t, resp) != sessionLocked {
			t.Errorf("%s on a locked session: status %d, want 403 %s", method, resp.StatusCode, sessionLocked)
		}
	}
}

// TestPutPreferencesIsNotAudited: a save is cosmetic and per person, and one
// per column toggle would bury the useful entries of My activity (3.105).
func TestPutPreferencesIsNotAudited(t *testing.T) {
	s := apitest.New(t)
	c := s.LoginAs(s.SeedUser("prefs-audit@example.test", "correct horse battery"))
	in := map[string]any{"density": "compact", "page_size": 100,
		"lists": map[string]any{"hosts": map[string]any{"columns": []string{"name", "zone"}}}}
	if resp := requestJSON(t, c, http.MethodPut, s.HTTP.URL+preferencesPath, in); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	for _, ev := range myActivity(t, s, c) {
		if ev.Action == "PUT setPreferences" {
			t.Fatalf("a preference save was audited: %+v", ev)
		}
	}
}
