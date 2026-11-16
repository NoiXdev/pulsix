// The assistant access switch (spec docs/superpowers/specs/2026-11-16-
// assistant-access-design.md, "Assistant access switch"): GET and PUT under
// /orgs/{org}/settings/assistant, beside the job settings.
package api

import (
	"context"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// AssistantSettings is the switch service the routes and requireOrg use.
type AssistantSettings interface {
	Enabled(ctx context.Context, orgID uuid.UUID) (bool, error)
	SetEnabled(ctx context.Context, orgID uuid.UUID, enabled bool) error
}

type assistantSettingsDTO struct {
	AssistantEnabled bool `json:"assistant_enabled" doc:"Whether assistant tokens of this organization may use the assistant endpoint"`
}

type assistantSettingsOutput struct {
	Body struct {
		Settings assistantSettingsDTO `json:"settings"`
	}
}

// registerAssistantSettingsRoutes wires the switch. Reading it is a viewer
// permission like the job settings; turning it on or off needs an admin in
// an interactive session, so no token can switch the assistant on.
func registerAssistantSettingsRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/settings/assistant", func(ctx context.Context, in *struct {
		Org string `path:"org"`
	}) (*assistantSettingsOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermOrgSettingsRead)
		if err != nil {
			return nil, err
		}
		return readAssistantSettings(ctx, deps, scope.OrgID)
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/settings/assistant", func(ctx context.Context, in *struct {
		Org  string `path:"org"`
		Body assistantSettingsDTO
	}) (*assistantSettingsOutput, error) {
		if _, err := requireInteractiveSession(ctx); err != nil {
			return nil, err
		}
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermOrgSettingsWrite)
		if err != nil {
			return nil, err
		}
		if err := deps.Assistant.SetEnabled(ctx, scope.OrgID, in.Body.AssistantEnabled); err != nil {
			slog.ErrorContext(ctx, "store assistant switch", "err", err)
			return nil, huma.Error500InternalServerError("internal error")
		}
		summary := "turned assistant access off"
		if in.Body.AssistantEnabled {
			summary = "turned assistant access on"
		}
		setAuditObject(ctx, "", summary)
		return readAssistantSettings(ctx, deps, scope.OrgID)
	}, withAudit("updateAssistantSettings", "org_settings"))
}

func readAssistantSettings(ctx context.Context, deps Deps, orgID uuid.UUID) (*assistantSettingsOutput, error) {
	on, err := deps.Assistant.Enabled(ctx, orgID)
	if err != nil {
		slog.ErrorContext(ctx, "read assistant switch", "err", err)
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &assistantSettingsOutput{}
	out.Body.Settings.AssistantEnabled = on
	return out, nil
}
