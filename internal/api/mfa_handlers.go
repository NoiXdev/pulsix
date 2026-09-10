package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
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
// Gating: enrol/confirm/disable use requireInteractiveSession because they
// mutate or remove a second factor for the calling account — a session that
// has not yet supplied its own second factor must not be able to enrol a
// new one (that would let a stolen password-only session silently establish
// an attacker-controlled TOTP secret and lock the real owner out) or delete
// the existing one (which would strip MFA protection without ever having
// proven possession of it) — and, per requireInteractiveSession's own doc
// comment, an API token must not be able to do either regardless of its
// scope either, since it is always MFA-complete by construction (Finding 1,
// fix round 1). verify is the one route that must accept a
// half-authenticated (MFA-pending) session, per requireSession's doc
// comment: it is the second-factor step-up itself, so requiring
// MFA-complete first would make it unreachable from the state it exists to
// resolve — it stays on requireSession, unchanged.
func registerMFARoutes(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/auth/totp/enrol", func(ctx context.Context, in *codeInput) (*totpEnrolOutput, error) {
		p, err := requireInteractiveSession(ctx)
		if err != nil {
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
		p, err := requireInteractiveSession(ctx)
		if err != nil {
			return nil, err
		}
		codes, err := deps.TOTP.ConfirmEnrol(ctx, p.UserID, in.Body.Code)
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

	huma.Post(hapi, "/api/v1/auth/totp/verify", func(ctx context.Context, in *codeInput) (*okOutput, error) {
		p, err := requireSession(ctx)
		if err != nil {
			return nil, err
		}
		var ok bool
		if in.Body.RecoveryCode != "" {
			ok, err = deps.TOTP.VerifyRecovery(ctx, p.UserID, in.Body.RecoveryCode)
		} else {
			ok, err = deps.TOTP.Verify(ctx, p.UserID, in.Body.Code)
		}
		if err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		if !ok {
			return nil, huma.Error401Unauthorized("invalid code")
		}
		if err := deps.Sessions.MarkMFAComplete(ctx, p.SessionID); err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		return &okOutput{}, nil
	})

	huma.Delete(hapi, "/api/v1/auth/totp", func(ctx context.Context, in *codeInput) (*okOutput, error) {
		p, err := requireInteractiveSession(ctx)
		if err != nil {
			return nil, err
		}
		ok, err := deps.TOTP.Verify(ctx, p.UserID, in.Body.Code)
		if err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		if !ok {
			return nil, huma.Error401Unauthorized("invalid code")
		}
		if err := deps.TOTP.Disable(ctx, p.UserID); err != nil {
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
