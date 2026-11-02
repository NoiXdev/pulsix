package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/locale"
)

// ownLocaleInput is the request of PUT /auth/me/locale. locale is required
// but may be null, which clears the user's own choice.
type ownLocaleInput struct {
	Body struct {
		Locale *string `json:"locale" enum:"en,de" nullable:"true" required:"true"`
	}
}

// ownLocaleOutput answers with the stored choice and the language that now
// applies to the user (their choice, else the instance default).
type ownLocaleOutput struct {
	Body struct {
		Locale          *string `json:"locale" enum:"en,de" nullable:"true"`
		EffectiveLocale string  `json:"effective_locale" enum:"en,de"`
	}
}

// registerLocaleRoutes wires the signed-in user's own GUI language. It is an
// account setting, so it takes an interactive session (MFA-complete, never
// an API token), like the other routes that change the account itself, and
// is audited as an instance-level event on the user's own activity trail.
func registerLocaleRoutes(hapi huma.API, deps Deps) {
	huma.Put(hapi, "/api/v1/auth/me/locale", func(ctx context.Context, in *ownLocaleInput) (*ownLocaleOutput, error) {
		p, err := requireInteractiveSession(ctx)
		if err != nil {
			return nil, err
		}
		if err := deps.Auth.SetLocale(ctx, p.UserID, in.Body.Locale); err != nil {
			return nil, mapAuthErr(ctx, err)
		}
		def, err := deps.Setup.DefaultLocale(ctx)
		if err != nil {
			return nil, mapSetupErr(ctx, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, p.UserID.String(), "changed their GUI language")
		out := &ownLocaleOutput{}
		out.Body.Locale = in.Body.Locale
		out.Body.EffectiveLocale = locale.Resolve(in.Body.Locale, def)
		return out, nil
	}, withAudit("setLocale", "user"))
}
