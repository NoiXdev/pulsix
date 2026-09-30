// This file is the ticker's store: the claim that shares hosts between
// managers and the write-back that ends one host's evaluation — both
// inside the transaction the claim opened, so a host is evaluated and
// recorded atomically or not at all. The reads that gather a host's inputs
// are load_sql.go, and the one window statement is window_sql.go.
//
// It runs on the raw pool with no pulsix.org_id bound, exactly like
// internal/schedules' own due store: the claim scans every organization's
// cursors at once, where the org_isolation_bypass policy applies, and
// every other statement names its org_id in its own WHERE clause.
package rules

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// EvalSQLStore implements EvalStore.
type EvalSQLStore struct {
	d      *db.DB
	checks HostChecks
	log    *slog.Logger
}

// NewEvalSQLStore builds the store; a nil log is slog.Default().
func NewEvalSQLStore(d *db.DB, checks HostChecks, log *slog.Logger) *EvalSQLStore {
	if log == nil {
		log = slog.Default()
	}
	return &EvalSQLStore{d: d, checks: checks, log: log}
}

// evalTxKey is the context key ProcessDue binds one host's transaction
// under. Load, Windows and Apply pick it up through conn below, which is
// what makes "fn runs inside its own transaction" true of the three store
// methods fn calls rather than only of fn itself. It is deliberately this
// package's own key and not db.WithWriteTx's: the ticker runs unscoped,
// across every organization, which is precisely what that helper's
// org-bound transaction cannot express.
type evalTxKey struct{}

// conn is the connection a statement runs on: the claimed host's
// transaction when there is one, and the pool otherwise (a direct call to
// Load or Windows from outside ProcessDue, which the tests and any future
// read-only caller make).
func (s *EvalSQLStore) conn(ctx context.Context) gen.DBTX {
	if tx, ok := ctx.Value(evalTxKey{}).(pgx.Tx); ok {
		return tx
	}
	return s.d.Pool()
}

// ProcessDue implements EvalStore. It follows internal/schedules'
// DueSQLStore exactly rather than inventing a second claim protocol: one
// transaction per batch takes the row locks, and each claimed host is then
// processed inside its own savepoint (a pgx.Tx.Begin nested in a
// transaction is a SAVEPOINT, not a new transaction), so a host whose data
// is broken rolls back only its own writes. Hosts already committed to a
// savepoint earlier in the batch are unaffected and are part of the one
// commit at the end.
//
// now is the ticker's own clock reading, which the pure evaluation is
// stamped with. Nothing in the SQL below uses it: every dirty/staleness
// comparison is against the database's own now() (backlog 2.80), so
// managers with skewed clocks still agree on which hosts are due.
//
// The returned count is the number of hosts fn returned nil for. A host
// fn failed is left exactly as it was — its cursor untouched, so it is
// still dirty or still overdue and the next tick retries it, the same
// at-least-once guarantee a failed commit already gives.
func (s *EvalSQLStore) ProcessDue(ctx context.Context, now time.Time, limit int, fn func(ctx context.Context, d Due) error) (int, error) {
	tx, err := s.d.Pool().Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin evaluation tick: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed

	rows, err := gen.New(tx).ClaimDueEvalCursors(ctx, int32(limit)) //nolint:gosec // limit is the ticker's own batch size
	if err != nil {
		return 0, fmt.Errorf("claim due evaluation cursors: %w", err)
	}
	done := 0
	for i, r := range rows {
		if ctx.Err() != nil {
			// The manager is shutting down. Every host from here on is left
			// exactly as it is — still locked until the commit below
			// releases it, still due — rather than started and each logging
			// its own failure: one line here beats one per remaining host.
			s.log.WarnContext(context.WithoutCancel(ctx), "evaluation batch cancelled; leaving the remaining hosts due for the next tick",
				"evaluated", done, "remaining", len(rows)-i)
			break
		}
		if s.evaluateOne(ctx, tx, Due{OrgID: r.OrgID, HostID: r.HostID}, fn) {
			done++
		}
	}
	// Committed on a detached context: a cancellation that arrives mid-batch
	// must not discard the hosts whose savepoints already committed —
	// re-evaluating them would be harmless, but the state changes they
	// appended are history and would be written twice.
	if err := tx.Commit(context.WithoutCancel(ctx)); err != nil {
		return 0, fmt.Errorf("commit evaluation tick: %w", err)
	}
	return done, nil
}

