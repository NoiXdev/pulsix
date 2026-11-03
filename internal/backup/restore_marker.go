package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// RestoreMarkerKey is the instance_settings row that records the last restore.
const RestoreMarkerKey = "restore"

// RestoreMarker says which backup the last restore loaded and when. The
// manager adopts a certificate issued between the two (it was issued after the
// backup and is unknown to the restored database) and nothing outside that
// window. A database no restore ever touched has no marker.
type RestoreMarker struct {
	// BackupCreatedAt is the creation time of the restored backup.
	BackupCreatedAt time.Time `json:"backup_created_at"`
	// RestoredAt is the database's clock when the restore loaded the backup.
	RestoredAt time.Time `json:"restored_at"`
}

// writeRestoreMarker stores the marker for a restore of the backup created at
// backupCreatedAt, replacing whatever row the backup itself carried. RestoredAt
// is the database's now(), read in tx, so it shares a clock with the
// not_before dates of certificates the manager records.
func writeRestoreMarker(ctx context.Context, tx pgx.Tx, backupCreatedAt time.Time) error {
	var restoredAt time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&restoredAt); err != nil {
		return fmt.Errorf("load: read the database clock: %w", err)
	}
	value, err := json.Marshal(RestoreMarker{BackupCreatedAt: backupCreatedAt.UTC(), RestoredAt: restoredAt.UTC()})
	if err != nil {
		return fmt.Errorf("load: encode the restore marker: %w", err)
	}
	if err := gen.New(tx).UpsertInstanceSetting(ctx, gen.UpsertInstanceSettingParams{Key: RestoreMarkerKey, Value: value}); err != nil {
		return fmt.Errorf("load: write the restore marker: %w", err)
	}
	return nil
}
