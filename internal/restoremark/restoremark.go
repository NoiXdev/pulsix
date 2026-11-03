// Package restoremark owns the restore marker (backlog 7.47): the
// instance_settings row a restore writes to say which backup it loaded and
// when. internal/backup writes it; the worker and agent registries read it
// to adopt a certificate the instance issued between the restored backup
// and the restore, which the restored database therefore does not know.
package restoremark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// Key is the instance_settings row that records the last restore.
const Key = "restore"

// Marker is the row's JSON value. A database no restore ever touched has
// no marker.
type Marker struct {
	// BackupCreatedAt is the creation time of the restored backup.
	BackupCreatedAt time.Time `json:"backup_created_at"`
	// RestoredAt is the database's clock when the restore loaded the backup.
	RestoredAt time.Time `json:"restored_at"`
}

// Window is the span between the restored backup's creation and the
// restore. A certificate issued inside it is unknown to the restored
// database although the instance issued it.
type Window struct {
	BackupCreatedAt time.Time
	RestoredAt      time.Time
}

// Contains reports whether t lies strictly inside the window: a
// certificate issued at the very second of the backup or of the restore
// is outside. A nil window (no restore) contains nothing.
func (w *Window) Contains(t time.Time) bool {
	return w != nil && t.After(w.BackupCreatedAt) && t.Before(w.RestoredAt)
}

// Parse turns a marker's JSON value into its window. A marker without both
// times, or whose restore is not after its backup, is no window (nil, nil):
// adoption fails closed on a marker it cannot trust. JSON that does not
// decode is an error.
func Parse(value []byte) (*Window, error) {
	var m Marker
	if err := json.Unmarshal(value, &m); err != nil {
		return nil, fmt.Errorf("decode restore marker: %w", err)
	}
	if m.BackupCreatedAt.IsZero() || m.RestoredAt.IsZero() || !m.RestoredAt.After(m.BackupCreatedAt) {
		return nil, nil
	}
	return &Window{BackupCreatedAt: m.BackupCreatedAt, RestoredAt: m.RestoredAt}, nil
}

// Settings is the query Read needs (gen.Queries satisfies it).
type Settings interface {
	GetInstanceSetting(ctx context.Context, key string) (gen.InstanceSetting, error)
}

// Read loads the last restore's window: nil when no marker exists or it is
// not usable (see Parse). instance_settings carries no row-level security,
// so it reads the same inside or outside an organization scope.
func Read(ctx context.Context, q Settings) (*Window, error) {
	row, err := q.GetInstanceSetting(ctx, Key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read restore marker: %w", err)
	}
	return Parse(row.Value)
}