// evaluateOne runs fn for one claimed host inside its own savepoint and
// reports whether it committed. Any failure — fn's own error, or a
// savepoint that will not begin or commit — leaves the host untouched and
// still due; a panic is recovered here so that one host's bug costs that
// host and not the batch, the belt to Ticker.tickSafely's suspenders.
func (s *EvalSQLStore) evaluateOne(ctx context.Context, tx pgx.Tx, d Due, fn func(ctx context.Context, d Due) error) (committed bool) {
	defer func() {
		if r := recover(); r != nil {
			s.log.ErrorContext(context.WithoutCancel(ctx), "evaluating a host panicked; leaving it due for the next tick",
				"org", d.OrgID, "host", d.HostID, "panic", r, "stack", string(debug.Stack()))
			committed = false
		}
	}()

	sp, err := tx.Begin(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "begin host savepoint; leaving the host due for the next tick",
			"org", d.OrgID, "host", d.HostID, "err", err)
		return false
	}
	defer func() {
		if !committed {
			sp.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
		}
	}()

	if err := fn(context.WithValue(ctx, evalTxKey{}, sp), d); err != nil {
		// fn (Ticker.evaluate) has already logged which step failed and why.
		return false
	}
	if err := sp.Commit(ctx); err != nil {
		s.log.ErrorContext(ctx, "commit host savepoint; leaving the host due for the next tick",
			"org", d.OrgID, "host", d.HostID, "err", err)
		return false
	}
	return true
}

// Apply implements EvalStore: states, changes, roll-up and the cursor's
// advance, all on the connection ProcessDue bound to ctx — so either the
// whole evaluation lands or none of it does.
func (s *EvalSQLStore) Apply(ctx context.Context, d Due, res Result) error {
	q := gen.New(s.conn(ctx))

	for _, st := range res.States {
		// candidate_state and candidate_since are set together or not at
		// all — the table's own CHECK. Evaluate never produces one without
		// the other; this is what makes that true of the statement too.
		var candidate *string
		var candidateSince pgtype.Timestamptz
		if st.Candidate != "" && st.CandidateSince != nil {
			c := string(st.Candidate)
			candidate, candidateSince = &c, db.NullTime(st.CandidateSince)
		}
		if err := q.UpsertCheckState(ctx, gen.UpsertCheckStateParams{
			OrgID: d.OrgID, HostID: d.HostID,
			CheckKey: st.Tuple.CheckKey, Item: st.Tuple.Item, Metric: st.Tuple.Metric,
			State: string(st.State), Since: st.Since, RuleID: db.NullUUID(st.RuleID),
			LastValue: st.LastValue, LastLabel: st.LastLabel,
			CandidateState: candidate, CandidateSince: candidateSince,
			Flapping: st.Flapping, EvaluatedAt: st.EvaluatedAt,
		}); err != nil {
			return fmt.Errorf("upsert check state: %w", err)
		}
	}

	for _, ch := range res.Changes {
		if err := q.InsertCheckStateChange(ctx, gen.InsertCheckStateChangeParams{
			At: ch.At, OrgID: d.OrgID, HostID: d.HostID,
			CheckKey: ch.Tuple.CheckKey, Item: ch.Tuple.Item, Metric: ch.Tuple.Metric,
			FromState: string(ch.From), ToState: string(ch.To),
			RuleID: db.NullUUID(ch.RuleID), Value: ch.Value, Label: ch.Label,
		}); err != nil {
			return fmt.Errorf("insert state change: %w", err)
		}
	}

	if err := s.applyRollUp(ctx, q, d, res.Host); err != nil {
		return err
	}

	if err := q.AdvanceEvalCursor(ctx, gen.AdvanceEvalCursorParams{
		OrgID: d.OrgID, HostID: d.HostID, NextDueAt: res.NextDue,
	}); err != nil {
		return fmt.Errorf("advance evaluation cursor: %w", err)
	}
	return nil
}

// applyRollUp writes the host roll-up, or deletes it when the evaluation
// judged no tuple at all. The zero HostState is that instruction (see
// Result's own doc comment): a host all of whose checks lost their
// resolved interval must not keep showing the colour it had when its last
// check was deleted. State is what distinguishes the two — it is never
// empty on a roll-up that was actually computed.
func (s *EvalSQLStore) applyRollUp(ctx context.Context, q *gen.Queries, d Due, host HostState) error {
	if host.State == "" {
		if err := q.DeleteCheckHostState(ctx, gen.DeleteCheckHostStateParams{OrgID: d.OrgID, HostID: d.HostID}); err != nil {
			return fmt.Errorf("delete host roll-up: %w", err)
		}
		return nil
	}
	if err := q.UpsertCheckHostState(ctx, gen.UpsertCheckHostStateParams{
		OrgID: d.OrgID, HostID: d.HostID, State: string(host.State),
		CritCount: int32(host.Crit), WarnCount: int32(host.Warn), UnknownCount: int32(host.Unknown), //nolint:gosec // counts are bounded by the host's tuple count
		Since: host.Since, EvaluatedAt: host.EvaluatedAt,
	}); err != nil {
		return fmt.Errorf("upsert host roll-up: %w", err)
	}
	return nil
}
