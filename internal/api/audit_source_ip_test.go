package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// TestAuditRecordsSourceIP: every audited route records the client
// address in meta.source_ip. httptest serves on loopback and no proxy is
// trusted, so the peer address is the client.
func TestAuditRecordsSourceIP(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("src-ip-owner@example.test", "correct horse battery")
	s.SeedOrg("src-ip", owner)
	c := s.LoginAs(owner)
	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs/src-ip/repos", map[string]any{"name": "infra", "url": "https://git.example.com/x"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	var ip string
	if err := s.DB.Pool().QueryRow(context.Background(),
		`SELECT meta->>'source_ip' FROM audit_events WHERE action = 'POST createRepo'`).Scan(&ip); err != nil {
		t.Fatal(err)
	}
	if ip != "127.0.0.1" && ip != "::1" {
		t.Fatalf("source_ip = %q, want the loopback peer", ip)
	}
}
