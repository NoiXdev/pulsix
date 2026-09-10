package api

import (
	"context"
	"encoding/json"
	"errors"
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

type passkeyIDInput struct {
	ID string `path:"id" format:"uuid"`
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
// Gating: register/begin, register/finish, list and delete all change or
// read the account's second factors, so they require requireMFAComplete —
// the same reasoning as TOTP's enrol/confirm/disable in mfa_handlers.go: a
// session that has not itself completed a second factor must not be able to
// plant a new passkey (or enumerate/remove existing ones) on the account it
// rode in on. login/begin and login/finish are the opposite case: they are
// how an unauthenticated caller signs in with no session at all, so they
// carry no auth check — matching requireSession's own doc comment, which
// already lists "the passkey login endpoints" among the routes a
// half-authenticated or absent session must still be able to reach.
func registerPasskeyRoutes(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/auth/passkeys/register/begin", func(ctx context.Context, _ *struct{}) (*passkeyBeginOutput, error) {
		p, err := requireMFAComplete(ctx)
		if err != nil {
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

	huma.Post(hapi, "/api/v1/auth/passkeys/register/finish", func(ctx context.Context, in *passkeyRegisterFinishInput) (*okOutput, error) {
		p, err := requireMFAComplete(ctx)
		if err != nil {
			return nil, err
		}
		cid, err := uuid.Parse(in.Body.CeremonyID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid ceremony id")
		}
		if err := deps.Passkeys.FinishRegistration(ctx, p.UserID, cid, in.Body.Name, in.Body.Response); err != nil {
			return nil, mapPasskeyErr(ctx, err)
		}
		return &okOutput{}, nil
	})

	huma.Get(hapi, "/api/v1/auth/passkeys", func(ctx context.Context, _ *struct{}) (*passkeyListOutput, error) {
		p, err := requireMFAComplete(ctx)
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
		p, err := requireMFAComplete(ctx)
		if err != nil {
			return nil, err
		}
		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid passkey id")
		}
		if err := deps.Passkeys.Delete(ctx, p.UserID, id); err != nil {
			return nil, mapPasskeyErr(ctx, err)
		}
		return &okOutput{}, nil
	})

	huma.Post(hapi, "/api/v1/auth/passkeys/login/begin", func(ctx context.Context, _ *struct{}) (*passkeyBeginOutput, error) {
		opts, cid, err := deps.Passkeys.BeginLogin(ctx)
		if err != nil {
			return nil, mapPasskeyErr(ctx, err)
		}
		out := &passkeyBeginOutput{}
		out.Body.CeremonyID, out.Body.Options = cid.String(), opts
		return out, nil
	})

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
	})
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
	if errors.Is(err, auth.ErrNotFound) {
		return huma.Error404NotFound("not found")
	}
	return mapAuthErr(ctx, err)
}
