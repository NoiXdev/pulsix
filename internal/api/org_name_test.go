package api_test

import (
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// TestOrgNamesWithLineBreaksAreRejected covers M4 over HTTP: an instance
// admin can create an organization, and its name goes into the Subject
// header of every invitation, so a CR or LF in it is refused on create and
// on rename.
func TestOrgNamesWithLineBreaksAreRejected(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("crlf-owner@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, owner)
	s.SeedOrg("crlf-org", owner)
	c := s.LoginAs(owner)

	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/orgs", map[string]string{"slug": "evil", "name": "Evil\r\nBcc: attacker@evil.test"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create with CRLF in the name: status %d, want 400", resp.StatusCode)
	}
	if resp := requestJSON(t, c, http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/crlf-org", map[string]string{"name": "Acme\nX-Injected: yes"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("rename with LF in the name: status %d, want 400", resp.StatusCode)
	}
}
