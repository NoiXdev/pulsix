// An organization's view of the personal tokens granted in it, and its
// switch "Allow personal tokens" (backlog 3.100, spec rule 6).
package api

import (
	"context"
	"log/slog"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/personaltokens"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// personalTokenEntryDTO is one personal token's grant on the organization's
// token page: whose token, its cap here, whether an admin blocked it, and
// its last use here.
type personalTokenEntryDTO struct {
	TokenID    string `json:"token_id"`
	Name       string `json:"name"`
	Kind       string `json:"kind" enum:"api,assistant"`
	OwnerID    string `json:"owner_id"`
	OwnerName  string `json:"owner_name"`
	OwnerEmail string `json:"owner_email"`
	Role       string `json:"role" enum:"owner,admin,operator,viewer" doc:"The grant's cap here; the owner's membership role may lower it further"`
	Blocked    bool   `json:"blocked" doc:"An admin revoked the grant; no token of its owner gets a grant here until an admin lifts the block"`
	// TokenRevoked: the owner revoked the token. Such a grant is listed
	// only while blocked, since the block outlives the token.
	TokenRevoked bool `json:"token_revoked" doc:"The owner revoked the token; listed only while its grant here is blocked"`
	// OwnerBlocked: an admin blocked a grant of the owner here (this one or
	// another token's); every token of theirs is refused here until lifted.
	OwnerBlocked bool       `json:"owner_blocked" doc:"An admin blocked a grant of this token's owner here, so every token of theirs is refused here; lifting the block through this entry lifts it for the owner"`
	LastUsedAt   *time.Time `json:"last_used_at,omitempty" doc:"The token's last use in this organization"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

type personalTokenEntriesOutput struct {
	Body struct {
		Entries []personalTokenEntryDTO `json:"entries"`
	}
}

type personalTokenSettingsBody struct {
	Allowed bool `json:"allowed" doc:"Whether members may grant their personal tokens access to this organization"`
}

type personalTokenSettingsOutput struct{ Body personalTokenSettingsBody }

// registerPersonalTokenOrgRoutes wires the organization's side. A revoked
// grant blocks its owner in the organization (per person, for every token
// of theirs, until lifted); lifting it through any of their entries lifts
// all of them. Seeing and revoking grants needs tokens.manage, as for
// organization tokens (and,
// like revoking an organization token, a token with that right may do
// it); lifting a block and the switch need an interactive session, so no
// token can reopen access. No route here takes a cap: an admin can revoke a
// grant, never create or widen one.
func registerPersonalTokenOrgRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/personal-tokens", func(ctx context.Context, in *orgTokenListInput) (*personalTokenEntriesOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTokensManage)
		if err != nil {
			return nil, err
		}
		entries, err := deps.PersonalTokens.ListForOrg(ctx, scope.OrgID)
		if err != nil {
			return nil, mapPersonalTokenErr(ctx, err, nil)
		}
		out := &personalTokenEntriesOutput{}
		out.Body.Entries = make([]personalTokenEntryDTO, len(entries))
		for i, e := range entries {
			out.Body.Entries[i] = toPersonalTokenEntryDTO(e)
		}
		return out, nil
	})

	huma.Delete(hapi, "/api/v1/orgs/{org}/personal-tokens/{id}", func(ctx context.Context, in *orgTokenIDInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTokensManage)
		if err != nil {
			return nil, err
		}
		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid token id")
		}
		e, err := deps.PersonalTokens.RevokeGrant(ctx, scope.OrgID, id, scope.UserID)
		if err != nil {
			return nil, mapPersonalTokenErr(ctx, err, nil)
		}
		setAuditObject(ctx, id.String(), "revoked personal token "+e.Name+" ("+string(e.Role)+") of "+e.OwnerName)
		return &okOutput{}, nil
	}, withAudit("revokePersonalTokenGrant", "personal_token"))

	huma.Post(hapi, "/api/v1/orgs/{org}/personal-tokens/{id}/unblock", func(ctx context.Context, in *orgTokenIDInput) (*okOutput, error) {
		if _, err := requireInteractiveSession(ctx); err != nil {
			return nil, err
		}
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermTokensManage)
		if err != nil {
			return nil, err
		}
		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid token id")
		}
		e, err := deps.PersonalTokens.UnblockGrant(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapPersonalTokenErr(ctx, err, nil)
		}
		// The entry the admin went through may be one that was only blocked
		// through its owner; the audit says which it was.
		via := " (blocked with " + e.Name + ")"
		if !e.Blocked {
			via = " (through " + e.Name + ")"
		}
		setAuditObject(ctx, id.String(), "lifted the block on personal tokens of "+e.OwnerName+via)
		return &okOutput{}, nil
	}, withAudit("unblockPersonalTokenGrant", "personal_token"))

	huma.Get(hapi, "/api/v1/orgs/{org}/settings/personal-tokens", func(ctx context.Context, in *orgTokenListInput) (*personalTokenSettingsOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermOrgSettingsRead)
		if err != nil {
			return nil, err
		}
		return readPersonalTokenSettings(ctx, deps, scope.OrgID)
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/settings/personal-tokens", func(ctx context.Context, in *struct {
		Org  string `path:"org"`
		Body personalTokenSettingsBody
	}) (*personalTokenSettingsOutput, error) {
		if _, err := requireInteractiveSession(ctx); err != nil {
			return nil, err
		}
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermOrgSettingsWrite)
		if err != nil {
			return nil, err
		}
		if err := deps.PersonalTokens.SetAllowed(ctx, scope.OrgID, in.Body.Allowed); err != nil {
			slog.ErrorContext(ctx, "store personal token switch", "err", err)
			return nil, huma.Error500InternalServerError("internal error")
		}
		summary := "turned personal tokens off"
		if in.Body.Allowed {
			summary = "turned personal tokens on"
		}
		setAuditObject(ctx, "", summary)
		return readPersonalTokenSettings(ctx, deps, scope.OrgID)
	}, withAudit("updatePersonalTokenSettings", "org_settings"))
}

func readPersonalTokenSettings(ctx context.Context, deps Deps, orgID uuid.UUID) (*personalTokenSettingsOutput, error) {
	on, err := deps.PersonalTokens.Allowed(ctx, orgID)
	if err != nil {
		slog.ErrorContext(ctx, "read personal token switch", "err", err)
		return nil, huma.Error500InternalServerError("internal error")
	}
	return &personalTokenSettingsOutput{Body: personalTokenSettingsBody{Allowed: on}}, nil
}

func toPersonalTokenEntryDTO(e personaltokens.OrgEntry) personalTokenEntryDTO {
	return personalTokenEntryDTO{
		TokenID: e.TokenID.String(), Name: e.Name, Kind: string(e.Kind), OwnerID: e.OwnerID.String(),
		OwnerName: e.OwnerName, OwnerEmail: e.OwnerEmail, Role: string(e.Role), Blocked: e.Blocked, TokenRevoked: e.TokenRevoked, OwnerBlocked: e.OwnerBlocked,
		LastUsedAt: e.LastUsedAt, CreatedAt: e.CreatedAt, ExpiresAt: e.ExpiresAt,
	}
}
