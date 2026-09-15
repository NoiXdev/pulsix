// The connection-test and host-key routes (spec §5, backlog 2.7c): an
// operator triggers a connection test for a managed host (the manager
// enqueues a connection_test job; the outcome flows back through the
// queue's finish path into the host's test history), reads the latest
// result, and pins or clears the host key — only ever pinning a
// fingerprint a test actually observed (you can only pin what you saw).
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// connectionTestStartOutput is the 202 body of POST .../connection-test:
// the job the test runs as, so the SPA can navigate to the run view.
type connectionTestStartOutput struct {
	Body struct {
		JobID string `json:"job_id"`
	}
}

type hostRouteInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

// hostTestDTO is the wire shape of one connection-test outcome.
type hostTestDTO struct {
	At           time.Time  `json:"at"`
	OK           bool       `json:"ok"`
	HostKey      string     `json:"host_key"`
	HostKeyState string     `json:"host_key_state"`
	Checks       []checkDTO `json:"checks"`
}

type checkDTO struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
	OK     bool   `json:"ok"`
}

func toHostTestDTO(t hosts.HostTest) hostTestDTO {
	out := hostTestDTO{At: t.At, OK: t.OK, HostKey: t.HostKey, HostKeyState: t.HostKeyState}
	if t.Checks != nil {
		out.Checks = make([]checkDTO, len(t.Checks))
		for i, c := range t.Checks {
			out.Checks[i] = checkDTO{Name: c.Name, Detail: c.Detail, OK: c.OK}
		}
	}
	return out
}

type hostTestOutput struct {
	Body struct {
		Test hostTestDTO `json:"test"`
	}
}

// hostKeyInput is the pin request: the fingerprint a test observed.
type hostKeyInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body struct {
		Fingerprint string `json:"fingerprint" minLength:"1" maxLength:"71" pattern:"^SHA256:[A-Za-z0-9+/]{43}$"`
	}
}

// registerHostopsRoutes wires the four host-operation routes under
// /orgs/{org}/hosts/{id}: connection-test start (enqueue) and read, and the
// host-key pin/clear. Reads gate on inventory.read, writes on
// inventory.write — the same convention the host SSH settings routes use.
func registerHostopsRoutes(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/orgs/{org}/hosts/{id}/connection-test", func(ctx context.Context, in *hostRouteInput) (*connectionTestStartOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryWrite)
		if err != nil {
			return nil, err
		}
		hostID, err := parseUUID(in.ID, "host id")
		if err != nil {
			return nil, err
		}
		actor := scope.UserID
		j, err := deps.Jobs.EnqueueHostJob(ctx, scope.OrgID, jobs.KindConnectionTest, []uuid.UUID{hostID}, nil, jobs.TriggerManual, &actor)
		if err != nil {
			return nil, mapHostopsErr(ctx, err)
		}
		setAuditObject(ctx, j.ID.String(), "started connection test for host "+hostID.String())
		out := &connectionTestStartOutput{}
		out.Body.JobID = j.ID.String()
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusAccepted }, withAudit("startConnectionTest", "host_connection_test"))

	huma.Get(hapi, "/api/v1/orgs/{org}/hosts/{id}/connection-test", func(ctx context.Context, in *hostRouteInput) (*hostTestOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryRead)
		if err != nil {
			return nil, err
		}
		hostID, err := parseUUID(in.ID, "host id")
		if err != nil {
			return nil, err
		}
		test, err := deps.Hosts.LatestTest(ctx, scope.OrgID, hostID)
		if err != nil {
			return nil, mapHostopsErr(ctx, err)
		}
		out := &hostTestOutput{}
		out.Body.Test = toHostTestDTO(test)
		return out, nil
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/hosts/{id}/host-key", func(ctx context.Context, in *hostKeyInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryWrite)
		if err != nil {
			return nil, err
		}
		hostID, err := parseUUID(in.ID, "host id")
		if err != nil {
			return nil, err
		}
		// The host is resolved first: a host of another organization is as
		// invisible as one that does not exist (404), before anything about
		// its test history is asked.
		if _, err := deps.Hosts.GetHost(ctx, scope.OrgID, hostID); err != nil {
			return nil, mapHostopsErr(ctx, err)
		}
		// Only pin what you saw: the pin must equal the fingerprint the
		// latest test observed. A pin made from a value the manager never
		// saw would blind trust-on-first-use — the mismatch case this
		// feature exists to catch.
		test, err := deps.Hosts.LatestTest(ctx, scope.OrgID, hostID)
		if err != nil {
			if errors.Is(err, hosts.ErrNotFound) {
				return nil, huma.Error422UnprocessableEntity("the host has no connection test yet; pin the fingerprint a test observed")
			}
			return nil, mapHostopsErr(ctx, err)
		}
		if test.HostKey == "" || test.HostKey != in.Body.Fingerprint {
			return nil, huma.Error422UnprocessableEntity("the fingerprint does not match the latest connection test result; only the observed key can be pinned")
		}
		if err := deps.Hosts.SetHostKey(ctx, scope.OrgID, hostID, in.Body.Fingerprint); err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		setAuditObject(ctx, hostID.String(), "pinned host key")
		return &okOutput{}, nil
	}, withAudit("setHostKey", "host"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/hosts/{id}/host-key", func(ctx context.Context, in *hostRouteInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryWrite)
		if err != nil {
			return nil, err
		}
		hostID, err := parseUUID(in.ID, "host id")
		if err != nil {
			return nil, err
		}
		if err := deps.Hosts.ClearHostKey(ctx, scope.OrgID, hostID); err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		setAuditObject(ctx, hostID.String(), "cleared host key pin")
		return &okOutput{}, nil
	}, withAudit("clearHostKey", "host"))
}

// mapHostopsErr converts the jobs and hosts errors the two write routes
// can produce. A host that does not exist — or exists in another
// organization — is 404, exactly like every other org-scoped resource; a
// malformed input is 400; anything else is logged server-side and 500.
func mapHostopsErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, jobs.ErrNotFound) || errors.Is(err, hosts.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, jobs.ErrInvalidInput) || errors.Is(err, hosts.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	default:
		slog.ErrorContext(ctx, "unmapped hostops error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
