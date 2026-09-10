package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/tenancy"
)

type memberDTO struct {
	UserID      string     `json:"user_id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"display_name"`
	Role        string     `json:"role"`
	DisabledAt  *time.Time `json:"disabled_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

type memberListInput struct {
	Org string `path:"org"`
}

type memberListOutput struct {
	Body struct {
		Members []memberDTO `json:"members"`
	}
}

type inviteCreateInput struct {
	Org  string `path:"org"`
	Body struct {
		Email string `json:"email" format:"email" minLength:"3"`
		// owner is deliberately excluded from the enum: tenancy.Service.Invite
		// rejects it outright (only ChangeRole, by an existing owner, can
		// create a second owner), so rejecting it here at the schema layer
		// gives a clean 422 instead of routing through the service only to
		// be mapped to a 403.
		Role string `json:"role" enum:"admin,operator,viewer"`
	}
}

type inviteCreateOutput struct {
	Body struct {
		Email     string    `json:"email"`
		Role      string    `json:"role"`
		ExpiresAt time.Time `json:"expires_at"`
	}
}

type inviteAcceptInput struct {
	Body struct {
		Token       string `json:"token" minLength:"1"`
		DisplayName string `json:"display_name" minLength:"1"`
		// minLength is a basic guard, not full password-strength policy —
		// this endpoint can create a brand-new account (see
		// tenancy.Service.AcceptInvite), so it deserves at least the same
		// floor a signup form would, unlike /auth/login's Password field
		// (which validates credentials for an account that may predate any
		// such policy).
		Password string `json:"password" minLength:"8"`
	}
}

type inviteAcceptOutput struct {
	Body struct {
		UserID string `json:"user_id"`
		OrgID  string `json:"org_id"`
	}
}

type memberRoleInput struct {
	Org    string `path:"org"`
	UserID string `path:"userId"`
	Body   struct {
		Role string `json:"role" enum:"owner,admin,operator,viewer"`
	}
}

type memberRemoveInput struct {
	Org    string `path:"org"`
	UserID string `path:"userId"`
}

type adminDeactivateInput struct {
	UserID string `path:"userId"`
}

// registerMemberRoutes wires org-scoped member and invite management:
// listing members, inviting, changing a member's role and removing a
// member. All four go through requireOrg and so are mounted on the
// bearer-enabled *huma.Group in api.go, alongside the org-scoped
// organization and token routes.
func registerMemberRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/members", func(ctx context.Context, in *memberListInput) (*memberListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermMembersManage)
		if err != nil {
			return nil, err
		}
		members, err := deps.Tenancy.ListMembers(ctx, scope.OrgID)
		if err != nil {
			return nil, mapTenancyErr(ctx, err)
		}
		out := &memberListOutput{}
		out.Body.Members = make([]memberDTO, len(members))
		for i, m := range members {
			out.Body.Members[i] = memberDTO{
				UserID: m.UserID.String(), Email: m.Email, DisplayName: m.DisplayName,
				Role: string(m.Role), DisabledAt: m.DisabledAt, CreatedAt: m.CreatedAt,
			}
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/invites", func(ctx context.Context, in *inviteCreateInput) (*inviteCreateOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermMembersManage)
		if err != nil {
			return nil, err
		}
		inv, err := deps.Tenancy.Invite(ctx, scope.OrgID, scope.UserID, in.Body.Email, tenancy.Role(in.Body.Role))
		if err != nil {
			return nil, mapTenancyErr(ctx, err)
		}
		setAuditObject(ctx, inv.ID.String(), "invited "+inv.Email+" as "+string(inv.Role))
		out := &inviteCreateOutput{}
		out.Body.Email, out.Body.Role, out.Body.ExpiresAt = inv.Email, string(inv.Role), inv.ExpiresAt
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createInvite", "invite"))

	huma.Patch(hapi, "/api/v1/orgs/{org}/members/{userId}", func(ctx context.Context, in *memberRoleInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermMembersManage)
		if err != nil {
			return nil, err
		}
		target, err := uuid.Parse(in.UserID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid user id")
		}
		if err := deps.Tenancy.ChangeRole(ctx, scope.OrgID, scope.Role, scope.UserID, target, tenancy.Role(in.Body.Role)); err != nil {
			return nil, mapTenancyErr(ctx, err)
		}
		setAuditObject(ctx, target.String(), "changed member role to "+in.Body.Role)
		return &okOutput{}, nil
	}, withAudit("changeMemberRole", "member"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/members/{userId}", func(ctx context.Context, in *memberRemoveInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermMembersManage)
		if err != nil {
			return nil, err
		}
		target, err := uuid.Parse(in.UserID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid user id")
		}
		if err := deps.Tenancy.RemoveMember(ctx, scope.OrgID, scope.Role, scope.UserID, target); err != nil {
			return nil, mapTenancyErr(ctx, err)
		}
		setAuditObject(ctx, target.String(), "removed member")
		return &okOutput{}, nil
	}, withAudit("removeMember", "member"))
}

// registerInviteAcceptRoute wires POST /invites/accept: the one route in
// this file that takes no org in its path and needs no principal at all —
// it is how an invitee (who may not have an account yet) redeems the
// invitation link they were e-mailed. It is registered on the root API in
// api.go, not the bearer-enabled group: an invite token is not a bearer
// credential, and this route must stay reachable with no Authorization
// header and no session cookie.
func registerInviteAcceptRoute(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/invites/accept", func(ctx context.Context, in *inviteAcceptInput) (*inviteAcceptOutput, error) {
		uid, orgID, err := deps.Tenancy.AcceptInvite(ctx, in.Body.Token, in.Body.DisplayName, in.Body.Password)
		if err != nil {
			return nil, mapTenancyErr(ctx, err)
		}
		out := &inviteAcceptOutput{}
		out.Body.UserID, out.Body.OrgID = uid.String(), orgID.String()
		return out, nil
	})
}

// registerAdminRoutes wires the one instance-level (not org-scoped) route
// this task adds: deactivating a user. It is registered on the root API,
// not the bearer-enabled group — see requireInstanceAdmin's doc comment for
// why a bearer token must never reach it.
func registerAdminRoutes(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/admin/users/{userId}/deactivate", func(ctx context.Context, in *adminDeactivateInput) (*okOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		target, err := uuid.Parse(in.UserID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid user id")
		}
		if err := deps.Tenancy.DeactivateUser(ctx, p.InstanceAdmin, target); err != nil {
			return nil, mapTenancyErr(ctx, err)
		}
		return &okOutput{}, nil
	})
}
