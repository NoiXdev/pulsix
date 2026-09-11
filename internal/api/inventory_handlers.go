package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type problemDTO struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func toProblemDTOs(ps []schema.Problem) []problemDTO {
	out := make([]problemDTO, len(ps))
	for i, p := range ps {
		out[i] = problemDTO{Path: p.Path, Message: p.Message}
	}
	return out
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

type groupSummaryDTO struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	ParentID    *string `json:"parent_id,omitempty"`
	BastionID   *string `json:"bastion_id,omitempty"`
	Members     int     `json:"members"`
	GroupFields int     `json:"group_fields"`
	HostFields  int     `json:"host_fields"`
	Complete    bool    `json:"complete" doc:"The group's own values satisfy its schema"`
	Revision    int64   `json:"revision"`
}

// groupDTO's schema is schema.Schema itself: spec §6 defines that JSON as the
// contract the SPA renders forms from, so the API does not re-shape it.
type groupDTO struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	ParentID    *string        `json:"parent_id,omitempty"`
	BastionID   *string        `json:"bastion_id,omitempty"`
	Revision    int64          `json:"revision"`
	Schema      schema.Schema  `json:"schema"`
	Vars        map[string]any `json:"vars" doc:"Group-scoped values; secrets read as {\"$secret\": \"set\"}"`
	Problems    []problemDTO   `json:"problems"`
	Orphaned    []string       `json:"orphaned" doc:"Stored keys the schema no longer declares; the next save drops them"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

func toGroupDTO(v inventory.GroupView) groupDTO {
	return groupDTO{
		ID: v.ID.String(), Name: v.Name, Description: v.Description, ParentID: optionalString(v.ParentID),
		BastionID: optionalString(v.BastionID), Revision: v.Revision, Schema: v.Schema, Vars: v.Vars,
		Problems: toProblemDTOs(v.Problems), Orphaned: nonNilStrings(v.Orphaned), CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
	}
}

func toGroupSummaryDTO(s inventory.GroupSummary) groupSummaryDTO {
	return groupSummaryDTO{
		ID: s.ID.String(), Name: s.Name, Description: s.Description, ParentID: optionalString(s.ParentID),
		BastionID: optionalString(s.BastionID), Members: s.Members, GroupFields: len(s.Schema.GroupFields()),
		HostFields: len(s.Schema.HostFields()), Complete: len(s.Problems) == 0, Revision: s.Revision,
	}
}

type groupListInput struct {
	Org string `path:"org"`
}

type groupListOutput struct {
	Body struct {
		Groups []groupSummaryDTO `json:"groups"`
	}
}

type groupOutput struct {
	ETag string `header:"ETag"`
	Body struct {
		Group groupDTO `json:"group"`
	}
}

func newGroupOutput(v inventory.GroupView) *groupOutput {
	out := &groupOutput{ETag: revisionETag(v.Revision)}
	out.Body.Group = toGroupDTO(v)
	return out
}

type groupCreateInput struct {
	Org  string `path:"org"`
	Body struct {
		Name        string  `json:"name" minLength:"1" maxLength:"63"`
		Description string  `json:"description,omitempty" maxLength:"1024"`
		ParentID    *string `json:"parent_id,omitempty"`
	}
}

type groupGetInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type groupUpdateInput struct {
	Org     string `path:"org"`
	ID      string `path:"id"`
	IfMatch string `header:"If-Match" doc:"The group's ETag; required"`
	Body    struct {
		Name        string  `json:"name" minLength:"1" maxLength:"63"`
		Description string  `json:"description,omitempty" maxLength:"1024"`
		ParentID    *string `json:"parent_id,omitempty" doc:"Omit to make the group a root"`
		BastionID   *string `json:"bastion_id,omitempty" doc:"Omit to clear the group's jump host"`
	}
}

type groupSchemaInput struct {
	Org     string `path:"org"`
	ID      string `path:"id"`
	IfMatch string `header:"If-Match" doc:"The group's ETag; required"`
	Body    struct {
		Schema schema.Schema `json:"schema"`
	}
}

type groupSchemaOutput struct {
	ETag string `header:"ETag"`
	Body struct {
		Group             groupDTO `json:"group"`
		IncompleteMembers int      `json:"incomplete_members"`
	}
}

type groupVarsInput struct {
	Org     string `path:"org"`
	ID      string `path:"id"`
	IfMatch string `header:"If-Match" doc:"The group's ETag; required"`
	Body    struct {
		Vars map[string]any `json:"vars" doc:"Group-scoped values. Secrets: a string sets a new value, {\"$secret\": \"keep\", \"from\": \"<path>\"} keeps the stored one"`
	}
}

// registerInventoryRoutes wires inventory groups. Every write to a group
// needs If-Match with the group's ETag (spec §15).
func registerInventoryRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/inventory/groups", func(ctx context.Context, in *groupListInput) (*groupListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryRead)
		if err != nil {
			return nil, err
		}
		list, err := deps.Inventory.ListGroups(ctx, scope.OrgID)
		if err != nil {
			return nil, mapInventoryErr(ctx, err, "")
		}
		out := &groupListOutput{}
		out.Body.Groups = make([]groupSummaryDTO, len(list))
		for i, g := range list {
			out.Body.Groups[i] = toGroupSummaryDTO(g)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/inventory/groups", func(ctx context.Context, in *groupCreateInput) (*groupOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryWrite)
		if err != nil {
			return nil, err
		}
		parent, err := parseOptionalUUID(in.Body.ParentID, "parent_id")
		if err != nil {
			return nil, err
		}
		g, err := deps.Inventory.CreateGroup(ctx, scope.OrgID, inventory.GroupInput{Name: in.Body.Name, Description: in.Body.Description, ParentID: parent})
		if err != nil {
			return nil, mapInventoryErr(ctx, err, "")
		}
		setAuditObject(ctx, g.ID.String(), "created inventory group "+g.Name)
		return newGroupOutput(g), nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createInventoryGroup", "inventory_group"))

	huma.Get(hapi, "/api/v1/orgs/{org}/inventory/groups/{id}", func(ctx context.Context, in *groupGetInput) (*groupOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "group id")
		if err != nil {
			return nil, err
		}
		g, err := deps.Inventory.GetGroup(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapInventoryErr(ctx, err, "")
		}
		return newGroupOutput(g), nil
	})

	huma.Patch(hapi, "/api/v1/orgs/{org}/inventory/groups/{id}", func(ctx context.Context, in *groupUpdateInput) (*groupOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "group id")
		if err != nil {
			return nil, err
		}
		rev, err := requireIfMatch(in.IfMatch)
		if err != nil {
			return nil, err
		}
		parent, err := parseOptionalUUID(in.Body.ParentID, "parent_id")
		if err != nil {
			return nil, err
		}
		bastion, err := parseOptionalUUID(in.Body.BastionID, "bastion_id")
		if err != nil {
			return nil, err
		}
		g, err := deps.Inventory.UpdateGroup(ctx, scope.OrgID, id, rev, inventory.GroupPatch{
			Name: in.Body.Name, Description: in.Body.Description, ParentID: parent, BastionID: bastion,
		})
		if err != nil {
			return nil, mapInventoryErr(ctx, err, "")
		}
		setAuditObject(ctx, g.ID.String(), "updated inventory group "+g.Name)
		return newGroupOutput(g), nil
	}, withAudit("updateInventoryGroup", "inventory_group"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/inventory/groups/{id}", func(ctx context.Context, in *groupGetInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "group id")
		if err != nil {
			return nil, err
		}
		if err := deps.Inventory.DeleteGroup(ctx, scope.OrgID, id); err != nil {
			return nil, mapInventoryErr(ctx, err, "")
		}
		setAuditObject(ctx, id.String(), "deleted inventory group")
		return &okOutput{}, nil
	}, withAudit("deleteInventoryGroup", "inventory_group"))

	huma.Put(hapi, "/api/v1/orgs/{org}/inventory/groups/{id}/schema", func(ctx context.Context, in *groupSchemaInput) (*groupSchemaOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "group id")
		if err != nil {
			return nil, err
		}
		rev, err := requireIfMatch(in.IfMatch)
		if err != nil {
			return nil, err
		}
		g, incomplete, err := deps.Inventory.SetSchema(ctx, scope.OrgID, id, rev, in.Body.Schema)
		if err != nil {
			return nil, mapInventoryErr(ctx, err, "body.schema")
		}
		setAuditObject(ctx, g.ID.String(), "changed the variable schema of inventory group "+g.Name)
		out := &groupSchemaOutput{ETag: revisionETag(g.Revision)}
		out.Body.Group, out.Body.IncompleteMembers = toGroupDTO(g), incomplete
		return out, nil
	}, withAudit("setInventoryGroupSchema", "inventory_group"))

	huma.Put(hapi, "/api/v1/orgs/{org}/inventory/groups/{id}/vars", func(ctx context.Context, in *groupVarsInput) (*groupOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "group id")
		if err != nil {
			return nil, err
		}
		rev, err := requireIfMatch(in.IfMatch)
		if err != nil {
			return nil, err
		}
		g, err := deps.Inventory.SetVars(ctx, scope.OrgID, id, rev, in.Body.Vars)
		if err != nil {
			return nil, mapInventoryErr(ctx, err, "body.vars")
		}
		setAuditObject(ctx, g.ID.String(), "changed the values of inventory group "+g.Name)
		return newGroupOutput(g), nil
	}, withAudit("setInventoryGroupVars", "inventory_group"))
}

// mapInventoryErr converts inventory errors to HTTP. A *schema.ValidationError
// becomes 422 with one detail per problem, located under location (for
// example body.vars.vhosts[0].domain), so the SPA can show each message at
// its field.
func mapInventoryErr(ctx context.Context, err error, location string) error {
	var ve *schema.ValidationError
	if errors.As(err, &ve) {
		details := make([]error, len(ve.Problems))
		for i, p := range ve.Problems {
			loc := location
			if p.Path != "" {
				if loc != "" {
					loc += "."
				}
				loc += p.Path
			}
			details[i] = &huma.ErrorDetail{Location: loc, Message: p.Message}
		}
		return huma.Error422UnprocessableEntity("validation failed", details...)
	}
	switch {
	case errors.Is(err, inventory.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, inventory.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, inventory.ErrNameTaken):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, inventory.ErrHasChildren):
		return huma.Error409Conflict("the group still has child groups; move or delete them first")
	case errors.Is(err, inventory.ErrNotManaged):
		return huma.Error409Conflict("only managed hosts can join inventory groups")
	case errors.Is(err, inventory.ErrUnrenderable):
		// The message names the group or host and the reason; the renderer
		// never puts a value, let alone a secret, into it.
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, inventory.ErrRevisionMismatch):
		return huma.Error412PreconditionFailed("this changed since you loaded it; reload and apply your change again")
	default:
		slog.ErrorContext(ctx, "unmapped inventory error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
