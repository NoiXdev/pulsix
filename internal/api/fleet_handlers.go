package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/hosts/selector"
	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/rules"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// The fleet overview (v0.3b-2 spec, "API and permissions"): one row per
// host, worst state first, with the organization's own totals so the page
// needs no second call. A route of its own rather than a sort parameter on
// GET /hosts (the design's own ruling, spec "API and permissions"): it
// reads check_host_states, one row per host, and shares nothing with host
// administration's query shape. Same permission as the check definitions
// and the rule surface (checks.read) — this, like GET /hosts/{id}/checks,
// is a read of what monitoring already concluded, not a host-management
// action.

// fleetHostDTO is one host's roll-up. State is nil for a host
// check_host_states carries no row for — never evaluated, or evaluated
// down to zero tuples, which rules.EvalSQLStore's own applyRollUp deletes
// the row for rather than leaving a stale colour behind (states.go's own
// "a host with no tuples at all gets no row" contract) — and the GUI
// renders that as "no data", never a colour. No omitempty on State, for
// the same reason GET /hosts/{id}/checks' own new field has none: the key
// is always present, explicitly null rather than silently missing.
//
// AllIgnored (backlog 3.44) is the one case State == nil does not, on its
// own, tell apart: a host nobody has ever measured reads exactly like a
// host whose every discovered item currently resolves to ignored — both
// leave check_host_states with no row, since an evaluation with zero
// judged tuples deletes it (the same contract State's own doc comment
// names). AllIgnored is true only for the second case, and only ever
// alongside State == nil — a host with even one judged tuple keeps its
// real state regardless of how many other items are ignored. It is a
// field of its own rather than a fifth rules.State value: ignoring is a
// decision, not a measurement (rules.State's own doc comment, and the
// v0.3b-3 design's "The third value" ruling applied one level up), and a
// state comparison, a roll-up ordering or an alerting rule must never be
// asked to have an opinion about it.
type fleetHostDTO struct {
	HostID string  `json:"host_id"`
	Name   string  `json:"name"`
	ZoneID string  `json:"zone_id"`
	State  *string `json:"state" enum:"ok,warn,crit,unknown" doc:"null when this host has no roll-up row at all (\"no data\", or \"all ignored\" — see all_ignored)"`
	Crit   int     `json:"crit"`
	Warn   int     `json:"warn"`
	// Unknown counts this host's tuples currently in the unknown state —
	// distinct from State being null, which means the HOST itself was
	// never evaluated at all.
	Unknown     int        `json:"unknown"`
	Since       *time.Time `json:"since,omitempty" doc:"When the current state began; absent when state is null"`
	EvaluatedAt *time.Time `json:"evaluated_at,omitempty" doc:"absent when state is null"`
	// AllIgnored is always false when State is non-nil. See this type's own
	// doc comment for what distinguishes it from the plain "no data" case.
	AllIgnored bool `json:"all_ignored" doc:"True when state is null because every item this host has discovered currently resolves to ignored, rather than because nothing has ever been measured"`
}

// fleetTotalsDTO counts HOSTS (not tuples) by roll-up state across the
// filtered set — the header a fleet page renders without a second
// request. NoData is the count of hosts carrying no roll-up row at all.
//
// Deliberately over the FILTERED set, not the organization as a whole: a
// count that disagrees with the list the operator is looking at is worse
// than useless, and an operator who wants the organization's own totals
// clears the filter. huma builds the generated schema purely from these
// struct tags, never from this doc comment, so the doc: tags below (and on
// Totals itself, fleetOutput) are what actually carries this scope to a
// generated-client author — without them "totals" reads as ambiguous
// between "of this filtered list" and "of the whole organization".
type fleetTotalsDTO struct {
	Crit    int `json:"crit" doc:"Hosts in the filtered set currently crit"`
	Warn    int `json:"warn" doc:"Hosts in the filtered set currently warn"`
	Unknown int `json:"unknown" doc:"Hosts in the filtered set currently unknown"`
	OK      int `json:"ok" doc:"Hosts in the filtered set currently ok"`
	// NoData no longer counts a host whose roll-up row is absent because
	// every item it discovered is ignored — that host counts under
	// AllIgnored instead (backlog 3.44), so the two buckets stay honest
	// about which "no roll-up row" case each host is in, and still sum
	// with every other bucket to the size of the filtered set.
	NoData int `json:"no_data" doc:"Hosts in the filtered set with no roll-up row at all, and nothing discovered that is all ignored — never evaluated, or evaluated to zero tuples for a reason other than ignoring"`
	// AllIgnored counts a host with no roll-up row whose discovered items
	// all currently resolve to ignored — see fleetHostDTO.AllIgnored's own
	// doc comment for the full distinction.
	AllIgnored int `json:"all_ignored" doc:"Hosts in the filtered set with no roll-up row, because every item they discovered currently resolves to ignored"`
}

