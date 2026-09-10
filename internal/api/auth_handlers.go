package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/auth"
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
		MFARequired bool `json:"mfa_required"`
	}
}

type meOutput struct {
	Body struct {
		User struct {
			ID          string `json:"id"`
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
		} `json:"user"`
		MFAComplete   bool `json:"mfa_complete"`
		InstanceAdmin bool `json:"instance_admin"`
	}
}

type logoutOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
}

func registerAuthRoutes(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/auth/login", func(ctx context.Context, in *loginInput) (*loginOutput, error) {
		res, err := deps.Auth.Login(ctx, in.Body.Email, in.Body.Password)
		if err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		out := &loginOutput{}
		out.SetCookie = sessionCookie(res.Cookie, deps.Secure)
		out.Body.MFARequired = res.MFARequired
		return out, nil
	})

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
		out := &meOutput{}
		out.Body.User.ID, out.Body.User.Email, out.Body.User.DisplayName = u.ID.String(), u.Email, u.DisplayName
		out.Body.MFAComplete, out.Body.InstanceAdmin = p.MFAComplete, p.InstanceAdmin
		return out, nil
	})
}

func sessionCookie(value string, secure bool) http.Cookie {
	return http.Cookie{Name: auth.CookieName, Value: value, Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(auth.SessionTTL.Seconds())}
}

// requireSession returns the principal or a 401. It is the right check for
// /auth/logout, /auth/me and /auth/totp/verify: routes that a
// half-authenticated (MFA-pending) session must still be able to reach,
// since the SPA needs /me to discover that a second factor is pending, and
// needs logout/verify to still work before that factor is supplied.
//
// The passkey login endpoints (registerPasskeyRoutes in
// passkey_handlers.go) use no gate at all, not this one: they are how an
// unauthenticated caller signs in in the first place, so there is no
// session yet for requireSession to check.
func requireSession(ctx context.Context) (auth.Principal, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return p, huma.Error401Unauthorized("login required")
	}
	return p, nil
}

// requireMFAComplete is the check for everything else: it additionally
// rejects a session that exists but has not completed a required second
// factor. Today NoMFA{} makes every session MFA-complete so this behaves
// like requireSession, but from Task 7 login can create sessions with
// mfa_complete = false, and any route that isn't in requireSession's
// exception list above must use this instead or it will silently accept a
// half-authenticated caller.
func requireMFAComplete(ctx context.Context) (auth.Principal, error) {
	p, err := requireSession(ctx)
	if err != nil {
		return p, err
	}
	if !p.MFAComplete {
		return p, huma.Error403Forbidden("second factor required")
	}
	return p, nil
}

// requireInteractiveSession is requireMFAComplete plus one more condition:
// the principal must not have come from an API token (p.TokenID == nil).
// It is the gate for every route that manages the account's own
// credentials — passkey register/list/delete and TOTP enrol/confirm/delete
// — because an API token is, by construction, always MFA-complete
// (Tokens.Resolve: "a token is not a browser session with its own
// second-factor step-up"), so requireMFAComplete alone lets a token —
// however narrowly scoped, e.g. a viewer-scoped token minted for read-only
// automation — register an attacker-controlled passkey or plant a TOTP
// secret on the token owner's account and take it over completely
// (Finding 1, fix round 1). These routes carry no organization in their
// path, so Task 10's org-scoped authorization can never close this gap:
// account-level credential management must reject a token principal
// outright, regardless of its scope or role.
//
// The rejection is 403, the same status requireMFAComplete already uses
// for "second factor required": the credential is genuine and currently
// valid, it is simply not the kind of principal this route accepts.
//
// requireSession's own exception list — /auth/logout, /auth/me,
// /auth/totp/verify, and the passkey login endpoints — is deliberately
// untouched: those either need to remain reachable by a half-authenticated
// session (verify) or have no reason to reject a token principal (logout,
// me) or no session at all (passkey login).
func requireInteractiveSession(ctx context.Context) (auth.Principal, error) {
	p, err := requireMFAComplete(ctx)
	if err != nil {
		return p, err
	}
	if p.TokenID != nil {
		return p, huma.Error403Forbidden("interactive session required")
	}
	return p, nil
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
	case errors.Is(err, auth.ErrInvalidTOTPCode):
		return huma.Error400BadRequest("invalid code")
	default:
		slog.ErrorContext(ctx, "unmapped auth error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
