package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type repoDTO struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	URL           string     `json:"url"`
	Branch        string     `json:"branch"`
	Subpath       string     `json:"subpath"`
	CredentialID  string     `json:"credential_id,omitempty"`
	LastSync      *time.Time `json:"last_sync,omitempty"`
	LastSyncError string     `json:"last_sync_error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func toRepoDTO(r repos.Repo) repoDTO {
	dto := repoDTO{
		ID: r.ID.String(), Name: r.Name, URL: r.URL, Branch: r.Branch, Subpath: r.Subpath,
		LastSync: r.LastSync, LastSyncError: r.LastSyncError,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.CredentialID != nil {
		dto.CredentialID = r.CredentialID.String()
	}
	return dto
}

// jobRefDTO is the queued job a sync action returns: enough to poll the
// job's state, never its payload.
type jobRefDTO struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	ZoneID     string    `json:"zone_id"`
	RunGroupID string    `json:"run_group_id"`
	Trigger    string    `json:"trigger"`
	CreatedAt  time.Time `json:"created_at"`
}

func toJobRefDTO(j jobs.Job) jobRefDTO {
	return jobRefDTO{
		ID: j.ID.String(), Kind: string(j.Kind), Status: string(j.Status),
		ZoneID: j.ZoneID.String(), RunGroupID: j.RunGroupID.String(),
		Trigger: j.Trigger, CreatedAt: j.CreatedAt,
	}
}

type repoListInput struct {
	Org string `path:"org"`
}

type repoListOutput struct {
	Body struct {
		Repos []repoDTO `json:"repos"`
	}
}

type repoGetInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type repoDetailOutput struct {
	Body struct {
		Repo repoDTO `json:"repo"`
	}
}

type repoCreateInput struct {
	Org  string `path:"org"`
	Body struct {
		Name         string `json:"name" minLength:"1" maxLength:"63"`
		URL          string `json:"url" minLength:"1" maxLength:"500"`
		Branch       string `json:"branch,omitempty" maxLength:"100"`
		Subpath      string `json:"subpath,omitempty" maxLength:"200"`
		CredentialID string `json:"credential_id,omitempty"`
	}
}

type repoUpdateInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body struct {
		Name         string `json:"name" minLength:"1" maxLength:"63"`
		URL          string `json:"url" minLength:"1" maxLength:"500"`
		Branch       string `json:"branch,omitempty" maxLength:"100"`
		Subpath      string `json:"subpath,omitempty" maxLength:"200"`
		CredentialID string `json:"credential_id,omitempty"`
	}
}

// registerRepoRoutes wires the git repository registry: reads need
// repos.read (viewer), writes repos.write (operator). Responses carry the
// registry row; the credential reference is an id, never secret material,
// and no response ever carries playbook content.
func registerRepoRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/repos", func(ctx context.Context, in *repoListInput) (*repoListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermReposRead)
		if err != nil {
			return nil, err
		}
		list, err := deps.Repos.List(ctx, scope.OrgID)
		if err != nil {
			return nil, mapReposErr(ctx, err)
		}
		out := &repoListOutput{}
		out.Body.Repos = make([]repoDTO, len(list))
		for i, r := range list {
			out.Body.Repos[i] = toRepoDTO(r)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/repos", func(ctx context.Context, in *repoCreateInput) (*repoDetailOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermReposWrite)
		if err != nil {
			return nil, err
		}
		credentialID, err := optionalUUID(in.Body.CredentialID, "credential id")
		if err != nil {
			return nil, err
		}
		r, err := deps.Repos.Create(ctx, scope.OrgID, repos.CreateInput{
			Name: in.Body.Name, URL: in.Body.URL, Branch: in.Body.Branch,
			Subpath: in.Body.Subpath, CredentialID: credentialID,
		})
		if err != nil {
			return nil, mapReposErr(ctx, err)
		}
		setAuditObject(ctx, r.ID.String(), "created repo "+r.Name)
		return repoDetail(r), nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createRepo", "repo"))

	huma.Get(hapi, "/api/v1/orgs/{org}/repos/{id}", func(ctx context.Context, in *repoGetInput) (*repoDetailOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermReposRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "repo id")
		if err != nil {
			return nil, err
		}
		r, err := deps.Repos.Get(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapReposErr(ctx, err)
		}
		return repoDetail(r), nil
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/repos/{id}", func(ctx context.Context, in *repoUpdateInput) (*repoDetailOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermReposWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "repo id")
		if err != nil {
			return nil, err
		}
		credentialID, err := optionalUUID(in.Body.CredentialID, "credential id")
		if err != nil {
			return nil, err
		}
		r, err := deps.Repos.Update(ctx, scope.OrgID, id, repos.UpdateInput{
			Name: in.Body.Name, URL: in.Body.URL, Branch: in.Body.Branch,
			Subpath: in.Body.Subpath, CredentialID: credentialID,
		})
		if err != nil {
			return nil, mapReposErr(ctx, err)
		}
		setAuditObject(ctx, r.ID.String(), "updated repo "+r.Name)
		return repoDetail(r), nil
	}, withAudit("updateRepo", "repo"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/repos/{id}", func(ctx context.Context, in *repoGetInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermReposWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "repo id")
		if err != nil {
			return nil, err
		}
		if err := deps.Repos.Delete(ctx, scope.OrgID, id); err != nil {
			return nil, mapReposErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "deleted repo")
		return &okOutput{}, nil
	}, withAudit("deleteRepo", "repo"))

	huma.Post(hapi, "/api/v1/orgs/{org}/repos/{id}/sync", func(ctx context.Context, in *repoGetInput) (*repoSyncOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermReposWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "repo id")
		if err != nil {
			return nil, err
		}
		actor := scope.UserID
		job, err := deps.Repos.Sync(ctx, scope.OrgID, id, &actor)
		if err != nil {
			return nil, mapReposErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "requested sync of repo")
		return repoSync(job), nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusAccepted }, withAudit("syncRepo", "repo"))
}

type repoSyncOutput struct {
	Body struct {
		Job jobRefDTO `json:"job"`
	}
}

func repoDetail(r repos.Repo) *repoDetailOutput {
	out := &repoDetailOutput{}
	out.Body.Repo = toRepoDTO(r)
	return out
}

func repoSync(j jobs.Job) *repoSyncOutput {
	out := &repoSyncOutput{}
	out.Body.Job = toJobRefDTO(j)
	return out
}

// mapReposErr converts repos.Service sentinels (and the sync path's wrapped
// jobs errors) to HTTP errors; anything else is logged and replaced with a
// generic 500.
func mapReposErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, repos.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, repos.ErrNotFound), errors.Is(err, jobs.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, repos.ErrNameTaken):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, repos.ErrInUse):
		return huma.Error409Conflict("the repository is still referenced by a playbook")
	case errors.Is(err, jobs.ErrNoZone):
		return huma.Error409Conflict("the organization has no default zone, so nothing can be synced yet")
	default:
		slog.ErrorContext(ctx, "unmapped repos error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
