package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/auth"
)

const lockBase = "/api/v1/auth/lock"

func putPIN(t *testing.T, s *apitest.Server, c *http.Client, pin string) *http.Response {
	t.Helper()
	return requestJSON(t, c, http.MethodPut, s.HTTP.URL+lockBase+"/pin", map[string]string{"pin": pin})
}

func unlock(t *testing.T, s *apitest.Server, c *http.Client, pin string) *http.Response {
	t.Helper()
	return postJSON(t, c, s.HTTP.URL+lockBase+"/unlock", map[string]string{"pin": pin})
}

// lockableSession is a complete session whose account has a PIN, not yet locked.
func lockableSession(t *testing.T, s *apitest.Server, email, pin string) (*http.Client, uuid.UUID) {
	t.Helper()
	uid := s.SeedUser(email, "correct horse battery")
	s.SeedPasskey(uid, "key")
	c := s.LoginAs(uid)
	if resp := putPIN(t, s, c, pin); resp.StatusCode != http.StatusOK {
		t.Fatalf("set PIN: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	return c, uid
}

func TestFirstPINNeedsNoStepUpButReplacingDoes(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("pin-stepup@example.test", "correct horse battery")
	s.SeedPasskey(uid, "key")
	c := s.LoginAs(uid)
	stale(t, s, uid)

	if resp := putPIN(t, s, c, "1234"); resp.StatusCode != http.StatusOK {
		t.Fatalf("first PIN on a stale session: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp := putPIN(t, s, c, "4321")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("replacing the PIN on a stale session: %d, want 403", resp.StatusCode)
	}
	if d := problemDetail(t, resp); d != "step_up_required" {
		t.Fatalf("detail %q, want step_up_required", d)
	}
	resp = requestJSON(t, c, http.MethodDelete, s.HTTP.URL+lockBase+"/pin", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("removing the PIN on a stale session: %d, want 403", resp.StatusCode)
	}
}

func TestPINMustBeFourToEightDigits(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("pin-shape@example.test", "correct horse battery")
	s.SeedPasskey(uid, "key")
	c := s.LoginAs(uid)
	for _, pin := range []string{"123", "123456789", "12a4", ""} {
		if resp := putPIN(t, s, c, pin); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("PIN %q: %d, want 422", pin, resp.StatusCode)
		}
	}
}

func TestSetRemoveAndSkipPIN(t *testing.T) {
	s := apitest.New(t)
	c, _ := lockableSession(t, s, "pin-cycle@example.test", "1234")
	if resp := requestJSON(t, c, http.MethodDelete, s.HTTP.URL+lockBase+"/pin", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("remove: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := postJSON(t, c, s.HTTP.URL+lockBase+"/prompt-skipped", struct{}{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("prompt-skipped: %d", resp.StatusCode)
	}
	resp := postJSON(t, c, s.HTTP.URL+lockBase, struct{}{})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("lock without PIN: %d, want 409", resp.StatusCode)
	}
}

func TestLockWithoutPINIs409(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("lock-nopin@example.test", "correct horse battery")
	s.SeedPasskey(uid, "key")
	c := s.LoginAs(uid)
	resp := postJSON(t, c, s.HTTP.URL+lockBase, struct{}{})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status %d, want 409", resp.StatusCode)
	}
	if d := problemDetail(t, resp); d != "lock_pin_missing" {
		t.Fatalf("detail %q, want lock_pin_missing", d)
	}
}

func TestLockThenUnlock(t *testing.T) {
	s := apitest.New(t)
	c, _ := lockableSession(t, s, "lock-unlock@example.test", "1234")
	if resp := postJSON(t, c, s.HTTP.URL+lockBase, struct{}{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("lock: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs", nil)
	if resp.StatusCode != http.StatusForbidden || problemDetail(t, resp) != sessionLocked {
		t.Fatalf("GET /orgs while locked: %d, want 403 session_locked", resp.StatusCode)
	}
	if resp := unlock(t, s, c, "1234"); resp.StatusCode != http.StatusOK {
		t.Fatalf("unlock: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /orgs after unlock: %d", resp.StatusCode)
	}
	if resp := unlock(t, s, c, "1234"); resp.StatusCode != http.StatusConflict {
		t.Fatalf("unlock when not locked: %d, want 409", resp.StatusCode)
	}
}

func TestUnlockWrongPINReportsAttemptsLeft(t *testing.T) {
	s := apitest.New(t)
	c, _ := lockableSession(t, s, "unlock-wrong@example.test", "1234")
	postJSON(t, c, s.HTTP.URL+lockBase, struct{}{})
	resp := unlock(t, s, c, "9999")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", resp.StatusCode)
	}
	var body struct {
		Detail       string `json:"detail"`
		AttemptsLeft int    `json:"attempts_left"`
	}
	if err := json.Unmarshal([]byte(mustReadBody(t, resp)), &body); err != nil {
		t.Fatal(err)
	}
	if body.Detail != "wrong pin" || body.AttemptsLeft != 4 {
		t.Fatalf("body %+v, want detail %q and attempts_left 4", body, "wrong pin")
	}
}

func TestFifthWrongPINEndsSession(t *testing.T) {
	s := apitest.New(t)
	c, _ := lockableSession(t, s, "unlock-fifth@example.test", "1234")
	postJSON(t, c, s.HTTP.URL+lockBase, struct{}{})
	for i := 0; i < 4; i++ {
		if resp := unlock(t, s, c, "9999"); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("attempt %d: %d, want 422", i+1, resp.StatusCode)
		}
	}
	resp := unlock(t, s, c, "9999")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("fifth attempt: %d, want 401", resp.StatusCode)
	}
	cleared := false
	for _, ck := range resp.Cookies() {
		if ck.Name == auth.CookieName && ck.MaxAge < 0 && ck.Value == "" {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("the session cookie was not cleared: %v", resp.Header["Set-Cookie"])
	}
	// The SPA tells this 401 from "login required" by its detail
	// (web/src/features/lock/LockScreen.tsx), so the string is contract.
	if d := problemDetail(t, resp); d != "too many wrong pins" {
		t.Fatalf("fifth attempt detail %q, want %q", d, "too many wrong pins")
	}
	if resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/auth/me", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/auth/me after the fifth failure: %d, want 401", resp.StatusCode)
	}
	// A session that is simply gone answers the ordinary 401, which the SPA
	// must not take for the wrong-PIN end.
	if resp := unlock(t, s, c, "1234"); resp.StatusCode != http.StatusUnauthorized || problemDetail(t, resp) != "login required" {
		t.Fatalf("unlock of an ended session: %d, want 401 login required", resp.StatusCode)
	}
}

