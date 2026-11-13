package checks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
)

// RetentionPolicySQLStore is RetentionPolicyStore on TimescaleDB. Replacing
// a retention policy needs the hypertable's owner, so it must be built on
// the manager's migration connection (the schema owner), never on the
// pulsix_app pool. The statements call TimescaleDB's own catalog functions
// and views, which sqlc cannot type, so they are written here by hand.
type RetentionPolicySQLStore struct{ d *db.DB }

// NewRetentionPolicySQLStore wraps d, a connection as the schema owner.
func NewRetentionPolicySQLStore(d *db.DB) *RetentionPolicySQLStore {
	return &RetentionPolicySQLStore{d: d}
}

// InRetentionTx begins a transaction, takes RetentionSyncLockKey for it
// (pg_advisory_xact_lock: released at commit or rollback, nothing to
// forget) and runs fn. A second manager starting at the same time waits
// here and then reads what the first committed.
func (s *RetentionPolicySQLStore) InRetentionTx(ctx context.Context, fn func(RetentionPolicyTx) error) error {
	tx, err := s.d.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin retention transaction: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, RetentionSyncLockKey); err != nil {
		return fmt.Errorf("acquire retention sync lock: %w", err)
	}
	if err := fn(retentionPolicySQLTx{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit retention transaction: %w", err)
	}
	return nil
}

// retentionPolicySQLTx is RetentionPolicyTx on one transaction.
type retentionPolicySQLTx struct{ tx pgx.Tx }

// RetentionPolicy reads drop_after from timescaledb_information.jobs, in
// seconds so the comparison does not depend on how the interval is
// written. A hypertable has at most one retention policy.
func (t retentionPolicySQLTx) RetentionPolicy(ctx context.Context, table string) (time.Duration, bool, error) {
	var seconds int64
	err := t.tx.QueryRow(ctx, `
		SELECT EXTRACT(EPOCH FROM (config->>'drop_after')::interval)::bigint
		FROM timescaledb_information.jobs
		WHERE proc_name = 'policy_retention' AND hypertable_schema = 'public' AND hypertable_name = $1`,
		table).Scan(&seconds)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return time.Duration(seconds) * time.Second, true, nil
}

// SetRetentionPolicy removes the table's policy, if any, and adds one with
// drop_after days. Both run in the caller's transaction, so a failure
// leaves the old policy in place.
func (t retentionPolicySQLTx) SetRetentionPolicy(ctx context.Context, table string, days int) error {
	if _, err := t.tx.Exec(ctx,
		`SELECT remove_retention_policy(format('public.%I', $1::text)::regclass, if_exists => true)`, table); err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	if _, err := t.tx.Exec(ctx,
		`SELECT add_retention_policy(format('public.%I', $1::text)::regclass, make_interval(days => $2::int))`, table, days); err != nil {
		return fmt.Errorf("add: %w", err)
	}
	return nil
}
