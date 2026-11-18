package api

import (
	"context"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/clientip"
)

// The optional second factor (PULSIX_MFA_OPTIONAL, Deps.MFAOptional) is for
// demo and test instances. While it is on, an account WITHOUT a second
// factor signs in with its password alone (auth.WithOptionalSecondFactor,
// setup.Service.SetSecondFactorOptional, the invitation's new account), and
// for that account the password takes the factor's place everywhere a
// factor is proven: the step-up (POST /auth/password/reauth below) and the
// session renewal. An account WITH a factor is unaffected: it verifies the
// factor at sign-in, and its step-up and renewal still need the factor.
//
// Every session completed on the password alone in this mode is
// password-only (auth.Sessions.CreatePasswordOnly, and the renewal and the
// step-up below keep the mark). It holds only while the account has no
// factor: once the owner enrols one, in any session, every password-only
// session of the account reads as pending and stale on its next request and
// must verify the new factor through /auth/totp/verify (a code or a
// recovery code), which clears the mark. The passkey step-up needs a
// complete session, so a superseded session of a passkey-only account must
// sign in again. So a session someone opened with a known password before
// the owner secured the account does not outlive that enrolment, and
// neither do its API tokens: the first factor enrolled from a password-only
// session revokes them (auth.Enrolment, recordTokensRevokedOnFirstFactor).

// passwordNotTheFactor is the refusal of the password step-up for an
// account that has a second factor.
const passwordNotTheFactor = "the password is not a step-up for this account; prove the second factor"

// signInWithoutSecondFactor is the audit action of a sign-in that completed
// without a second factor because the mode is on.
const signInWithoutSecondFactor = "signInWithoutSecondFactor"

// passwordIsTheFactor reports whether userID's password stands in for a
// second factor: only while the mode is on and only for an account that has
// no factor at all. The factor check fails closed: a store error is
// returned, never read as "no factor".
func passwordIsTheFactor(ctx context.Context, deps Deps, userID uuid.UUID) (bool, error) {
	if !deps.MFAOptional {
		return false, nil
	}
	enrolled, err := secondFactors(deps).Enrolled(ctx, userID)
	if err != nil {
		return false, err
	}
	return !enrolled, nil
}

// recordSignInWithoutSecondFactor records, on the account's own activity,
// that sessionID was opened complete on a password alone because the mode
// is on. The sign-in routes have no principal yet, so the event names the
// account explicitly as a user actor. A recording failure is logged, never
// returned: the session already exists.
func recordSignInWithoutSecondFactor(ctx context.Context, deps Deps, userID, sessionID uuid.UUID) {
	if err := deps.Audit.Record(ctx, audit.Event{
		ActorID: &userID, ActorKind: audit.ActorKindUser,
		Action: signInWithoutSecondFactor, ObjectType: "session", ObjectID: sessionID.String(),
		Summary: "signed in without a second factor (optional mode)",
	}); err != nil {
		slog.ErrorContext(ctx, "audit: failed to record event", "err", err, "action", signInWithoutSecondFactor)
	}
}

// tokensRevokedOnFirstFactor is the audit action of the token revocation that
// comes with an account's first second factor enrolled from a password-only
// session (auth.Enrolment.ConfirmTOTP, FinishPasskey).
const tokensRevokedOnFirstFactor = "revokeTokensOnFirstFactor"

// recordTokensRevokedOnFirstFactor records, on the account's own activity,
// that its tokens were revoked because it enrolled its first second factor
// from a session signed in on the password alone. The enrolment itself has
// its own event; this one says what else it did. revoked false records
// nothing. A recording failure is logged, never returned: the revocation
// has committed.
func recordTokensRevokedOnFirstFactor(ctx context.Context, deps Deps, userID uuid.UUID, revoked bool) {
	if !revoked {
		return
	}
	if err := deps.Audit.Record(ctx, audit.Event{
		ActorID: &userID, ActorKind: audit.ActorKindUser,
		Action: tokensRevokedOnFirstFactor, ObjectType: "user", ObjectID: userID.String(),
		Summary: "revoked the account's tokens on its first second factor",
	}); err != nil {
		slog.ErrorContext(ctx, "audit: failed to record event", "err", err, "action", tokensRevokedOnFirstFactor)
	}
}

type passwordReauthInput struct {
	Body struct {
		Password string `json:"password"`
	}
}

// registerPasswordStepUpRoute wires POST /auth/password/reauth: the step-up
// of an account whose password is its factor (passwordIsTheFactor). It
// refreshes the session's proof in place, exactly as /auth/totp/verify does
// for a code and …/passkeys/reauth/finish for a passkey — same session, new
// cookie. It is refused with 403 for an account that has a factor and
// whenever the mode is off, so it never weakens the step-up of an account
// that has one.
//
// Gate: requirePasswordChangeSession, like the passkey step-up — a
// complete cookie session, locked and bearer principals refused, and still
// reachable while the account must change its password (that change needs
// a fresh step-up). It is on the auth rate limiter it shares with the
// renewal by password (passwordGuesses, registerSessionRenewRoutes): both
// guess the same password of the same session, so the step-up adds no
// budget of its own.
//
// The completion is conditional (Enrolment.RefreshPasswordProof): the
// account's lack of a factor is checked again in the statement that
// refreshes the proof, after the password's argon2 time, and a factor
// enrolled after even that supersedes the session on lookup.
func registerPasswordStepUpRoute(hapi huma.API, deps Deps, passwordGuesses *clientip.Limiter) {
	huma.Post(hapi, "/api/v1/auth/password/reauth", func(ctx context.Context, in *passwordReauthInput) (*sessionUpgradeOutput, error) {
		p, err := requirePasswordChangeSession(ctx)
		if err != nil {
			return nil, err
		}
		ok, err := passwordIsTheFactor(ctx, deps, p.UserID)
		if err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		if !ok {
			return nil, huma.Error403Forbidden(passwordNotTheFactor)
		}
		if err := deps.Auth.VerifyOwnPassword(ctx, p.UserID, in.Body.Password); err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		cookie, err := deps.Enrolment.RefreshPasswordProof(ctx, p.UserID, p.SessionID)
		if err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		c := sessionCookie(cookie, deps.Secure)
		return &sessionUpgradeOutput{SetCookie: &c}, nil
	}, withAuthRateLimitOn(deps, passwordGuesses))
}

// passwordStepUp is /auth/me's password_step_up: whether this session's
// step-up is the password (passwordIsTheFactor). Only a complete session
// has a step-up: a pending one (opened before the mode was turned on, or
// superseded by a factor) proves a factor instead, and the password step-up
// route refuses it. A bearer token has no step-up at all.
func passwordStepUp(ctx context.Context, deps Deps, p auth.Principal) (bool, error) {
	if p.TokenID != nil || !p.MFAComplete {
		return false, nil
	}
	return passwordIsTheFactor(ctx, deps, p.UserID)
}
