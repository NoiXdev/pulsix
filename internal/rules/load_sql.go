// This file is the read side of one evaluation: everything a host's
// judgement depends on, gathered in one place before the pure function
// runs. It is split from due_sql.go — the claim and the write-back —
// because assembling a snapshot is its own responsibility, and because the
// port into the check pipeline belongs next to the one method that uses it.
package rules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/items"
)

// flappingWindow is how far back the change counter looks (the design's
// "changes within 30 minutes"). The boundary itself is computed by the
// database, not here — see CountStateChangesInFlappingWindow.
const flappingWindow = 30 * time.Minute

// CheckContext is what the check pipeline knows about one host that an
// evaluation needs: the resolved interval of every check that still
// applies to it — which is what "stale" is defined against, and whose
// absence is what takes a tuple out of the evaluation entirely — whether
// each of those checks still asks for a given item, and the monitoring
// groups whose selector currently matches it, which is how a group-scoped
// rule reaches the host.
type CheckContext struct {
	Intervals map[string]time.Duration
	// Asks reports, per check key, whether the check still asks the host
	// for an item (backlog 3.55): a mount taken off a disk check's list
	// keeps its check_current row, but nobody measures it any more, so it
	// is left out exactly like a check that no longer resolves. A key with
	// no predicate asks for every item, and the whole-host item "" is
	// always asked — Load never consults the predicate for it.
	Asks map[string]func(item string) bool
	// AsksMetric reports, per check key, whether the check still asks for a
	// metric of an item whose current state is labels: http.body_match once
	// body_contains is removed (backlog 3.88), any zfs metric but zfs.state
	// of a pool that reads EXPORTED or MISSING (backlog 3.74). labels are
	// the item's current discrete readings without an error, metric name
	// to label (itemLabels), nil when it has none. It is consulted for
	// every item, the whole-host item "" included. A key with no predicate
	// asks for every metric.
	AsksMetric map[string]func(labels map[string]string, metric string) bool
	// Synthetic are the check keys the manager probes the host with
	// (backlog 3.13): their readings are stamped by the manager's clock and
	// never arrive in the agent's replay, so they neither end a replay nor
	// are held by one (HostReplaying, backlog 3.118).
	Synthetic map[string]bool
	Groups    []GroupRank
}

// interval is the resolved interval tuple t is measured at, or zero —
// which takes it out of the evaluation — when its check no longer
// resolves or no longer asks for its item or for its metric of an item
// that reads labels now.
func (c CheckContext) interval(t Tuple, labels map[string]string) time.Duration {
	if t.Item != "" {
		if asks, ok := c.Asks[t.CheckKey]; ok && !asks(t.Item) {
			return 0
		}
	}
	if asks, ok := c.AsksMetric[t.CheckKey]; ok && !asks(labels, t.Metric) {
		return 0
	}
	return c.Intervals[t.CheckKey]
}

// HostChecks is the port into internal/checks. It is declared here, next
// to its consumer, and satisfied in internal/app: internal/rules never
// imports internal/checks (the two own different concerns, and an edge
// between them would make either untestable in isolation) — the same rule
// GroupRank already follows.
type HostChecks interface {
	// ContextForHost resolves hostID's checks and group membership. It is
	// called once per host per evaluation, inside that host's transaction
	// but on its own connection.
	ContextForHost(ctx context.Context, orgID, hostID uuid.UUID) (CheckContext, error)
}

// ItemStore is the slice of items.Store this package needs: recording the
// items a host reports this tick, and resolving what an operator decided
// about them. internal/rules is free to import internal/items directly —
// items is a leaf that imports neither rules nor checks (see its own doc
// comment) — so, unlike HostChecks, this interface is not an
// adapter breaking a cycle; it is declared here, next to Load, the one
// method that calls it, purely to keep that dependency to the two methods
// Load actually exercises rather than the whole of items.Store, and so
// that a unit test can swap in a fake without touching the database (see
// due_sql_test.go's own fakeItemStore).
type ItemStore interface {
	// Discover inserts every non-empty item in seen that check_items does
	// not already have a row for; first_seen never moves on one it does.
	Discover(ctx context.Context, orgID, hostID uuid.UUID, seen []items.Seen) error
	// Decisions returns the organization's decisions plus, when hostID is
	// non-nil, that host's — one query regardless of how many items the
	// host reports.
	Decisions(ctx context.Context, orgID uuid.UUID, hostID *uuid.UUID) ([]items.Decision, error)
}

