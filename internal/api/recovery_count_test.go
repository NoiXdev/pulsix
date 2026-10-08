package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// recoveryRemaining reads the one number this file is about off /auth/me.
func recoveryRemaining(t *testing.T, s *apitest.Server, c *http.Client) int {
	t.Helper()
	resp, err := c.Get(s.HTTP.URL + "/api/v1/auth/me")
	if err != nil {
		t.Fatalf("GET /auth/me: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /auth/me: %d", resp.StatusCode)
	}
	var out struct {
		RecoveryCodesRemaining int `json:"recovery_codes_remaining"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding /auth/me: %v", err)
	}
	return out.RecoveryCodesRemaining
}

// The count exists so that running out stops being a surprise. Recovery
// codes are spent one at a time, by exactly the person who has already
// lost their usual factor and is least able to absorb another obstacle;
// without a number on the account page, the set empties silently and the
// first sign of trouble is being locked out.
func TestAuthMeReportsRemainingRecoveryCodes(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("recovery-count@example.test", "correct horse battery")
	s.SeedOrg("recovery-count-org", uid)
	c := s.LoginAs(uid)

	_, recovery := enrolTOTP(t, c, s)
	issued := recoveryRemaining(t, s, c)
	if issued != len(recovery) {
		t.Fatalf("after enrolment the count is %d, want %d — the number of codes actually handed out",
			issued, len(recovery))
	}

	// Spending one must move the number. A count that reports the size of
	// the set as issued rather than as it stands would be worse than none:
	// it would read as reassurance right up to the last code.
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/verify",
		map[string]string{"recovery_code": recovery[0]}); resp.StatusCode != http.StatusOK {
		t.Fatalf("spending a recovery code: %d", resp.StatusCode)
	}
	if got := recoveryRemaining(t, s, c); got != issued-1 {
		t.Fatalf("after spending one the count is %d, want %d", got, issued-1)
	}
}

// Backlog 1.34. The codes are shown exactly once, at enrolment. If that
// response is lost on the way back — a dropped connection, a closed tab —
// they are active and unseen, and the only way out is re-enrolling TOTP.
// Regenerating closes that with no new concepts: prove the factor, get a
// fresh set, and the old set stops working in the same breath.
func TestRegenerateRecoveryCodesReplacesTheOldSet(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("regen-codes@example.test", "correct horse battery")
	s.SeedOrg("regen-codes-org", uid)
	c := s.LoginAs(uid)

	_, old := enrolTOTP(t, c, s)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/recovery-codes", struct{}{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("regenerating: %d, want 200", resp.StatusCode)
	}
	var out struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding the new set: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	if len(out.RecoveryCodes) != len(old) {
		t.Fatalf("got %d codes, want a full set of %d", len(out.RecoveryCodes), len(old))
	}

	// The old set must be dead, not merely joined by a new one. A
	// regenerate that leaves the previous codes redeemable would be worse
	// than none: the reason to regenerate is usually that the old ones
	// might be in someone else's hands.
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/verify",
		map[string]string{"recovery_code": old[0]}); resp.StatusCode == http.StatusOK {
		t.Fatal("a code from the replaced set still works")
	}
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/verify",
		map[string]string{"recovery_code": out.RecoveryCodes[0]}); resp.StatusCode != http.StatusOK {
		t.Fatalf("a code from the new set: %d, want it to work", resp.StatusCode)
	}
}

// Regenerating is an account-security action on a par with removing a
// factor, so it must sit behind the same freshness gate. A session whose
// factor proof has gone stale must be sent to step up, not handed a new
// set of codes.
func TestRegenerateRecoveryCodesRequiresAFreshFactor(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("regen-stale@example.test", "correct horse battery")
	s.SeedOrg("regen-stale-org", uid)
	c := s.LoginAs(uid)

	enrolTOTP(t, c, s)
	stale(t, s, uid)

	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/recovery-codes", struct{}{}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("regenerating on a stale session: %d, want 403", resp.StatusCode)
	}
}
