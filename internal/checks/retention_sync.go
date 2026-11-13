package checks

// The measurement retention policy sync (backlog 3.15, spec "Instance
// maximum"): at start the manager makes the TimescaleDB retention policy of
// check_samples and check_state_changes equal to PULSIX_SAMPLE_RETENTION.
// It replaces a policy only when its drop_after differs, so an unchanged
// value never touches the job, and it does both tables in one transaction
// under RetentionSyncLockKey, so managers starting together apply it one
// after the other: the second finds the first's value and does nothing.

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"
)

// RetentionSyncLockKey is the pg_advisory_xact_lock key the policy sync
// holds for its transaction, next to the expiry loop's 8315. Arbitrary but
// must never change.
const RetentionSyncLockKey = int64(8316)

// retentionTables are the hypertables the instance maximum governs: the
// measurements and the state history. Alerts, alert events and
// check_current have no retention.
func retentionTables() [2]string {
	return [2]string{"check_samples", "check_state_changes"}
}

// RetentionPolicyStore opens the sync's transaction.
type RetentionPolicyStore interface {
	// InRetentionTx runs fn in one transaction holding RetentionSyncLockKey;
	// fn's error rolls it back and comes back unchanged, success commits.
	InRetentionTx(ctx context.Context, fn func(RetentionPolicyTx) error) error
}

// RetentionPolicyTx reads and replaces a hypertable's retention policy
// inside the sync's transaction.
type RetentionPolicyTx interface {
	// RetentionPolicy returns the policy's drop_after; found is false when
	// the table has no retention policy.
	RetentionPolicy(ctx context.Context, table string) (dropAfter time.Duration, found bool, err error)
	// SetRetentionPolicy replaces the table's retention policy with one
	// dropping chunks older than days.
	SetRetentionPolicy(ctx context.Context, table string, days int) error
}

// RetentionSync makes the retention policies match the instance maximum.
type RetentionSync struct {
	store RetentionPolicyStore
	log   *slog.Logger
}

// NewRetentionSync wires the sync; a nil logger degrades to slog.Default.
func NewRetentionSync(store RetentionPolicyStore, log *slog.Logger) *RetentionSync {
	if log == nil {
		log = slog.Default()
	}
	return &RetentionSync{store: store, log: log}
}

// Sync sets both tables' retention policy to days where it differs. A
// non-positive days is refused before anything runs: it would drop every
// chunk.
func (s *RetentionSync) Sync(ctx context.Context, days int) error {
	if days <= 0 {
		return fmt.Errorf("sync retention policy: %d days is not a retention", days)
	}
	want := time.Duration(days) * 24 * time.Hour
	err := s.store.InRetentionTx(ctx, func(tx RetentionPolicyTx) error {
		for _, table := range retentionTables() {
			current, found, err := tx.RetentionPolicy(ctx, table)
			if err != nil {
				return fmt.Errorf("read retention policy of %s: %w", table, err)
			}
			if found && current == want {
				continue
			}
			if err := tx.SetRetentionPolicy(ctx, table, days); err != nil {
				return fmt.Errorf("set retention policy of %s: %w", table, err)
			}
			from := "none"
			if found {
				from = formatDays(current)
			}
			s.log.Info("measurement retention policy changed", "table", table, "from_days", from, "to_days", days)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("sync retention policy: %w", err)
	}
	return nil
}

// formatDays renders a drop_after as days, fractional when it is not a
// whole number of them (a policy set by hand).
func formatDays(d time.Duration) string {
	return strconv.FormatFloat(d.Hours()/24, 'f', -1, 64)
}
