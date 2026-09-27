package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// The Checks page (v0.3b-1, spec docs/superpowers/specs/2026-09-27-v0.3b-
// checks-design.md, "API and permissions"): check definitions bound to the
// organization, a monitoring group or a single host. Reading them is a
// viewer's right — what is measured on a host is part of seeing the fleet —
// and changing them is an operator's, the same bar hosts.write clears.
//
// Monitoring groups live in monitoring_group_handlers.go, the per-host
// effective set and its graphs in host_check_handlers.go; all three share
// mapChecksErr and the push helpers at the bottom of this file.

// CheckConfigPusher hands one host's freshly resolved check configuration
// to whatever agent is currently connected for it. *agentapi.Service
// satisfies it (PushHost); the interface is declared here, with exactly
// the one method this package calls, so internal/api never imports
// internal/agentapi or the generated protobuf package — the HTTP layer
// knows only "tell this host's agent to catch up".
//
// A push is best-effort by design, never part of the write it follows:
// the agent re-asserts its configuration version every 60 seconds and the
// manager answers that with the current set, so a push that never lands
// self-heals within one interval (the spec's "Configuration delivery"
// says exactly this). pushHosts below therefore logs a failure and
// returns, rather than turning a committed, successful write into a 500
// the operator would retry.
type CheckConfigPusher interface {
	PushHost(ctx context.Context, orgID, hostID uuid.UUID) error
}

