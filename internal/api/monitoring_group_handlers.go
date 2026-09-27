package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// The Monitoring groups page (v0.3b-1, spec "API and permissions"): a
// named, saved host-tag selector with a priority, used to bind check
// definitions to a set of hosts at once. Same permissions as the
// definitions themselves (check_handlers.go): a viewer reads, an operator
// writes.

type monitoringGroupDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Selector  string    `json:"selector"`
	Priority  int       `json:"priority" doc:"Higher wins when two groups bind the same check key to one host; an older group breaks a tie"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// monitoringGroupBody carries no constraints of its own for the same
// reason checkDefinitionBody does not: internal/checks validates the name,
// the priority range and the selector (by parsing it), and its message is
// the one an operator can act on.
type monitoringGroupBody struct {
	Name     string `json:"name"`
	Selector string `json:"selector" doc:"A host-tag selector expression, e.g. role=web,env!=dev"`
	Priority int    `json:"priority,omitempty"`
}

type monitoringGroupCreateInput struct {
	Org  string `path:"org"`
	Body monitoringGroupBody
}

type monitoringGroupUpdateInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body monitoringGroupBody
}

type monitoringGroupListOutput struct {
	Body struct {
		Groups []monitoringGroupDTO `json:"groups"`
	}
}

type monitoringGroupOutput struct {
	Body struct {
		Group monitoringGroupDTO `json:"group"`
	}
}

type monitoringGroupPreviewInput struct {
	Org  string `path:"org"`
	Body struct {
		Selector string `json:"selector" doc:"The selector to resolve; nothing is saved"`
	}
}

// previewHostDTO is deliberately narrower than hostDTO: the group editor
// needs to recognise what it matched (and notice a host that is not
// monitored, whose checks would never actually run), not render the host
// list a second time.
type previewHostDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Monitored bool   `json:"monitored"`
}

type monitoringGroupPreviewOutput struct {
	Body struct {
		Count int              `json:"count"`
		Hosts []previewHostDTO `json:"hosts"`
	}
}

func toMonitoringGroupDTO(g checks.Group) monitoringGroupDTO {
	return monitoringGroupDTO{
		ID: g.ID.String(), Name: g.Name, Selector: g.Selector, Priority: g.Priority,
		CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
	}
}

// registerMonitoringGroupRoutes wires the monitoring group surface,
// including the editor's live preview.
func registerMonitoringGroupRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/monitoring-groups", func(ctx context.Context, in *checkOrgInput) (*monitoringGroupListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksRead)
		if err != nil {
			return nil, err
		}
		list, err := deps.Checks.ListGroups(ctx, scope.OrgID)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		out := &monitoringGroupListOutput{}
		out.Body.Groups = make([]monitoringGroupDTO, len(list))
		for i, g := range list {
			out.Body.Groups[i] = toMonitoringGroupDTO(g)
		}
		return out, nil
	})

	huma.Get(hapi, "/api/v1/orgs/{org}/monitoring-groups/{id}", func(ctx context.Context, in *checkIDInput) (*monitoringGroupOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "monitoring group id")
		if err != nil {
			return nil, err
		}
		g, err := deps.Checks.GetGroup(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		out := &monitoringGroupOutput{}
		out.Body.Group = toMonitoringGroupDTO(g)
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/monitoring-groups", func(ctx context.Context, in *monitoringGroupCreateInput) (*monitoringGroupOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksWrite)
		if err != nil {
			return nil, err
		}
		g, err := deps.Checks.CreateGroup(ctx, scope.OrgID, checks.GroupInput{
			Name: in.Body.Name, Selector: in.Body.Selector, Priority: in.Body.Priority,
		})
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		setAuditObject(ctx, g.ID.String(), fmt.Sprintf("created monitoring group %q (%s)", g.Name, g.Selector))
		// No push: a group nothing is bound to yet cannot change any
		// host's resolved set, whatever its selector matches. The write
		// that binds a definition to it is what pushes (pushForGroup's own
		// comment covers the same rule for update and delete).
		out := &monitoringGroupOutput{}
		out.Body.Group = toMonitoringGroupDTO(g)
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createMonitoringGroup", "monitoring_group"))

	huma.Put(hapi, "/api/v1/orgs/{org}/monitoring-groups/{id}", func(ctx context.Context, in *monitoringGroupUpdateInput) (*monitoringGroupOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "monitoring group id")
		if err != nil {
			return nil, err
		}
		// The selector as it was: hosts that stop matching lose the
		// group's checks and need the push exactly as much as the ones
		// that start matching.
		before, err := deps.Checks.GetGroup(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		g, err := deps.Checks.UpdateGroup(ctx, scope.OrgID, id, checks.GroupInput{
			Name: in.Body.Name, Selector: in.Body.Selector, Priority: in.Body.Priority,
		})
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		setAuditObject(ctx, g.ID.String(), fmt.Sprintf("updated monitoring group %q (%s, priority %d)", g.Name, g.Selector, g.Priority))
		pushForGroup(ctx, deps, scope.OrgID, before, g)
		out := &monitoringGroupOutput{}
		out.Body.Group = toMonitoringGroupDTO(g)
		return out, nil
	}, withAudit("updateMonitoringGroup", "monitoring_group"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/monitoring-groups/{id}", func(ctx context.Context, in *checkIDInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "monitoring group id")
		if err != nil {
			return nil, err
		}
		before, err := deps.Checks.GetGroup(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		// Which hosts the deletion affects must be worked out while the
		// group and the definitions bound to it still exist: deleting the
		// group cascades those definitions away (migration 00043's ON
		// DELETE CASCADE), after which nothing names the hosts that just
		// lost them.
		affected := groupHosts(ctx, deps, scope.OrgID, before)
		if err := deps.Checks.DeleteGroup(ctx, scope.OrgID, id); err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), fmt.Sprintf("deleted monitoring group %q and the checks bound to it", before.Name))
		pushHosts(ctx, deps, scope.OrgID, affected)
		return &okOutput{}, nil
	}, withAudit("deleteMonitoringGroup", "monitoring_group"))

	huma.Post(hapi, "/api/v1/orgs/{org}/monitoring-groups/preview", func(ctx context.Context, in *monitoringGroupPreviewInput) (*monitoringGroupPreviewOutput, error) {
		// checks.read, not checks.write: this saves nothing, and a viewer
		// can already resolve any selector through POST /hosts/query with
		// hosts.read. Gating the editor's match count behind the write
		// permission would be a lock on a door with no room behind it.
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksRead)
		if err != nil {
			return nil, err
		}
		ids, err := deps.Checks.Preview(ctx, scope.OrgID, in.Body.Selector)
		if err != nil {
			// An unparseable selector arrives as checks.ErrInvalidInput
			// carrying the parser's own message, which names where the
			// expression went wrong — mapChecksErr answers 400 with it, the
			// same answer and wording saving that selector would give.
			return nil, mapChecksErr(ctx, err)
		}
		byID, err := hostsByID(ctx, deps, scope.OrgID)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		out := &monitoringGroupPreviewOutput{}
		out.Body.Hosts = make([]previewHostDTO, 0, len(ids))
		for _, id := range ids {
			h, ok := byID[id]
			if !ok {
				// The host list and the match were read separately; a host
				// deleted in between is simply no longer a match.
				continue
			}
			out.Body.Hosts = append(out.Body.Hosts, previewHostDTO{ID: id.String(), Name: h.Name, Monitored: h.Monitored})
		}
		out.Body.Count = len(out.Body.Hosts)
		return out, nil
	}) // no withAudit: this is a read (a POST only because the selector is
	// a request body, not a query param) — see auditAllowlist's entry for
	// this exact route (audit_coverage_test.go).
}

// pushForGroup re-pushes every host either version of a changed group
// covers — the selector as it was and as it now is.
func pushForGroup(ctx context.Context, deps Deps, orgID uuid.UUID, before, after checks.Group) {
	affected := append(groupHosts(ctx, deps, orgID, before), groupHosts(ctx, deps, orgID, after)...)
	pushHosts(ctx, deps, orgID, affected)
}

// groupHosts is the set of hosts a group change actually affects: none at
// all unless some check definition binds to that group, since a group
// carries no configuration of its own — neither its selector nor its
// priority can change what any host resolves to while nothing is bound to
// it. Where a definition is bound, it is every host the group's selector
// matches.
//
// A failure to work that out is logged and answered with no hosts, never
// propagated: every caller runs after its own write has already committed
// (see CheckConfigPusher).
func groupHosts(ctx context.Context, deps Deps, orgID uuid.UUID, g checks.Group) []uuid.UUID {
	defs, err := deps.Checks.ListDefinitions(ctx, orgID)
	if err != nil {
		slog.ErrorContext(ctx, "check configuration push: list definitions for group",
			"org_id", orgID, "group_id", g.ID, "err", err)
		return nil
	}
	bound := false
	for _, d := range defs {
		if d.BindingKind == checks.BindingGroup && d.GroupID != nil && *d.GroupID == g.ID {
			bound = true
			break
		}
	}
	if !bound {
		return nil
	}
	ids, err := deps.Checks.Preview(ctx, orgID, g.Selector)
	if err != nil {
		slog.ErrorContext(ctx, "check configuration push: expand group selector",
			"org_id", orgID, "group_id", g.ID, "err", err)
		return nil
	}
	return ids
}

// hostsByID indexes the organization's hosts for the preview's own
// rendering. One list read, not one lookup per match.
func hostsByID(ctx context.Context, deps Deps, orgID uuid.UUID) (map[uuid.UUID]hostSummary, error) {
	all, err := deps.Hosts.ListAllHosts(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	byID := make(map[uuid.UUID]hostSummary, len(all))
	for _, h := range all {
		byID[h.ID] = hostSummary{Name: h.Name, Monitored: h.Monitored}
	}
	return byID, nil
}

// hostSummary is the slice of a host the check routes need: its name, and
// whether checks run on it at all.
type hostSummary struct {
	Name      string
	Monitored bool
}
