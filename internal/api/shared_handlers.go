package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/shared"
)

// repoDefinitionDTO is the installation-owned half of a shared repository
// (spec v0.2f decision 2): name, url, branch and subpath, the fields every
// projection's own repoDTO (repo_handlers.go) echoes read-only once
// SharedRepoID is set. No credential, no vault identity — those are the
// projection's own, per spec decision 3.
type repoDefinitionDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Branch  string `json:"branch"`
	Subpath string `json:"subpath"`
}

func toRepoDefinitionDTO(d shared.RepoDefinition) repoDefinitionDTO {
	return repoDefinitionDTO{ID: d.ID.String(), Name: d.Name, URL: d.URL, Branch: d.Branch, Subpath: d.Subpath}
}

type repoDefinitionListOutput struct {
	Body struct {
		Definitions []repoDefinitionDTO `json:"definitions"`
	}
}

type repoDefinitionGetInput struct {
	ID string `path:"id"`
}

type repoDefinitionDetailOutput struct {
	Body struct {
		Definition repoDefinitionDTO `json:"definition"`
	}
}

// repoDefinitionBody is the editable shape both create and update take —
// the same full-replacement convention repoCreateInput/repoUpdateInput
// already use for an ordinary repository, and the same length bounds
// (shared.RepoDefinitionInput itself enforces none: Task 2's own report
// reads the absence of ErrInvalidInput from its sentinel list as a
// deliberate signal that format validation belongs at this schema layer).
type repoDefinitionBody struct {
	Name    string `json:"name" minLength:"1" maxLength:"63"`
	URL     string `json:"url" minLength:"1" maxLength:"500"`
	Branch  string `json:"branch,omitempty" maxLength:"100"`
	Subpath string `json:"subpath,omitempty" maxLength:"200"`
}

type repoDefinitionCreateInput struct {
	Body repoDefinitionBody
}

type repoDefinitionUpdateInput struct {
	ID   string `path:"id"`
	Body repoDefinitionBody
}

func repoDefinitionDetail(d shared.RepoDefinition) *repoDefinitionDetailOutput {
	out := &repoDefinitionDetailOutput{}
	out.Body.Definition = toRepoDefinitionDTO(d)
	return out
}

// registerSharedRepoRoutes wires the installation admin's own CRUD over
// repository definitions (spec v0.2f decision 8: no org_id, no per-org
// access at all — access is requireInstanceAdmin, the same gate
// registerSMTPSettingsRoutes and registerAdminRoutes already use for their
// own instance-level routes, and for the same reason: a bearer token has no
// per-org scope to narrow here, so it must never reach these routes
// regardless of the privilege its owner holds elsewhere).
//
// Every verb gets its own requireInstanceAdmin call rather than a group-
// level middleware, matching every other instance-level route file in this
// package (registerAdminRoutes, registerSMTPSettingsRoutes) — see the
// per-verb 403 test (TestRepoDefinitionRoutesRequireInstanceAdmin) that
// exists specifically because a group-level gate is easy to apply to only
// the first route registered under it and easy to forget on the rest.
//
// Registered on the root API (api.go's registerRoutes), not the
// bearer-enabled orgScoped group: these routes take no {org} in their path
// and requireInstanceAdmin itself (via requireInteractiveSession) already
// refuses a bearer token outright, the same reasoning
// registerAdminRoutes' own doc comment gives.
func registerSharedRepoRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/admin/repos/definitions", func(ctx context.Context, _ *struct{}) (*repoDefinitionListOutput, error) {
		if _, err := requireInstanceAdmin(ctx); err != nil {
			return nil, err
		}
		list, err := deps.Shared.ListRepos(ctx)
		if err != nil {
			return nil, mapSharedErr(ctx, deps, err)
		}
		out := &repoDefinitionListOutput{}
		out.Body.Definitions = make([]repoDefinitionDTO, len(list))
		for i, d := range list {
			out.Body.Definitions[i] = toRepoDefinitionDTO(d)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/admin/repos/definitions", func(ctx context.Context, in *repoDefinitionCreateInput) (*repoDefinitionDetailOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		d, err := deps.Shared.CreateRepo(ctx, shared.RepoDefinitionInput{
			Name: in.Body.Name, URL: in.Body.URL, Branch: in.Body.Branch, Subpath: in.Body.Subpath,
		})
		if err != nil {
			return nil, mapSharedErr(ctx, deps, err)
		}
		// No requireOrg here — there is no organization to resolve one
		// from, the same reasoning registerAdminRoutes/
		// registerSMTPSettingsRoutes give their own instance-level writes —
		// so the carrier's scope is filled in directly, with a nil org and
		// the instance admin as the actor.
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, d.ID.String(), "created shared repository definition "+d.Name)
		return repoDefinitionDetail(d), nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createRepoDefinition", "shared_repo"))

	huma.Get(hapi, "/api/v1/admin/repos/definitions/{id}", func(ctx context.Context, in *repoDefinitionGetInput) (*repoDefinitionDetailOutput, error) {
		if _, err := requireInstanceAdmin(ctx); err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "repo definition id")
		if err != nil {
			return nil, err
		}
		d, err := deps.Shared.GetRepo(ctx, id)
		if err != nil {
			return nil, mapSharedErr(ctx, deps, err)
		}
		return repoDefinitionDetail(d), nil
	})

	huma.Put(hapi, "/api/v1/admin/repos/definitions/{id}", func(ctx context.Context, in *repoDefinitionUpdateInput) (*repoDefinitionDetailOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "repo definition id")
		if err != nil {
			return nil, err
		}
		d, err := deps.Shared.UpdateRepo(ctx, id, shared.RepoDefinitionInput{
			Name: in.Body.Name, URL: in.Body.URL, Branch: in.Body.Branch, Subpath: in.Body.Subpath,
		})
		if err != nil {
			return nil, mapSharedErr(ctx, deps, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, d.ID.String(), "updated shared repository definition "+d.Name)
		return repoDefinitionDetail(d), nil
	}, withAudit("updateRepoDefinition", "shared_repo"))

	huma.Delete(hapi, "/api/v1/admin/repos/definitions/{id}", func(ctx context.Context, in *repoDefinitionGetInput) (*okOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "repo definition id")
		if err != nil {
			return nil, err
		}
		if err := deps.Shared.DeleteRepo(ctx, id); err != nil {
			return nil, mapSharedErr(ctx, deps, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, id.String(), "deleted shared repository definition")
		return &okOutput{}, nil
	}, withAudit("deleteRepoDefinition", "shared_repo"))
}