type checkDefinitionDTO struct {
	ID              string         `json:"id"`
	Key             string         `json:"key"`
	Plugin          string         `json:"plugin" enum:"cpu,memory,disk,service"`
	Params          map[string]any `json:"params" doc:"Plugin parameters; {} for a plugin that takes none"`
	IntervalSeconds int            `json:"interval_seconds"`
	Enabled         bool           `json:"enabled"`
	Origin          string         `json:"origin" enum:"seed,custom" doc:"seed rows ship with every organization; their key and plugin are fixed"`
	BindingKind     string         `json:"binding_kind" enum:"org,group,host"`
	GroupID         string         `json:"group_id,omitempty"`
	HostID          string         `json:"host_id,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// checkDefinitionBody is the create/update shape. It deliberately carries
// no enum, minimum or maxLength constraints of its own: internal/checks
// validates every one of these fields already (plugin set, key pattern,
// interval bounds, per-plugin parameter schema, binding consistency), and
// duplicating those bounds here would make the same bad input answer 422
// with huma's generic wording on one field and 400 with the service's
// specific message on the next. One validator, one message.
type checkDefinitionBody struct {
	Key             string         `json:"key,omitempty" doc:"Lower-case identifier a narrower scope overrides on; defaults to the plugin name"`
	Plugin          string         `json:"plugin" doc:"One of cpu, memory, disk, service"`
	Params          map[string]any `json:"params,omitempty" doc:"disk takes an optional mounts list, service a required units list; cpu and memory take none"`
	IntervalSeconds int            `json:"interval_seconds" doc:"How often the check runs, between 10 and 86400 seconds"`
	Enabled         *bool          `json:"enabled,omitempty" doc:"Defaults to true; false keeps the row but stops the check at this scope"`
	BindingKind     string         `json:"binding_kind" doc:"One of org, group, host"`
	GroupID         string         `json:"group_id,omitempty" format:"uuid" doc:"Required for binding_kind=group, forbidden otherwise"`
	HostID          string         `json:"host_id,omitempty" format:"uuid" doc:"Required for binding_kind=host, forbidden otherwise"`
}

type checkOrgInput struct {
	Org string `path:"org"`
}

type checkIDInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type checkCreateInput struct {
	Org  string `path:"org"`
	Body checkDefinitionBody
}

type checkUpdateInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body checkDefinitionBody
}

type checkListOutput struct {
	Body struct {
		Checks []checkDefinitionDTO `json:"checks"`
	}
}

type checkOutput struct {
	Body struct {
		Check checkDefinitionDTO `json:"check"`
	}
}

func toCheckDefinitionDTO(d checks.Definition) checkDefinitionDTO {
	params := d.Params
	if params == nil {
		// Never "null": a plugin that takes no parameters and one whose
		// map merely round-tripped as nil must read identically to the
		// GUI's form, which binds an object.
		params = map[string]any{}
	}
	dto := checkDefinitionDTO{
		ID: d.ID.String(), Key: d.Key, Plugin: d.Plugin, Params: params,
		IntervalSeconds: int(d.Interval / time.Second), Enabled: d.Enabled, Origin: d.Origin,
		BindingKind: string(d.BindingKind), CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
	if d.GroupID != nil {
		dto.GroupID = d.GroupID.String()
	}
	if d.HostID != nil {
		dto.HostID = d.HostID.String()
	}
	return dto
}

// toDefinitionInput decodes a request body into the service's own input
// shape. Only the two ids are parsed here (a malformed uuid is a transport
// concern); everything else is handed over exactly as it arrived, for
// internal/checks to accept or refuse.
func toDefinitionInput(b checkDefinitionBody) (checks.DefinitionInput, error) {
	groupID, err := optionalUUID(b.GroupID, "group id")
	if err != nil {
		return checks.DefinitionInput{}, err
	}
	hostID, err := optionalUUID(b.HostID, "host id")
	if err != nil {
		return checks.DefinitionInput{}, err
	}
	enabled := true
	if b.Enabled != nil {
		enabled = *b.Enabled
	}
	return checks.DefinitionInput{
		Key: b.Key, Plugin: b.Plugin, Params: b.Params,
		Interval:    time.Duration(b.IntervalSeconds) * time.Second,
		Enabled:     enabled,
		BindingKind: checks.BindingKind(b.BindingKind),
		GroupID:     groupID, HostID: hostID,
	}, nil
}

// registerCheckRoutes wires the check definition surface.
func registerCheckRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/checks", func(ctx context.Context, in *checkOrgInput) (*checkListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksRead)
		if err != nil {
			return nil, err
		}
		list, err := deps.Checks.ListDefinitions(ctx, scope.OrgID)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		out := &checkListOutput{}
		out.Body.Checks = make([]checkDefinitionDTO, len(list))
		for i, d := range list {
			out.Body.Checks[i] = toCheckDefinitionDTO(d)
		}
		return out, nil
	})

	huma.Get(hapi, "/api/v1/orgs/{org}/checks/{id}", func(ctx context.Context, in *checkIDInput) (*checkOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "check id")
		if err != nil {
			return nil, err
		}
		d, err := deps.Checks.GetDefinition(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		out := &checkOutput{}
		out.Body.Check = toCheckDefinitionDTO(d)
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/checks", func(ctx context.Context, in *checkCreateInput) (*checkOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksWrite)
		if err != nil {
			return nil, err
		}
		input, err := toDefinitionInput(in.Body)
		if err != nil {
			return nil, err
		}
		d, err := deps.Checks.CreateDefinition(ctx, scope.OrgID, input)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		setAuditObject(ctx, d.ID.String(), fmt.Sprintf("created check %q (%s) at %s scope", d.Key, d.Plugin, d.BindingKind))
		pushForDefinitions(ctx, deps, scope.OrgID, d)
		out := &checkOutput{}
		out.Body.Check = toCheckDefinitionDTO(d)
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createCheck", "check_definition"))

	huma.Put(hapi, "/api/v1/orgs/{org}/checks/{id}", func(ctx context.Context, in *checkUpdateInput) (*checkOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "check id")
		if err != nil {
			return nil, err
		}
		input, err := toDefinitionInput(in.Body)
		if err != nil {
			return nil, err
		}
		// Read the row as it stands first: an update may move the binding
		// (a host-bound check rebound to a group, say), and the hosts that
		// lose the check are as affected as the ones that gain it — both
		// sets need the push, and the old one is unknowable afterwards.
		before, err := deps.Checks.GetDefinition(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		d, err := deps.Checks.UpdateDefinition(ctx, scope.OrgID, id, input)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		setAuditObject(ctx, d.ID.String(), fmt.Sprintf("updated check %q (%s) at %s scope", d.Key, d.Plugin, d.BindingKind))
		pushForDefinitions(ctx, deps, scope.OrgID, before, d)
		out := &checkOutput{}
		out.Body.Check = toCheckDefinitionDTO(d)
		return out, nil
	}, withAudit("updateCheck", "check_definition"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/checks/{id}", func(ctx context.Context, in *checkIDInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "check id")
		if err != nil {
			return nil, err
		}
		// Read before deleting, for the same reason the update above does:
		// the binding is what names the affected hosts, and it is gone
		// once the row is. It also makes a delete of another
		// organization's id answer 404 before anything is attempted.
		before, err := deps.Checks.GetDefinition(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		if err := deps.Checks.DeleteDefinition(ctx, scope.OrgID, id); err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), fmt.Sprintf("deleted check %q (%s) at %s scope", before.Key, before.Plugin, before.BindingKind))
		pushForDefinitions(ctx, deps, scope.OrgID, before)
		return &okOutput{}, nil
	}, withAudit("deleteCheck", "check_definition"))
}

// pushForDefinitions re-pushes the check configuration of every host the
// given definitions' bindings cover, after the write has already
// committed. Several definitions may be passed (an update's before and
// after state); the union is pushed once per host.
func pushForDefinitions(ctx context.Context, deps Deps, orgID uuid.UUID, defs ...checks.Definition) {
	var affected []uuid.UUID
	for _, d := range defs {
		ids, err := definitionHosts(ctx, deps, orgID, d)
		if err != nil {
			// The write itself succeeded; only working out who to notify
			// failed. Logged, never propagated — see CheckConfigPusher.
			slog.ErrorContext(ctx, "check configuration push: resolve affected hosts",
				"org_id", orgID, "check_key", d.Key, "binding_kind", d.BindingKind, "err", err)
			continue
		}
		affected = append(affected, ids...)
	}
	pushHosts(ctx, deps, orgID, affected)
}

// definitionHosts is the set of hosts a definition's binding currently
// covers: the one host it names, every host its monitoring group's
// selector matches, or — for an organization-bound definition — every
// monitored host in the organization (the only ones that resolve to a
// non-empty set at all, per the resolver's own gate).
func definitionHosts(ctx context.Context, deps Deps, orgID uuid.UUID, d checks.Definition) ([]uuid.UUID, error) {
	switch d.BindingKind {
	case checks.BindingHost:
		if d.HostID == nil {
			return nil, nil
		}
		return []uuid.UUID{*d.HostID}, nil
	case checks.BindingGroup:
		if d.GroupID == nil {
			return nil, nil
		}
		g, err := deps.Checks.GetGroup(ctx, orgID, *d.GroupID)
		if err != nil {
			return nil, fmt.Errorf("group of check %q: %w", d.Key, err)
		}
		return deps.Checks.Preview(ctx, orgID, g.Selector)
	default:
		return monitoredHostIDs(ctx, deps, orgID)
	}
}

// monitoredHostIDs lists the organization's monitored hosts — the same set
// the resolver's own HostRoster port reports, read here through the hosts
// service this package already depends on rather than a second port into
// internal/checks.
func monitoredHostIDs(ctx context.Context, deps Deps, orgID uuid.UUID) ([]uuid.UUID, error) {
	all, err := deps.Hosts.ListAllHosts(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	ids := make([]uuid.UUID, 0, len(all))
	for _, h := range all {
		if h.Monitored {
			ids = append(ids, h.ID)
		}
	}
	return ids, nil
}

// pushHosts asks each distinct host's connected agent to catch up. Failures
// are logged and dropped: the write has already committed, and the agent's
// own 60-second re-assert repairs a missed push (see CheckConfigPusher).
func pushHosts(ctx context.Context, deps Deps, orgID uuid.UUID, hostIDs []uuid.UUID) {
	if deps.AgentPush == nil {
		return
	}
	seen := make(map[uuid.UUID]struct{}, len(hostIDs))
	for _, id := range hostIDs {
		if _, done := seen[id]; done {
			continue
		}
		seen[id] = struct{}{}
		if err := deps.AgentPush.PushHost(ctx, orgID, id); err != nil {
			slog.ErrorContext(ctx, "check configuration push failed; the agent's own re-assert will repair it",
				"org_id", orgID, "host_id", id, "err", err)
		}
	}
}

func mapChecksErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, checks.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, checks.ErrNotFound), errors.Is(err, hosts.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, checks.ErrNameTaken):
		return huma.Error409Conflict("a monitoring group with this name already exists")
	case errors.Is(err, checks.ErrKeyTaken):
		return huma.Error409Conflict("a check with this key already exists at this scope")
	}
	slog.ErrorContext(ctx, "unmapped checks error", "err", err)
	return huma.Error500InternalServerError("internal error")
}