// Load implements EvalStore. It assembles the whole HostLoad itself,
// resolution included: gathering a host's inputs is one job, and the
// ticker above stays a pure orchestrator because of it.
func (s *EvalSQLStore) Load(ctx context.Context, d Due, now time.Time) (HostLoad, error) {
	// The port runs on its own connection, org-scoped: this store is
	// unscoped by necessity (it claims across every organization), but an
	// ordinary domain service called from inside it gets the ordinary
	// binding, so row-level security applies to its statements rather than
	// only their own WHERE clauses. That is the same posture
	// notify.DigestTicker and schedules' own decide already take when they
	// call a service from an unscoped tick.
	checkCtx, err := s.checks.ContextForHost(db.WithOrgScope(ctx, d.OrgID), d.OrgID, d.HostID)
	if err != nil {
		return HostLoad{}, fmt.Errorf("resolve host checks: %w", err)
	}
	q := gen.New(s.conn(ctx))

	current, err := q.ListCheckCurrent(ctx, gen.ListCheckCurrentParams{OrgID: d.OrgID, HostID: d.HostID})
	if err != nil {
		return HostLoad{}, fmt.Errorf("list current values: %w", err)
	}
	tuples := make([]TupleSnapshot, len(current))
	reported := make([]Tuple, len(current))
	labels := itemLabels(current)
	newest := ItemNewest{}
	// agentNewest is the newest reading of a check the agent runs, an error
	// or a metric nobody asks for any more included: any of them taken in
	// the agent's online period proves its replay is through
	// (HostReplaying).
	var agentNewest time.Time
	for i, r := range current {
		t := Tuple{CheckKey: r.CheckKey, Item: r.Item, Metric: r.Metric}
		reported[i] = t
		if !checkCtx.Synthetic[t.CheckKey] && r.At.After(agentNewest) {
			agentNewest = r.At
		}
		tuples[i] = TupleSnapshot{
			Tuple: t, Value: r.Value, Label: r.Label, Err: textOf(r.Error), At: r.At,
			// A check the manager no longer resolves for this host, or one
			// that no longer asks for this item (backlog 3.55) or this
			// metric (backlog 3.88), or not while the item reads what it
			// reads now (backlog 3.74: an exported pool), leaves the
			// interval at zero, which is what takes the tuple out of the
			// evaluation entirely (Evaluate's own skip). The current row
			// itself is never deleted — that is v0.3b-1's ruling, and this
			// is the read side of it.
			Interval: checkCtx.interval(t, labels[itemKeyOf(t)]),
		}
		// Only a reading still asked for is evidence the check still
		// reports on its item — the same set the host page measures
		// against (GET /hosts/{id}/checks, after stillAsked) — and of
		// those only an error-free one (Observe skips errors).
		if tuples[i].Interval > 0 {
			newest.Observe(t, r.At, tuples[i].Err)
		}
	}
	// Presence is read here, ahead of the rest, because the vanished rule
	// needs the agent's online period: a replay holds it (withoutVanished).
	presence, err := s.presence(ctx, q, d)
	if err != nil {
		return HostLoad{}, err
	}
	replaying := HostReplaying(presence.AgentOnlineSince, agentNewest, now)
	tuples = withoutVanished(tuples, newest, replaying, checkCtx.Synthetic)

	// Discovery and decision resolution (v0.3b-3 design, "Discovery and
	// evaluation"): item names fall out of the tuples just read, so this
	// runs here, in the same per-host evaluation and on the same unscoped,
	// explicit-org_id posture as every other read in this function — both
	// check_items and check_item_decisions carry the same org_isolation_
	// bypass policy the rest of this store relies on (migration 00048), for
	// exactly this caller. Discover runs before Decisions is even read, so
	// an item appearing for the first time this very tick is already a
	// candidate for a decision that predates it (review focus 3): nothing
	// here is seeded once, Decide is run fresh every tick.
	if err := s.items.Discover(ctx, d.OrgID, d.HostID, seenItems(reported)); err != nil {
		return HostLoad{}, fmt.Errorf("discover items: %w", err)
	}
	decisions, err := s.items.Decisions(ctx, d.OrgID, &d.HostID)
	if err != nil {
		return HostLoad{}, fmt.Errorf("list item decisions: %w", err)
	}
	ignored := resolveIgnored(d.HostID, reported, decisions)

	ruleRows, err := q.ListRules(ctx, gen.ListRulesParams{OrgID: d.OrgID})
	if err != nil {
		return HostLoad{}, fmt.Errorf("list rules: %w", err)
	}
	orgRules := make([]Rule, len(ruleRows))
	for i, row := range ruleRows {
		r, err := toRule(row)
		if err != nil {
			return HostLoad{}, err
		}
		orgRules[i] = r
	}

	facts, err := s.facts(ctx, q, d)
	if err != nil {
		return HostLoad{}, err
	}

	stateRows, err := q.ListCheckStates(ctx, gen.ListCheckStatesParams{OrgID: d.OrgID, HostID: d.HostID})
	if err != nil {
		return HostLoad{}, fmt.Errorf("list check states: %w", err)
	}
	previous := make(map[Tuple]TupleState, len(stateRows))
	for _, row := range stateRows {
		st := toTupleState(row)
		previous[st.Tuple] = st
	}

	countRows, err := q.CountStateChangesInFlappingWindow(ctx, gen.CountStateChangesInFlappingWindowParams{
		OrgID: d.OrgID, HostID: d.HostID, FlappingWindow: toInterval(flappingWindow),
	})
	if err != nil {
		return HostLoad{}, fmt.Errorf("count state changes: %w", err)
	}
	changes30 := make(map[Tuple]int, len(countRows))
	for _, row := range countRows {
		changes30[Tuple{CheckKey: row.CheckKey, Item: row.Item, Metric: row.Metric}] = int(row.ChangeCount)
	}

	previousHost, err := s.previousHost(ctx, q, d)
	if err != nil {
		return HostLoad{}, err
	}

	return HostLoad{
		Tuples:       tuples,
		Effective:    Resolve(orgRules, HostContext{HostID: d.HostID, Groups: checkCtx.Groups, Tuples: reported}),
		Facts:        facts,
		Previous:     previous,
		Changes30:    changes30,
		PreviousHost: previousHost,
		Ignored:      ignored,
		Presence:     presence,
	}, nil
}

