package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// The security review's probe (C1): with the mode on, a password-only
// session planted recovery codes beside a TOTP secret it never confirmed,
// redeemed one to clear its password-only mark, and so outlived the owner's
// passkey — and a second code completed a fresh password sign-in past that
// passkey. Recovery codes now need a confirmed secret at every step.
func TestMFAOptionalPlantedRecoveryCodesDoNotBypassAPasskey(t *testing.T) {
	s := apitest.New(t, apitest.WithMFAOptional())
	const email = "planted@example.test"
	uid := s.SeedUser(email, "correct horse battery")
	s.SeedOrg("planted", uid)

	attacker := s.Client()
	login(t, s, attacker, email)
	if resp := postJSON(t, attacker, s.HTTP.URL+"/api/v1/auth/totp/enrol", struct{}{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("totp/enrol: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	resp := postJSON(t, attacker, s.HTTP.URL+"/api/v1/auth/recovery-codes", struct{}{})
	var planted struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if resp.StatusCode == http.StatusOK {
		decodeInto(t, resp, &planted)
	}
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("recovery-codes without a confirmed TOTP secret: %d, want 409", resp.StatusCode)
	}
	for _, code := range planted.RecoveryCodes {
		if resp := postJSON(t, attacker, s.HTTP.URL+"/api/v1/auth/totp/verify", map[string]string{"recovery_code": code}); resp.StatusCode == http.StatusOK {
			t.Errorf("a planted recovery code verified the session")
			break
		}
	}

	owner := s.Client()
	login(t, s, owner, email)
	registerPasskeyForTest(t, s, owner, "owner key", []byte("planted-"+uid.String()))

	assertNeedsTheFactor(t, s, attacker)

	// A fresh password sign-in must verify the passkey; no planted code
	// stands in for it.
	later := s.Client()
	if got := login(t, s, later, email); !got.MFARequired {
		t.Fatalf("login = %+v, want the passkey to be verified", got)
	}
	for _, code := range planted.RecoveryCodes {
		if resp := postJSON(t, later, s.HTTP.URL+"/api/v1/auth/totp/verify", map[string]string{"recovery_code": code}); resp.StatusCode == http.StatusOK {
			t.Fatal("a planted recovery code completed a sign-in past the passkey")
		}
	}
}

// Default mode: recovery codes still work exactly as before for an account
// with TOTP, and are refused (409) for one without.
func TestRecoveryCodesNeedAConfirmedTOTP(t *testing.T) {
	t.Run("passkey only", func(t *testing.T) {
		s := apitest.New(t)
		uid := s.SeedUser("pk-only-codes@example.test", "correct horse battery")
		s.SeedPasskey(uid, "key")
		if resp := postJSON(t, s.LoginAs(uid), s.HTTP.URL+"/api/v1/auth/recovery-codes", struct{}{}); resp.StatusCode != http.StatusConflict {
			t.Fatalf("recovery-codes: %d (%s), want 409", resp.StatusCode, mustReadBody(t, resp))
		}
	})
	t.Run("totp", func(t *testing.T) {
		s := apitest.New(t)
		const email = "totp-codes@example.test"
		uid := s.SeedUser(email, "correct horse battery")
		c := s.LoginAs(uid)
		enrolTOTP(t, c, s)
		resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/recovery-codes", struct{}{})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("recovery-codes: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
		}
		var out struct {
			RecoveryCodes []string `json:"recovery_codes"`
		}
		decodeInto(t, resp, &out)
		if len(out.RecoveryCodes) == 0 {
			t.Fatal("no recovery codes")
		}
		pending := s.Client()
		login(t, s, pending, email)
		if resp := postJSON(t, pending, s.HTTP.URL+"/api/v1/auth/totp/verify", map[string]string{"recovery_code": out.RecoveryCodes[0]}); resp.StatusCode != http.StatusOK {
			t.Fatalf("verify with a regenerated code: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
		}
	})
}

// enrolResult is what a first-factor enrolment answers besides its cookie.
type enrolResult struct {
	TokensRevoked bool `json:"tokens_revoked"`
}

func confirmTOTPForTest(t *testing.T, s *apitest.Server, c *http.Client) enrolResult {
	t.Helper()
	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/enrol", struct{}{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("totp/enrol: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	var enrol struct {
		Secret string `json:"secret"`
	}
	decodeInto(t, resp, &enrol)
	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/confirm", map[string]string{"code": totpCode(t, enrol.Secret)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("totp/confirm: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	var out enrolResult
	decodeInto(t, resp, &out)
	return out
}

func finishPasskeyForTest(t *testing.T, s *apitest.Server, c *http.Client, credID []byte) enrolResult {
	t.Helper()
	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/begin", struct{}{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register/begin: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	var begin struct {
		CeremonyID string          `json:"ceremony_id"`
		Options    json.RawMessage `json:"options"`
	}
	decodeInto(t, resp, &begin)
	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/finish", map[string]any{
		"ceremony_id": begin.CeremonyID, "name": "key",
		"response": json.RawMessage(buildNoneAttestationResponse(t, begin.Options, "localhost", s.HTTP.URL, credID)),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register/finish: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	var out enrolResult
	decodeInto(t, resp, &out)
	return out
}

// mintPersonalToken creates a personal token acting as owner in slug.
func mintPersonalToken(t *testing.T, s *apitest.Server, c *http.Client, slug string) string {
	t.Helper()
	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/me/tokens", map[string]any{
		"name": "mine", "grants": []map[string]string{{"org": slug, "role": "owner"}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create personal token: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Token string `json:"token"`
	}
	decodeInto(t, resp, &out)
	return out.Token
}

// I-A: the API tokens a password-only sign-in minted do not survive the
// account's first second factor.
func TestMFAOptionalFirstFactorRevokesTokens(t *testing.T) {
	for _, kind := range []string{"totp", "passkey"} {
		t.Run(kind, func(t *testing.T) {
			s := apitest.New(t, apitest.WithMFAOptional())
			email := "first-factor-" + kind + "@example.test"
			slug := "first-factor-" + kind
			uid := s.SeedUser(email, "correct horse battery")
			s.SeedOrg(slug, uid)
			c := s.Client()
			login(t, s, c, email)
			tokens := []string{mintToken(t, s, c, slug), mintPersonalToken(t, s, c, slug)}
			for _, tok := range tokens {
				if r := s.Get(t, tok, "/api/v1/orgs/"+slug); r.Code != http.StatusOK {
					t.Fatalf("token before the enrolment: %d", r.Code)
				}
			}

			var got enrolResult
			if kind == "totp" {
				got = confirmTOTPForTest(t, s, c)
			} else {
				got = finishPasskeyForTest(t, s, c, []byte("first-"+uid.String()))
			}
			if !got.TokensRevoked {
				t.Fatal("tokens_revoked = false, want true on the first factor")
			}
			for _, tok := range tokens {
				if r := s.Get(t, tok, "/api/v1/orgs/"+slug); r.Code != http.StatusUnauthorized {
					t.Fatalf("token after the first factor: %d, want 401", r.Code)
				}
			}
			if n, ok, _ := auditEvents(t, s, "revokeTokensOnFirstFactor", uid, uid); n != 1 || !ok {
				t.Fatalf("audit = %d events (shape ok %v), want 1 on the account", n, ok)
			}

			// A token made after it survives a second factor.
			tok := mintToken(t, s, c, slug)
			var second enrolResult
			if kind == "totp" {
				second = finishPasskeyForTest(t, s, c, []byte("second-"+uid.String()))
			} else {
				second = confirmTOTPForTest(t, s, c)
			}
			if second.TokensRevoked {
				t.Fatal("tokens_revoked = true on a second factor")
			}
			if r := s.Get(t, tok, "/api/v1/orgs/"+slug); r.Code != http.StatusOK {
				t.Fatalf("token after a second factor: %d, want 200", r.Code)
			}
		})
	}
}

// Default mode: an enrolment never revokes tokens.
func TestMFAOffFirstFactorKeepsTokens(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("off-tokens@example.test", "correct horse battery")
	s.SeedOrg("off-tokens", uid)
	c := s.LoginAs(uid)
	tok := mintToken(t, s, c, "off-tokens")
	if got := confirmTOTPForTest(t, s, c); got.TokensRevoked {
		t.Fatal("tokens_revoked = true with the mode off")
	}
	if r := s.Get(t, tok, "/api/v1/orgs/off-tokens"); r.Code != http.StatusOK {
		t.Fatalf("token: %d, want 200", r.Code)
	}
	if n, _, _ := auditEvents(t, s, "revokeTokensOnFirstFactor", uid, uuid.Nil); n != 0 {
		t.Fatalf("audit = %d revocations, want none", n)
	}
}
