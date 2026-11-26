package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type inventoryMemberDTO struct {
	GroupID   string         `json:"group_id"`
	GroupName string         `json:"group_name"`
	HostID    string         `json:"host_id"`
	HostName  string         `json:"host_name"`
	Vars      map[string]any `json:"vars"`
	Complete  bool           `json:"complete"`
	Problems  []problemDTO   `json:"problems"`
	Orphaned  []string       `json:"orphaned"`
	// AssistantKeys is decision D6's mark (spec 2026-11-25 assistant API
	// gaps).
	AssistantKeys []string `json:"assistant_keys" doc:"Keys of vars an assistant wrote: a run receives them as plain text (never templated) until a person saves the membership"`
	Revision      int64    `json:"revision"`
}

func toInventoryMemberDTO(m inventory.MemberView) inventoryMemberDTO {
	return inventoryMemberDTO{
		GroupID: m.GroupID.String(), GroupName: m.GroupName, HostID: m.HostID.String(), HostName: m.HostName,
		Vars: m.Vars, Complete: m.Complete, Problems: toProblemDTOs(m.Problems), Orphaned: nonNilStrings(m.Orphaned),
		AssistantKeys: nonNilStrings(m.AssistantKeys), Revision: m.Revision,
	}
}

type inventoryMemberListInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type inventoryMemberListOutput struct {
	Body struct {
		Members []inventoryMemberDTO `json:"members"`
	}
}

type inventoryMemberPutInput struct {
	Org     string `path:"org"`
	ID      string `path:"id"`
	Host    string `path:"host"`
	IfMatch string `header:"If-Match" doc:"The membership's ETag; optional"`
	Body    struct {
		Vars map[string]any `json:"vars" doc:"Host-scoped values and overrides. Secrets as for group values"`
	}
}

type inventoryMemberDeleteInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Host string `path:"host"`
}

type inventoryMemberOutput struct {
	ETag string `header:"ETag"`
	Body struct {
		Member inventoryMemberDTO `json:"member"`
	}
}

type previewInput struct {
	Org   string `path:"org"`
	Group string `query:"group" doc:"Render only the members of this group and its descendants"`
}

type previewOutput struct {
	Body struct {
		Inventory        map[string]any               `json:"inventory"`
		Connections      map[string]render.Connection `json:"connections"`
		Warnings         []render.Warning             `json:"warnings"`
		AssistantWritten []render.AssistantValue      `json:"assistant_written" doc:"The values the inventory carries as plain text (never templated) because an assistant wrote them: a group's own (no host) or a host's in that group; a person saving them makes them live"`
	}
}

type hostInventoryInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type hostGroupsOutput struct {
	Body struct {
		Groups []inventoryMemberDTO `json:"groups"`
	}
}

type hostPreviewOutput struct {
	Body struct {
		Vars       map[string]any     `json:"vars"`
		Sources    map[string]string  `json:"sources" doc:"Where each variable comes from: the name of the group that gave it, or \"host\" for the host's own (address, SSH settings, facts)"`
		Connection *render.Connection `json:"connection,omitempty"`
		Warnings   []render.Warning   `json:"warnings"`
		// AssistantWritten: decision D6 (spec 2026-11-25 assistant API gaps).
		AssistantWritten []string `json:"assistant_written" doc:"The variables whose value an assistant wrote: a run receives them as plain text (never templated) until a person saves them"`
	}
}

func nonNilWarnings(w []render.Warning) []render.Warning {
	if w == nil {
		return []render.Warning{}
	}
	return w
}

