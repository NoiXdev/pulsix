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
)

// flappingWindow is how far back the change counter looks (the design's
// "changes within 30 minutes"). The boundary itself is computed by the
// database, not here — see CountStateChangesInFlappingWindow.
const flappingWindow = 30 * time.Minute

// CheckContext is what the check pipeline knows about one host that an
// evaluation needs: the resolved interval of every check that still
// applies to it — which is what "stale" is defined against, and whose
// absence is what takes a tuple out of the evaluation entirely — and the
// monitoring groups whose selector currently matches it, which is how a
// group-scoped rule reaches the host.
type CheckContext struct {
	Intervals map[string]time.Duration
	Groups    []GroupRank
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

// Load implements EvalStore. It assembles the whole HostLoad itself,
// resolution included: gathering a host's inputs is one job, and the
// ticker above stays a pure orchestrator because of it.
func (s *EvalSQLStore) Load(ctx context.Context, d Due) (HostLoad, error) {
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
	for i, r := range current {
		t := Tuple{CheckKey: r.CheckKey, Item: r.Item, Metric: r.Metric}
		reported[i] = t
		tuples[i] = TupleSnapshot{
			Tuple: t, Value: r.Value, Label: r.Label, Err: textOf(r.Error), At: r.At,
			// A check the manager no longer resolves for this host leaves
			// the interval at zero, which is what takes the tuple out of the
			// evaluation entirely (Evaluate's own skip). The current row
			// itself is never deleted — that is v0.3b-1's ruling, and this
			// is the read side of it.
			Interval: checkCtx.Intervals[r.CheckKey],
		}
	}

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
	}, nil
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
