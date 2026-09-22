// The org job settings routes (backlog 2.44): GET and PUT under
// /orgs/{org}/settings/jobs, the route family the SMTP settings and the
// weekly digest follow. The jobs service owns the setting (the enqueue
// paths consult it); the transport maps its own sentinels.
package api

import (
	"context"
	"errors"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type orgJobSettingsDTO struct {
	JobsRequirePinnedHosts bool `json:"jobs_require_pinned_hosts"`
}

type orgJobSettingsOutput struct {
	Body struct {
		Settings orgJobSettingsDTO `json:"settings"`
	}
}

// registerOrgSettingsRoutes wires the organization's job settings.
// org.settings.read is a viewer permission (a refusal message names the
// requirement, so it must be visible to everyone who can enqueue); the
// write is org.settings.write (admin), since it decides what jobs may run
// against at all.
func registerOrgSettingsRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/settings/jobs", func(ctx context.Context, in *struct {
		Org string `path:"org"`
	}) (*orgJobSettingsOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermOrgSettingsRead)
		if err != nil {
			return nil, err
		}
		st, err := deps.Jobs.OrgJobSettings(ctx, scope.OrgID)
		if err != nil {
			return nil, mapOrgSettingsErr(ctx, err)
		}
		out := &orgJobSettingsOutput{}
		out.Body.Settings = orgJobSettingsDTO{JobsRequirePinnedHosts: st.JobsRequirePinnedHosts}
		return out, nil
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/settings/jobs", func(ctx context.Context, in *struct {
		Org  string `path:"org"`
		Body struct {
			JobsRequirePinnedHosts bool `json:"jobs_require_pinned_hosts"`
		}
	}) (*orgJobSettingsOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermOrgSettingsWrite)
		if err != nil {
			return nil, err
		}
		if err := deps.Jobs.SetOrgJobSettings(ctx, scope.OrgID, jobs.OrgJobSettings{JobsRequirePinnedHosts: in.Body.JobsRequirePinnedHosts}); err != nil {
			return nil, mapOrgSettingsErr(ctx, err)
		}
		st, err := deps.Jobs.OrgJobSettings(ctx, scope.OrgID)
		if err != nil {
			return nil, mapOrgSettingsErr(ctx, err)
		}
		setAuditObject(ctx, "", "updated the organization's job settings")
		out := &orgJobSettingsOutput{}
		out.Body.Settings = orgJobSettingsDTO{JobsRequirePinnedHosts: st.JobsRequirePinnedHosts}
		return out, nil
	}, withAudit("updateOrgJobSettings", "org"))
}

// mapOrgSettingsErr converts the jobs errors the settings routes can
// produce. A store that does not carry the settings capability is a wiring
// gap: invalid input would mask it, so it stays unmapped (logged 500).
func mapOrgSettingsErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, jobs.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	default:
		slog.ErrorContext(ctx, "unmapped org settings error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
