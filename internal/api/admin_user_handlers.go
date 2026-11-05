package api

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/accounts"
	"github.com/noixdev/pulsix/internal/auth"
)

// Refusal details of the account actions the SPA matches on.
const (
	notOnOwnAccount   = "not_on_own_account"
	invitePending     = "invite_pending"
	mailNotConfigured = "mail_not_configured"
	lastInstanceAdmin = "last_instance_admin"
)

// AdminUserDTO is one account on the Users page. It is exported because
// adminUserDetailDTO embeds it: huma's schema builder skips an embedded
// field of an unexported type, so the detail's OpenAPI schema would lose
// these fields although the JSON carries them (see AlertDTO).
type AdminUserDTO struct {
	ID            string     `json:"id"`
	Email         string     `json:"email"`
	DisplayName   string     `json:"display_name"`
	InstanceAdmin bool       `json:"instance_admin"`
	State         string     `json:"state" enum:"active,deactivated,invite_pending,must_change_password,no_second_factor"`
	OrgCount      int        `json:"org_count"`
	LastSignInAt  *time.Time `json:"last_sign_in_at" nullable:"true"`
}

type adminMembershipDTO struct {
	OrgID string `json:"org_id"`
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Role  string `json:"role" enum:"owner,admin,operator,viewer"`
	// ActorIsMember tells whether the administrator belongs to the
	// organization too, and so can open its member page.
	ActorIsMember bool `json:"actor_is_member"`
}

type adminFactorsDTO struct {
	TOTP              bool `json:"totp"`
	Passkeys          int  `json:"passkeys"`
	RecoveryCodesLeft int  `json:"recovery_codes_left"`
}

type adminUserDetailDTO struct {
	AdminUserDTO
	CreatedAt             time.Time            `json:"created_at"`
	DisabledAt            *time.Time           `json:"disabled_at" nullable:"true"`
	MustChangePassword    bool                 `json:"must_change_password"`
	TempPasswordExpiresAt *time.Time           `json:"temp_password_expires_at" nullable:"true"`
	Memberships           []adminMembershipDTO `json:"memberships"`
	Factors               adminFactorsDTO      `json:"factors"`
	// ActiveSessions counts the signed-in sessions: complete, unrevoked
	// and unexpired.
	ActiveSessions int `json:"active_sessions"`
}

type adminUserListInput struct {
	Q      string `query:"q"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit" minimum:"1" maximum:"200" default:"50"`
}

type adminUserListOutput struct {
	Body struct {
		Users      []AdminUserDTO `json:"users"`
		NextCursor string         `json:"next_cursor"`
	}
}

type adminUserInput struct {
	UserID string `path:"userId"`
}

type adminUserOutput struct {
	Body struct {
		User adminUserDetailDTO `json:"user"`
	}
}

type temporaryPasswordOutput struct {
	// The password is in this response and nowhere else: no cache may keep
	// it.
	CacheControl string `header:"Cache-Control"`
	Body         struct {
		Password  string    `json:"password"`
		ExpiresAt time.Time `json:"expires_at"`
		Notice    string    `json:"notice" enum:"sent,not_configured,failed,skipped"`
	}
}

// noticeOutput reports whether the person was told by mail. The action
// happened either way; skipped is an account that was not in use
// (deactivated, or its invitation never accepted), whose person is not
// mailed.
type noticeOutput struct {
	Body struct {
		Notice string `json:"notice" enum:"sent,not_configured,failed,skipped"`
	}
}