// fleetInput's Zone and Group are plain strings, not *uuid.UUID: like
// hostListInput's own Zone (host_handlers.go), huma validates the shape it
// can and the handler rejects a malformed value itself, and an empty
// string unambiguously means "this filter is not set".
type fleetInput struct {
	Org   string `path:"org"`
	Zone  string `query:"zone" doc:"A zone id"`
	Group string `query:"group" doc:"A monitoring group id — hosts its selector currently matches"`
	// Tag deliberately narrower than internal/hosts/selector's full
	// grammar: FleetFilter.Tag (internal/rules/states.go) implements only
	// equality and existence, so this route accepts only what that
	// implements — a Task 7 review called this out as a binding
	// constraint on this route. POST /hosts/query is where a caller who
	// needs "!=", "in(...)" or "and" composition belongs.
	Tag string `query:"tag" doc:"A single \"key\" or \"key=value\" token (equality or existence only); != , in(...) and boolean composition are refused"`
}

type fleetOutput struct {
	Body struct {
		Hosts []fleetHostDTO `json:"hosts"`
		// Totals counts Hosts by roll-up state, i.e. it is scoped to
		// whatever zone/group/tag filter this request carried — never the
		// organization as a whole. Clear the filter to see the
		// organization's own totals; this field never disagrees with the
		// list beside it.
		Totals fleetTotalsDTO `json:"totals" doc:"Per-state host counts over the filtered set above, not the whole organization"`
	}
}

// toFleetHostDTO renders h's roll-up. hasState is false exactly when
// check_host_states carries no row for h; allIgnored is only consulted in
// that case — a host with a real state keeps it regardless of what
// allIgnored says (see fleetHostDTO.AllIgnored's own doc comment).
func toFleetHostDTO(h hosts.Host, st rules.HostState, hasState, allIgnored bool) fleetHostDTO {
	dto := fleetHostDTO{HostID: h.ID.String(), Name: h.Name, ZoneID: h.ZoneID.String()}
	if !hasState {
		dto.AllIgnored = allIgnored
		return dto
	}
	state := string(st.State)
	since, evaluatedAt := st.Since, st.EvaluatedAt
	dto.State, dto.Since, dto.EvaluatedAt = &state, &since, &evaluatedAt
	dto.Crit, dto.Warn, dto.Unknown = st.Crit, st.Warn, st.Unknown
	return dto
}

// allIgnoredHosts resolves, for every host that discovered at least one
// item anywhere in the organization, whether every one of those items
// currently resolves to ignored through items.Decide — the exact function
// the evaluation ticker runs (internal/rules/load_sql.go's own
// resolveIgnored). views and decisions are each the organization's own
// single, unfiltered read (deps.Items.ItemsForOrg with the zero Filter,
// deps.Items.ListDecisions): one query apiece for the whole fleet request,
// not one per host or per item, the same bound the evaluation loader keeps
// for the identical resolution run once per host rather than once per
// tuple. A host absent from the result discovered nothing at all — still
// "no data", never "all ignored" (fleetHostDTO.AllIgnored's own doc
// comment).
func allIgnoredHosts(views []items.View, decisions []items.Decision) map[uuid.UUID]bool {
	byHost := make(map[uuid.UUID][]items.View, len(views))
	for _, v := range views {
		byHost[v.HostID] = append(byHost[v.HostID], v)
	}
	out := make(map[uuid.UUID]bool, len(byHost))
	for hostID, hostViews := range byHost {
		all := true
		for _, v := range hostViews {
			target := items.Target{HostID: hostID, CheckKey: v.CheckKey, Item: v.Name}
			if !items.Decide(decisions, target).Ignored() {
				all = false
				break
			}
		}
		out[hostID] = all
	}
	return out
}

// fleetStateRank orders the listing worst-first: a known problem before an
// unknown one (rules.StateRank's own crit > warn > unknown > ok ordering,
// the single definition of that order — this package no longer restates
// it), and a host that was never evaluated last of all — "no data" is
// neither a problem to lead with nor a clean bill of health to bury real
// problems under, so it trails every judged state, good or bad.
func fleetStateRank(state *string) int {
	if state == nil {
		return 4
	}
	// rules.StateRank is ascending severity (ok=0 .. crit=3); the fleet
	// listing sorts worst-first, so this rank is its complement.
	return 3 - rules.StateRank(rules.State(*state))
}

