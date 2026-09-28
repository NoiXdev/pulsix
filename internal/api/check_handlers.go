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

// CheckConfigPusher hands freshly resolved check configuration to whatever
// agents are currently connected. *agentapi.Service satisfies it; the
// interface is declared here, with exactly the two methods this package
// calls, so internal/api never imports internal/agentapi or the generated
// protobuf package — the HTTP layer knows only "tell these agents to catch
// up".
//
// Both methods take a whole affected set, never one host: resolving a
// host's configuration means loading the organization's definitions, its
// monitoring groups and every group's selector expansion, so a per-host
// loop repeats that entire bundle once per host — on a group covering a
// couple of hundred hosts, thousands of serial queries inside one HTTP
// request. PushHosts shares one load across the named ids; PushOrg does
// the same for every monitored host without the caller enumerating them.
// pushAffected below picks between them by what the domain reports
// (checks.Affected.WholeOrg), never by counting ids — including the one
// case where the organization-wide path is narrower than the bindings
// asked about, an unmonitored host, which checks.Affected documents.
//
// A push is best-effort by design, never part of the write it follows:
// the agent re-asserts its configuration version every 60 seconds and the
// manager answers that with the current set, so a push that never lands
// self-heals within one interval (the spec's "Configuration delivery"
// says exactly this). pushAffected therefore logs a failure and returns,
// rather than turning a committed, successful write into a 500 the
// operator would retry.
type CheckConfigPusher interface {
	PushHosts(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) error
	PushOrg(ctx context.Context, orgID uuid.UUID) error
}

// pushTimeout bounds the post-write push. It is generous: the work behind
// it is one resolver pass plus a mailbox queue per connected agent, never
// a network round trip to an agent (see StreamRegistry.PushToHost), so
// reaching this deadline means the database itself is in trouble — at
// which point abandoning the push is right, since the agents' own
// re-assert will repair what was missed.
const pushTimeout = 30 * time.Second

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
	HostName        string         `json:"host_name,omitempty" doc:"The bound host's name, for a host-bound definition; absent otherwise, and absent for a host this organization can no longer read"`
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
		names := checkHostNames(ctx, deps, scope.OrgID, list)
		out := &checkListOutput{}
		out.Body.Checks = make([]checkDefinitionDTO, len(list))
		for i, d := range list {
			out.Body.Checks[i] = toCheckDefinitionDTO(d)
			if d.HostID != nil {
				out.Body.Checks[i].HostName = names[*d.HostID]
			}
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
		out.Body.Check.HostName = checkHostName(ctx, deps, scope.OrgID, d)
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
		// The hosts the new binding reaches are fully determined by the
		// request body, so they are worked out before the write for the
		// same reason the update and delete paths do it: one rule, one
		// moment, no expansion that could observe its own write.
		affected := affectedByBindings(ctx, deps, scope.OrgID, input.Binding())
		d, err := deps.Checks.CreateDefinition(ctx, scope.OrgID, input)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		setAuditObject(ctx, d.ID.String(), fmt.Sprintf("created check %q (%s) at %s scope", d.Key, d.Plugin, d.BindingKind))
		pushAffected(ctx, deps, scope.OrgID, affected)
		out := &checkOutput{}
		out.Body.Check = toCheckDefinitionDTO(d)
		out.Body.Check.HostName = checkHostName(ctx, deps, scope.OrgID, d)
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
		// lose the check are as affected as the ones that gain it.
		before, err := deps.Checks.GetDefinition(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		// Both sides are expanded here, before the write: the binding
		// being replaced names hosts only the pre-write state knows, and
		// the replacing one is already in the request body. Nothing this
		// write touches (a check_definitions row) is read by that
		// expansion (monitoring groups, host tags) — and it is not left
		// to stay that way by accident.
		affected := affectedByBindings(ctx, deps, scope.OrgID, before.Binding(), input.Binding())
		d, err := deps.Checks.UpdateDefinition(ctx, scope.OrgID, id, input)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		setAuditObject(ctx, d.ID.String(), fmt.Sprintf("updated check %q (%s) at %s scope", d.Key, d.Plugin, d.BindingKind))
		pushAffected(ctx, deps, scope.OrgID, affected)
		out := &checkOutput{}
		out.Body.Check = toCheckDefinitionDTO(d)
		out.Body.Check.HostName = checkHostName(ctx, deps, scope.OrgID, d)
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
		affected := affectedByBindings(ctx, deps, scope.OrgID, before.Binding())
		if err := deps.Checks.DeleteDefinition(ctx, scope.OrgID, id); err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), fmt.Sprintf("deleted check %q (%s) at %s scope", before.Key, before.Plugin, before.BindingKind))
		pushAffected(ctx, deps, scope.OrgID, affected)
		return &okOutput{}, nil
	}, withAudit("deleteCheck", "check_definition"))
}

