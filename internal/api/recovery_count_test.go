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
