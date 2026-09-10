package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/auth"
)

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
			return nil, mapAuthErr(err)
		}
		out := &loginOutput{}
		out.SetCookie = sessionCookie(res.Cookie, deps.Secure)
		out.Body.MFARequired = res.MFARequired
		return out, nil
	})

	huma.Post(hapi, "/api/v1/auth/logout", func(ctx context.Context, _ *struct{}) (*logoutOutput, error) {
		if p, ok := auth.FromContext(ctx); ok {
			_ = deps.Auth.Logout(ctx, p)
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
			return nil, mapAuthErr(err)
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

// requireSession returns the principal or a 401.
func requireSession(ctx context.Context) (auth.Principal, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return p, huma.Error401Unauthorized("login required")
	}
	return p, nil
}

// mapAuthErr translates service sentinels to HTTP errors.
func mapAuthErr(err error) error {
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		return huma.Error401Unauthorized("invalid credentials")
	case errors.Is(err, auth.ErrNotFound):
		return huma.Error401Unauthorized("login required")
	default:
		return err
	}
}
