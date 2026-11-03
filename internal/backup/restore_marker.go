package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/restoremark"
)

// writeRestoreMarker stores the restore marker (internal/restoremark) for a restore of the backup created at
// backupCreatedAt, replacing whatever row the backup itself carried. RestoredAt
// is the database's now(), read in tx, so it shares a clock with the
// not_before dates of certificates the manager records.
func writeRestoreMarker(ctx context.Context, tx pgx.Tx, backupCreatedAt time.Time) error {
	var restoredAt time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&restoredAt); err != nil {
		return fmt.Errorf("load: read the database clock: %w", err)
	}
	value, err := json.Marshal(restoremark.Marker{BackupCreatedAt: backupCreatedAt.UTC(), RestoredAt: restoredAt.UTC()})
	if err != nil {
		return fmt.Errorf("load: encode the restore marker: %w", err)
	}
	if err := gen.New(tx).UpsertInstanceSetting(ctx, gen.UpsertInstanceSettingParams{Key: restoremark.Key, Value: value}); err != nil {
		return fmt.Errorf("load: write the restore marker: %w", err)
	}
	return nil
}