// registerAdminUserRoutes wires the instance admin's Users page and account
// page (backlog 7.74): every account on the instance, one account, and the
// actions on it. Deactivation is registered with the other admin route in
// registerAdminRoutes (member_handlers.go).
//
// All of them are on the root API and gated by requireInstanceAdmin, so a
// bearer token never reaches them, and so are a pending session and one
// that must change its password (requireMFAComplete). The actions also need
// a fresh second factor (requireFreshFactor), refuse the admin's own
// account (409 not_on_own_account) and, all but reactivation, an account
// whose invitation is still pending (409 invite_pending), and are audited
// with no organization, the admin as actor and the account as object.
func registerAdminUserRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/admin/users", func(ctx context.Context, in *adminUserListInput) (*adminUserListOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		users, next, err := deps.Accounts.ListUsers(ctx, p.InstanceAdmin, in.Q, in.Cursor, in.Limit)
		if err != nil {
			return nil, mapAdminAccountsErr(ctx, err)
		}
		out := &adminUserListOutput{}
		out.Body.Users = make([]AdminUserDTO, len(users))
		for i, u := range users {
			out.Body.Users[i] = adminUserDTOOf(u)
		}
		out.Body.NextCursor = next
		return out, nil
	}, func(o *huma.Operation) { o.OperationID = "listAdminUsers" })

	huma.Get(hapi, "/api/v1/admin/users/{userId}", func(ctx context.Context, in *adminUserInput) (*adminUserOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		target, err := parseUserID(in.UserID)
		if err != nil {
			return nil, err
		}
		u, err := deps.Accounts.GetUser(ctx, p.InstanceAdmin, p.UserID, target)
		if err != nil {
			return nil, mapAdminAccountsErr(ctx, err)
		}
		out := &adminUserOutput{}
		out.Body.User = adminUserDetailDTOOf(u)
		return out, nil
	}, func(o *huma.Operation) { o.OperationID = "getAdminUser" })

	huma.Post(hapi, "/api/v1/admin/users/{userId}/temporary-password", func(ctx context.Context, in *adminUserInput) (*temporaryPasswordOutput, error) {
		p, target, err := adminAction(ctx, deps, in.UserID)
		if err != nil {
			return nil, err
		}
		tmp, notice, err := deps.Accounts.SetTemporaryPassword(ctx, p.InstanceAdmin, p.UserID, target)
		if err != nil {
			return nil, mapAdminAccountsErr(ctx, err)
		}
		auditAdminAction(ctx, p.UserID, target, "set a temporary password")
		out := &temporaryPasswordOutput{CacheControl: "no-store"}
		out.Body.Password, out.Body.ExpiresAt, out.Body.Notice = tmp.Password, tmp.ExpiresAt, string(notice)
		return out, nil
	}, withAudit("setTemporaryPassword", "user"))

	huma.Post(hapi, "/api/v1/admin/users/{userId}/second-factor/reset", func(ctx context.Context, in *adminUserInput) (*noticeOutput, error) {
		p, target, err := adminAction(ctx, deps, in.UserID)
		if err != nil {
			return nil, err
		}
		notice, err := deps.Accounts.ResetSecondFactor(ctx, p.InstanceAdmin, p.UserID, target)
		if err != nil {
			return nil, mapAdminAccountsErr(ctx, err)
		}
		auditAdminAction(ctx, p.UserID, target, "reset the second factor")
		out := &noticeOutput{}
		out.Body.Notice = string(notice)
		return out, nil
	}, withAudit("resetSecondFactor", "user"))

	huma.Post(hapi, "/api/v1/admin/users/{userId}/sessions/revoke", func(ctx context.Context, in *adminUserInput) (*noContentOutput, error) {
		p, target, err := adminAction(ctx, deps, in.UserID)
		if err != nil {
			return nil, err
		}
		if err := deps.Accounts.EndSessions(ctx, p.InstanceAdmin, p.UserID, target); err != nil {
			return nil, mapAdminAccountsErr(ctx, err)
		}
		auditAdminAction(ctx, p.UserID, target, "ended all sessions")
		return &noContentOutput{}, nil
	}, withAudit("revokeUserSessions", "user"))

	huma.Post(hapi, "/api/v1/admin/users/{userId}/reactivate", func(ctx context.Context, in *adminUserInput) (*noContentOutput, error) {
		p, target, err := adminAction(ctx, deps, in.UserID)
		if err != nil {
			return nil, err
		}
		if err := deps.Accounts.Reactivate(ctx, p.InstanceAdmin, p.UserID, target); err != nil {
			return nil, mapAdminAccountsErr(ctx, err)
		}
		auditAdminAction(ctx, p.UserID, target, "reactivated user account")
		return &noContentOutput{}, nil
	}, withAudit("reactivateUser", "user"))

	// The link goes to the account's own address and nowhere else: not in
	// the response, the log or the audit entry. Without outbound mail there
	// is no link (409 mail_not_configured); otherwise the answer is how the
	// mail went (notice), and so is the audit summary — a deactivated
	// account gets no link at all (skipped).
	huma.Post(hapi, "/api/v1/admin/users/{userId}/password-reset-link", func(ctx context.Context, in *adminUserInput) (*noticeOutput, error) {
		p, target, err := adminAction(ctx, deps, in.UserID)
		if err != nil {
			return nil, err
		}
		notice, err := deps.Accounts.SendResetLink(ctx, p.InstanceAdmin, p.UserID, target)
		if err != nil {
			return nil, mapAdminAccountsErr(ctx, err)
		}
		auditAdminAction(ctx, p.UserID, target, resetLinkSummary(notice))
		out := &noticeOutput{}
		out.Body.Notice = string(notice)
		return out, nil
	}, withAudit("sendPasswordResetLink", "user"))
}