// checkHostNames resolves, for a whole page of definitions, the names of
// the hosts the host-bound ones name. It is ONE store read for the page,
// whatever the page holds — hosts.Service.ListHostsByIDs, which
// deduplicates the ids and reads nothing when there are none.
//
// Two shapes it has to be right for, which is why it is neither a loop nor
// a whole-fleet read:
//
//   - Dense: a host binding IS the per-host override, so a fleet that uses
//     them has roughly as many host-bound rows as it has hosts, and
//     GET /checks is unpaginated. One lookup per definition would be
//     hundreds of queries in one request — and leaving the name out
//     would not make that work go away, only move it into the browser,
//     once per definition and again on every navigation back.
//   - Sparse: a single host-bound check in a 10 000-host organization must
//     not cost a read of the entire hosts table (which is what
//     ListAllHosts, the inventory renderer's query, would do here). The
//     work is proportional to the page, not to the fleet.
//
// A failure to read is logged and answered with no names: host_name is a
// label on a binding the operator can still see by id, and losing the
// label is a far smaller failure than a 500 on the whole page.
func checkHostNames(ctx context.Context, deps Deps, orgID uuid.UUID, list []checks.Definition) map[uuid.UUID]string {
	ids := make([]uuid.UUID, 0, len(list))
	for _, d := range list {
		if d.HostID != nil {
			ids = append(ids, *d.HostID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	found, err := deps.Hosts.ListHostsByIDs(ctx, orgID, ids)
	if err != nil {
		slog.ErrorContext(ctx, "resolve host names for check definitions", "org_id", orgID, "err", err)
		return nil
	}
	names := make(map[uuid.UUID]string, len(found))
	for _, h := range found {
		names[h.ID] = h.Name
	}
	return names
}

// checkHostName is checkHostNames for a route that answers with a single
// definition: one lookup rather than a whole-fleet list read. An empty
// string wherever the host cannot be read — a host deleted between the two
// reads, say — which is exactly what the omitempty field means.
func checkHostName(ctx context.Context, deps Deps, orgID uuid.UUID, d checks.Definition) string {
	if d.HostID == nil {
		return ""
	}
	h, err := deps.Hosts.GetHost(ctx, orgID, *d.HostID)
	if err != nil {
		slog.DebugContext(ctx, "resolve host name for a check definition", "org_id", orgID, "host_id", *d.HostID, "err", err)
		return ""
	}
	return h.Name
}

// pushAffected asks the agents of the hosts a committed write reached to
// catch up. Which hosts those are is the checks service's own answer
// (checks.Affected) — the scope rules belong with the resolver that
// applies them, not to this package.
//
// Two things about how it runs, both deliberate:
//
//   - Whatever the affected set is, it is handed over in one call, never
//     iterated here. See CheckConfigPusher.
//   - It runs on a context detached from the request's
//     (context.WithoutCancel) under its own deadline, the same pattern
//     internal/schedules and internal/notify use for work that must
//     outlive the trigger. The write has already committed; a client that
//     has given up, or a proxy that cut the response, must not also
//     abandon the push halfway through — which would leave some agents
//     told and others not, for as long as their next re-assert takes.
//     Detaching cancellation, not moving the work to a goroutine, is the
//     whole of it: the push stays inside the request so the tests (and an
//     operator reading the log) see it happen before the response.
//
// Every failure here is logged and dropped, never propagated.
func pushAffected(ctx context.Context, deps Deps, orgID uuid.UUID, affected checks.Affected) {
	if deps.AgentPush == nil {
		return
	}
	if !affected.WholeOrg && len(affected.Hosts) == 0 {
		// Nothing to tell anybody: a write that turns out to reach no
		// host at all — a monitoring group nothing binds to, the common
		// case — costs nothing downstream either.
		return
	}
	pushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pushTimeout)
	defer cancel()

	if affected.WholeOrg {
		if err := deps.AgentPush.PushOrg(pushCtx, orgID); err != nil {
			slog.ErrorContext(pushCtx, "check configuration push failed; the agents' own re-assert will repair it",
				"org_id", orgID, "scope", "org", "err", err)
		}
		return
	}
	if err := deps.AgentPush.PushHosts(pushCtx, orgID, affected.Hosts); err != nil {
		slog.ErrorContext(pushCtx, "check configuration push failed; the agents' own re-assert will repair it",
			"org_id", orgID, "hosts", len(affected.Hosts), "err", err)
	}
}

// affectedByBindings asks the checks service which hosts the given
// bindings reach. Callers work this out BEFORE their write: a binding
// being replaced names hosts only the pre-write state knows, and even the
// replacing binding is fully determined by the request body, so there is
// no reason for either side to observe state its own write produced.
//
// A failure is logged and answered with "nothing", never propagated: the
// caller runs it around a write whose success must not depend on being
// able to work out who to notify (see CheckConfigPusher).
func affectedByBindings(ctx context.Context, deps Deps, orgID uuid.UUID, bindings ...checks.Binding) checks.Affected {
	affected, err := deps.Checks.AffectedHosts(ctx, orgID, bindings...)
	if err != nil {
		logAffectedFailure(ctx, "check configuration push: resolve affected hosts", err, "org_id", orgID)
		return checks.Affected{}
	}
	return affected
}

// affectedByGroup is affectedByBindings for a monitoring group write, over
// the group's selector text on either side of it (see
// checks.Service.AffectedHostsForGroup).
func affectedByGroup(ctx context.Context, deps Deps, orgID, groupID uuid.UUID, selectors ...string) checks.Affected {
	affected, err := deps.Checks.AffectedHostsForGroup(ctx, orgID, groupID, selectors...)
	if err != nil {
		logAffectedFailure(ctx, "check configuration push: resolve hosts affected by a group", err,
			"org_id", orgID, "group_id", groupID)
		return checks.Affected{}
	}
	return affected
}

// logAffectedFailure records a failure to work out who a write reaches, at
// the level it deserves. Because the expansion now runs BEFORE the write,
// it sees the caller's own input first: a binding naming a group that does
// not exist, or a selector that does not parse, is about to be refused by
// the write itself with a 4xx that names it. Logging that at error level
// would turn every operator typo into a server fault in the log. Anything
// else — a store that cannot answer — is a real failure nothing downstream
// will report, since the write itself will succeed.
func logAffectedFailure(ctx context.Context, msg string, err error, args ...any) {
	args = append(args, "err", err)
	if errors.Is(err, checks.ErrInvalidInput) || errors.Is(err, checks.ErrNotFound) {
		slog.DebugContext(ctx, msg+" (the write itself will refuse this input)", args...)
		return
	}
	slog.ErrorContext(ctx, msg, args...)
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
