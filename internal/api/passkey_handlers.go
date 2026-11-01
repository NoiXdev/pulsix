package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

type passkeyBeginOutput struct {
	Body struct {
		CeremonyID string          `json:"ceremony_id"`
		Options    json.RawMessage `json:"options"`
	}
}

// CeremonyID deliberately has no format:"uuid" tag: huma would then reject a
// malformed value at the schema layer with 422 before the handler ever runs,
// which would make FinishRegistration/FinishLogin's own uuid.Parse and the
// explicit huma.Error400BadRequest below dead code. Parsing (and mapping the
// failure) is done by hand instead, so that path is actually exercised.
type passkeyRegisterFinishInput struct {
	Body struct {
		CeremonyID string          `json:"ceremony_id"`
		Name       string          `json:"name" minLength:"1"`
		Response   json.RawMessage `json:"response"`
	}
}

type passkeyDTO struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type passkeyListOutput struct {
	Body struct {
		Passkeys []passkeyDTO `json:"passkeys"`
	}
}

// ID deliberately has no format:"uuid" tag, for the same reason
// passkeyRegisterFinishInput.CeremonyID doesn't: with the tag, huma rejects
// a malformed value at the schema layer with 422 before the handler ever
// runs, making the handler's own uuid.Parse and its 400 response dead code.
type passkeyIDInput struct {
	ID string `path:"id"`
}

type passkeyLoginFinishInput struct {
	Body struct {
		CeremonyID string          `json:"ceremony_id"`
		Response   json.RawMessage `json:"response"`
	}
}

type passkeyLoginFinishOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      struct{}
}

