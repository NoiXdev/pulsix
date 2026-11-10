package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/locale"
)

// AuthService is what the auth handlers need from the login/session
// service. *auth.Service satisfies it in production. The indirection lets
// tests substitute a fake that fails in ways the real service, backed by a
// working test database, cannot easily be made to fail on demand — for
// example Finding 1's forced-revoke-failure test.
type AuthService interface {
	Login(ctx context.Context, email, password string) (auth.LoginResult, error)
	Logout(ctx context.Context, p auth.Principal) error
	Me(ctx context.Context, p auth.Principal) (auth.User, error)
	SetLocale(ctx context.Context, userID uuid.UUID, l *string) error
}

type loginInput struct {
	Body struct {
		Email string `json:"email" format:"email" minLength:"3"`
		// No minLength here deliberately: this validates login credentials,
		// not password strength. Task 6 has no signup/password-set endpoint
		// that enforces a minimum length, so requiring one here would
		// permanently lock out any account whose real password happens to
		// be shorter than the limit, and it would swallow a wrong-password
		// test case in a 422 before the credential check ever runs.
		Password string `json:"password"`
	}
}

type loginOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      struct {
		// MFARequired: the account has a second factor, and the session
		// must verify it (POST /auth/totp/verify or a passkey) before it
		// can do anything else.
		MFARequired bool `json:"mfa_required"`
		// MFAEnrolmentRequired: the account has no second factor yet, and
		// the session can reach only the enrolment routes until it enrols
		// one. Exactly one of the two is always true — a password never
		// completes a session on its own.
		MFAEnrolmentRequired bool `json:"mfa_enrolment_required"`
		// RecoveryCodesRemaining is how many recovery codes the account can
		// still redeem — the account page shows it beside the regenerate
		// action, so that a set emptying one code at a time is visible
		// before it empties rather than after.
		RecoveryCodesRemaining int `json:"recovery_codes_remaining"`
	}
}

type meOutput struct {
	Body struct {
		User struct {
			ID          string `json:"id"`
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
		} `json:"user"`
		MFAComplete bool `json:"mfa_complete"`
		// MFAEnrolmentRequired is true for a session that is not
		// MFA-complete because its account has no second factor at all:
		// it may reach only the enrolment routes (and this one, and
		// logout) until it enrols one, which completes it. The SPA takes
		// such a session straight to enrolment; a session that is not
		// MFA-complete with this false has a factor to verify instead.
		MFAEnrolmentRequired bool `json:"mfa_enrolment_required"`
		// RecoveryCodesRemaining is how many recovery codes the account can
		// still redeem — the account page shows it beside the regenerate
		// action, so that a set emptying one code at a time is visible
		// before it empties rather than after.
		RecoveryCodesRemaining int `json:"recovery_codes_remaining"`
		// TOTPConfirmed says whether the account has a confirmed TOTP
		// factor. Combined client-side with whether GET /auth/passkeys
		// returns any credential, it tells the account security page
		// which factors exist.
		TOTPConfirmed bool `json:"totp_confirmed"`
		InstanceAdmin bool `json:"instance_admin"`
		// Locale is the user's own GUI language, null until they choose
		// one; EffectiveLocale is the language that applies: that choice,
		// else the instance default, else English (backlog 1.41).
		Locale          *string `json:"locale" enum:"en,de" nullable:"true"`
		EffectiveLocale string  `json:"effective_locale" enum:"en,de"`
		// MustChangePassword is true while the account must replace a
		// password an administrator set: such a session may reach only this
		// route, logout, second-factor verification and POST
		// /auth/password (a pending session of an account without a factor
		// enrols one first), and everything else answers 403
		// password_change_required. The SPA
		// takes it straight to the change-password screen.
		MustChangePassword bool `json:"must_change_password"`
	}
}

type logoutOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
}

