package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// renewInput carries what a sign-in carries, minus the email: a renewal can
// only renew the caller's own session. Code and RecoveryCode are the two
// ways of proving the second factor, as in codeInput.
type renewInput struct {
	Body struct {
		Password     string `json:"password"`
		Code         string `json:"code,omitempty" pattern:"^[0-9]{6}$"`
		RecoveryCode string `json:"recovery_code,omitempty"`
	}
}

// renewOutput answers a renewal: the new session's cookie and when it
// expires, so the SPA can restart its expiry timer without a second call.
type renewOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      struct {
		SessionExpiresAt time.Time `json:"session_expires_at" format:"date-time"`
	}
}

// registerSessionRenewRoutes wires the in-place session renewal (backlog
// 3.94) and the user-bound passkey ceremony that the renewal and the
// passkey step-up share.
//
// Gating: the two renewals use requireInteractiveSession, so a locked
// session (403 session_locked), a pending one, one whose account must
// change its password and a bearer token are all refused — a locked
// session simply runs out. The re-authentication ceremony and the passkey
// step-up use requirePasswordChangeSession instead: the step-up is the
// passkey counterpart of /auth/totp/verify, and the forced password change
// needs a fresh factor too, so it must stay reachable while the account
// must change its password. It still refuses a locked session, a pending
// one and a bearer token.
//
// Every route is on the auth rate limiter: each one is where a password, a
// code or an assertion is guessed or replayed.
//
// A refusal never touches the session or its cookie. Neither renewal
// extends a session's lifetime: success opens a new session with a fresh
// auth.SessionTTL and ends the old one (Enrolment.RenewSession).
func registerSessionRenewRoutes(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/auth/session/renew", func(ctx context.Context, in *renewInput) (*renewOutput, error) {
		p, err := requireInteractiveSession(ctx)
		if err != nil {
			return nil, err
		}
		// The password first: a wrong one must not spend a factor attempt
		// or consume a code.
		if err := deps.Auth.VerifyOwnPassword(ctx, p.UserID, in.Body.Password); err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		// With the second factor optional, a factor-less account's
		// password is its factor (mfa_optional.go): nothing more to prove.
		passwordOnly, err := passwordIsTheFactor(ctx, deps, p.UserID)
		if err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		if !passwordOnly {
			code := &codeInput{}
			code.Body.Code, code.Body.RecoveryCode = in.Body.Code, in.Body.RecoveryCode
			if err := verifyFactorCode(ctx, deps, p.UserID, code); err != nil {
				return nil, err
			}
		}
		return renewed(ctx, deps, p)
	}, withAudit("renewSession", "session"), withAuthRateLimit(deps))

	huma.Post(hapi, "/api/v1/auth/passkeys/reauth/begin", func(ctx context.Context, _ *struct{}) (*passkeyBeginOutput, error) {
		p, err := requirePasswordChangeSession(ctx)
		if err != nil {
			return nil, err
		}
		opts, cid, err := deps.Passkeys.BeginReauth(ctx, p.UserID)
		if err != nil {
			return nil, mapPasskeyErr(ctx, err)
		}
		out := &passkeyBeginOutput{}
		out.Body.CeremonyID, out.Body.Options = cid.String(), opts
		return out, nil
	}, withAuthRateLimit(deps))

	// The passkey step-up: it refreshes the current session's factor in
	// place, exactly as /auth/totp/verify does for a code — same session,
	// new cookie, no second session.
	huma.Post(hapi, "/api/v1/auth/passkeys/reauth/finish", func(ctx context.Context, in *passkeyLoginFinishInput) (*sessionUpgradeOutput, error) {
		p, err := requirePasswordChangeSession(ctx)
		if err != nil {
			return nil, err
		}
		if err := finishReauth(ctx, deps, p, in); err != nil {
			return nil, err
		}
		cookie, err := deps.Enrolment.CompleteVerifiedSession(ctx, p.UserID, p.SessionID)
		if err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		c := sessionCookie(cookie, deps.Secure)
		return &sessionUpgradeOutput{SetCookie: &c}, nil
	}, withAuthRateLimit(deps))

	// A passkey alone renews, as a passkey alone signs in (spec 3.94).
	huma.Post(hapi, "/api/v1/auth/session/renew/passkey", func(ctx context.Context, in *passkeyLoginFinishInput) (*renewOutput, error) {
		p, err := requireInteractiveSession(ctx)
		if err != nil {
			return nil, err
		}
		if err := finishReauth(ctx, deps, p, in); err != nil {
			return nil, err
		}
		return renewed(ctx, deps, p)
	}, withAudit("renewSessionWithPasskey", "session"), withAuthRateLimit(deps))
}

// finishReauth verifies the assertion in against a re-authentication
// ceremony begun for p's own account.
func finishReauth(ctx context.Context, deps Deps, p auth.Principal, in *passkeyLoginFinishInput) error {
	cid, err := uuid.Parse(in.Body.CeremonyID)
	if err != nil {
		return huma.Error400BadRequest("invalid ceremony id")
	}
	if err := deps.Passkeys.FinishReauth(ctx, p.UserID, cid, in.Body.Response); err != nil {
		return mapPasskeyErr(ctx, err)
	}
	return nil
}

// renewed replaces p's session once the caller has proved itself, records
// the event and answers with the new session.
func renewed(ctx context.Context, deps Deps, p auth.Principal) (*renewOutput, error) {
	r, err := deps.Enrolment.RenewSession(ctx, p.UserID, p.SessionID)
	if err != nil {
		return nil, mapAuthErr(ctx, err)
	}
	setAuditScope(ctx, nil, p.UserID)
	setAuditObject(ctx, r.ID.String(), fmt.Sprintf("renewed the session, replacing %s", p.SessionID))
	out := &renewOutput{SetCookie: sessionCookie(r.Cookie, deps.Secure)}
	out.Body.SessionExpiresAt = r.ExpiresAt
	return out, nil
}
