package api

import (
	"context"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// adminOrgDTO is one organization in the instance admin's list: any
// organization, not only the admin's own, so there is no role here.
type adminOrgDTO struct {
	ID          string    `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	MemberCount int       `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
	// ActorIsMember tells whether the administrator belongs to the
	// organization too, and so can open it.
	ActorIsMember bool `json:"actor_is_member"`
}

type adminOrgListOutput struct {
	Body struct {
		Orgs []adminOrgDTO `json:"orgs"`
	}
}

// registerAdminOrgRoutes wires the instance admin's list of every
// organization (backlog 7.74). Like the other admin routes it is on the
// root API, so a bearer token never reaches it, and gated by
// requireInstanceAdmin. It only reads, so it needs no fresh second factor.
func registerAdminOrgRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/admin/orgs", func(ctx context.Context, _ *struct{}) (*adminOrgListOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		orgs, err := deps.Tenancy.ListAllOrgs(ctx, p.InstanceAdmin, p.UserID)
		if err != nil {
			return nil, mapTenancyErr(ctx, err)
		}
		out := &adminOrgListOutput{}
		out.Body.Orgs = make([]adminOrgDTO, len(orgs))
		for i, o := range orgs {
			out.Body.Orgs[i] = adminOrgDTOOf(o)
		}
		return out, nil
	}, func(o *huma.Operation) { o.OperationID = "listAdminOrgs" })
}

func adminOrgDTOOf(o tenancy.OrgOverview) adminOrgDTO {
	return adminOrgDTO{
		ID: o.ID.String(), Slug: o.Slug, Name: o.Name, MemberCount: o.MemberCount,
		CreatedAt: o.CreatedAt, ActorIsMember: o.ActorIsMember,
	}
}
