package rules

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// ContextWithClaimedTx is exported only for this package's own tests (the
// export_test.go idiom): ticker_test.go's fakeEvalStore has no way to name
// the unexported evalTxKey evaluateOne (due_sql.go) binds the claimed
// host's transaction under, so it reaches the same context slot through
// here instead — which is what lets Ticker.evaluate's new claimTx(ctx)
// lookup succeed against a fake store the same way it does against the
// real EvalSQLStore. It exists only in the test binary; production code
// has no reason to ever call it, since evaluateOne already binds the real
// transaction directly.
func ContextWithClaimedTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, evalTxKey{}, tx)
}

// ClaimedTx is the other half of ContextWithClaimedTx: it hands a test
// the transaction evaluateOne bound for the host fn is evaluating, so a
// test can read the session settings that transaction runs under.
func ClaimedTx(ctx context.Context) (pgx.Tx, bool) {
	return claimTx(ctx)
}

// FailedHostBackoff exposes failedHostBackoff to this package's tests.
const FailedHostBackoff = failedHostBackoff

// WithBatchDeadlineFloor sets the ticker's batch-deadline floor, which a
// test must be able to shorten: at its real 10 seconds, proving that a
// batch deadline cuts a host off would mean waiting for it. (The
// in-package ticker_internal_test.go reaches the field directly.)
func WithBatchDeadlineFloor(d time.Duration) TickerOption {
	return func(t *Ticker) { t.deadlineFloor = d }
}

// SetHostDeadline overrides the per-host deadline of one store (backlog
// 7.112) so a test can cut a host off in milliseconds.
func (s *EvalSQLStore) SetHostDeadline(d time.Duration) { s.hostDeadline = d }
