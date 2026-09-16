// The ad-hoc command route (spec §7, backlog 2.14): an operator with
// jobs.run runs one shell command on explicit hosts. The manager enqueues
// one adhoc job per zone the targets live in (all sharing a run group, the
// same shape a template run takes), the worker materializes the inline
// one-task playbook from the payload (internal/worker/runner/playbook.go)
// and the run view shows the group.
//
// Audit semantics: the event's summary carries the module, the host count
// and the argv — the operator typed the command into their own
// organization's UI, so it is not secret material; recording it is the
// traceability the audit trail exists for (the one-time bootstrap login,
// by contrast, is never audited for exactly this asymmetry).
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// adhocInput is the run-command request: the explicit target hosts, the
// module (v0.2b pins shell; the field exists so v0.3 can extend) and the
// command. The body bounds the argv at 8 KiB with huma, the same bound the
// jobs service and the runner defend; a schema violation comes back 422
// with the offending value redacted (redactErrorValues).
type adhocInput struct {
	Org  string `path:"org"`
	Body struct {
		HostIDs []string `json:"host_ids" minItems:"1" doc:"The ids of the managed hosts to run on"`
		Module  string   `json:"module" doc:"Only shell is supported in v0.2b"`
		Argv    string   `json:"argv" minLength:"1" maxLength:"8192" doc:"The shell command, at most 8 KiB"`
	}
}

// registerAdhocRoute wires POST /orgs/{org}/hosts/command. It gates on
// jobs.run (operator) — running a command is a run action, not an
// inventory write — and is mounted on the bearer-enabled group in api.go
// like every other org-scoped route.
func registerAdhocRoute(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/orgs/{org}/hosts/command", func(ctx context.Context, in *adhocInput) (*jobStartOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermJobsRun)
		if err != nil {
			return nil, err
		}
		// The module check is the API's own 422 (the service re-checks
		// it): v0.2b's vocabulary is shell, and the field exists so v0.3
		// can extend without a schema change.
		if in.Body.Module != jobs.AdhocModule {
			return nil, huma.Error422UnprocessableEntity(fmt.Sprintf("module %q is not supported; shell is the only ad-hoc module in v0.2b", in.Body.Module))
		}
		hostIDs := make([]uuid.UUID, 0, len(in.Body.HostIDs))
		for i, raw := range in.Body.HostIDs {
			id, err := parseUUID(raw, fmt.Sprintf("host_ids[%d]", i))
			if err != nil {
				return nil, err
			}
			hostIDs = append(hostIDs, id)
		}
		actor := scope.UserID
		created, err := deps.Jobs.EnqueueAdhoc(ctx, scope.OrgID, hostIDs, in.Body.Module, in.Body.Argv, &actor)
		if err != nil {
			return nil, mapAdhocErr(ctx, err)
		}
		// The event's object is the run group (every per-zone job carries
		// it), and the summary names the module, the host count and the
		// command itself — operator-provided, not secret material.
		setAuditObject(ctx, created[0].RunGroupID.String(),
			fmt.Sprintf("ran ad-hoc %s command on %d host(s): %s", in.Body.Module, len(in.Body.HostIDs), in.Body.Argv))
		return jobStart(created), nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusAccepted }, withAudit("runAdhocCommand", "host"))
}

// mapAdhocErr converts the jobs errors the ad-hoc route can produce.
// Unlike the run view's mapper it answers 422 for validation failures —
// the module/argv/host list is the operator's own command shape, the same
// class huma's body validation reports — while a target that does not
// exist, exists in another organization, or is not managed, is 404: the
// route's targets are hosts, and an unusable host is as invisible as an
// unknown one.
func mapAdhocErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, jobs.ErrInvalidInput):
		return huma.Error422UnprocessableEntity(err.Error())
	default:
		slog.ErrorContext(ctx, "unmapped adhoc error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
