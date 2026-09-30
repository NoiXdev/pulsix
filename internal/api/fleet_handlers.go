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
type fleetHostDTO struct {
	HostID string  `json:"host_id"`
	Name   string  `json:"name"`
	ZoneID string  `json:"zone_id"`
	State  *string `json:"state" enum:"ok,warn,crit,unknown" doc:"null when this host has no roll-up row at all (\"no data\")"`
	Crit   int     `json:"crit"`
	Warn   int     `json:"warn"`
	// Unknown counts this host's tuples currently in the unknown state —
	// distinct from State being null, which means the HOST itself was
	// never evaluated at all.
	Unknown     int        `json:"unknown"`
	Since       *time.Time `json:"since,omitempty" doc:"When the current state began; absent when state is null"`
	EvaluatedAt *time.Time `json:"evaluated_at,omitempty" doc:"absent when state is null"`
}

// fleetTotalsDTO counts HOSTS (not tuples) by roll-up state across the
// filtered set — the header a fleet page renders without a second
// request. NoData is the count of hosts carrying no roll-up row at all.
type fleetTotalsDTO struct {
	Crit    int `json:"crit"`
	Warn    int `json:"warn"`
	Unknown int `json:"unknown"`
	OK      int `json:"ok"`
	NoData  int `json:"no_data"`
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
		Hosts  []fleetHostDTO `json:"hosts"`
		Totals fleetTotalsDTO `json:"totals"`
	}
}

// toFleetHostDTO renders h's roll-up. hasState is false exactly when
// check_host_states carries no row for h.
func toFleetHostDTO(h hosts.Host, st rules.HostState, hasState bool) fleetHostDTO {
	dto := fleetHostDTO{HostID: h.ID.String(), Name: h.Name, ZoneID: h.ZoneID.String()}
	if !hasState {
		return dto
	}
	state := string(st.State)
	since, evaluatedAt := st.Since, st.EvaluatedAt
	dto.State, dto.Since, dto.EvaluatedAt = &state, &since, &evaluatedAt
	dto.Crit, dto.Warn, dto.Unknown = st.Crit, st.Warn, st.Unknown
	return dto
}

// fleetStateRank orders the listing worst-first: a known problem before an
// unknown one (rules.Worse's own crit > warn > unknown > ok ordering), and
// a host that was never evaluated last of all — "no data" is neither a
// problem to lead with nor a clean bill of health to bury real problems
// under, so it trails every judged state, good or bad.
func fleetStateRank(state *string) int {
	if state == nil {
		return 4
	}
	switch rules.State(*state) {
	case rules.StateCrit:
		return 0
	case rules.StateWarn:
		return 1
	case rules.StateUnknown:
		return 2
	default: // rules.StateOK
		return 3
	}
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

		out := &fleetOutput{}
		out.Body.Hosts = make([]fleetHostDTO, len(roster))
		var totals fleetTotalsDTO
		for i, h := range roster {
			st, ok := byHost[h.ID]
			out.Body.Hosts[i] = toFleetHostDTO(h, st, ok)
			switch {
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
		sort.SliceStable(out.Body.Hosts, func(i, j int) bool {
			return fleetStateRank(out.Body.Hosts[i].State) < fleetStateRank(out.Body.Hosts[j].State)
		})
		out.Body.Totals = totals
		return out, nil
	})
}
