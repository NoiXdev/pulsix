package rules

import (
	"context"

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
