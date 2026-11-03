package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/mail"
	"github.com/noixdev/pulsix/internal/setup"
)

type setupStatusOutput struct {
	Body struct {
		Completed        bool   `json:"completed"`
		AdminMFAEnrolled bool   `json:"admin_mfa_enrolled"`
		Version          string `json:"version"`
		// DefaultLocale is public like Completed: the sign-in page and the
		// wizard need it before any session exists.
		DefaultLocale string `json:"default_locale" enum:"en,de"`
	}
}

type setupCompleteInput struct {
	Body struct {
		AdminEmail    string `json:"admin_email" format:"email" minLength:"3"`
		AdminPassword string `json:"admin_password" minLength:"12"`
		AdminName     string `json:"admin_name" minLength:"1"`
		// The pattern mirrors setup.slugPattern exactly (see its own doc
		// comment for the rule); it cannot be shared as a named constant
		// because a struct tag must be a literal, the same constraint
		// orgCreateInput's own Slug field notes.
		OrgSlug string `json:"org_slug" minLength:"1" maxLength:"40" pattern:"^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$"`
		OrgName string `json:"org_name" minLength:"1"`
		// SMTP is optional: the wizard can be completed with mail
		// configured later, through PUT /admin/settings/smtp.
		SMTP *smtpSettingsBody `json:"smtp,omitempty"`
		// DefaultLocale is the instance's default GUI language; absent
		// means English.
		DefaultLocale string `json:"default_locale,omitempty" enum:"en,de"`
		// SetupToken has no length rule here: an empty one must reach the
		// service and be refused as a wrong token (403), like any other.
		SetupToken string `json:"setup_token" doc:"The instance's setup token, printed in the manager's log at start while setup is not completed (also: pulsix-manager setup-token); case, spaces and dashes do not matter"`
	}
}

type setupCompleteOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      struct {
		UserID string `json:"user_id"`
		OrgID  string `json:"org_id"`
	}
}

// registerSetupRoutes wires the first-run wizard: a public status probe the
// SPA polls on load to decide whether to redirect into the wizard, and the
// one-shot completion route itself. Neither takes an org in its path or
// consults a bearer credential — there is no principal, and often no
// organization, until Complete succeeds — so both are registered directly
// on the root API, the same placement as registerOrgRoutes and
// registerAdminRoutes.
func registerSetupRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/setup/status", func(ctx context.Context, _ *struct{}) (*setupStatusOutput, error) {
		st, err := deps.Setup.Status(ctx)
		if err != nil {
			return nil, mapSetupErr(ctx, err)
		}
		out := &setupStatusOutput{}
		out.Body.Completed, out.Body.Version = st.Completed, managerVersion(deps)
		out.Body.DefaultLocale = st.DefaultLocale
		// Completed and Version stay public unconditionally — the SPA calls
		// this route on load, before it has any session, to decide whether
		// to redirect into the wizard. AdminMFAEnrolled is different: it
		// exposes whether the instance administrator has a second factor,
		// which is sensitive enough that an unauthenticated caller must not
		// learn it for free. Gate it behind the mere presence of a session
		// (not a fully MFA-complete or instance-admin one — the wizard's own
		// freshly opened session, and any other logged-in caller, both
		// qualify; only a caller with no session at all is denied the
		// field).
		if _, ok := auth.FromContext(ctx); ok {
			out.Body.AdminMFAEnrolled = st.AdminMFAEnrolled
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/setup/complete", func(ctx context.Context, in *setupCompleteInput) (*setupCompleteOutput, error) {
		input := setup.Input{
			AdminEmail: in.Body.AdminEmail, AdminPassword: in.Body.AdminPassword, AdminName: in.Body.AdminName,
			OrgSlug: in.Body.OrgSlug, OrgName: in.Body.OrgName, DefaultLocale: in.Body.DefaultLocale,
			SetupToken: in.Body.SetupToken,
		}
		if in.Body.SMTP != nil {
			s := in.Body.SMTP.toSettings()
			input.SMTP = &s
		}
		res, err := deps.Setup.Complete(ctx, input)
		if err != nil {
			return nil, mapSetupErr(ctx, err)
		}
		// This route never calls requireOrg (there is no principal to
		// authorize in the first place — completing setup is what creates
		// the very first one) and, unlike org creation, has no actor
		// resolved by the framework either, so it fills in the audit
		// carrier itself, naming the freshly created administrator as the
		// actor — mirroring registerOrgRoutes and
		// registerInviteAcceptRoute's own reasoning for the same call.
		setAuditScopeAs(ctx, &res.OrgID, res.UserID, audit.ActorKindUser)
		setAuditObject(ctx, res.UserID.String(), "completed first-run setup, created instance admin and organization "+input.OrgSlug)
		out := &setupCompleteOutput{}
		out.SetCookie = sessionCookie(res.Cookie, deps.Secure)
		out.Body.UserID, out.Body.OrgID = res.UserID.String(), res.OrgID.String()
		return out, nil
		// Backlog 7.27: public and argon2id-hashing, so it shares the
		// sign-in routes' per-address budget; a refusal on a completed
		// instance (409) or a wrong setup token (403) is a 4xx and is
		// charged like one, which is what bounds guessing the token.
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAuthRateLimit(deps), withAudit("completeSetup", "setup"))
}

