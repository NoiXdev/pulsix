package rules

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/hosts/selector"
)

// toTupleState converts one check_states row. CandidateState/CandidateSince
// scan as nil together or set together — the schema's own CHECK
// ((candidate_state IS NULL) = (candidate_since IS NULL)) — so Candidate
// reads back as the zero State exactly when CandidateSince is nil.
func toTupleState(r gen.CheckState) TupleState {
	var candidate State
	if r.CandidateState != nil {
		candidate = State(*r.CandidateState)
	}
	return TupleState{
		Tuple:          Tuple{CheckKey: r.CheckKey, Item: r.Item, Metric: r.Metric},
		State:          State(r.State),
		Since:          r.Since,
		RuleID:         db.UUIDPtr(r.RuleID),
		LastValue:      r.LastValue,
		LastLabel:      r.LastLabel,
		Candidate:      candidate,
		CandidateSince: db.TimePtr(r.CandidateSince),
		Flapping:       r.Flapping,
		EvaluatedAt:    r.EvaluatedAt,
	}
}

// StatesForHost implements StateStore.
func (s *SQLStore) StatesForHost(ctx context.Context, orgID, hostID uuid.UUID) ([]TupleState, error) {
	rows, err := s.q.ListCheckStates(ctx, gen.ListCheckStatesParams{OrgID: orgID, HostID: hostID})
	if err != nil {
		return nil, fmt.Errorf("states for host: %w", err)
	}
	out := make([]TupleState, len(rows))
	for i, r := range rows {
		out[i] = toTupleState(r)
	}
	return out, nil
}

// ChangeCounts implements StateStore.
func (s *SQLStore) ChangeCounts(ctx context.Context, orgID, hostID uuid.UUID, since time.Time) (map[Tuple]int, error) {
	rows, err := s.q.CountStateChangesSince(ctx, gen.CountStateChangesSinceParams{OrgID: orgID, HostID: hostID, Since: since})
	if err != nil {
		return nil, fmt.Errorf("change counts: %w", err)
	}
	out := make(map[Tuple]int, len(rows))
	for _, r := range rows {
		out[Tuple{CheckKey: r.CheckKey, Item: r.Item, Metric: r.Metric}] = int(r.ChangeCount)
	}
	return out, nil
}

// tagFragment builds the host_tags condition FleetFilter.Tag describes:
// "key=value" for an exact match, or a bare key for "this key is set to
// anything" — see FleetFilter's own doc comment for why this is written
// directly instead of through selector.Parse. offset is the number of
// query parameters already used, exactly like selector.Selector.SQL's own
// offset parameter. The fragment assumes a CTE or table named "host_tags"
// with (host_id, key, value) columns is in scope, correlated to the outer
// query's "h" alias — the same contract selector.Selector.SQL documents.
func tagFragment(tag string, offset int) (string, []any) {
	if key, value, ok := strings.Cut(tag, "="); ok {
		return fmt.Sprintf(
			"EXISTS (SELECT 1 FROM host_tags t WHERE t.host_id = h.id AND t.key = $%d AND t.value = $%d)",
			offset+1, offset+2,
		), []any{key, value}
	}
	return fmt.Sprintf(
		"EXISTS (SELECT 1 FROM host_tags t WHERE t.host_id = h.id AND t.key = $%d)",
		offset+1,
	), []any{tag}
}

// errGroupNotFound is returned by groupSelector when groupID names no
// group in orgID — including one that belongs to another organization,
// which reads the same as one that does not exist (ErrNotFound's own
// contract elsewhere in this package). HostStates treats it as "matches no
// host" rather than surfacing it as a store error: a fleet filter naming a
// deleted or foreign group is not a caller mistake HostStates itself is
// positioned to reject, only Task 11's API layer knows whether that is
// worth a 404 of its own.
var errGroupNotFound = errors.New("group not found")

