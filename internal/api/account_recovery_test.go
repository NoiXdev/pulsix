package api_test

import (
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// Backlog 1.19. Losing the authenticator is the case recovery codes exist
// for, and until now it was the one case they could not serve: DELETE
// /auth/totp verified the submitted code against the TOTP secret alone, so
// the only way to remove a factor nobody can prove any more was an
// administrator reset — which a single-admin install does not have.
//
// The account must still be left with a second factor, so the passkey is
// registered first; that is the real sequence an operator walks (step up
// with a recovery code, enrol something they still hold, then drop the
// factor they lost), not a contrivance to satisfy the last-factor guard.
func TestRecoveryCodeDisablesTOTP(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("totp-lost@example.test", "correct horse battery")
	s.SeedOrg("totp-lost-org", uid)
	c := s.LoginAs(uid)

	_, recovery := enrolTOTP(t, c, s)
	registerPasskeyForTest(t, s, c, "replacement", []byte("cred-totp-lost"))

	resp := deleteJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp",
		map[string]string{"recovery_code": recovery[0]})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disabling TOTP with a recovery code: %d, want 200 — "+
			"the authenticator is gone and this is the code that replaces it",
			resp.StatusCode)
	}
}

// The companion guard: a code that does not match must cost nothing. A
// rejected attempt that still burned one of the user's finite, today
// non-renewable codes would turn every typo into permanent progress
// towards being locked out.
func TestRejectedDisableConsumesNoRecoveryCode(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("totp-typo@example.test", "correct horse battery")
	s.SeedOrg("totp-typo-org", uid)
	c := s.LoginAs(uid)

	_, recovery := enrolTOTP(t, c, s)
	registerPasskeyForTest(t, s, c, "replacement", []byte("cred-totp-typo"))

	if resp := deleteJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp",
		map[string]string{"recovery_code": "not-a-real-recovery-code"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a wrong recovery code: %d, want 401", resp.StatusCode)
	}

	// The real code must still work afterwards — the failed attempt above
	// must not have spent anything.
	if resp := deleteJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp",
		map[string]string{"recovery_code": recovery[0]}); resp.StatusCode != http.StatusOK {
		t.Fatalf("the genuine code after a rejected attempt: %d, want 200 — "+
			"the rejected attempt consumed it", resp.StatusCode)
	}
}
