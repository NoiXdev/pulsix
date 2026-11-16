// The assistant access switch (spec docs/superpowers/specs/2026-11-16-
// assistant-access-design.md, "Assistant access switch"): GET and PUT under
// /orgs/{org}/settings/assistant, beside the job settings.
package api

import (
	"context"
	"log/slog"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// AssistantSettings is the switch service the routes and requireOrg use.
type AssistantSettings interface {
	Enabled(ctx context.Context, orgID uuid.UUID) (bool, error)
	SetEnabled(ctx context.Context, orgID uuid.UUID, enabled bool) error
	StartsFollowUps(ctx context.Context, orgID uuid.UUID) (bool, error)
	SetStartsFollowUps(ctx context.Context, orgID uuid.UUID, on bool) error
}

type assistantSettingsDTO struct {
	AssistantEnabled           bool `json:"assistant_enabled" doc:"Whether assistant tokens of this organization may use the assistant endpoint"`
	ApprovedRunsStartFollowUps bool `json:"approved_runs_start_follow_ups" doc:"Whether an approved run request also starts the template's follow-ups"`
}

// assistantSettingsUpdate is the PUT body: the follow-up field is optional so
// a client that only knows the switch keeps the stored choice.
type assistantSettingsUpdate struct {
	AssistantEnabled           bool  `json:"assistant_enabled" doc:"Whether assistant tokens of this organization may use the assistant endpoint"`
	ApprovedRunsStartFollowUps *bool `json:"approved_runs_start_follow_ups,omitempty" doc:"Whether an approved run request also starts the template's follow-ups; omitted keeps it"`
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
		Body assistantSettingsUpdate
	}) (*assistantSettingsOutput, error) {
		if _, err := requireInteractiveSession(ctx); err != nil {
			return nil, err
		}
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermOrgSettingsWrite)
		if err != nil {
			return nil, err
		}
		wasOn, err := deps.Assistant.Enabled(ctx, scope.OrgID)
		if err != nil {
			slog.ErrorContext(ctx, "read assistant switch", "err", err)
			return nil, huma.Error500InternalServerError("internal error")
		}
		wasFollowUps, err := deps.Assistant.StartsFollowUps(ctx, scope.OrgID)
		if err != nil {
			slog.ErrorContext(ctx, "read follow-up setting", "err", err)
			return nil, huma.Error500InternalServerError("internal error")
		}
		if err := deps.Assistant.SetEnabled(ctx, scope.OrgID, in.Body.AssistantEnabled); err != nil {
			slog.ErrorContext(ctx, "store assistant switch", "err", err)
			return nil, huma.Error500InternalServerError("internal error")
		}
		var parts []string
		if in.Body.AssistantEnabled != wasOn {
			parts = append(parts, assistantSwitchSummary(in.Body.AssistantEnabled))
		}
		if in.Body.ApprovedRunsStartFollowUps != nil {
			on := *in.Body.ApprovedRunsStartFollowUps
			if err := deps.Assistant.SetStartsFollowUps(ctx, scope.OrgID, on); err != nil {
				slog.ErrorContext(ctx, "store follow-up setting", "err", err)
				return nil, huma.Error500InternalServerError("internal error")
			}
			if on != wasFollowUps {
				if on {
					parts = append(parts, "approved run requests start follow-ups")
				} else {
					parts = append(parts, "approved run requests start no follow-ups")
				}
			}
		}
		summary := strings.Join(parts, "; ")
		if summary == "" {
			summary = assistantSwitchSummary(in.Body.AssistantEnabled)
		}
		setAuditObject(ctx, "", summary)
		return readAssistantSettings(ctx, deps, scope.OrgID)
	}, withAudit("updateAssistantSettings", "org_settings"))
}

func assistantSwitchSummary(on bool) string {
	if on {
		return "turned assistant access on"
	}
	return "turned assistant access off"
}

func readAssistantSettings(ctx context.Context, deps Deps, orgID uuid.UUID) (*assistantSettingsOutput, error) {
	on, err := deps.Assistant.Enabled(ctx, orgID)
	if err != nil {
		slog.ErrorContext(ctx, "read assistant switch", "err", err)
		return nil, huma.Error500InternalServerError("internal error")
	}
	followUps, err := deps.Assistant.StartsFollowUps(ctx, orgID)
	if err != nil {
		slog.ErrorContext(ctx, "read follow-up setting", "err", err)
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &assistantSettingsOutput{}
	out.Body.Settings.AssistantEnabled = on
	out.Body.Settings.ApprovedRunsStartFollowUps = followUps
	return out, nil
}