// sortFleetHosts orders hosts worst-first by fleetStateRank and, within a
// tied rank, alphabetically by name — an explicit secondary key, not a
// reliance on sort.SliceStable merely preserving whatever order the roster
// happened to arrive in. Host names are unique within an organization
// (00010_hosts.sql's own (org_id, name) constraint), so (rank, name) is a
// strict total order and sort.Slice needs no stability to get it right.
//
// Before this, the sort compared only rank, and relied entirely on
// QueryHosts already returning the roster name-ordered (store_sql.go) for
// same-state hosts to land alphabetically — which is also why nothing ever
// caught a regression here: QueryHosts always holds that precondition in
// production, so sort.Slice and sort.SliceStable (and a comparator that
// forgot the name entirely) all produced the same answer on every input
// any test fed them. Backlog 3.34.
func sortFleetHosts(hosts []fleetHostDTO) {
	sort.Slice(hosts, func(i, j int) bool {
		ri, rj := fleetStateRank(hosts[i].State), fleetStateRank(hosts[j].State)
		if ri != rj {
			return ri < rj
		}
		return hosts[i].Name < hosts[j].Name
	})
}

// validateFleetTag enforces FleetFilter.Tag's own contract: a single "key"
// or "key=value" token, equality or existence only. raw == "" (no filter)
// always passes.
func validateFleetTag(raw string) error {
	if raw == "" {
		return nil
	}
	if strings.Count(raw, "=") > 1 {
		return huma.Error422UnprocessableEntity(fmt.Sprintf(
			"tag %q must be a single \"key\" or \"key=value\" token", raw))
	}
	lower := strings.ToLower(raw)
	for _, bad := range []string{"!=", "(", ")", ",", " and ", " or "} {
		if strings.Contains(lower, bad) {
			return huma.Error422UnprocessableEntity(fmt.Sprintf(
				"tag %q must be a single \"key\" or \"key=value\" token; \"!=\", \"in(...)\" and boolean composition are not accepted here — use POST /hosts/query for a full selector", raw))
		}
	}
	return nil
}

// parseFleetFilter decodes in's query parameters into rules.FleetFilter,
// rejecting a malformed id (400, huma-style) or an unsupported tag shape
// (422, validateFleetTag's own message) before anything is queried.
func parseFleetFilter(in *fleetInput) (rules.FleetFilter, error) {
	zoneID, err := optionalUUID(in.Zone, "zone id")
	if err != nil {
		return rules.FleetFilter{}, err
	}
	groupID, err := optionalUUID(in.Group, "group id")
	if err != nil {
		return rules.FleetFilter{}, err
	}
	if err := validateFleetTag(in.Tag); err != nil {
		return rules.FleetFilter{}, err
	}
	return rules.FleetFilter{ZoneID: zoneID, GroupID: groupID, Tag: in.Tag}, nil
}

// tagSelector renders an already-validated fleet tag filter as a
// selector.Selector, so fleetRoster can reuse QueryHosts' own merged-tag
// SQL (the same manual-overrides-fact view rules.HostStates' tagFragment
// matches against) instead of a second implementation of that merge. A
// value containing a literal double quote cannot be expressed in the
// grammar at all (selector's lexer has no escape syntax) — the same
// pre-existing limitation POST /hosts/query already has, not a new one.
func tagSelector(raw string) (selector.Selector, error) {
	key, value, hasValue := strings.Cut(raw, "=")
	var text string
	if hasValue {
		if strings.Contains(value, `"`) {
			return selector.Selector{}, huma.Error422UnprocessableEntity(fmt.Sprintf(
				"tag %q: a value containing a quote character cannot be expressed here", raw))
		}
		text = key + `="` + value + `"`
	} else {
		text = key + " exists"
	}
	sel, err := selector.Parse(text)
	if err != nil {
		return selector.Selector{}, huma.Error422UnprocessableEntity(err.Error())
	}
	return sel, nil
}

// filterHostsByZone keeps list's hosts in zoneID, preserving order.
func filterHostsByZone(list []hosts.Host, zoneID uuid.UUID) []hosts.Host {
	out := list[:0:0]
	for _, h := range list {
		if h.ZoneID == zoneID {
			out = append(out, h)
		}
	}
	return out
}

// intersectHosts keeps a's hosts that also appear (by id) in b, preserving
// a's order — QueryHosts always returns its result ordered by (name, id),
// so a roster built by successively intersecting into the full list stays
// name-ordered throughout.
func intersectHosts(a, b []hosts.Host) []hosts.Host {
	ids := make(map[uuid.UUID]bool, len(b))
	for _, h := range b {
		ids[h.ID] = true
	}
	out := a[:0:0]
	for _, h := range a {
		if ids[h.ID] {
			out = append(out, h)
		}
	}
	return out
}