func TestUnlockFailuresAreAudited(t *testing.T) {
	s := apitest.New(t)
	c, uid := lockableSession(t, s, "unlock-audit@example.test", "5731")
	postJSON(t, c, s.HTTP.URL+lockBase, struct{}{})
	const wrong = "9086"
	for i := 0; i < 5; i++ {
		unlock(t, s, c, wrong)
	}

	rows, err := s.DB.Pool().Query(t.Context(),
		`SELECT action, object_type, object_id, summary, meta::text, org_id IS NULL, actor_id
		   FROM audit_events WHERE actor_id = $1 AND action LIKE 'unlockSession%' ORDER BY at, id`, uid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var action, objType, objID, summary, meta string
		var noOrg bool
		var actor uuid.UUID
		if err := rows.Scan(&action, &objType, &objID, &summary, &meta, &noOrg, &actor); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, action)
		if !noOrg {
			t.Errorf("%s carries an organization", action)
		}
		for _, pin := range []string{wrong, "5731"} {
			if strings.Contains(action+objType+summary+meta, pin) {
				t.Errorf("%s leaks a PIN: %q %q", action, summary, meta)
			}
		}
		if action == "unlockSessionFailed" && !strings.HasPrefix(summary, "wrong PIN, ") {
			t.Errorf("summary %q", summary)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	failed, exhausted := 0, 0
	for _, a := range actions {
		switch a {
		case "unlockSessionFailed":
			failed++
		case "unlockSessionExhausted":
			exhausted++
		}
	}
	if failed != 4 || exhausted != 1 {
		t.Fatalf("events %v: want 4 unlockSessionFailed and 1 unlockSessionExhausted", actions)
	}
}

func TestLockAndPINChangesAreAudited(t *testing.T) {
	s := apitest.New(t)
	c, _ := lockableSession(t, s, "lock-audit@example.test", "1234")
	postJSON(t, c, s.HTTP.URL+lockBase, struct{}{})
	unlock(t, s, c, "1234")
	postJSON(t, c, s.HTTP.URL+lockBase+"/prompt-skipped", struct{}{})
	requestJSON(t, c, http.MethodDelete, s.HTTP.URL+lockBase+"/pin", nil)
	events := myActivity(t, s, c)
	for _, a := range []string{"PUT setLockPin", "POST lockSession", "POST unlockSession", "POST skipLockPinPrompt", "DELETE removeLockPin"} {
		findEvent(t, events, a)
	}
}