// keepStoredPassword resolves a body whose password is absent against the
// stored settings, so saving any other field leaves the secret alone. A
// first save has nothing to keep (mail.ErrNotConfigured): the empty
// password stands, which is the same thing the wizard means.
func keepStoredPassword(ctx context.Context, deps Deps, body smtpSettingsBody) (mail.SMTPSettings, error) {
	out := body.toSettings()
	if body.Password != nil {
		return out, nil
	}
	stored, err := deps.Setup.LoadSMTP(ctx)
	switch {
	case errors.Is(err, mail.ErrNotConfigured):
		return out, nil
	case err != nil:
		return mail.SMTPSettings{}, mapSetupErr(ctx, err)
	}
	out.Password = stored.Password
	return out, nil
}

// smtpSettingsBody is the wire shape of SMTP settings shared by
// setupCompleteInput's optional SMTP field and PUT /admin/settings/smtp's
// request body.
type smtpSettingsBody struct {
	Host     string `json:"host" minLength:"1"`
	Port     int    `json:"port" minimum:"1" maximum:"65535"`
	Username string `json:"username"`
	// Password is a pointer so the wire can distinguish three cases the
	// settings form needs: absent keeps whatever is stored (the form
	// cannot show the password — this route refuses to return it — so a
	// save that only changes the host carries none), an empty string
	// clears it deliberately, and a value replaces it. Before this it was
	// a plain required string: the form had to send something, sent "",
	// and silently wiped the stored secret.
	Password *string `json:"password,omitempty" doc:"Omit to keep the stored password; send an empty string to clear it"`
	From     string  `json:"from" format:"email" minLength:"3"`
	StartTLS bool    `json:"start_tls"`
}

// toSettings maps the body onto the domain type; an absent password
// becomes the empty one, which is what a first save (the wizard) means by
// it. The update route resolves "absent" against what is stored — see
// keepStoredPassword.
func (b smtpSettingsBody) toSettings() mail.SMTPSettings {
	out := mail.SMTPSettings{Host: b.Host, Port: b.Port, Username: b.Username, From: b.From, StartTLS: b.StartTLS}
	if b.Password != nil {
		out.Password = *b.Password
	}
	return out
}

type smtpSettingsOutput struct {
	Body struct {
		// Configured is false on a fresh instance — the wizard skipped
		// mail, or setup did not include it — with every other field left
		// at its zero value. Password is never present, configured or not:
		// this route must never return a secret in the clear once it has
		// been sealed and stored.
		Configured bool   `json:"configured"`
		Host       string `json:"host,omitempty"`
		Port       int    `json:"port,omitempty"`
		Username   string `json:"username,omitempty"`
		From       string `json:"from,omitempty"`
		StartTLS   bool   `json:"start_tls,omitempty"`
	}
}

type smtpTestInput struct {
	Body struct {
		To string `json:"to" format:"email" minLength:"3"`
	}
}

type smtpTestOutput struct {
	Body struct {
		Sent bool `json:"sent"`
	}
}

// noContentOutput is used by PUT /admin/settings/smtp, whose success has
// nothing to report. Unlike okOutput (mfa_handlers.go), it declares no Body
// field at all, so huma's own default — 204 No Content — applies, matching
// the brief's test sketch ("PUT .../smtp as the admin → 204").
type noContentOutput struct{}

