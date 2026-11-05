package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/accounts"
	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/auth"
)

// invalidOrExpiredLink is the refusal detail of a reset with a token that
// is unknown, used, expired or belongs to a deactivated account. The SPA
// matches on it; it does not say which.
const invalidOrExpiredLink = "invalid_or_expired_link"

type resetAvailableOutput struct {
	Body struct {
		// Available tells the login page whether "Forgot password?" can
		// send a link (outbound mail is configured) or must send the
		// person to an administrator.
		Available bool `json:"available"`
	}
}

type forgotPasswordInput struct {
	Body struct {
		Email string `json:"email"`
	}
}

// forgotPasswordOutput is the one answer the forgot route gives: 202 and an
// empty object, whatever the address.
type forgotPasswordOutput struct {
	Body struct{}
}

type resetPasswordInput struct {
	Body struct {
		// Token comes from the link's fragment, which the browser never
		// sends to the server: the SPA reads it and posts it here, in the
		// body, which no request log records.
		Token string `json:"token"`
		// No minLength: the floor is the service's
		// (auth.CheckPasswordPolicy).
		NewPassword string `json:"new_password"`
	}
}

// registerPasswordResetRoutes wires the forgotten-password flow (backlog
// 7.74). All three routes are public: they take no session, and a session
// cookie the browser happens to send changes nothing.
//
//   - GET /auth/password/reset-available tells the login page whether
//     self-service reset works (outbound mail is configured).
//   - POST /auth/password/forgot answers 202 {} to every request, so it
//     never tells whether an address has an account or what state it is
//     in; the accounts service decides silently whether a link goes out.
//     Every request counts against its own per-address limiter
//     (withForgotRateLimit).
//   - POST /auth/password/reset sets the new password with the token. A bad
//     token is a 4xx, so the auth limiter (which charges refused attempts)
//     bounds guessing. It never signs anyone in and sets no cookie: the
//     person signs in afterwards with the new password and their second
//     factor. Audited, on success only, as the account's own action.
func registerPasswordResetRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/auth/password/reset-available", func(ctx context.Context, _ *struct{}) (*resetAvailableOutput, error) {
		ok, err := deps.Accounts.ResetAvailable(ctx)
		if err != nil {
			slog.ErrorContext(ctx, "check outgoing mail", "err", err)
			return nil, huma.Error500InternalServerError("internal error")
		}
		out := &resetAvailableOutput{}
		out.Body.Available = ok
		return out, nil
	}, func(o *huma.Operation) { o.OperationID = "getPasswordResetAvailable" })

	huma.Post(hapi, "/api/v1/auth/password/forgot", func(ctx context.Context, in *forgotPasswordInput) (*forgotPasswordOutput, error) {
		// Past the mail check, which is the same for every address, the
		// whole request runs in the background (accounts.RequestReset), so
		// neither the answer nor its timing depends on the address. A
		// failure of that check still answers 202, logged.
		if err := deps.Accounts.RequestReset(ctx, in.Body.Email); err != nil {
			slog.ErrorContext(ctx, "forgot password", "err", err)
		}
		return &forgotPasswordOutput{}, nil
	}, func(o *huma.Operation) {
		o.OperationID = "requestPasswordReset"
		o.DefaultStatus = http.StatusAccepted
	}, withForgotRateLimit(deps))

	huma.Post(hapi, "/api/v1/auth/password/reset", func(ctx context.Context, in *resetPasswordInput) (*noContentOutput, error) {
		userID, err := deps.Accounts.ResetPassword(ctx, in.Body.Token, in.Body.NewPassword)
		if err != nil {
			return nil, mapResetErr(ctx, err)
		}
		// No principal here: the token is the credential. The kind is named
		// rather than read from a session cookie the browser may still
		// carry (see setAuditScopeAs).
		setAuditScopeAs(ctx, nil, userID, audit.ActorKindUser)
		setAuditObject(ctx, userID.String(), "reset their password with a link")
		return &noContentOutput{}, nil
	}, withAudit("resetPassword", "user"), withAuthRateLimit(deps))
}

// mapResetErr translates the errors of a reset to HTTP. Anything not
// recognized is logged server-side and answered with a generic 500; no
// accounts error carries a token or a password.
func mapResetErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, accounts.ErrInvalidResetToken):
		return huma.Error400BadRequest(invalidOrExpiredLink)
	case errors.Is(err, auth.ErrPasswordTooShort):
		return huma.Error422UnprocessableEntity(passwordTooShort)
	case errors.Is(err, auth.ErrInvalidInput):
		return huma.Error422UnprocessableEntity(err.Error())
	default:
		slog.ErrorContext(ctx, "unmapped reset error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
