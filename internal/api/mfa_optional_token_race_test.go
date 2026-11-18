package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// N1 of the security re-review: a token create that passed its session
// check before the owner's first enrolment committed must not insert after
// the enrolment revoked the account's tokens. The attacker's password-only
// session mints organization and personal tokens from many goroutines while
// the owner confirms TOTP; afterwards every token it got back is either
// refused at creation or revoked.
func TestMFAOptionalTokensMintedDuringTheFirstEnrolmentDoNotSurvive(t *testing.T) {
	s := apitest.New(t, apitest.WithMFAOptional())
	const email = "token-race@example.test"
	uid := s.SeedUser(email, "correct horse battery")
	s.SeedOrg("token-race", uid)

	attacker := s.Client()
	login(t, s, attacker, email)
	owner := s.Client()
	login(t, s, owner, email)
	resp := postJSON(t, owner, s.HTTP.URL+"/api/v1/auth/totp/enrol", struct{}{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("totp/enrol: %d (%s)", resp.StatusCode, mustReadBody(t, resp))
	}
	var enrol struct {
		Secret string `json:"secret"`
	}
	decodeInto(t, resp, &enrol)
	code := totpCode(t, enrol.Secret)

	mint := func(i int) (string, int, error) {
		path, body := "/api/v1/orgs/token-race/tokens", map[string]any{"name": "race", "role": "owner"}
		if i%2 == 1 {
			path, body = "/api/v1/auth/me/tokens", map[string]any{
				"name": "race", "grants": []map[string]string{{"org": "token-race", "role": "owner"}},
			}
		}
		raw, _ := json.Marshal(body)
		req, err := http.NewRequest(http.MethodPost, s.HTTP.URL+path, bytes.NewReader(raw))
		if err != nil {
			return "", 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		r, err := attacker.Do(req)
		if err != nil {
			return "", 0, err
		}
		defer func() { _ = r.Body.Close() }()
		var out struct {
			Token string `json:"token"`
		}
		if r.StatusCode == http.StatusCreated {
			if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
				return "", 0, err
			}
		}
		return out.Token, r.StatusCode, nil
	}

	const n = 12
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		minted []string
		errs   []error
	)
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tok, status, err := mint(i)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			if status == http.StatusCreated {
				minted = append(minted, tok)
			}
		}()
	}
	close(start)
	confirm := postJSON(t, owner, s.HTTP.URL+"/api/v1/auth/totp/confirm", map[string]string{"code": code})
	wg.Wait()
	if confirm.StatusCode != http.StatusOK {
		t.Fatalf("totp/confirm: %d (%s)", confirm.StatusCode, mustReadBody(t, confirm))
	}
	if len(errs) > 0 {
		t.Fatalf("mint requests failed: %v", errs)
	}

	survivors := 0
	for _, tok := range minted {
		if r := s.Get(t, tok, "/api/v1/orgs/token-race"); r.Code != http.StatusUnauthorized {
			survivors++
		}
	}
	if survivors > 0 {
		t.Fatalf("%d of %d tokens minted during the enrolment still work, want none", survivors, len(minted))
	}
	// And once the enrolment has committed, the superseded session mints
	// nothing at all.
	if _, status, err := mint(0); err != nil || status != http.StatusForbidden {
		t.Fatalf("mint after the enrolment: %d, %v; want 403", status, err)
	}
}