// registerSMTPSettingsRoutes wires the three instance-admin SMTP settings
// routes: reading the current configuration (password never included),
// replacing it, and sending a live test message through it. All three are
// instance-level, not org-scoped, and gated the same way
// POST /admin/users/{id}/deactivate is: requireInstanceAdmin, which
// requireInteractiveSession builds on, so neither a pending session nor a
// bearer token — however privileged its owner — can reach them.
func registerSMTPSettingsRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/admin/settings/smtp", func(ctx context.Context, _ *struct{}) (*smtpSettingsOutput, error) {
		if _, err := requireInstanceAdmin(ctx); err != nil {
			return nil, err
		}
		out := &smtpSettingsOutput{}
		settings, err := deps.Setup.LoadSMTP(ctx)
		switch {
		case errors.Is(err, setup.ErrNotFound):
			return out, nil
		case err != nil:
			return nil, mapSetupErr(ctx, err)
		}
		out.Body.Configured = true
		out.Body.Host, out.Body.Port, out.Body.Username, out.Body.From, out.Body.StartTLS =
			settings.Host, settings.Port, settings.Username, settings.From, settings.StartTLS
		return out, nil
	})

	huma.Put(hapi, "/api/v1/admin/settings/smtp", func(ctx context.Context, in *struct {
		Body smtpSettingsBody
	}) (*noContentOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		settings, err := keepStoredPassword(ctx, deps, in.Body)
		if err != nil {
			return nil, err
		}
		if err := deps.Setup.SaveSMTP(ctx, settings); err != nil {
			return nil, mapSetupErr(ctx, err)
		}
		// No requireOrg here — there is no organization to resolve one
		// from — so the carrier's scope is filled in directly, the same
		// pattern registerAdminRoutes uses for deactivation.
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, "smtp", "updated SMTP settings")
		return &noContentOutput{}, nil
	}, withAudit("updateSMTPSettings", "smtp_settings"))

	huma.Post(hapi, "/api/v1/admin/settings/smtp/test", func(ctx context.Context, in *smtpTestInput) (*smtpTestOutput, error) {
		if _, err := requireInstanceAdmin(ctx); err != nil {
			return nil, err
		}
		if err := deps.Mailer.Test(ctx, in.Body.To); err != nil {
			if errors.Is(err, mail.ErrNotConfigured) {
				// SMTP was never configured: Mailer.Test fails at loading
				// settings (setup.LoadSMTP returns ErrNotFound), before it
				// ever dials anything. That is a state conflict on this
				// instance — there is nothing to test yet — not a failure
				// to reach an upstream server, so 409 reads better than
				// 502 here.
				return nil, huma.Error409Conflict("smtp is not configured")
			}
			// The whole point of a "test" action is to surface the SMTP
			// server's actual response (a rejected login, an unreachable
			// host, ...) rather than a generic failure — see this route's
			// own registration comment on registerSMTPSettingsRoutes.
			return nil, huma.Error502BadGateway(err.Error())
		}
		out := &smtpTestOutput{}
		out.Body.Sent = true
		return out, nil
	})
}

// localeSettingsBody is the wire shape of the instance default language,
// the request body of PUT and the response body of GET
// /admin/settings/locale.
type localeSettingsBody struct {
	DefaultLocale string `json:"default_locale" enum:"en,de"`
}

type localeSettingsOutput struct {
	Body localeSettingsBody
}

// registerLocaleSettingsRoutes wires the instance default GUI language:
// the fallback for every user who has not chosen a language of their own.
// Both routes are instance-admin only, gated like the SMTP settings.
func registerLocaleSettingsRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/admin/settings/locale", func(ctx context.Context, _ *struct{}) (*localeSettingsOutput, error) {
		if _, err := requireInstanceAdmin(ctx); err != nil {
			return nil, err
		}
		l, err := deps.Setup.DefaultLocale(ctx)
		if err != nil {
			return nil, mapSetupErr(ctx, err)
		}
		return &localeSettingsOutput{Body: localeSettingsBody{DefaultLocale: l}}, nil
	})

	huma.Put(hapi, "/api/v1/admin/settings/locale", func(ctx context.Context, in *struct {
		Body localeSettingsBody
	}) (*noContentOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		if err := deps.Setup.SetDefaultLocale(ctx, in.Body.DefaultLocale); err != nil {
			return nil, mapSetupErr(ctx, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, "default_locale", "set the instance default language to "+in.Body.DefaultLocale)
		return &noContentOutput{}, nil
	}, withAudit("updateDefaultLocale", "default_locale"))
}

// mapSetupErr translates setup.Service sentinels to HTTP errors, the same
// shape mapAuthErr and mapTenancyErr give their own packages. It is never
// called with setup.ErrNotFound: the one caller that can receive it (GET
// /admin/settings/smtp, via LoadSMTP) treats "no settings saved yet" as a
// normal 200 (Configured: false), not an error — see
// registerSMTPSettingsRoutes.
func mapSetupErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, setup.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, setup.ErrAlreadyCompleted):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, setup.ErrSetupToken):
		return huma.Error403Forbidden("the setup token does not match; it is printed in the manager's log at start")
	default:
		slog.ErrorContext(ctx, "unmapped setup error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
