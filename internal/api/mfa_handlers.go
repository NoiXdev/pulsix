package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
)

type totpEnrolOutput struct {
	Body struct {
		Secret     string `json:"secret"`
		OTPAuthURL string `json:"otpauth_url"`
	}
}
type codeInput struct {
	Body struct {
		Code         string `json:"code,omitempty" pattern:"^[0-9]{6}$"`
		RecoveryCode string `json:"recovery_code,omitempty"`
	}
}
type recoveryOutput struct {
	Body struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
}

// verifyFactorCode proves the caller still holds the account's second
// factor, by either of the two things that count as holding it: a code
// from the authenticator, or one of the recovery codes issued at
// enrolment. The recovery code is consumed on a match and only on a
// match — a rejected attempt must cost nothing, or every typo would be
// permanent progress towards being locked out of a finite set.
//
// Both callers need exactly this, and for the same reason: the step-up
// (/auth/totp/verify) and the removal of TOTP itself (DELETE /auth/totp).
// Removal is why the recovery code must be accepted here at all (backlog
// 1.19): losing the authenticator is the case recovery codes exist for,
// and verifying against the TOTP secret alone left the only factor
// nobody can prove any more as the only factor nobody can remove.
//
// The two codes travel in separate fields rather than one: `code` is
// pattern-bound to six digits at the schema, so a recovery code could
// not pass through it, and nothing has to guess which kind arrived.
func verifyFactorCode(ctx context.Context, deps Deps, userID uuid.UUID, in *codeInput) error {
	var (
		ok  bool
		err error
	)
	if in.Body.RecoveryCode != "" {
		ok, err = deps.TOTP.VerifyRecovery(ctx, userID, in.Body.RecoveryCode)
	} else {
		ok, err = deps.TOTP.Verify(ctx, userID, in.Body.Code)
	}
	if err != nil {
		return mapAuthErr(ctx, err)
	}
	if !ok {
		return huma.Error401Unauthorized("invalid code")
	}
	return nil
}

// okOutput is used by mutation endpoints that have nothing to report beyond
// success. huma defaults an output type with no Body field to 204 No
// Content; declaring an (empty) Body here keeps these on 200 OK instead,
// matching the rest of the auth API's mutation endpoints.
type okOutput struct {
	Body struct{}
}

// registerMFARoutes wires the TOTP enrolment, confirmation, session step-up
// and disable endpoints.
//
// Gating: enrol and confirm use requireEnrolmentSession — an MFA-complete
// session, or a pending one whose account has no second factor yet (the
// only thing such an account may do is enrol one; confirming completes the
// session). A pending session whose account already has a factor is
// refused: a stolen password alone must never be able to establish an
// attacker-controlled TOTP secret next to the real one. Disable uses
// requireInteractiveSession — removing a factor needs a completed second
// factor — and refuses to remove the account's last one. Per
// requireInteractiveSession's own doc comment, an API token can do none of
// this regardless of its scope, since it is always MFA-complete by
// construction (Finding 1, fix round 1). verify is the one route that must accept a
// half-authenticated (MFA-pending) session, per requireSession's doc
// comment: it is the second-factor step-up itself, so requiring
// MFA-complete first would make it unreachable from the state it exists to
// resolve — it stays on requireSession, unchanged.
func registerMFARoutes(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/auth/totp/enrol", func(ctx context.Context, in *codeInput) (*totpEnrolOutput, error) {
		p, err := requireEnrolmentSession(ctx, deps)
		if err != nil {
			return nil, err
		}
		// Moving the factor is an account-security action: an account that
		// already has one must have proved it recently (backlog 1.28).
		if err := requireFreshFactor(ctx, deps, p); err != nil {
			return nil, err
		}
		u, err := deps.Auth.Me(ctx, p)
		if err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		// in.Body.Code is only checked when the account already has a
		// confirmed secret (BeginEnrol requires a fresh code in that case,
		// so a stolen MFA-complete session can't silently plant a new
		// secret over an existing one); first-time enrolment ignores it.
		secret, url, err := deps.TOTP.BeginEnrol(ctx, p.UserID, u.Email, in.Body.Code)
		if err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		out := &totpEnrolOutput{}
		out.Body.Secret, out.Body.OTPAuthURL = secret, url
		return out, nil
	})

	huma.Post(hapi, "/api/v1/auth/totp/confirm", func(ctx context.Context, in *codeInput) (*recoveryOutput, error) {
		p, err := requireEnrolmentSession(ctx, deps)
		if err != nil {
			return nil, err
		}
		// Confirming the factor and completing the session are one write
		// (backlog 1.26): the recovery codes below exist in readable form
		// exactly once, in this response, so they must not be committed
		// unless the session upgrade commits with them.
		codes, err := deps.Enrolment.ConfirmTOTP(ctx, p.UserID, p.SessionID, in.Body.Code)
		if err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		// Instance-level event, same mechanism as POST
		// /admin/users/{id}/deactivate (Task 11 fix round 1, Finding 1): a
		// second factor has no organization, but this is exactly the kind
		// of account-security state change a user needs to see on
		// GET /me/activity after a compromise, so it is no longer
		// allowlisted (Task 11 fix round 2).
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, p.UserID.String(), "enabled two-factor authentication (TOTP)")
		out := &recoveryOutput{}
		out.Body.RecoveryCodes = codes
		return out, nil
	}, withAudit("confirmTOTP", "totp"))

	// Backlog 1.20: verify is the step-up where guessing TOTP or recovery
	// codes is the attack, so it is rate limited per client address; the
	// enrolment routes above already require a session a password had to
	// buy (and login itself is limited).
	huma.Post(hapi, "/api/v1/auth/totp/verify", func(ctx context.Context, in *codeInput) (*okOutput, error) {
		p, err := requireSession(ctx)
		if err != nil {
			return nil, err
		}
		if err := verifyFactorCode(ctx, deps, p.UserID, in); err != nil {
			return nil, err
		}
		if err := deps.Sessions.MarkMFAComplete(ctx, p.SessionID); err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		return &okOutput{}, nil
	}, withAuthRateLimit(deps))

	huma.Delete(hapi, "/api/v1/auth/totp", func(ctx context.Context, in *codeInput) (*okOutput, error) {
		p, err := requireInteractiveSession(ctx)
		if err != nil {
			return nil, err
		}
		if err := requireFreshFactor(ctx, deps, p); err != nil {
			return nil, err
		}
		if err := verifyFactorCode(ctx, deps, p.UserID, in); err != nil {
			return nil, err
		}
		// Refused with 409 when TOTP is the account's last second factor.
		if err := secondFactors(deps).DisableTOTP(ctx, p.UserID); err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		// Instance-level event, same mechanism as confirm above: removing a
		// second factor is one of the highest-value forensic signals in the
		// account-security trail, so it must surface on GET /me/activity.
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, p.UserID.String(), "disabled two-factor authentication (TOTP)")
		return &okOutput{}, nil
	}, withAudit("disableTOTP", "totp"))
}