// mapSharedErr converts shared.Service sentinels into HTTP errors for the
// instance-admin-only repository-definition routes registered above. It is
// deliberately its own function, never merged into mapReposErr
// (repo_handlers.go) — every caller here already passed
// requireInstanceAdmin, which is what makes it safe for this one function to
// compose a sentence naming another organization (spec decision 7's
// collision message, decision 5's in-use message); mapReposErr backs the
// organization-scoped repository routes a member of ANY organization can
// reach, and must never gain a case that can do the same. See this
// package's TestOrgScopedRepoRoutesNeverLeakAnotherOrgsIdentity, which
// exists specifically to catch the two functions ever being merged or
// either message being composed somewhere an organization-scoped caller can
// reach it.
//
// shared.Service deliberately returns organization ids as data — Orgs
// []uuid.UUID on *NameCollisionError, OrgID on *InUseError (see their own
// doc comments, internal/shared/definition.go) — rather than a formatted
// string, precisely so the one layer allowed to show a name decides how.
// resolveOrgLabel/resolveOrgLabels do that resolution here, in the handler,
// never in shared.Service.
func mapSharedErr(ctx context.Context, deps Deps, err error) error {
	var coll *shared.NameCollisionError
	if errors.As(err, &coll) {
		return huma.Error409Conflict(fmt.Sprintf(
			"name %q is already used by %s", coll.Name, strings.Join(resolveOrgLabels(ctx, deps, coll.Orgs), ", "),
		))
	}
	var inUse *shared.InUseError
	if errors.As(err, &inUse) {
		label := resolveOrgLabel(ctx, deps, inUse.OrgID)
		if len(inUse.Reasons) == 0 {
			return huma.Error409Conflict(fmt.Sprintf("organization %s still has a dependant referencing this definition's projection", label))
		}
		return huma.Error409Conflict(fmt.Sprintf(
			"organization %s: %s still reference this definition's projection", label, strings.Join(inUse.Reasons, ", "),
		))
	}
	switch {
	case errors.Is(err, shared.ErrNotFound):
		return huma.Error404NotFound("not found")
	// Neither bare sentinel below ever carries an organization (that is
	// exactly what the richer *NameCollisionError/*InUseError forms above
	// are for — see their own doc comments on when the store returns the
	// plain sentinel instead), so passing their own Error() string through
	// is safe here the same way it would NOT be for the richer forms.
	case errors.Is(err, shared.ErrNameTaken):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, shared.ErrInUse):
		return huma.Error409Conflict(err.Error())
	default:
		slog.ErrorContext(ctx, "unmapped shared error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}

// resolveOrgLabel resolves id to the organization's own slug, for an
// instance-admin-facing message only (every caller of mapSharedErr has
// already passed requireInstanceAdmin). Falling back to the raw id on a
// lookup failure, rather than dropping the organization from the message or
// failing the whole request, is deliberate: an instance admin already sees
// every organization, so the id itself is not a leak here the way it would
// be from an organization-scoped route — it is simply a worse label than
// the slug this function exists to produce.
func resolveOrgLabel(ctx context.Context, deps Deps, id uuid.UUID) string {
	org, err := deps.Tenancy.GetOrgByID(ctx, id)
	if err != nil {
		slog.ErrorContext(ctx, "resolve organization slug for an instance-admin-facing error", "org_id", id, "err", err)
		return id.String()
	}
	return org.Slug
}

// resolveOrgLabels is resolveOrgLabel over a list, preserving order — order
// matters here only for a stable, testable message; CollidingOrgs
// (internal/shared/definition.go) already sorts ids, so this does not need
// to sort them again.
func resolveOrgLabels(ctx context.Context, deps Deps, ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = resolveOrgLabel(ctx, deps, id)
	}
	return out
}
