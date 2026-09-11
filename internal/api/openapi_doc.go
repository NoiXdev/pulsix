package api

import (
	"context"
	"encoding/json"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/mail"
	"github.com/noixdev/pulsix/internal/setup"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// Document builds the OpenAPI document for the full API surface and returns
// it as indented JSON, byte-for-byte what a real server's GET
// /api/openapi.json would serve for the same route registrations — but
// without a database, a listening server, or any working service behind
// Deps.
//
// This works because huma builds an operation's schema from its declared
// Go input/output struct types at registration time (huma.Get, huma.Post,
// ...), never by invoking the handler closure; the handler only runs once a
// real HTTP request is dispatched to it, which never happens here. So
// stubDeps below only has to satisfy the Go compiler — every field just
// needs to be a non-nil value of the right type, not a functioning
// service — while registerRoutes wires the exact same routes New does.
//
// This is what the `pulsix-manager openapi` subcommand calls (see
// cmd/manager/openapi.go), which is in turn what the frontend's `npm run
// api:gen` points openapi-typescript at: deterministic, no running server,
// safe to run in CI.
func Document() ([]byte, error) {
	r := chi.NewRouter()
	cfg := huma.DefaultConfig("Pulsix API", "0.1.0")
	cfg.OpenAPIPath = "/api/openapi"
	cfg.DocsPath = ""
	hapi := humachi.New(r, cfg)
	registerRoutes(hapi, stubDeps())
	return json.MarshalIndent(hapi.OpenAPI(), "", "  ")
}

// stubDeps returns a Deps whose fields are all non-nil zero values, exactly
// enough to satisfy registerRoutes's parameter type — see Document's doc
// comment for why nothing here ever needs to actually work.
func stubDeps() Deps {
	return Deps{
		DB:       &db.DB{},
		Sessions: &auth.Sessions{},
		Auth:     stubAuthService{},
		TOTP:     &auth.TOTP{},
		Passkeys: &auth.Passkeys{},
		Tokens:   &auth.Tokens{},
		Tenancy:  &tenancy.Service{},
		Audit:    &audit.Recorder{},
		Setup:    &setup.Service{},
		Hosts:    &hosts.Service{},
		Mailer:   &mail.SMTP{},
	}
}

// stubAuthService is a Deps.Auth that is never called (see Document's doc
// comment): it exists only so Deps.Auth, an interface field, holds a
// non-nil value of a type that implements AuthService.
type stubAuthService struct{}

func (stubAuthService) Login(context.Context, string, string) (auth.LoginResult, error) {
	return auth.LoginResult{}, nil
}
func (stubAuthService) Logout(context.Context, auth.Principal) error { return nil }
func (stubAuthService) Me(context.Context, auth.Principal) (auth.User, error) {
	return auth.User{}, nil
}
