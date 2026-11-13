// The org monitoring settings routes (backlog 3.57): GET and PUT under
// /orgs/{org}/settings/monitoring, beside the job settings and with their
// permissions. Today the one setting is client_absent_days.
package api

import (
	"context"
	"errors"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/monitorsettings"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type orgMonitoringSettingsDTO struct {
	ClientAbsentDays int `json:"client_absent_days" minimum:"0" maximum:"365" doc:"Days without contact after which a client host raises a warning; 0 = never"`
}

type orgMonitoringSettingsOutput struct {
	Body struct {
		Settings orgMonitoringSettingsDTO `json:"settings"`
	}
}

func toOrgMonitoringSettingsDTO(st monitorsettings.Settings) orgMonitoringSettingsDTO {
	return orgMonitoringSettingsDTO{ClientAbsentDays: st.ClientAbsentDays}
}

// registerMonitoringSettingsRoutes wires the organization's monitoring
// settings. Reads are org.settings.read (viewer: the host page explains
// the warning with it), writes org.settings.write (admin), the job
// settings' bar.
func registerMonitoringSettingsRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/settings/monitoring", func(ctx context.Context, in *struct {
		Org string `path:"org"`
	}) (*orgMonitoringSettingsOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermOrgSettingsRead)
		if err != nil {
			return nil, err
		}
		st, err := deps.MonitorSettings.Get(ctx, scope.OrgID)
		if err != nil {
			return nil, mapMonitoringSettingsErr(ctx, err)
		}
		out := &orgMonitoringSettingsOutput{}
		out.Body.Settings = toOrgMonitoringSettingsDTO(st)
		return out, nil
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/settings/monitoring", func(ctx context.Context, in *struct {
		Org  string `path:"org"`
		Body orgMonitoringSettingsDTO
	}) (*orgMonitoringSettingsOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermOrgSettingsWrite)
		if err != nil {
			return nil, err
		}
		st, err := deps.MonitorSettings.Set(ctx, scope.OrgID, monitorsettings.Settings{ClientAbsentDays: in.Body.ClientAbsentDays})
		if err != nil {
			return nil, mapMonitoringSettingsErr(ctx, err)
		}
		setAuditObject(ctx, "", "updated the organization's monitoring settings")
		out := &orgMonitoringSettingsOutput{}
		out.Body.Settings = toOrgMonitoringSettingsDTO(st)
		return out, nil
	}, withAudit("updateOrgMonitoringSettings", "org"))
}

// mapMonitoringSettingsErr converts the settings service's errors. The
// schema already bounds the value (422), so ErrInvalidInput only reaches
// here from a caller the schema did not cover.
func mapMonitoringSettingsErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, monitorsettings.ErrInvalidInput):
		return huma.Error422UnprocessableEntity(err.Error())
	default:
		slog.ErrorContext(ctx, "unmapped monitoring settings error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
