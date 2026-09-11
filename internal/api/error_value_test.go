package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// TestValidationErrorsNeverEchoTheValue pins that a 422 body carries where
// and why a request is invalid, never the offending value itself: huma puts
// it into errors[].value for schema violations such as maxLength, which
// would hand an over-long password, token or private key straight back.
func TestValidationErrorsNeverEchoTheValue(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("err-value-owner@example.test", "correct horse battery")
	s.SeedOrg("err-value", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/err-value"

	cases := []struct {
		name   string
		body   map[string]any
		secret string
	}{
		{"over-long password", map[string]any{"name": "db", "kind": "ssh_password", "username": "u", "password": strings.Repeat("pw-leak-", 200)}, "pw-leak-"},
		{"over-long token", map[string]any{"name": "git", "kind": "git_token", "token": strings.Repeat("tok-leak-", 600)}, "tok-leak-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postJSON(t, c, base+"/credentials", tc.body)
			body := mustReadBody(t, resp)
			if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `"location"`) {
				t.Fatalf("status %d: %s", resp.StatusCode, body)
			}
			if strings.Contains(body, tc.secret) {
				t.Fatalf("the 422 body echoes the secret value: %.300s", body)
			}
		})
	}
}
