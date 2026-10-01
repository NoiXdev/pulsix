// This file is the ticker's store: the claim that shares hosts between
// managers and the write-back that ends one host's evaluation — both
// inside one short transaction per host, so a host is evaluated and
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
	"errors"
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
	items  ItemStore
	log    *slog.Logger
}

// NewEvalSQLStore builds the store; a nil log is slog.Default().
func NewEvalSQLStore(d *db.DB, checks HostChecks, items ItemStore, log *slog.Logger) *EvalSQLStore {
	if log == nil {
		log = slog.Default()
	}
	return &EvalSQLStore{d: d, checks: checks, items: items, log: log}
}

// evalTxKey is the context key evaluateOne binds one host's transaction
// under. Load, Windows and Apply pick it up through conn below, which is
// what makes "fn runs inside its own transaction" true of the three store
// methods fn calls rather than only of fn itself. It is deliberately this
// package's own key and not db.WithWriteTx's: the ticker runs unscoped,
// across every organization, which is precisely what that helper's
// org-bound transaction cannot express.
type evalTxKey struct{}

// claimTx reports the claimed host's transaction bound to ctx, if any.
func claimTx(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(evalTxKey{}).(pgx.Tx)
	return tx, ok
}

// conn is the connection a READ runs on: the claimed host's transaction
// when there is one, and the pool otherwise — a direct call to Load or
// Windows from outside ProcessDue, which the tests and any future
// read-only caller make, is a legitimate thing to do and reads the same
// rows either way.
//
// Keeping the reads on the claim's own transaction is what makes a host's
// inputs and its write-back one consistent snapshot even though the
// transaction now covers one host rather than the whole batch.
//
// Apply deliberately does not use this fallback (see errNoClaimTx): a
// write outside the claim would be exactly the silent failure this file's
// atomicity is built to prevent.
func (s *EvalSQLStore) conn(ctx context.Context) gen.DBTX {
	if tx, ok := claimTx(ctx); ok {
		return tx
	}
	return s.d.Pool()
}

// errNoClaimTx is what Apply returns when its context carries no claimed
// host's transaction. Falling back to the pool the way the reads do would
// write the states, the changes, the roll-up and the cursor advance on
// four separate autocommitted statements, with no row lock on the cursor
// and no way to undo the first three if the fourth failed — the whole
// atomicity this store exists for, lost to a caller that simply forgot to
// pass the context evaluateOne handed it. Refusing is the only behaviour
// that cannot be got wrong quietly.
var errNoClaimTx = errors.New("rules: apply evaluation outside a claimed host's transaction")

// ProcessDue implements EvalStore: it lists up to limit dirty-or-due
// hosts and then evaluates each one in a transaction of its own.
//
// The claim is deliberately in two halves (claimDue, then
// LockOneEvalCursor per host) rather than internal/schedules' single
// batch-wide transaction, which this store originally copied. The reason
// that precedent does not transfer is the other side of the lock:
// MarkHostDirty is an INSERT … ON CONFLICT on this same table, run inside
// internal/checks' ingest transaction, on the 7 000-samples-a-second hot
// path the whole cursor design exists to keep free. A row lock lives until
// its transaction commits, so a transaction spanning a 200-host batch —
// each host costing ContextForHost on a SECOND pool connection,
// ListCheckCurrent, a full-organization ListRules, GetHostFacts,
// ListCheckStates, the flapping count, the roll-up read, maybe the window
// statement, then one upsert per tuple — would block every agent whose
// host it had claimed for the length of the batch, and pin Postgres' xmin
// horizon for just as long. Nothing writes internal/schedules' own due
// table from a path like that.
//
// It is also what the spec asks for in so many words: "Each host is
// evaluated in its own transaction, so one host whose data is broken
// cannot roll back the others." A savepoint delivered the rollback
// isolation that sentence gives as its reason, but not the lock lifetime.
//
// What the split costs is one race, and it is benign: between the scan and
// the per-host lock another manager may take a host, or finish it. Either
// way LockOneEvalCursor returns no row and the host is skipped — exactly
// what the scan itself would have done a moment earlier.
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
	due, err := s.claimDue(ctx, limit)
	if err != nil {
		return 0, err
	}
	done := 0
	for i, d := range due {
		if ctx.Err() != nil {
			// The manager is shutting down, or the batch deadline fired.
			// Every host from here on is left exactly as it is — still due —
			// rather than started and each logging its own failure: one line
			// here beats one per remaining host.
			s.log.WarnContext(context.WithoutCancel(ctx), "evaluation batch cancelled; leaving the remaining hosts due for the next tick",
				"evaluated", done, "remaining", len(due)-i)
			break
		}
		if s.evaluateOne(ctx, d, fn) {
			done++
		}
	}
	return done, nil
}