// registerPasskeyRoutes wires passkey registration, listing, deletion and
// discoverable-credential login.
//
// Gating: register/begin, register/finish and list use
// requireEnrolmentSession — the same reasoning as TOTP's enrol/confirm in
// mfa_handlers.go: an MFA-complete session may add a passkey, and so may a
// pending session whose account has no second factor yet (finishing the
// registration completes it), but a pending session whose account already
// has a factor must not plant a new passkey on the account it rode in on.
// Delete uses requireInteractiveSession and refuses to remove the account's
// last second factor. Per requireInteractiveSession's own doc comment, an API
// token can do none of this, regardless of its scope (Finding 1, fix round
// 1).
// login/begin and login/finish are the opposite case: they are how an
// unauthenticated caller signs in with no session at all, so they carry no
// auth check — matching requireSession's own doc comment, which already
// lists "the passkey login endpoints" among the routes a half-authenticated
// or absent session must still be able to reach.
func registerPasskeyRoutes(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/auth/passkeys/register/begin", func(ctx context.Context, _ *struct{}) (*passkeyBeginOutput, error) {
		p, err := requireEnrolmentSession(ctx, deps)
		if err != nil {
			return nil, err
		}
		// Adding a credential to an account that already has a factor
		// requires having proved one recently (backlog 1.28); a first
		// enrolment has nothing to prove and is exempt.
		if err := requireFreshFactor(ctx, deps, p); err != nil {
			return nil, err
		}
		opts, cid, err := deps.Passkeys.BeginRegistration(ctx, p.UserID)
		if err != nil {
			return nil, mapPasskeyErr(ctx, err)
		}
		out := &passkeyBeginOutput{}
		out.Body.CeremonyID, out.Body.Options = cid.String(), opts
		return out, nil
	})

	huma.Post(hapi, "/api/v1/auth/passkeys/register/finish", func(ctx context.Context, in *passkeyRegisterFinishInput) (*sessionUpgradeOutput, error) {
		p, err := requireEnrolmentSession(ctx, deps)
		if err != nil {
			return nil, err
		}
		// Checked at both ends of the ceremony: a registration begun while
		// the factor was fresh must not be finishable an hour later.
		if err := requireFreshFactor(ctx, deps, p); err != nil {
			return nil, err
		}
		cid, err := uuid.Parse(in.Body.CeremonyID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid ceremony id")
		}
		if err := deps.Passkeys.FinishRegistration(ctx, p.UserID, cid, in.Body.Name, in.Body.Response); err != nil {
			return nil, mapPasskeyErr(ctx, err)
		}
		// The account now has a second factor: a session that was pending
		// only for want of one is complete from here on.
		cookie, err := completeEnrolment(ctx, deps, p)
		if err != nil {
			return nil, err
		}
		// Instance-level event, same mechanism as POST
		// /admin/users/{id}/deactivate (Task 11 fix round 1, Finding 1) and
		// TOTP confirm/disable (mfa_handlers.go): this is the step that
		// actually attaches a new credential to the account, so it belongs
		// on GET /me/activity like every other account-security change.
		// FinishRegistration does not return the new credential's id (it
		// assigns one internally but only persists it), so the object is
		// identified by account rather than by credential row; the
		// human-readable name the caller supplied carries the specific
		// credential detail instead.
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, p.UserID.String(), fmt.Sprintf("registered passkey %q", in.Body.Name))
		return &sessionUpgradeOutput{SetCookie: cookie}, nil
	}, withAudit("finishPasskeyRegistration", "passkey"))

	huma.Get(hapi, "/api/v1/auth/passkeys", func(ctx context.Context, _ *struct{}) (*passkeyListOutput, error) {
		p, err := requireEnrolmentSession(ctx, deps)
		if err != nil {
			return nil, err
		}
		creds, err := deps.Passkeys.List(ctx, p.UserID)
		if err != nil {
			return nil, mapPasskeyErr(ctx, err)
		}
		out := &passkeyListOutput{}
		out.Body.Passkeys = make([]passkeyDTO, len(creds))
		for i, c := range creds {
			out.Body.Passkeys[i] = passkeyDTO{ID: c.ID.String(), Name: c.Name, CreatedAt: c.CreatedAt, LastUsedAt: c.LastUsedAt}
		}
		return out, nil
	})

	huma.Delete(hapi, "/api/v1/auth/passkeys/{id}", func(ctx context.Context, in *passkeyIDInput) (*okOutput, error) {
		p, err := requireInteractiveSession(ctx)
		if err != nil {
			return nil, err
		}
		// Removing a credential is as security-relevant as adding one:
		// an attacker who strips the factors owns the account next login.
		if err := requireFreshFactor(ctx, deps, p); err != nil {
			return nil, err
		}
		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid passkey id")
		}
		// Refused with 409 when this passkey is the account's last second
		// factor.
		if err := secondFactors(deps).DeletePasskey(ctx, p.UserID, id); err != nil {
			return nil, mapPasskeyErr(ctx, err)
		}
		// Instance-level event, same mechanism as the other three routes
		// above: removing a passkey is a real account-security state
		// change, and — unlike register/finish — the credential's own id
		// is already in hand from the path, so the object identifies the
		// specific credential removed, not just the account.
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, id.String(), "removed passkey")
		return &okOutput{}, nil
	}, withAudit("deletePasskey", "passkey"))

	huma.Post(hapi, "/api/v1/auth/passkeys/login/begin", func(ctx context.Context, _ *struct{}) (*passkeyBeginOutput, error) {
		opts, cid, err := deps.Passkeys.BeginLogin(ctx)
		if err != nil {
			return nil, mapPasskeyErr(ctx, err)
		}
		out := &passkeyBeginOutput{}
		out.Body.CeremonyID, out.Body.Options = cid.String(), opts
		return out, nil
	})

	// Backlog 1.20: the login ceremony's finish verifies an assertion
	// against the manager's ceremonies — the passkey equivalent of a
	// password login, and rate limited like it, per client address.
	huma.Post(hapi, "/api/v1/auth/passkeys/login/finish", func(ctx context.Context, in *passkeyLoginFinishInput) (*passkeyLoginFinishOutput, error) {
		cid, err := uuid.Parse(in.Body.CeremonyID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid ceremony id")
		}
		cookie, err := deps.Passkeys.FinishLogin(ctx, cid, in.Body.Response)
		if err != nil {
			return nil, mapPasskeyErr(ctx, err)
		}
		out := &passkeyLoginFinishOutput{}
		out.SetCookie = sessionCookie(cookie, deps.Secure)
		return out, nil
	}, withAuthRateLimit(deps))
}

// mapPasskeyErr is mapAuthErr plus one override: auth.ErrNotFound becomes
// 404, not mapAuthErr's 401 "login required". mapAuthErr's 401 mapping fits
// routes where "not found" really means "you're not logged in" (auth/me,
// logout); here ErrNotFound instead means an unknown, already-used, or
// expired ceremony, or a credential id that does not belong to the caller —
// none of which are the caller's own session being invalid, so a generic
// login-required 401 would be misleading. auth.ErrInvalidCredentials (a
// failed assertion) and every other error still go through mapAuthErr
// unchanged.
func mapPasskeyErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, auth.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, auth.ErrCredentialExists):
		return huma.Error409Conflict("credential already registered")
	default:
		return mapAuthErr(ctx, err)
	}
}