// resetLinkSummary is the audit summary of a reset link request: what
// actually happened, so the trail never claims a link reached someone it
// did not.
func resetLinkSummary(n accounts.Notice) string {
	switch n {
	case accounts.NoticeSent:
		return "sent a password reset link"
	case accounts.NoticeSkipped:
		return "requested a password reset link; none was issued, the account is deactivated"
	default:
		return "issued a password reset link; the mail was not sent (" + string(n) + ")"
	}
}

// adminAction is the gate of every account action: an instance admin's
// cookie session with a fresh second factor, and a well-formed target id.
// Refusing the admin's own account is the service's.
func adminAction(ctx context.Context, deps Deps, userID string) (auth.Principal, uuid.UUID, error) {
	p, err := requireInstanceAdmin(ctx)
	if err != nil {
		return p, uuid.Nil, err
	}
	if err := requireFreshFactor(ctx, deps, p); err != nil {
		return p, uuid.Nil, err
	}
	target, err := parseUserID(userID)
	return p, target, err
}

func parseUserID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, huma.Error400BadRequest("invalid user id")
	}
	return id, nil
}

// auditAdminAction records an account action at the instance level: no
// organization, the admin as actor, the account as object. The summary
// names the action only; nothing secret goes into it.
func auditAdminAction(ctx context.Context, admin, target uuid.UUID, summary string) {
	setAuditScope(ctx, nil, admin)
	setAuditObject(ctx, target.String(), summary)
}

func adminUserDTOOf(u accounts.UserSummary) AdminUserDTO {
	return AdminUserDTO{
		ID: u.ID.String(), Email: u.Email, DisplayName: u.DisplayName, InstanceAdmin: u.InstanceAdmin,
		State: string(u.State), OrgCount: u.OrgCount, LastSignInAt: u.LastSignInAt,
	}
}

func adminUserDetailDTOOf(u accounts.UserDetail) adminUserDetailDTO {
	out := adminUserDetailDTO{
		AdminUserDTO:          adminUserDTOOf(u.UserSummary),
		CreatedAt:             u.CreatedAt,
		DisabledAt:            u.DisabledAt,
		MustChangePassword:    u.MustChangePassword,
		TempPasswordExpiresAt: u.TempPasswordExpiresAt,
		Memberships:           make([]adminMembershipDTO, len(u.Memberships)),
		Factors:               adminFactorsDTO{TOTP: u.Factors.TOTP, Passkeys: u.Factors.Passkeys, RecoveryCodesLeft: u.Factors.RecoveryCodesLeft},
		ActiveSessions:        u.ActiveSessions,
	}
	for i, m := range u.Memberships {
		out.Memberships[i] = adminMembershipDTO{OrgID: m.OrgID.String(), Slug: m.Slug, Name: m.Name, Role: m.Role, ActorIsMember: m.ActorIsMember}
	}
	return out
}

// mapAdminAccountsErr translates the accounts.Service sentinels of the
// admin routes to HTTP errors. Unlike mapAccountsErr (the caller's own
// password), an unknown account here is the target, not the caller: 404.
// Anything not recognized is logged server-side and answered with a generic
// 500; no accounts error carries a password.
func mapAdminAccountsErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, accounts.ErrForbidden):
		return huma.Error403Forbidden("instance admin required")
	case errors.Is(err, accounts.ErrNotOnOwnAccount):
		return huma.Error409Conflict(notOnOwnAccount)
	case errors.Is(err, accounts.ErrInvitePending):
		return huma.Error409Conflict(invitePending)
	case errors.Is(err, accounts.ErrMailNotConfigured):
		return huma.Error409Conflict(mailNotConfigured)
	case errors.Is(err, accounts.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, accounts.ErrInvalidCursor):
		return huma.Error400BadRequest("invalid cursor")
	default:
		slog.ErrorContext(ctx, "unmapped accounts error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