// registerInventoryMemberRoutes wires memberships, a host's groups and the
// masked inventory previews.
func registerInventoryMemberRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/inventory/groups/{id}/members", func(ctx context.Context, in *inventoryMemberListInput) (*inventoryMemberListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "group id")
		if err != nil {
			return nil, err
		}
		list, err := deps.Inventory.Members(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapInventoryErr(ctx, err, "")
		}
		out := &inventoryMemberListOutput{}
		out.Body.Members = make([]inventoryMemberDTO, len(list))
		for i, m := range list {
			out.Body.Members[i] = toInventoryMemberDTO(m)
		}
		return out, nil
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/inventory/groups/{id}/members/{host}", func(ctx context.Context, in *inventoryMemberPutInput) (*inventoryMemberOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryWrite)
		if err != nil {
			return nil, err
		}
		groupID, err := parseUUID(in.ID, "group id")
		if err != nil {
			return nil, err
		}
		hostID, err := parseUUID(in.Host, "host id")
		if err != nil {
			return nil, err
		}
		rev, err := optionalIfMatch(in.IfMatch)
		if err != nil {
			return nil, err
		}
		m, err := deps.Inventory.AssignBy(ctx, scope.OrgID, groupID, hostID, in.Body.Vars, rev, changeActor(scope))
		if err != nil {
			return nil, mapInventoryErr(ctx, err, "body.vars")
		}
		setAuditObject(ctx, groupID.String(), "assigned host "+m.HostName+" to inventory group "+m.GroupName)
		out := &inventoryMemberOutput{ETag: revisionETag(m.Revision)}
		out.Body.Member = toInventoryMemberDTO(m)
		return out, nil
	}, withAudit("assignInventoryHost", "inventory_membership"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/inventory/groups/{id}/members/{host}", func(ctx context.Context, in *inventoryMemberDeleteInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryWrite)
		if err != nil {
			return nil, err
		}
		groupID, err := parseUUID(in.ID, "group id")
		if err != nil {
			return nil, err
		}
		hostID, err := parseUUID(in.Host, "host id")
		if err != nil {
			return nil, err
		}
		if err := deps.Inventory.Unassign(ctx, scope.OrgID, groupID, hostID); err != nil {
			return nil, mapInventoryErr(ctx, err, "")
		}
		setAuditObject(ctx, groupID.String(), "removed host "+hostID.String()+" from the inventory group")
		return &okOutput{}, nil
	}, withAudit("unassignInventoryHost", "inventory_membership"))

	huma.Get(hapi, "/api/v1/orgs/{org}/inventory/preview", func(ctx context.Context, in *previewInput) (*previewOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryRead)
		if err != nil {
			return nil, err
		}
		gid, err := parseOptionalUUID(&in.Group, "group")
		if err != nil {
			return nil, err
		}
		inv, err := deps.Renderer.Preview(ctx, scope.OrgID, render.Scope{GroupID: gid})
		if err != nil {
			return nil, mapInventoryErr(ctx, err, "")
		}
		out := &previewOutput{}
		out.Body.Inventory, out.Body.Connections, out.Body.Warnings = inv.Doc, inv.Connections, nonNilWarnings(inv.Warnings)
		out.Body.AssistantWritten = inv.AssistantWritten
		if out.Body.AssistantWritten == nil {
			out.Body.AssistantWritten = []render.AssistantValue{}
		}
		return out, nil
	})

	huma.Get(hapi, "/api/v1/orgs/{org}/hosts/{id}/inventory/groups", func(ctx context.Context, in *hostInventoryInput) (*hostGroupsOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "host id")
		if err != nil {
			return nil, err
		}
		list, err := deps.Inventory.HostGroups(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapInventoryErr(ctx, err, "")
		}
		out := &hostGroupsOutput{}
		out.Body.Groups = make([]inventoryMemberDTO, len(list))
		for i, m := range list {
			out.Body.Groups[i] = toInventoryMemberDTO(m)
		}
		return out, nil
	})

	huma.Get(hapi, "/api/v1/orgs/{org}/hosts/{id}/inventory/preview", func(ctx context.Context, in *hostInventoryInput) (*hostPreviewOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "host id")
		if err != nil {
			return nil, err
		}
		p, err := deps.Renderer.HostPreview(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapInventoryErr(ctx, err, "")
		}
		out := &hostPreviewOutput{}
		out.Body.Vars, out.Body.Sources, out.Body.Connection, out.Body.Warnings = p.Vars, p.Sources, p.Connection, nonNilWarnings(p.Warnings)
		out.Body.AssistantWritten = nonNilStrings(p.AssistantWritten)
		return out, nil
	})
}
