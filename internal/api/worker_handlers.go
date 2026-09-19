package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/tenancy"
	"github.com/noixdev/pulsix/internal/workers"
)

type workerDTO struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	ZoneID        string     `json:"zone_id"`
	ZoneSlug      string     `json:"zone_slug"`
	Status        string     `json:"status" enum:"online,offline,revoked" doc:"online = seen within 2 minutes"`
	LastSeen      *time.Time `json:"last_seen,omitempty"`
	Version       string     `json:"version,omitempty"`
	CertExpiresAt *time.Time `json:"cert_expires_at,omitempty"`
	CurrentJobID  string     `json:"current_job_id,omitempty"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

type workerEnrollmentDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	ZoneID    string    `json:"zone_id"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

type workerListInput struct {
	Org string `path:"org"`
}

type workerListOutput struct {
	Body struct {
		Workers []workerDTO `json:"workers"`
	}
}

type workerIDInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type workerRevokeOutput struct {
	Body struct {
		Worker        workerDTO `json:"worker"`
		StreamsClosed int       `json:"streams_closed"`
		JobsRequeued  int       `json:"jobs_requeued"`
		JobsEnded     int       `json:"jobs_ended"`
	}
}

type enrollmentListOutput struct {
	Body struct {
		Enrollments []workerEnrollmentDTO `json:"enrollments"`
	}
}

type enrollmentCreateInput struct {
	Org  string `path:"org"`
	Body struct {
		ZoneID string `json:"zone_id" format:"uuid"`
		Name   string `json:"name" minLength:"1" maxLength:"63"`
	}
}

type enrollmentCreateOutput struct {
	Body struct {
		Enrollment     workerEnrollmentDTO `json:"enrollment"`
		Token          string              `json:"token" doc:"Shown once; valid for one hour and one enrollment"`
		DockerSnippet  string              `json:"docker_snippet"`
		ComposeSnippet string              `json:"compose_snippet"`
	}
}

func toWorkerDTO(v workers.View) workerDTO {
	dto := workerDTO{
		ID: v.ID.String(), Name: v.Name, ZoneID: v.ZoneID.String(), ZoneSlug: v.ZoneSlug, LastSeen: v.LastSeen,
		Version: v.Version, CertExpiresAt: v.CertExpiresAt, RevokedAt: v.RevokedAt, CreatedAt: v.CreatedAt,
	}
	switch {
	case v.Status == workers.StatusRevoked:
		dto.Status = "revoked"
	case v.Online:
		dto.Status = "online"
	default:
		dto.Status = "offline"
	}
	if v.CurrentJobID != nil {
		dto.CurrentJobID = v.CurrentJobID.String()
	}
	return dto
}

func toEnrollmentDTO(e workers.Enrollment) workerEnrollmentDTO {
	return workerEnrollmentDTO{ID: e.ID.String(), Name: e.Name, ZoneID: e.ZoneID.String(), ExpiresAt: e.ExpiresAt, CreatedAt: e.CreatedAt}
}

// registerWorkerRoutes wires the Workers page (spec 2026-09-18 §4):
// workers.read (admin) to see, workers.write (admin) to add, revoke and
// delete. Tokens are shown once, in the create response only.
func registerWorkerRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/workers", func(ctx context.Context, in *workerListInput) (*workerListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermWorkersRead)
		if err != nil {
			return nil, err
		}
		list, err := deps.Workers.ListView(ctx, scope.OrgID)
		if err != nil {
			return nil, mapWorkersErr(ctx, err)
		}
		out := &workerListOutput{}
		out.Body.Workers = make([]workerDTO, len(list))
		for i, v := range list {
			out.Body.Workers[i] = toWorkerDTO(v)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/workers/{id}/revoke", func(ctx context.Context, in *workerIDInput) (*workerRevokeOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermWorkersWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "worker id")
		if err != nil {
			return nil, err
		}
		w, res, err := deps.Workers.Revoke(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapWorkersErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), fmt.Sprintf("revoked worker %s (%d stream(s) closed, %d job(s) requeued, %d ended)", w.Name, res.Streams, res.Requeued, res.Ended))
		out := &workerRevokeOutput{}
		out.Body.Worker = toWorkerDTO(workers.View{Worker: w})
		out.Body.StreamsClosed, out.Body.JobsRequeued, out.Body.JobsEnded = res.Streams, res.Requeued, res.Ended
		return out, nil
	}, withAudit("revokeWorker", "worker"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/workers/{id}", func(ctx context.Context, in *workerIDInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermWorkersWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "worker id")
		if err != nil {
			return nil, err
		}
		if err := deps.Workers.Delete(ctx, scope.OrgID, id); err != nil {
			return nil, mapWorkersErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "deleted revoked worker")
		return &okOutput{}, nil
	}, withAudit("deleteWorker", "worker"))

	huma.Get(hapi, "/api/v1/orgs/{org}/worker-enrollments", func(ctx context.Context, in *workerListInput) (*enrollmentListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermWorkersRead)
		if err != nil {
			return nil, err
		}
		list, err := deps.Workers.ListEnrollments(ctx, scope.OrgID)
		if err != nil {
			return nil, mapWorkersErr(ctx, err)
		}
		out := &enrollmentListOutput{}
		out.Body.Enrollments = make([]workerEnrollmentDTO, len(list))
		for i, e := range list {
			out.Body.Enrollments[i] = toEnrollmentDTO(e)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/worker-enrollments", func(ctx context.Context, in *enrollmentCreateInput) (*enrollmentCreateOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermWorkersWrite)
		if err != nil {
			return nil, err
		}
		if deps.WorkerEndpoint.Addr == "" {
			return nil, huma.Error409Conflict("the worker endpoint is disabled (PULSIX_GRPC_LISTEN)")
		}
		zoneID, err := parseUUID(in.Body.ZoneID, "zone id")
		if err != nil {
			return nil, err
		}
		actor := scope.UserID
		e, token, err := deps.Workers.CreateEnrollment(ctx, scope.OrgID, &actor, zoneID, in.Body.Name)
		if err != nil {
			return nil, mapWorkersErr(ctx, err)
		}
		setAuditObject(ctx, e.ID.String(), "created an enrollment token for worker "+e.Name)
		out := &enrollmentCreateOutput{}
		out.Body.Enrollment, out.Body.Token = toEnrollmentDTO(e), token
		out.Body.DockerSnippet, out.Body.ComposeSnippet = workerSnippets(deps.WorkerEndpoint, e.Name, token, deps.Version)
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createWorkerEnrollment", "worker_enrollment"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/worker-enrollments/{id}", func(ctx context.Context, in *workerIDInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermWorkersWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "enrollment id")
		if err != nil {
			return nil, err
		}
		if err := deps.Workers.WithdrawEnrollment(ctx, scope.OrgID, id); err != nil {
			return nil, mapWorkersErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "withdrew a worker enrollment token")
		return &okOutput{}, nil
	}, withAudit("withdrawWorkerEnrollment", "worker_enrollment"))
}

func mapWorkersErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, workers.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, workers.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, workers.ErrNameTaken):
		return huma.Error409Conflict("a worker with this name already exists")
	case errors.Is(err, workers.ErrNotRevoked):
		return huma.Error409Conflict("revoke the worker before deleting it")
	}
	slog.ErrorContext(ctx, "unmapped workers error", "err", err)
	return huma.Error500InternalServerError("internal error")
}
