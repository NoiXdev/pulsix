package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

func postJSON(t *testing.T, c *http.Client, url string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := c.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestLoginLogoutFlow(t *testing.T) {
	s := apitest.New(t)
	s.SeedUser("tim@example.test", "correct horse battery")
	c := s.Client()

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/login", map[string]string{"email": "tim@example.test", "password": "wrong"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: status %d", resp.StatusCode)
	}
	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/auth/login", map[string]string{"email": "tim@example.test", "password": "correct horse battery"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: status %d", resp.StatusCode)
	}
	me, err := c.Get(s.HTTP.URL + "/api/v1/auth/me")
	if err != nil || me.StatusCode != http.StatusOK {
		t.Fatalf("me: %v %d", err, me.StatusCode)
	}
	var body struct {
		User struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	_ = json.NewDecoder(me.Body).Decode(&body)
	if body.User.Email != "tim@example.test" {
		t.Fatalf("me email = %q", body.User.Email)
	}
	postJSON(t, c, s.HTTP.URL+"/api/v1/auth/logout", struct{}{})
	me, _ = c.Get(s.HTTP.URL + "/api/v1/auth/me")
	if me.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after logout: status %d", me.StatusCode)
	}
}

func TestExplorerServed(t *testing.T) {
	s := apitest.New(t)
	resp, err := http.Get(s.HTTP.URL + "/api/explorer")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("explorer: %v %d", err, resp.StatusCode)
	}
	spec, _ := http.Get(s.HTTP.URL + "/api/openapi.json")
	if spec.StatusCode != http.StatusOK {
		t.Fatalf("openapi: %d", spec.StatusCode)
	}
}