// presence reads the host's kind and its agent's presence (backlog 3.57).
// The organization's absent threshold is read, and the online window
// (the unknown alert's grace) set, for a client with an agent only — the
// one host either can matter to — so a server costs one statement
// here, not two. GetOrgClientAbsentDays applies the default itself and
// needs no organization scope, which this unscoped store does not have.
func (s *EvalSQLStore) presence(ctx context.Context, q *gen.Queries, d Due) (HostPresence, error) {
	row, err := q.GetHostPresence(ctx, gen.GetHostPresenceParams{
		OrgID: d.OrgID, HostID: d.HostID,
		OnlineWindowFloorSeconds:  s.agentWindow.Floor.Seconds(),
		HeartbeatsPerOnlineWindow: float64(s.agentWindow.HeartbeatsPerWindow),
	})
	if err != nil {
		return HostPresence{}, fmt.Errorf("get host presence: %w", err)
	}
	p := HostPresence{Client: row.Kind == hostKindClient, HasAgent: row.HasAgent}
	if !p.HasAgent {
		return p, nil
	}
	p.AgentOnline = row.AgentOnline
	p.AgentLastSeen = unlessEpoch(row.AgentLastContact)
	p.AgentOnlineSince = unlessEpoch(row.AgentOnlineSince)
	if p.Client {
		days, err := q.GetOrgClientAbsentDays(ctx, d.OrgID)
		if err != nil {
			return HostPresence{}, fmt.Errorf("get client absent days: %w", err)
		}
		p.AbsentAfter = time.Duration(days) * 24 * time.Hour
		p.OnlineWindow = time.Duration(row.AgentOnlineWindowSeconds * float64(time.Second))
	}
	return p, nil
}

// hostKindClient is hosts.kind's client value (hosts.KindClient; this
// package does not import internal/hosts for one string the schema's own
// CHECK already fixes).
const hostKindClient = "client"

// unlessEpoch reads the 'epoch' a query substitutes for NULL as unknown,
// the zero time.
func unlessEpoch(t time.Time) time.Time {
	if t.Unix() == 0 {
		return time.Time{}
	}
	return t
}

