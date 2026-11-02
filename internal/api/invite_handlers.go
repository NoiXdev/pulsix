package api

import (
	"context"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/tenancy"
)

type pendingInviteDTO struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	Role          string    `json:"role"`
	InvitedBy     string    `json:"invited_by"`
	InvitedByName string    `json:"invited_by_name"`
	ExpiresAt     time.Time `json:"expires_at"`
	CreatedAt     time.Time `json:"created_at"`
}

type inviteListOutput struct {
	Body struct {
		Invites []pendingInviteDTO `json:"invites"`
	}
}

type inviteRevokeInput struct {
	Org      string `path:"org"`
	InviteID string `path:"inviteId"`
}

// registerInviteRoutes wires the open-invitation list and its revocation
// (backlog 7.29), at the same members.manage bar as sending one: the list
// shows who is about to join, and the revoke decides that they will not.
// An invitation whose sender has since lost the right to add members is
// not open any more and is not listed (tenancy's issuer condition).
func registerInviteRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/invites", func(ctx context.Context, in *memberListInput) (*inviteListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermMembersManage)
		if err != nil {
			return nil, err
		}
		invites, err := deps.Tenancy.PendingInvites(ctx, scope.OrgID)
		if err != nil {
			return nil, mapTenancyErr(ctx, err)
		}
		out := &inviteListOutput{}
		out.Body.Invites = make([]pendingInviteDTO, len(invites))
		for i, inv := range invites {
			out.Body.Invites[i] = pendingInviteDTO{
				ID: inv.ID.String(), Email: inv.Email, Role: string(inv.Role),
				InvitedBy: inv.InvitedBy.String(), InvitedByName: inv.InvitedByName,
				ExpiresAt: inv.ExpiresAt, CreatedAt: inv.CreatedAt,
			}
		}
		return out, nil
	})

	huma.Delete(hapi, "/api/v1/orgs/{org}/invites/{inviteId}", func(ctx context.Context, in *inviteRevokeInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermMembersManage)
		if err != nil {
			return nil, err
		}
		id, err := uuid.Parse(in.InviteID)
		if err != nil {
			return nil, huma.Error404NotFound("not found")
		}
		if err := deps.Tenancy.RevokeInvite(ctx, scope.OrgID, id); err != nil {
			return nil, mapTenancyErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "revoked invitation")
		return &okOutput{}, nil
	}, withAudit("revokeInvite", "invite"))
}
