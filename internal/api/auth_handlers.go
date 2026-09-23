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
		// MFARequired: the account has a second factor, and the session
		// must verify it (POST /auth/totp/verify or a passkey) before it
		// can do anything else.
		MFARequired bool `json:"mfa_required"`
		// MFAEnrolmentRequired: the account has no second factor yet, and
		// the session can reach only the enrolment routes until it enrols
		// one. Exactly one of the two is always true — a password never
		// completes a session on its own.
		MFAEnrolmentRequired bool `json:"mfa_enrolment_required"`
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
		// TOTPConfirmed says whether the account has a confirmed TOTP
		// factor. Combined client-side with whether GET /auth/passkeys
		// returns any credential, it tells the account security page
		// which factors exist.
		TOTPConfirmed bool `json:"totp_confirmed"`
		InstanceAdmin bool `json:"instance_admin"`
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
		out.Body.TOTPConfirmed = totpConfirmed
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

// The four session gates. A second factor is mandatory, so a session is in
// one of three states, and every route names the gate that admits exactly
// the states it may serve:
//
//	state \ gate                  requireSession  requireEnrolmentSession  requireMFAComplete  requireInteractiveSession
//	pending, factor to verify      yes             no                       no                  no
//	pending, no factor (enrol)     yes             yes                      no                  no
//	MFA-complete session           yes             yes                      yes                 yes
//	bearer token (always complete) yes             no                       yes                 no
//
//   - requireSession: /auth/me, /auth/logout, /auth/totp/verify — reachable
//     in every state, because they are how a pending session learns what it
//     still owes and supplies it (or gives up).
//   - requireEnrolmentSession: TOTP enrol and confirm, passkey register
//     begin and finish, and the passkey list — the only routes an account
//     without a second factor can use, and the ones that give it one.
//     Confirming a TOTP secret or finishing a passkey registration
//     completes that session on the spot (Sessions.MarkMFAComplete), with
//     no fresh login.
//   - requireMFAComplete (and requireOrg, which applies the same rule):
//     everything else a signed-in person does — organizations, the
//     activity feed, every org-scoped route.
//   - requireInteractiveSession: removing a second factor (TOTP disable,
//     passkey delete), creating API tokens, and every instance-admin route
//     — MFA-complete and never a bearer token.

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
// rejects a session that exists but has not completed its second factor —
// whether it still has one to verify or has none to verify yet (a password
// login never completes a session on its own). Any route that isn't in
// requireSession's or requireEnrolmentSession's lists above must use this
// (or a stricter gate) or it will silently accept a half-authenticated
// caller.
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
// It is the gate for every route that removes one of the account's own
// credentials — passkey delete and TOTP disable — and for minting API
// tokens and every instance-admin route; the routes that add a credential
// use requireEnrolmentSession, which applies the same token rule. An API
// token is, by construction, always MFA-complete
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

// requireEnrolmentSession admits an interactive session that may enrol a
// second factor: one that is MFA-complete, or one that is pending only
// because its account has no confirmed second factor at all. A pending
// session whose account does have a factor is refused (403, "second factor
// required") exactly as requireInteractiveSession refuses it — it must
// verify that factor first, so a stolen password alone can never plant a new
// one next to it — and so is a bearer token, whatever its scope.
//
// Whether the account has a factor is read live, not remembered from login:
// once any session of the account enrols one, every other pending session of
// that account loses enrolment access and must verify instead.
func requireEnrolmentSession(ctx context.Context, deps Deps) (auth.Principal, error) {
	p, err := requireSession(ctx)
	if err != nil {
		return p, err
	}
	if p.TokenID != nil {
		return p, huma.Error403Forbidden("interactive session required")
	}
	if p.MFAComplete {
		return p, nil
	}
	enrolled, err := secondFactors(deps).Enrolled(ctx, p.UserID)
	if err != nil {
		return p, mapAuthErr(ctx, err)
	}
	if enrolled {
		return p, huma.Error403Forbidden("second factor required")
	}
	return p, nil
}

// completeEnrolment upgrades a session that was pending only for lack of a
// second factor once it has just enrolled one, so it continues without a
// fresh login. A session that was already MFA-complete is left alone.
func completeEnrolment(ctx context.Context, deps Deps, p auth.Principal) error {
	if p.MFAComplete {
		return nil
	}
	if err := deps.Sessions.MarkMFAComplete(ctx, p.SessionID); err != nil {
		return mapAuthErr(ctx, err)
	}
	return nil
}

// secondFactors is the account-level view over both kinds of second factor
// deps carries.
func secondFactors(deps Deps) auth.SecondFactors {
	return auth.SecondFactors{TOTP: deps.TOTP, Passkeys: deps.Passkeys}
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
	case errors.Is(err, auth.ErrLastSecondFactor):
		return huma.Error409Conflict("cannot remove the last second factor; add another one first")
	default:
		slog.ErrorContext(ctx, "unmapped auth error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