// groupSelector looks up groupID's stored selector text among orgID's
// monitoring groups and parses it. Groups are few per organization (they
// exist to be enumerated in an editor — see checks.Service.GetGroup's own
// doc comment for the same reasoning applied to a single lookup), so
// loading the whole list and filtering in Go costs nothing extra and needs
// no new query in a table this package does not otherwise own.
//
// The selector was already validated by checks.Service when the group was
// created (normalize's own selector.Parse call) — a parse failure here
// would mean that guarantee broke, not that the caller did anything wrong,
// so it is wrapped and returned as a plain error rather than ErrInvalidInput.
func (s *SQLStore) groupSelector(ctx context.Context, orgID, groupID uuid.UUID) (selector.Selector, error) {
	groups, err := s.q.ListMonitoringGroups(ctx, orgID)
	if err != nil {
		return selector.Selector{}, fmt.Errorf("list monitoring groups: %w", err)
	}
	for _, g := range groups {
		if g.ID == groupID {
			sel, err := selector.Parse(g.Selector)
			if err != nil {
				return selector.Selector{}, fmt.Errorf("parse stored selector for group %s: %w", groupID, err)
			}
			return sel, nil
		}
	}
	return selector.Selector{}, errGroupNotFound
}

// hostStateColumns is the exact column list and order HostStates selects.
// The Scan call below must list its destinations in this same order — like
// hosts.queryHostsColumns, this query is hand-written, so a change to one
// requires the matching change to the other.
const hostStateColumns = "chs.org_id, chs.host_id, chs.state, chs.crit_count, chs.warn_count, chs.unknown_count, chs.since, chs.evaluated_at"

// HostStates implements StateStore. Tag and GroupID both need a WHERE
// fragment built at runtime (see tagFragment and groupSelector), which is
// why this method runs through s.db directly rather than a sqlc-generated
// query — the same reason hosts.SQLStore.QueryHosts does, including the
// leading CTE that renames "host_tags" to the manual-overrides-fact merged
// view (see that method's own doc comment for why the merge cannot happen
// inside the fragment itself).
func (s *SQLStore) HostStates(ctx context.Context, orgID uuid.UUID, f FleetFilter) ([]HostState, error) {
	query := `WITH host_tags AS (
	SELECT DISTINCT ON (host_id, key) host_id, key, value
	FROM host_tags
	WHERE org_id = $1
	ORDER BY host_id, key, (source = 'manual') DESC
)
SELECT ` + hostStateColumns + `
FROM check_host_states chs
JOIN hosts h ON h.id = chs.host_id
WHERE chs.org_id = $1`
	args := []any{orgID}

	if f.ZoneID != nil {
		args = append(args, *f.ZoneID)
		query += fmt.Sprintf(" AND h.zone_id = $%d", len(args))
	}
	if f.Tag != "" {
		frag, tagArgs := tagFragment(f.Tag, len(args))
		args = append(args, tagArgs...)
		query += " AND " + frag
	}
	if f.GroupID != nil {
		sel, err := s.groupSelector(ctx, orgID, *f.GroupID)
		if errors.Is(err, errGroupNotFound) {
			return nil, nil // a filter naming a nonexistent group matches no host
		}
		if err != nil {
			return nil, fmt.Errorf("host states: %w", err)
		}
		frag, selArgs := sel.SQL(len(args))
		if frag != "" {
			args = append(args, selArgs...)
			query += " AND (" + frag + ")"
		}
	}
	query += " ORDER BY chs.host_id"

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("host states: %w", err)
	}
	defer rows.Close()

	var out []HostState
	for rows.Next() {
		var r gen.CheckHostState
		if err := rows.Scan(&r.OrgID, &r.HostID, &r.State, &r.CritCount, &r.WarnCount, &r.UnknownCount, &r.Since, &r.EvaluatedAt); err != nil {
			return nil, fmt.Errorf("host states: %w", err)
		}
		out = append(out, HostState{
			HostID: r.HostID, State: State(r.State),
			Crit: int(r.CritCount), Warn: int(r.WarnCount), Unknown: int(r.UnknownCount),
			Since: r.Since, EvaluatedAt: r.EvaluatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("host states: %w", err)
	}
	return out, nil
}