// facts reads hosts.facts. Facts are strings by contract — the agent
// reports map[string]string and nothing else writes the column — so a
// value that is not a JSON string is not a fact and is left out rather
// than coerced: fact() yields a number only when its stored text parses as
// one, and a fact the host does not report at all already makes the rule
// not match, which is exactly the right outcome for a value of a shape
// this contract does not have.
func (s *EvalSQLStore) facts(ctx context.Context, q *gen.Queries, d Due) (map[string]string, error) {
	raw, err := q.GetHostFacts(ctx, gen.GetHostFactsParams{OrgID: d.OrgID, HostID: d.HostID})
	if err != nil {
		return nil, fmt.Errorf("get host facts: %w", err)
	}
	if len(raw) == 0 {
		return map[string]string{}, nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("decode host facts: %w", err)
	}
	facts := make(map[string]string, len(decoded))
	for k, v := range decoded {
		if text, ok := v.(string); ok {
			facts[k] = text
		}
	}
	return facts, nil
}

// previousHost reads the roll-up the last evaluation wrote. No row means
// the host has never been evaluated, which is a nil PreviousHost and not
// an error — see Snapshot.PreviousHost for why filling this field matters
// even though nil is supported.
func (s *EvalSQLStore) previousHost(ctx context.Context, q *gen.Queries, d Due) (*HostState, error) {
	row, err := q.GetCheckHostState(ctx, gen.GetCheckHostStateParams{OrgID: d.OrgID, HostID: d.HostID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil //nolint:nilnil // "never evaluated" is a value, not a failure
	}
	if err != nil {
		return nil, fmt.Errorf("get host roll-up: %w", err)
	}
	return &HostState{
		HostID: row.HostID, State: State(row.State),
		Crit: int(row.CritCount), Warn: int(row.WarnCount), Unknown: int(row.UnknownCount),
		Since: row.Since, EvaluatedAt: row.EvaluatedAt,
	}, nil
}

// textOf flattens a nullable text column to the empty string.
func textOf(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// itemLabels is what each of a host's items reads now (backlog 3.74): the
// labels of its current discrete readings without an error, metric name
// to label — checks.CurrentLabels' rule over the rows Load reads, the one
// the host page applies (GET /hosts/{id}/checks, stillAsked); keep both in
// step (this package never imports internal/checks). An item with no such
// reading has none.
func itemLabels(current []gen.CheckCurrent) map[ItemKey]map[string]string {
	out := map[ItemKey]map[string]string{}
	for _, r := range current {
		if r.Label == nil || textOf(r.Error) != "" {
			continue
		}
		k := ItemKey{CheckKey: r.CheckKey, Item: r.Item}
		if out[k] == nil {
			out[k] = map[string]string{}
		}
		out[k][r.Metric] = *r.Label
	}
	return out
}

// seenItems derives Discover's own input from a host's reported tuples:
// one items.Seen per distinct non-empty item name. cpu and memory report
// one whole-host reading with an empty item — there is nothing there to
// discover (the design's own "Items with an empty name" ruling) — and two
// metrics of the same item (disk.used_pct and disk.free_bytes of the same
// mount) collapse to the one item they share, since Discover's own primary
// key is (host, check_key, item), not (…, metric).
func seenItems(reported []Tuple) []items.Seen {
	seen := make(map[items.Seen]bool, len(reported))
	out := make([]items.Seen, 0, len(reported))
	for _, t := range reported {
		if t.Item == "" {
			continue
		}
		s := items.Seen{CheckKey: t.CheckKey, Name: t.Item}
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// resolveIgnored runs items.Decide once per distinct ItemKey among
// reported — including the empty-item ones, since a decision could in
// principle name one even though nothing discovers it — and keeps only
// the ones it resolved to Ignore. An absent key means watched
// (Snapshot.Ignored's own doc comment), so a host with no matching
// decision at all costs nothing beyond the lookup itself, and a host whose
// decisions cover only some of its items marks exactly those.
func resolveIgnored(hostID uuid.UUID, reported []Tuple, decisions []items.Decision) map[ItemKey]bool {
	ignored := make(map[ItemKey]bool)
	seen := make(map[ItemKey]bool, len(reported))
	for _, t := range reported {
		key := itemKeyOf(t)
		if seen[key] {
			continue
		}
		seen[key] = true
		target := items.Target{HostID: hostID, CheckKey: key.CheckKey, Item: key.Item}
		if items.Decide(decisions, target).Ignored() {
			ignored[key] = true
		}
	}
	return ignored
}
