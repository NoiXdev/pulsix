package api

import (
	"context"
	"errors"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/accounts"
	"github.com/noixdev/pulsix/internal/auth"
)

// Refusal details of POST /auth/password the SPA matches on.
const (
	invalidCurrentPassword   = "invalid_current_password"
	temporaryPasswordExpired = "temporary_password_expired"
	samePassword             = "same_password"
)

type changePasswordInput struct {
	Body struct {
		// No minLength on either field: the floor is the service's
		// (auth.CheckPasswordPolicy), and the current password is checked,
		// not measured.
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
}

// registerPasswordRoutes wires the signed-in person changing their own
// password (backlog 7.74).
//
// The gate is requirePasswordChangeSession, deliberately not
// requireMFAComplete: this is the route that lifts a forced change, so an
// account that must change its password reaches it. It still needs an
// MFA-complete cookie session and a fresh second factor (requireFreshFactor),
// like every other account-security action. Every other session of the
// account ends; the one making the change stays.
//
// It is on the auth rate limiter, which charges only refused attempts, so a
// session cannot be used to guess the current password at speed.
func registerPasswordRoutes(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/auth/password", func(ctx context.Context, in *changePasswordInput) (*noContentOutput, error) {
		p, err := requirePasswordChangeSession(ctx)
		if err != nil {
			return nil, err
		}
		if err := requireFreshFactor(ctx, deps, p); err != nil {
			return nil, err
		}
		if err := deps.Accounts.ChangePassword(ctx, p.UserID, p.SessionID, in.Body.CurrentPassword, in.Body.NewPassword); err != nil {
			return nil, mapAccountsErr(ctx, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, p.UserID.String(), "changed their password")
		return &noContentOutput{}, nil
	}, withAudit("changePassword", "user"), withAuthRateLimit(deps))
}

// mapAccountsErr translates accounts.Service sentinels to HTTP errors.
// Anything not recognized is logged server-side and answered with a generic
// 500. No accounts error carries a password, so logging one is safe.
func mapAccountsErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, accounts.ErrInvalidCredentials):
		// Not 401: the session is fine, only the password it typed is not,
		// and the SPA treats a 401 as "signed out".
		return huma.Error400BadRequest(invalidCurrentPassword)
	case errors.Is(err, accounts.ErrTemporaryPasswordExpired):
		// Only a caller who typed the right password gets here, and they
		// are signed in: telling them it expired gives nothing away, and
		// lets the SPA send them to an administrator for a new one.
		return huma.Error400BadRequest(temporaryPasswordExpired)
	case errors.Is(err, accounts.ErrSamePassword):
		return huma.Error422UnprocessableEntity(samePassword)
	case errors.Is(err, auth.ErrInvalidInput):
		return huma.Error422UnprocessableEntity(err.Error())
	case errors.Is(err, accounts.ErrNotFound):
		return huma.Error401Unauthorized("login required")
	default:
		slog.ErrorContext(ctx, "unmapped accounts error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
