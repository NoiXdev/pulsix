package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// A session opened complete on the password alone (PULSIX_MFA_OPTIONAL) is
// only as good as the account's lack of a factor. Once the owner enrols one,
// in any session, every other password-only session must verify it: it is
// pending and not fresh, so it can neither reach the instance nor plant a
// passkey of its own or change the password.

// assertNeedsTheFactor checks that c holds a session that must verify the
// account's second factor before it can do anything else.
func assertNeedsTheFactor(t *testing.T, s *apitest.Server, c *http.Client) {
	t.Helper()
	if got := meOf(t, s, c); got.MFAComplete || got.MFAEnrolmentRequired || got.PasswordStepUp {
		t.Fatalf("me = %+v, want a pending session with a factor to verify", got)
	}
	for name, call := range map[string]func() *http.Response{
		"GET /orgs": func() *http.Response { return requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs", nil) },
		"passkey register begin": func() *http.Response {
			return postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/begin", struct{}{})
		},
		"password reauth": func() *http.Response {
			return postJSON(t, c, s.HTTP.URL+"/api/v1/auth/password/reauth", map[string]string{"password": "correct horse battery"})
		},
		"renew by password": func() *http.Response {
			return postJSON(t, c, s.HTTP.URL+"/api/v1/auth/session/renew", map[string]string{"password": "correct horse battery"})
		},
		"password change": func() *http.Response {
			return postJSON(t, c, s.HTTP.URL+"/api/v1/auth/password", map[string]string{
				"current_password": "correct horse battery", "new_password": "an entirely new horse battery",
			})
		},
	} {
		if resp := call(); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: %d (%s), want 403", name, resp.StatusCode, mustReadBody(t, resp))
		}
	}
}

func TestMFAOptionalPasswordOnlySessionNeedsTheFactorOnceEnrolled(t *testing.T) {
	for _, kind := range []string{"totp", "passkey"} {
		t.Run(kind, func(t *testing.T) {
			s := apitest.New(t, apitest.WithMFAOptional())
			email := "superseded-" + kind + "@example.test"
			uid := s.SeedUser(email, "correct horse battery")
			s.SeedOrg("superseded-"+kind, uid)

			// The other party: signed in on the password while the account
			// had no factor.
			other := s.Client()
			login(t, s, other, email)
			if got := meOf(t, s, other); !got.MFAComplete {
				t.Fatalf("me = %+v, want a complete session before the enrolment", got)
			}

			// The owner signs in and enrols a factor in a session of their own.
			owner := s.Client()
			login(t, s, owner, email)
			var recovery []string
			if kind == "totp" {
				_, recovery = enrolTOTP(t, owner, s)
			} else {
				registerPasskeyForTest(t, s, owner, "owner key", []byte("superseded-"+uid.String()))
			}

			// The enrolling session proved the new factor: complete, and its
			// step-up is the factor now.
			if got := meOf(t, s, owner); !got.MFAComplete || got.PasswordStepUp {
				t.Fatalf("owner me = %+v, want complete with the factor as its step-up", got)
			}
			if resp := requestJSON(t, owner, http.MethodGet, s.HTTP.URL+"/api/v1/orgs", nil); resp.StatusCode != http.StatusOK {
				t.Fatalf("owner GET /orgs: %d", resp.StatusCode)
			}

			assertNeedsTheFactor(t, s, other)

			if kind != "totp" {
				return
			}
			// Verifying the factor completes it again.
			resp := postJSON(t, other, s.HTTP.URL+"/api/v1/auth/totp/verify", map[string]string{"recovery_code": recovery[0]})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("totp/verify: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
			}
			if got := meOf(t, s, other); !got.MFAComplete || got.PasswordStepUp {
				t.Fatalf("me after verifying = %+v, want complete", got)
			}
			if resp := requestJSON(t, other, http.MethodGet, s.HTTP.URL+"/api/v1/orgs", nil); resp.StatusCode != http.StatusOK {
				t.Fatalf("GET /orgs after verifying: %d", resp.StatusCode)
			}
		})
	}
}

