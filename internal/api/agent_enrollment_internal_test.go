package api

import (
	"testing"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// mayEnrolAgents is the agents.write a run of a template with agent
// enrollment needs (spec 2026-11-25 B, "Who may"): the role's, and for an
// assistant token also its areas', which never grant it.
func TestMayEnrolAgents(t *testing.T) {
	for name, tc := range map[string]struct {
		scope Scope
		want  bool
	}{
		"operator session": {Scope{Role: tenancy.RoleOperator}, true},
		"viewer session":   {Scope{Role: tenancy.RoleViewer}, false},
		"api token":        {Scope{Role: tenancy.RoleAdmin, TokenKind: auth.TokenKindAPI}, true},
		"assistant token": {Scope{Role: tenancy.RoleOwner, TokenKind: auth.TokenKindAssistant,
			Areas: []tenancy.Area{tenancy.AreaTemplatesWrite, tenancy.AreaJobsRequest, tenancy.AreaAlertsHandle}}, false},
		"assistant token without areas": {Scope{Role: tenancy.RoleOwner, TokenKind: auth.TokenKindAssistant}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := mayEnrolAgents(tc.scope); got != tc.want {
				t.Fatalf("mayEnrolAgents = %v, want %v", got, tc.want)
			}
		})
	}
}
