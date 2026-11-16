package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/chain"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// ChainReader is the follow-up store the run detail and the dismiss route
// read through (chain.SQLStore).
type ChainReader interface {
	ListForGroup(ctx context.Context, orgID, groupID uuid.UUID) ([]chain.FollowUp, error)
	ParentOf(ctx context.Context, orgID, childGroupID uuid.UUID) (*chain.FollowUp, error)
	Dismiss(ctx context.Context, orgID, groupID uuid.UUID, reason string, now time.Time) (int, error)
}

// chainParentDTO is the run a chained run is the follow-up of.
type chainParentDTO struct {
	_            struct{} `nullable:"true"`
	GroupID      string   `json:"group_id"`
	FirstJobID   string   `json:"first_job_id" doc:"The parent run's first job, where its run view lives"`
	TemplateID   string   `json:"template_id,omitempty"`
	TemplateName string   `json:"template_name" doc:"empty when the template no longer exists"`
}

// chainFollowUpDTO is one follow-up of the run.
type chainFollowUpDTO struct {
	ID              string   `json:"id"`
	TemplateID      string   `json:"template_id"`
	TemplateName    string   `json:"template_name" doc:"empty when the template no longer exists"`
	State           string   `json:"state" enum:"pending,due,started,skipped,dismissed"`
	Depth           int      `json:"depth" doc:"How many runs deep the chain is at this follow-up (1 = follows the root run)"`
	Reason          string   `json:"reason" doc:"Why the follow-up was skipped, dismissed or waits; empty otherwise"`
	ChildFirstJobID string   `json:"child_first_job_id,omitempty" doc:"The follow-up run's first job, once it started"`
	Hosts           []string `json:"hosts" doc:"The hosts the follow-up runs on: the ones the parent run played on"`
}

// chainDTO is the run detail's chain block: always present.
type chainDTO struct {
	Parent    *chainParentDTO    `json:"parent" doc:"Set when this run is the follow-up of another run"`
	FollowUps []chainFollowUpDTO `json:"follow_ups" doc:"This run's follow-ups in start order"`
}

func toChainFollowUpDTO(f chain.FollowUp) chainFollowUpDTO {
	out := chainFollowUpDTO{
		ID: f.ID.String(), TemplateID: f.TemplateID.String(), TemplateName: f.TemplateName,
		State: string(f.State), Depth: f.Depth, Reason: f.Reason, Hosts: make([]string, len(f.HostIDs)),
	}
	for i, h := range f.HostIDs {
		out.Hosts[i] = h.String()
	}
	if f.ChildFirstJobID != nil {
		out.ChildFirstJobID = f.ChildFirstJobID.String()
	}
	return out
}

// runChain builds the chain block of the run detail for j's run group.
func runChain(ctx context.Context, deps Deps, orgID uuid.UUID, j jobs.Job) (chainDTO, error) {
	out := chainDTO{FollowUps: []chainFollowUpDTO{}}
	rows, err := deps.Chain.ListForGroup(ctx, orgID, j.RunGroupID)
	if err != nil {
		return out, fmt.Errorf("list follow-ups: %w", err)
	}
	for _, f := range rows {
		out.FollowUps = append(out.FollowUps, toChainFollowUpDTO(f))
	}
	parent, err := deps.Chain.ParentOf(ctx, orgID, j.RunGroupID)
	if err != nil {
		return out, fmt.Errorf("read parent follow-up: %w", err)
	}
	if parent == nil {
		return out, nil
	}
	sums, err := deps.Jobs.GroupSummaries(ctx, orgID, []uuid.UUID{parent.ParentGroupID})
	if err != nil {
		return out, fmt.Errorf("summarise parent run: %w", err)
	}
	sum, ok := sums[parent.ParentGroupID]
	if !ok {
		return out, nil
	}
	first, err := deps.Jobs.Get(ctx, orgID, sum.FirstJobID)
	if err != nil {
		return out, fmt.Errorf("read parent run: %w", err)
	}
	p := &chainParentDTO{GroupID: parent.ParentGroupID.String(), FirstJobID: first.ID.String(), TemplateName: first.TemplateName}
	if first.TemplateID != nil {
		p.TemplateID = first.TemplateID.String()
	}
	out.Parent = p
	return out, nil
}

type followUpsDismissOutput struct {
	Body struct {
		Dismissed int `json:"dismissed" doc:"How many pending follow-ups were dismissed"`
	}
}

// registerChainRoutes wires the follow-up routes of a run.
func registerChainRoutes(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/orgs/{org}/jobs/{id}/follow-ups/dismiss", func(ctx context.Context, in *jobGetInput) (*followUpsDismissOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermJobsRun)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "job id")
		if err != nil {
			return nil, err
		}
		j, err := deps.Jobs.Get(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapJobsErr(ctx, err)
		}
		sums, err := deps.Jobs.GroupSummaries(ctx, scope.OrgID, []uuid.UUID{j.RunGroupID})
		if err != nil {
			return nil, mapJobsErr(ctx, err)
		}
		if sum, ok := sums[j.RunGroupID]; !ok || sum.Status.Terminal() {
			return nil, huma.Error409Conflict("this run has finished; its follow-ups can no longer be dismissed")
		}
		n, err := deps.Chain.Dismiss(ctx, scope.OrgID, j.RunGroupID, "dismissed by "+actorName(ctx, deps), time.Now())
		if err != nil {
			return nil, mapJobsErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), fmt.Sprintf("dismissed %d pending follow-up(s) of job %s", n, id))
		out := &followUpsDismissOutput{}
		out.Body.Dismissed = n
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusOK }, withAudit("dismissFollowUps", "job"))
}

// actorName is the signed-in user's display name, falling back to the
// email and then to "a user" when the account cannot be read.
func actorName(ctx context.Context, deps Deps) string {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return "a user"
	}
	u, err := deps.Auth.Me(ctx, p)
	switch {
	case err != nil:
		return "a user"
	case u.DisplayName != "":
		return u.DisplayName
	case u.Email != "":
		return u.Email
	}
	return "a user"
}

// followUpNames lists the templates of a run's follow-up override for the
// audit summary: names where they read, ids otherwise; "none" for an empty
// override.
func followUpNames(ctx context.Context, deps Deps, orgID uuid.UUID, ids []uuid.UUID) string {
	if len(ids) == 0 {
		return "none"
	}
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = id.String()
		if t, err := deps.Templates.Get(ctx, orgID, id); err == nil {
			names[i] = t.Name
		}
	}
	return strings.Join(names, ", ")
}