func registerAuthRoutes(hapi huma.API, deps Deps) {
	// Backlog 1.20: the login endpoint is rate limited per client address —
	// the one path where guessing a password is the attack, so only failed
	// attempts charge the budget (see withAuthRateLimit).
	huma.Post(hapi, "/api/v1/auth/login", func(ctx context.Context, in *loginInput) (*loginOutput, error) {
		res, err := deps.Auth.Login(ctx, in.Body.Email, in.Body.Password)
		if err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		out := &loginOutput{}
		out.SetCookie = sessionCookie(res.Cookie, deps.Secure)
		out.Body.MFARequired = res.MFARequired
		out.Body.MFAEnrolmentRequired = res.EnrolmentRequired
		return out, nil
	}, withAuthRateLimit(deps))

	huma.Post(hapi, "/api/v1/auth/logout", func(ctx context.Context, _ *struct{}) (*logoutOutput, error) {
		p, err := requireSession(ctx)
		if err != nil {
			return nil, err
		}
		// Finding 1: propagate the revoke error instead of discarding it.
		// A client must never see the cookie reported as cleared (200 plus
		// a Set-Cookie that deletes it) unless the session was actually
		// revoked server-side — otherwise an already-exfiltrated cookie
		// silently survives a logout the user believes succeeded.
		if err := deps.Auth.Logout(ctx, p); err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		clear := sessionCookie("", deps.Secure)
		clear.MaxAge = -1
		return &logoutOutput{SetCookie: clear}, nil
	})

	huma.Get(hapi, "/api/v1/auth/me", func(ctx context.Context, _ *struct{}) (*meOutput, error) {
		p, err := requireSession(ctx)
		if err != nil {
			return nil, err
		}
		u, err := deps.Auth.Me(ctx, p)
		if err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		totpConfirmed, err := deps.TOTP.Enabled(ctx, p.UserID)
		if err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		out := &meOutput{}
		out.Body.User.ID, out.Body.User.Email, out.Body.User.DisplayName = u.ID.String(), u.Email, u.DisplayName
		out.Body.MFAComplete, out.Body.InstanceAdmin = p.MFAComplete, p.InstanceAdmin
		out.Body.MustChangePassword = p.MustChangePassword && p.TokenID == nil
		out.Body.TOTPConfirmed = totpConfirmed
		def, err := deps.Setup.DefaultLocale(ctx)
		if err != nil {
			return nil, mapSetupErr(ctx, err)
		}
		out.Body.Locale, out.Body.EffectiveLocale = u.Locale, locale.Resolve(u.Locale, def)
		remaining, err := deps.TOTP.RecoveryCodesRemaining(ctx, p.UserID)
		if err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		out.Body.RecoveryCodesRemaining = remaining
		if !p.MFAComplete {
			enrolled, err := secondFactors(deps).Enrolled(ctx, p.UserID)
			if err != nil {
				return nil, mapAuthErr(ctx, err)
			}
			out.Body.MFAEnrolmentRequired = !enrolled
		}
		return out, nil
	})
}

func sessionCookie(value string, secure bool) http.Cookie {
	return http.Cookie{Name: auth.CookieName, Value: value, Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(auth.SessionTTL.Seconds())}
}

// mapAuthErr translates service sentinels to HTTP errors. Anything not
// recognized is an internal detail (a corrupt password hash, a database
// outage, ...) that must never reach an unauthenticated client: it is
// logged server-side and replaced with a generic message.
func mapAuthErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		return huma.Error401Unauthorized("invalid credentials")
	case errors.Is(err, auth.ErrNotFound):
		return huma.Error401Unauthorized("login required")
	case errors.Is(err, auth.ErrUnauthenticated):
		// The request's session ended while it ran (backlog 7.109).
		return huma.Error401Unauthorized("login required")
	case errors.Is(err, auth.ErrInvalidInput):
		return huma.Error422UnprocessableEntity(err.Error())
	case errors.Is(err, auth.ErrInvalidTOTPCode):
		return huma.Error400BadRequest("invalid code")
	case errors.Is(err, auth.ErrTooManyFactorAttempts):
		return huma.Error429TooManyRequests("too many attempts; try again later")
	case errors.Is(err, auth.ErrLastSecondFactor):
		return huma.Error409Conflict("cannot remove the last second factor; add another one first")
	default:
		slog.ErrorContext(ctx, "unmapped auth error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