// fleetRoster returns every host in orgID matching f's zone, tag and group
// — the same three conditions rules.HostStates applies to check_host_states
// — so a host with no roll-up row at all still appears on the fleet page.
// This is the whole reason this function exists: HostStates only ever
// returns a row for a host that has been evaluated at least once, and
// there would otherwise be no way to tell "matches the filter, no data
// yet" from "does not match the filter" (Task 11's own brief, and the
// controller's own worked example: a host with no state row must appear
// with zero counts, never be silently dropped).
func fleetRoster(ctx context.Context, deps Deps, orgID uuid.UUID, f rules.FleetFilter) ([]hosts.Host, error) {
	all, err := deps.Hosts.QueryHosts(ctx, orgID, selector.Selector{})
	if err != nil {
		return nil, mapHostsErr(ctx, err)
	}
	if f.ZoneID != nil {
		all = filterHostsByZone(all, *f.ZoneID)
	}
	if f.Tag != "" {
		sel, err := tagSelector(f.Tag)
		if err != nil {
			return nil, err
		}
		tagged, err := deps.Hosts.QueryHosts(ctx, orgID, sel)
		if err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		all = intersectHosts(all, tagged)
	}
	if f.GroupID != nil {
		group, err := deps.Checks.GetGroup(ctx, orgID, *f.GroupID)
		if errors.Is(err, checks.ErrNotFound) {
			// A filter naming a deleted or foreign group matches no host —
			// mirrors rules.HostStates' own choice for the identical case
			// (states_store.go, errGroupNotFound), not a 404: the group id
			// is a filter value, not a path parameter naming a resource this
			// request is about.
			return nil, nil
		}
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		sel, err := selector.Parse(group.Selector)
		if err != nil {
			// The group's own selector was already validated when it was
			// saved (checks.Service's normalize); a parse failure here means
			// that guarantee broke, not that this caller did anything wrong.
			slog.ErrorContext(ctx, "fleet: stored monitoring group selector failed to parse", "group", *f.GroupID, "err", err)
			return nil, huma.Error500InternalServerError("internal error")
		}
		matched, err := deps.Hosts.QueryHosts(ctx, orgID, sel)
		if err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		all = intersectHosts(all, matched)
	}
	return all, nil
}

// registerFleetRoutes wires GET /orgs/{org}/fleet.
func registerFleetRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/fleet", func(ctx context.Context, in *fleetInput) (*fleetOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksRead)
		if err != nil {
			return nil, err
		}
		filter, err := parseFleetFilter(in)
		if err != nil {
			return nil, err
		}
		roster, err := fleetRoster(ctx, deps, scope.OrgID, filter)
		if err != nil {
			return nil, err
		}
		states, err := deps.RuleStates.HostStates(ctx, scope.OrgID, filter)
		if err != nil {
			slog.ErrorContext(ctx, "fleet: host states", "err", err)
			return nil, huma.Error500InternalServerError("internal error")
		}
		byHost := make(map[uuid.UUID]rules.HostState, len(states))
		for _, st := range states {
			byHost[st.HostID] = st
		}

		// Two more organization-wide reads, each already a single query
		// regardless of how many hosts the roster holds (items.Service's own
		// ItemsForOrg and ListDecisions) — see allIgnoredHosts' own doc
		// comment for why this never becomes a per-host fan-out.
		views, err := deps.Items.ItemsForOrg(ctx, scope.OrgID, items.Filter{})
		if err != nil {
			slog.ErrorContext(ctx, "fleet: items for org", "err", err)
			return nil, huma.Error500InternalServerError("internal error")
		}
		decisions, err := deps.Items.ListDecisions(ctx, scope.OrgID)
		if err != nil {
			slog.ErrorContext(ctx, "fleet: item decisions", "err", err)
			return nil, huma.Error500InternalServerError("internal error")
		}
		ignoredHosts := allIgnoredHosts(views, decisions)

		out := &fleetOutput{}
		out.Body.Hosts = make([]fleetHostDTO, len(roster))
		var totals fleetTotalsDTO
		for i, h := range roster {
			st, ok := byHost[h.ID]
			allIgnored := !ok && ignoredHosts[h.ID]
			out.Body.Hosts[i] = toFleetHostDTO(h, st, ok, allIgnored)
			switch {
			case allIgnored:
				totals.AllIgnored++
			case !ok:
				totals.NoData++
			case st.State == rules.StateCrit:
				totals.Crit++
			case st.State == rules.StateWarn:
				totals.Warn++
			case st.State == rules.StateUnknown:
				totals.Unknown++
			default:
				totals.OK++
			}
		}
		sortFleetHosts(out.Body.Hosts)
		out.Body.Totals = totals
		return out, nil
	})
}
