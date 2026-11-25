package api

import (
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// mayEnrolAgents is what switching agent enrollment and starting an
// enrolling run need (spec 2026-11-25 B, "Who may"; controller ruling B):
// a person signed in (no bearer token, backlog 7.49) whose role holds
// agents.write.
func TestMayEnrolAgents(t *testing.T) {
	tokenID := uuid.Must(uuid.NewV7())
	for name, tc := range map[string]struct {
		scope Scope
		want  bool
	}{
		"operator session": {Scope{Role: tenancy.RoleOperator}, true},
		"viewer session":   {Scope{Role: tenancy.RoleViewer}, false},
		"api token":        {Scope{Role: tenancy.RoleAdmin, TokenID: &tokenID, TokenKind: auth.TokenKindAPI}, false},
		"assistant token": {Scope{Role: tenancy.RoleOwner, TokenID: &tokenID, TokenKind: auth.TokenKindAssistant,
			Areas: []tenancy.Area{tenancy.AreaTemplatesWrite, tenancy.AreaJobsRequest, tenancy.AreaAlertsHandle}}, false},
		"assistant token without areas": {Scope{Role: tenancy.RoleOwner, TokenID: &tokenID, TokenKind: auth.TokenKindAssistant}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := mayEnrolAgents(tc.scope); got != tc.want {
				t.Fatalf("mayEnrolAgents = %v, want %v", got, tc.want)
			}
		})
	}
}