// claimDue lists the hosts this tick will try, in a transaction opened for
// that one statement and rolled back the moment it returns. The scan takes
// FOR UPDATE SKIP LOCKED so it does not list a host another manager is
// mid-evaluation on; it does NOT hold those locks, because holding them is
// precisely what ProcessDue's comment explains this store must not do.
// Rollback rather than commit: the statement wrote nothing, and releasing
// the locks is the entire point of ending the transaction.
func (s *EvalSQLStore) claimDue(ctx context.Context, limit int) ([]Due, error) {
	tx, err := s.d.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin evaluation claim: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // read-only, nothing to lose

	rows, err := gen.New(tx).ClaimDueEvalCursors(ctx, int32(limit)) //nolint:gosec // limit is the ticker's own batch size
	if err != nil {
		return nil, fmt.Errorf("claim due evaluation cursors: %w", err)
	}
	due := make([]Due, len(rows))
	for i, r := range rows {
		due[i] = Due{OrgID: r.OrgID, HostID: r.HostID}
	}
	return due, nil
}

// evaluateOne claims one host for real and runs fn for it inside that
// host's own transaction, reporting whether it committed. Any failure —
// the host already taken or no longer due, fn's own error, a transaction
// that will not begin or commit — leaves the host untouched and still due;
// a panic is recovered here so that one host's bug costs that host and not
// the batch, the belt to Ticker.tickSafely's suspenders.
//
// The commit runs on ctx itself rather than on a detached context.
// internal/schedules' processRow has to detach, because its write-back
// records a job it has ALREADY enqueued and losing the record re-fires the
// job. Nothing here has an outside effect: every statement Apply issues is
// an upsert or an append that is rolled back with it, so a host whose
// transaction is cut off by the batch deadline is simply evaluated again
// on the next tick, writing the same state and the same single change.
func (s *EvalSQLStore) evaluateOne(ctx context.Context, d Due, fn func(ctx context.Context, d Due) error) (committed bool) {
	defer func() {
		if r := recover(); r != nil {
			s.log.ErrorContext(context.WithoutCancel(ctx), "evaluating a host panicked; leaving it due for the next tick",
				"org", d.OrgID, "host", d.HostID, "panic", r, "stack", string(debug.Stack()))
			committed = false
		}
	}()

	tx, err := s.d.Pool().Begin(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "begin host transaction; leaving the host due for the next tick",
			"org", d.OrgID, "host", d.HostID, "err", err)
		return false
	}
	defer func() {
		if !committed {
			tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
		}
	}()

	// The claim proper. No row means another manager locked this host, or
	// finished it, between the scan and now — not an error, and not worth a
	// log line either: it is the ordinary outcome of two managers sharing a
	// fleet.
	switch _, err := gen.New(tx).LockOneEvalCursor(ctx, d.HostID); {
	case errors.Is(err, pgx.ErrNoRows):
		return false
	case err != nil:
		s.log.ErrorContext(ctx, "lock host cursor; leaving the host due for the next tick",
			"org", d.OrgID, "host", d.HostID, "err", err)
		return false
	}

	if err := fn(context.WithValue(ctx, evalTxKey{}, tx), d); err != nil {
		// fn (Ticker.evaluate) has already logged which step failed and why.
		return false
	}
	if err := tx.Commit(ctx); err != nil {
		s.log.ErrorContext(ctx, "commit host transaction; leaving the host due for the next tick",
			"org", d.OrgID, "host", d.HostID, "err", err)
		return false
	}
	return true
}

// Apply implements EvalStore: states, changes, roll-up and the cursor's
// advance, all on the connection evaluateOne bound to ctx — so either the
// whole evaluation lands or none of it does.
func (s *EvalSQLStore) Apply(ctx context.Context, d Due, res Result) error {
	tx, ok := claimTx(ctx)
	if !ok {
		return errNoClaimTx
	}
	q := gen.New(tx)

	// Dropped rows go first, before the states that were actually produced:
	// an ignored tuple's stored check_states row must not outlive this
	// evaluation (see Result.Dropped's own doc comment). There is nothing
	// for these two loops to race over — Dropped and States never name the
	// same tuple, since a tuple is only ever in one or the other — but
	// deleting first is still the right order: it is the one that reads as
	// "clear what is no longer judged, then write what is."
	for _, t := range res.Dropped {
		if err := q.DeleteCheckState(ctx, gen.DeleteCheckStateParams{
			OrgID: d.OrgID, HostID: d.HostID,
			CheckKey: t.CheckKey, Item: t.Item, Metric: t.Metric,
		}); err != nil {
			return fmt.Errorf("delete dropped check state: %w", err)
		}
	}

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
