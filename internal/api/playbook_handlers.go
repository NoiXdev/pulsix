package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type playbookDTO struct {
	ID        string    `json:"id"`
	Source    string    `json:"source" enum:"repo,editor"`
	RepoID    string    `json:"repo_id,omitempty"`
	Path      string    `json:"path,omitempty"`
	Inline    string    `json:"inline,omitempty"`
	Summary   string    `json:"summary"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toPlaybookDTO(p playbooks.Playbook) playbookDTO {
	dto := playbookDTO{
		ID: p.ID.String(), Source: p.Source, Summary: p.Summary(),
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
	if p.RepoID != nil {
		dto.RepoID = p.RepoID.String()
	}
	if p.Path != nil {
		dto.Path = *p.Path
	}
	if p.Inline != nil {
		dto.Inline = *p.Inline
	}
	return dto
}

// toPlaybookListDTO is the list form: the computed summary stands in for the
// document, so a listing never carries every playbook's full inline text.
func toPlaybookListDTO(p playbooks.Playbook) playbookDTO {
	dto := toPlaybookDTO(p)
	dto.Inline = ""
	return dto
}

type playbookListInput struct {
	Org string `path:"org"`
}

type playbookListOutput struct {
	Body struct {
		Playbooks []playbookDTO `json:"playbooks"`
	}
}

type playbookGetInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type playbookDetailOutput struct {
	Body struct {
		Playbook playbookDTO `json:"playbook"`
	}
}

type playbookCreateInput struct {
	Org  string `path:"org"`
	Body struct {
		// RepoID and Path make a repo playbook; Inline (with the optional
		// Name) makes an editor playbook. The two forms are mutually
		// exclusive, and the playbooks table's CHECK constraints are the
		// second, authoritative guard.
		RepoID string `json:"repo_id,omitempty"`
		Path   string `json:"path,omitempty"`
		Name   string `json:"name,omitempty" maxLength:"255"`
		Inline string `json:"inline,omitempty" doc:"Editor playbooks: the YAML document"`
	}
}

type playbookUpdateInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body struct {
		Name   string `json:"name,omitempty" maxLength:"255"`
		Inline string `json:"inline" doc:"Editor playbooks: the replacement YAML document"`
	}
}

// registerPlaybookRoutes wires the playbook registry: reads need
// playbooks.read (viewer), writes playbooks.write (operator). A repo
// playbook response carries its path, an editor playbook its inline text;
// summaries are computed, never the playbook's full contents in a list.
func registerPlaybookRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/playbooks", func(ctx context.Context, in *playbookListInput) (*playbookListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermPlaybooksRead)
		if err != nil {
			return nil, err
		}
		list, err := deps.Playbooks.List(ctx, scope.OrgID)
		if err != nil {
			return nil, mapPlaybooksErr(ctx, err)
		}
		out := &playbookListOutput{}
		out.Body.Playbooks = make([]playbookDTO, len(list))
		for i, p := range list {
			out.Body.Playbooks[i] = toPlaybookListDTO(p)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/playbooks", func(ctx context.Context, in *playbookCreateInput) (*playbookDetailOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermPlaybooksWrite)
		if err != nil {
			return nil, err
		}
		var p playbooks.Playbook
		if in.Body.RepoID != "" || in.Body.Path != "" {
			repoID, err := parseUUID(in.Body.RepoID, "repo id")
			if err != nil {
				return nil, err
			}
			p, err = deps.Playbooks.CreateRepo(ctx, scope.OrgID, repoID, in.Body.Path)
			if err != nil {
				return nil, mapPlaybooksErr(ctx, err)
			}
		} else {
			p, err = deps.Playbooks.CreateEditor(ctx, scope.OrgID, playbooks.CreateInput{Name: in.Body.Name, Inline: in.Body.Inline})
			if err != nil {
				return nil, mapPlaybooksErr(ctx, err)
			}
		}
		setAuditObject(ctx, p.ID.String(), playbookSummary(p, "created"))
		return playbookDetail(p), nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createPlaybook", "playbook"))

	huma.Get(hapi, "/api/v1/orgs/{org}/playbooks/{id}", func(ctx context.Context, in *playbookGetInput) (*playbookDetailOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermPlaybooksRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "playbook id")
		if err != nil {
			return nil, err
		}
		p, err := deps.Playbooks.Get(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapPlaybooksErr(ctx, err)
		}
		return playbookDetail(p), nil
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/playbooks/{id}", func(ctx context.Context, in *playbookUpdateInput) (*playbookDetailOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermPlaybooksWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "playbook id")
		if err != nil {
			return nil, err
		}
		p, err := deps.Playbooks.UpdateEditor(ctx, scope.OrgID, id, playbooks.CreateInput{Name: in.Body.Name, Inline: in.Body.Inline})
		if err != nil {
			return nil, mapPlaybooksErr(ctx, err)
		}
		setAuditObject(ctx, p.ID.String(), playbookSummary(p, "updated"))
		return playbookDetail(p), nil
	}, withAudit("updatePlaybook", "playbook"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/playbooks/{id}", func(ctx context.Context, in *playbookGetInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermPlaybooksWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "playbook id")
		if err != nil {
			return nil, err
		}
		if err := deps.Playbooks.Delete(ctx, scope.OrgID, id); err != nil {
			return nil, mapPlaybooksErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "deleted playbook")
		return &okOutput{}, nil
	}, withAudit("deletePlaybook", "playbook"))
}

func playbookDetail(p playbooks.Playbook) *playbookDetailOutput {
	out := &playbookDetailOutput{}
	out.Body.Playbook = toPlaybookDTO(p)
	return out
}

// playbookSummary builds the audit summary from the playbook's identity —
// repo path or computed summary — never the full inline text. Beyond the
// summary, playbook content never enters the audit trail.
func playbookSummary(p playbooks.Playbook, verb string) string {
	if p.Source == playbooks.SourceRepo && p.Path != nil {
		return verb + " repo playbook " + *p.Path
	}
	if s := p.Summary(); s != "" {
		return verb + " editor playbook " + s
	}
	return verb + " editor playbook"
}

// mapPlaybooksErr converts playbooks.Service sentinels to HTTP errors.
func mapPlaybooksErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, playbooks.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, playbooks.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, playbooks.ErrInUse):
		return huma.Error409Conflict("the playbook is still referenced by a job template")
	default:
		slog.ErrorContext(ctx, "unmapped playbooks error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
