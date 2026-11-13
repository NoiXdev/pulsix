// The org monitoring settings routes (backlog 3.57): GET and PUT under
// /orgs/{org}/settings/monitoring, beside the job settings and with their
// permissions. The settings are client_absent_days and, since backlog
// 3.15, the retention choice (retention_days) with the instance maximum
// and the effective retention.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/monitorsettings"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// orgMonitoringSettingsDTO is the settings as the API returns them.
type orgMonitoringSettingsDTO struct {
	ClientAbsentDays       int  `json:"client_absent_days" minimum:"0" maximum:"365" doc:"Days without contact after which a client host raises a warning; 0 = never"`
	RetentionDays          *int `json:"retention_days" enum:"30,90,365" nullable:"true" doc:"The organization's retention choice in days; null = no choice (the instance default, retention_max_days)"`
	RetentionMaxDays       int  `json:"retention_max_days" doc:"The instance's retention maximum in days (set by the operator); no organization keeps measurements longer"`
	RetentionEffectiveDays int  `json:"retention_effective_days" doc:"How many days measurements and state history are kept: the choice, capped at retention_max_days"`
}

// orgMonitoringSettingsInputDTO is the PUT body. client_absent_days is
// required and always replaced. retention_days is optional: omitted keeps
// the stored choice (so a client that only edits client_absent_days never
// touches it), null clears it back to the instance default, 30/90/365
// sets it; a choice above retention_max_days is stored and capped. The
// read-only fields of a GET are accepted and ignored, so a GET's settings
// object can be sent back as it is.
type orgMonitoringSettingsInputDTO struct {
	ClientAbsentDays       int                 `json:"client_absent_days" minimum:"0" maximum:"365" doc:"Days without contact after which a client host raises a warning; 0 = never"`
	RetentionDays          optionalNullableInt `json:"retention_days,omitempty" enum:"30,90,365" doc:"Retention choice in days. Omitted keeps the stored choice; null clears it back to the instance default"`
	RetentionMaxDays       *int                `json:"retention_max_days,omitempty" readOnly:"true" doc:"Read-only; ignored"`
	RetentionEffectiveDays *int                `json:"retention_effective_days,omitempty" readOnly:"true" doc:"Read-only; ignored"`
}

// optionalNullableInt is a request field that tells an omitted field
// (Sent false) from an explicit null (Null true) from a number. The
// schema is a nullable integer; the field's own tags (enum) apply to it.
type optionalNullableInt struct {
	Sent  bool
	Null  bool
	Value int
}

// UnmarshalJSON implements json.Unmarshaler; encoding/json calls it for
// a present field only, null included.
func (o *optionalNullableInt) UnmarshalJSON(b []byte) error {
	o.Sent = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

// Schema implements huma.SchemaProvider: a nullable integer.
func (o optionalNullableInt) Schema(r huma.Registry) *huma.Schema {
	s := r.Schema(reflect.TypeOf(o.Value), true, "")
	s.Nullable = true
	return s
}

// update converts the PUT body to the service's write: an omitted
// retention_days keeps the choice, null clears it (0).
func (in orgMonitoringSettingsInputDTO) update() monitorsettings.Update {
	u := monitorsettings.Update{ClientAbsentDays: in.ClientAbsentDays}
	if in.RetentionDays.Sent {
		days := 0
		if !in.RetentionDays.Null {
			days = in.RetentionDays.Value
		}
		u.RetentionDays = &days
	}
	return u
}

type orgMonitoringSettingsOutput struct {
	Body struct {
		Settings orgMonitoringSettingsDTO `json:"settings"`
	}
}

func toOrgMonitoringSettingsDTO(v monitorsettings.View) orgMonitoringSettingsDTO {
	dto := orgMonitoringSettingsDTO{
		ClientAbsentDays:       v.ClientAbsentDays,
		RetentionMaxDays:       v.RetentionMaxDays,
		RetentionEffectiveDays: v.RetentionEffectiveDays,
	}
	if v.RetentionDays != 0 {
		days := v.RetentionDays
		dto.RetentionDays = &days
	}
	return dto
}

// registerMonitoringSettingsRoutes wires the organization's monitoring
// settings. Reads are org.settings.read (viewer: the host page explains
// the warning with it, the graphs offer ranges by the retention), writes
// org.settings.write (admin), the job settings' bar.
func registerMonitoringSettingsRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/settings/monitoring", func(ctx context.Context, in *struct {
		Org string `path:"org"`
	}) (*orgMonitoringSettingsOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermOrgSettingsRead)
		if err != nil {
			return nil, err
		}
		v, err := deps.MonitorSettings.Get(ctx, scope.OrgID)
		if err != nil {
			return nil, mapMonitoringSettingsErr(ctx, err)
		}
		out := &orgMonitoringSettingsOutput{}
		out.Body.Settings = toOrgMonitoringSettingsDTO(v)
		return out, nil
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/settings/monitoring", func(ctx context.Context, in *struct {
		Org  string `path:"org"`
		Body orgMonitoringSettingsInputDTO
	}) (*orgMonitoringSettingsOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermOrgSettingsWrite)
		if err != nil {
			return nil, err
		}
		v, err := deps.MonitorSettings.Set(ctx, scope.OrgID, in.Body.update())
		if err != nil {
			return nil, mapMonitoringSettingsErr(ctx, err)
		}
		setAuditObject(ctx, "", "updated the organization's monitoring settings")
		out := &orgMonitoringSettingsOutput{}
		out.Body.Settings = toOrgMonitoringSettingsDTO(v)
		return out, nil
	}, withAudit("updateOrgMonitoringSettings", "org"))
}

// mapMonitoringSettingsErr converts the settings service's errors. The
// schema already bounds the values (422), so ErrInvalidInput only reaches
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