// With the mode off nothing is password-only: a complete session stays
// complete when the account enrols a factor elsewhere.
func TestMFAOffCompleteSessionSurvivesAnEnrolment(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("off-enrol@example.test", "correct horse battery")
	s.SeedOrg("off-enrol", uid)
	first := s.LoginAs(uid)
	enrolTOTP(t, s.LoginAs(uid), s)
	if got := meOf(t, s, first); !got.MFAComplete {
		t.Fatalf("me = %+v, want the complete session to stay complete", got)
	}
	if resp := requestJSON(t, first, http.MethodGet, s.HTTP.URL+"/api/v1/orgs", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /orgs: %d", resp.StatusCode)
	}
}

// password_step_up describes a complete session's step-up: a pending one
// (opened before the mode was turned on) has none.
func TestMFAOptionalPasswordStepUpOnlyForACompleteSession(t *testing.T) {
	s := apitest.New(t, apitest.WithMFAOptional())
	uid := s.SeedUser("pending-stepup@example.test", "correct horse battery")
	if got := meOf(t, s, s.LoginAsPending(uid)); got.MFAComplete || got.PasswordStepUp {
		t.Fatalf("me = %+v, want a pending session without a password step-up", got)
	}
}

// An invitation that gives a passkey-only account (no password yet) its
// password opens a session that must verify the passkey: the mode completes
// a session on the password only for an account without a factor.
func TestMFAOptionalInviteAcceptForAnAccountWithAFactor(t *testing.T) {
	s := apitest.New(t, apitest.WithMFAOptional())
	owner := s.SeedUser("invite-factor-owner@example.test", "correct horse battery")
	s.SeedOrg("invite-factor", owner)
	invitee := uuid.Must(uuid.NewV7())
	if _, err := s.Q.CreateUser(context.Background(), gen.CreateUserParams{ID: invitee, Email: "passkey-only@example.test", DisplayName: "Passkey only"}); err != nil {
		t.Fatal(err)
	}
	s.SeedPasskey(invitee, "only key")

	resp := postJSON(t, s.LoginAs(owner), s.HTTP.URL+"/api/v1/orgs/invite-factor/invites", map[string]any{"email": "passkey-only@example.test", "role": "operator"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create invite: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	c := s.Client()
	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/invites/accept", map[string]any{
		"token": extractInviteToken(t, s), "display_name": "Passkey only", "password": "correct horse battery",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("accept: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	if got := meOf(t, s, c); got.MFAComplete || got.MFAEnrolmentRequired {
		t.Fatalf("me after accepting = %+v, want a pending session with the passkey to verify", got)
	}
	if n, _ := signInsWithoutFactor(t, s, invitee); n != 0 {
		t.Fatalf("audit = %d sign-ins without a factor, want none", n)
	}
}

// The password step-up and the renewal by password guess the same password:
// they share one budget per address, so the step-up adds no guesses.
func TestPasswordReauthSharesTheRenewalBudget(t *testing.T) {
	s := apitest.New(t, apitest.WithMFAOptional(), apitest.WithAuthRateLimit(2))
	s.SeedUser("shared-budget@example.test", "correct horse battery")
	c := s.Client()
	login(t, s, c, "shared-budget@example.test")

	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/password/reauth", map[string]string{"password": "wrong"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong step-up: %d, want 401", resp.StatusCode)
	}
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/session/renew", map[string]string{"password": "wrong"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong renewal: %d, want 401", resp.StatusCode)
	}
	for name, path := range map[string]string{"step-up": "/api/v1/auth/password/reauth", "renewal": "/api/v1/auth/session/renew"} {
		if resp := postJSON(t, c, s.HTTP.URL+path, map[string]string{"password": "correct horse battery"}); resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("%s after two failures: %d, want 429", name, resp.StatusCode)
		}
	}
}
