package api

import (
	"context"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// Accepting an assistant's inventory values (spec 2026-11-25 assistant API
// gaps, decision D6 with the controller's refinement): a person's save
// makes live only the values it changes, so a person who wants an
// assistant's value live as it stands accepts it here. Only an interactive
// session may — no API or assistant token — so a script resending what it
// read cannot do it either; each accept is revision-guarded and recorded
// in the activity log.

type acceptKeysBody struct {
	Keys []string `json:"keys" minItems:"1" maxItems:"100" doc:"The keys whose assistant-written value (or default) becomes live, each currently marked"`
}

type groupAcceptInput struct {
	Org     string `path:"org"`
	ID      string `path:"id"`
	IfMatch string `header:"If-Match" doc:"The group's ETag; required"`
	Body    acceptKeysBody
}

type memberAcceptInput struct {
	Org     string `path:"org"`
	ID      string `path:"id"`
	Host    string `path:"host"`
	IfMatch string `header:"If-Match" doc:"The membership's ETag; required"`
	Body    acceptKeysBody
}

func registerInventoryAcceptRoutes(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/orgs/{org}/inventory/groups/{id}/vars/accept", func(ctx context.Context, in *groupAcceptInput) (*groupOutput, error) {
		if _, err := requireInteractiveSession(ctx); err != nil {
			return nil, err
		}
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
		g, err := deps.Inventory.AcceptVars(ctx, scope.OrgID, id, rev, in.Body.Keys)
		if err != nil {
			return nil, mapInventoryErr(ctx, err, "body.keys")
		}
		setAuditObject(ctx, g.ID.String(), "accepted the assistant's values "+strings.Join(in.Body.Keys, ", ")+" of inventory group "+g.Name+" as live")
		return newGroupOutput(g), nil
	}, withAudit("acceptInventoryGroupVars", "inventory_group"))

	huma.Post(hapi, "/api/v1/orgs/{org}/inventory/groups/{id}/schema/accept", func(ctx context.Context, in *groupAcceptInput) (*groupOutput, error) {
		if _, err := requireInteractiveSession(ctx); err != nil {
			return nil, err
		}
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
		g, err := deps.Inventory.AcceptDefaults(ctx, scope.OrgID, id, rev, in.Body.Keys)
		if err != nil {
			return nil, mapInventoryErr(ctx, err, "body.keys")
		}
		setAuditObject(ctx, g.ID.String(), "accepted the assistant's defaults of "+strings.Join(in.Body.Keys, ", ")+" of inventory group "+g.Name+" as live")
		return newGroupOutput(g), nil
	}, withAudit("acceptInventoryGroupDefaults", "inventory_group"))

	huma.Post(hapi, "/api/v1/orgs/{org}/inventory/groups/{id}/members/{host}/accept", func(ctx context.Context, in *memberAcceptInput) (*inventoryMemberOutput, error) {
		if _, err := requireInteractiveSession(ctx); err != nil {
			return nil, err
		}
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
		rev, err := requireIfMatch(in.IfMatch)
		if err != nil {
			return nil, err
		}
		m, err := deps.Inventory.AcceptMemberVars(ctx, scope.OrgID, groupID, hostID, rev, in.Body.Keys)
		if err != nil {
			return nil, mapInventoryErr(ctx, err, "body.keys")
		}
		setAuditObject(ctx, groupID.String(), "accepted the assistant's values "+strings.Join(in.Body.Keys, ", ")+" of host "+m.HostName+" in inventory group "+m.GroupName+" as live")
		out := &inventoryMemberOutput{ETag: revisionETag(m.Revision)}
		out.Body.Member = toInventoryMemberDTO(m)
		return out, nil
	}, withAudit("acceptInventoryMemberVars", "inventory_membership"))
}
