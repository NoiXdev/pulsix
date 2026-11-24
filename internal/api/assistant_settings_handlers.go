// The assistant access switch (spec docs/superpowers/specs/2026-11-16-
// assistant-access-design.md, "Assistant access switch"): GET and PUT under
// /orgs/{org}/settings/assistant, beside the job settings.
package api

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistant"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// AssistantSettings is the switch service the routes and requireOrg use.
type AssistantSettings interface {
	Enabled(ctx context.Context, orgID uuid.UUID) (bool, error)
	StartsFollowUps(ctx context.Context, orgID uuid.UUID) (bool, error)
	// Update changes the given fields in one transaction and reports the
	// settings before and after (assistant.ErrNothingToChange for none).
	Update(ctx context.Context, orgID uuid.UUID, c assistant.Change) (before, after assistant.Settings, err error)
}

type assistantSettingsDTO struct {
	AssistantEnabled           bool `json:"assistant_enabled" doc:"Whether assistant tokens of this organization may use the assistant endpoint"`
	ApprovedRunsStartFollowUps bool `json:"approved_runs_start_follow_ups" doc:"Whether an approved run request also starts the template's follow-ups"`
}

// assistantSettingsUpdate is the PUT body: each field is optional and an
// omitted one keeps the stored value, so two clients changing one field
// each never undo each other. At least one must be given.
type assistantSettingsUpdate struct {
	AssistantEnabled           *bool `json:"assistant_enabled,omitempty" doc:"Whether assistant tokens of this organization may use the assistant endpoint; omitted keeps it"`
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
		before, after, err := deps.Assistant.Update(ctx, scope.OrgID, assistant.Change{
			Enabled: in.Body.AssistantEnabled, StartsFollowUps: in.Body.ApprovedRunsStartFollowUps,
		})
		if errors.Is(err, assistant.ErrNothingToChange) {
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		if err != nil {
			slog.ErrorContext(ctx, "store assistant settings", "err", err)
			return nil, huma.Error500InternalServerError("internal error")
		}
		setAuditObject(ctx, "", assistantSettingsSummary(before, after, in.Body.AssistantEnabled != nil))
		out := &assistantSettingsOutput{}
		out.Body.Settings = assistantSettingsDTO{AssistantEnabled: after.Enabled, ApprovedRunsStartFollowUps: after.StartsFollowUps}
		return out, nil
	}, withAudit("updateAssistantSettings", "org_settings"))
}

// assistantSettingsSummary names what an update changed, access first. With
// nothing changed it names the requested state: the access switch when the
// request gave it, the follow-up setting otherwise.
func assistantSettingsSummary(before, after assistant.Settings, accessGiven bool) string {
	var parts []string
	if after.Enabled != before.Enabled {
		parts = append(parts, assistantSwitchSummary(after.Enabled))
	}
	if after.StartsFollowUps != before.StartsFollowUps {
		parts = append(parts, followUpsSummary(after.StartsFollowUps))
	}
	switch {
	case len(parts) > 0:
		return strings.Join(parts, "; ")
	case accessGiven:
		return assistantSwitchSummary(after.Enabled)
	default:
		return followUpsSummary(after.StartsFollowUps)
	}
}

func followUpsSummary(on bool) string {
	if on {
		return "approved run requests start follow-ups"
	}
	return "approved run requests start no follow-ups"
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
