package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// TestAuditRecordsTokenID: a change made with an API token shows the
// token's name in the activity feed.
func TestAuditRecordsTokenID(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	base := "/api/v1/orgs/" + org.String()
	if resp := s.Post(t, tok, base+"/inventory/groups", map[string]any{"name": "web"}); resp.Code != http.StatusCreated && resp.Code != http.StatusOK {
		t.Fatalf("create group = %d %s", resp.Code, resp.Body)
	}
	resp := s.Get(t, tok, base+"/activity")
	if resp.Code != http.StatusOK {
		t.Fatalf("activity = %d %s", resp.Code, resp.Body)
	}
	var out struct {
		Items []struct {
			ActorKind string `json:"actor_kind"`
			TokenName string `json:"token_name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) == 0 || out.Items[0].ActorKind != "token" || out.Items[0].TokenName != "ci" {
		t.Fatalf("activity items = %+v, want the token's name", out.Items)
	}
}
