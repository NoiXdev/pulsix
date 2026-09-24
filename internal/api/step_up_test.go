package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// enrolTOTP takes the account through enrolment and confirmation, and
// returns the secret so later steps can produce codes for it.
func enrolTOTP(t *testing.T, c *http.Client, s *apitest.Server) (string, []string) {
	t.Helper()
	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/enrol", struct{}{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("totp/enrol: %d", resp.StatusCode)
	}
	var out struct {
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	confirm := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/confirm", map[string]string{"code": totpCode(t, out.Secret)})
	if confirm.StatusCode != http.StatusOK {
		t.Fatalf("totp/confirm: %d", confirm.StatusCode)
	}
	var codes struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if err := json.NewDecoder(confirm.Body).Decode(&codes); err != nil {
		t.Fatal(err)
	}
	return out.Secret, codes.RecoveryCodes
}

func totpCode(t *testing.T, secret string) string {
	t.Helper()
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// The account-security actions require a second factor proved within the
// step-up window (backlog 1.28). A session left open on an unlocked
// screen must not be enough to attach a credential to the account — that
// is an account takeover with no password and no factor needed.

// stale backdates the session's proof past the window, which is what time
// would do on its own.
func stale(t *testing.T, s *apitest.Server, userID uuid.UUID) {
	t.Helper()
	if _, err := s.DB.Pool().Exec(context.Background(),
		`UPDATE sessions SET mfa_at = now() - interval '1 hour' WHERE user_id = $1`, userID); err != nil {
		t.Fatal(err)
	}
}

func TestStepUpGuardsAccountSecurityActions(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("stepup@example.test", "correct horse battery")
	s.SeedOrg("stepup-org", uid)
	// The account already carries a factor, which is what makes the guard
	// apply at all.
	pkID := s.SeedPasskey(uid, "yubikey")
	c := s.LoginAs(uid)

	// Fresh: the actions are open.
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/begin", struct{}{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("register/begin with a fresh factor: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}

	// Stale: every one of them is refused, and refused distinguishably so
	// the interface can ask for the factor instead of showing an error.
	stale(t, s, uid)
	for name, call := range map[string]func() *http.Response{
		"passkey register begin": func() *http.Response {
			return postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/begin", struct{}{})
		},
		"passkey register finish": func() *http.Response {
			return postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/finish",
				map[string]any{"ceremony_id": uuid.Must(uuid.NewV7()).String(), "name": "n", "response": map[string]any{}})
		},
		"passkey delete": func() *http.Response {
			return requestJSON(t, c, http.MethodDelete, s.HTTP.URL+"/api/v1/auth/passkeys/"+pkID.String(), nil)
		},
		"totp enrol": func() *http.Response {
			return postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/enrol", struct{}{})
		},
		"totp disable": func() *http.Response {
			return requestJSON(t, c, http.MethodDelete, s.HTTP.URL+"/api/v1/auth/totp", map[string]string{"code": "000000"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			resp := call()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", resp.StatusCode)
			}
			if body := mustReadBody(t, resp); !strings.Contains(body, "step_up_required") {
				t.Fatalf("the refusal must be recognizable as a step-up request: %s", body)
			}
		})
	}

	// The passkey is still there: a refused delete deleted nothing.
	var passkeys int
	if err := s.DB.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM passkeys WHERE user_id = $1`, uid).Scan(&passkeys); err != nil {
		t.Fatal(err)
	}
	if passkeys != 1 {
		t.Fatalf("passkeys = %d, want the refused delete to have changed nothing", passkeys)
	}
}

// An account that has no second factor yet is exempt: its first enrolment
// has nothing to prove, and demanding a proof would lock every new account
// out of finishing its own setup.
func TestStepUpExemptsAnAccountWithoutAFactor(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("stepup-first@example.test", "correct horse battery")
	s.SeedOrg("stepup-first-org", uid)
	c := s.LoginAs(uid)
	stale(t, s, uid)

	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/begin", struct{}{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("first passkey registration: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/enrol", struct{}{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("first TOTP enrolment: %d", resp.StatusCode)
	}
}

// Proving the factor again reopens the window, through the endpoint a
// pending session already uses — no route of its own.
func TestStepUpRefreshesThroughTheVerifyEndpoint(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("stepup-refresh@example.test", "correct horse battery")
	s.SeedOrg("stepup-refresh-org", uid)
	c := s.LoginAs(uid)

	// Enrol TOTP (allowed: no factor yet) and confirm it.
	_, recovery := enrolTOTP(t, c, s)
	stale(t, s, uid)
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/begin", struct{}{}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stale session: %d, want 403", resp.StatusCode)
	}

	// A recovery code, not a fresh TOTP code: the confirmation above just
	// consumed this 30-second step, and the replay guard is right to
	// refuse the same step twice.
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/verify", map[string]string{"recovery_code": recovery[0]}); resp.StatusCode != http.StatusOK {
		t.Fatalf("re-proving the factor: %d", resp.StatusCode)
	}
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/begin", struct{}{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("after re-proving: %d, want the window to be open again", resp.StatusCode)
	}
}
