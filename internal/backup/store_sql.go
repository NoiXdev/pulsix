package backup

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// AdvisoryKeyBackup is the session advisory lock a running backup holds from
// its first statement to its last. A backup attempt recorded as running
// while nobody holds it is stale. The value must never change, nor collide
// with the other keys (db 7256, schedule 8311, auth 8312).
const AdvisoryKeyBackup = int64(8313)

// unlockTimeout bounds releasing the lock, which runs detached from the
// backup's context.
const unlockTimeout = 10 * time.Second

// SQLStore is the Store on the instance-level backup_runs and
// instance_settings tables. It runs without an organization scope; neither
// table is organization-owned.
type SQLStore struct {
	d *db.DB
	q *gen.Queries
}

// NewSQLStore wraps d.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{d: d, q: gen.New(d)} }

// LoadSetting implements Store.
func (s *SQLStore) LoadSetting(ctx context.Context, key string) ([]byte, bool, error) {
	row, err := s.q.GetInstanceSetting(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return row.Value, true, nil
}

// SaveSetting implements Store.
func (s *SQLStore) SaveSetting(ctx context.Context, key string, value []byte) error {
	return s.q.UpsertInstanceSetting(ctx, gen.UpsertInstanceSettingParams{Key: key, Value: value})
}

// TryLock implements Store with pg_try_advisory_lock on a session pinned
// for as long as the lock is held: advisory locks belong to the session.
func (s *SQLStore) TryLock(ctx context.Context) (func(), bool, error) {
	conn, err := s.d.Pool().Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquire session for the backup lock: %w", err)
	}
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, AdvisoryKeyBackup).Scan(&locked); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("take the backup lock: %w", err)
	}
	if !locked {
		conn.Release()
		return nil, false, nil
	}
	var once sync.Once
	return func() { once.Do(func() { unlock(conn) }) }, true, nil
}

// unlock releases the backup lock and returns the session to the pool. If
// the unlock fails the session is closed instead, which ends the lock with
// it, rather than going back into the pool still holding it.
func unlock(conn *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), unlockTimeout)
	defer cancel()
	var released bool
	if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, AdvisoryKeyBackup).Scan(&released); err != nil || !released {
		_ = conn.Hijack().Close(ctx)
		return
	}
	conn.Release()
}

// AnyRunning implements Store.
func (s *SQLStore) AnyRunning(ctx context.Context) (bool, error) {
	return s.q.AnyBackupRunning(ctx)
}

// AbandonRunning implements Store.
func (s *SQLStore) AbandonRunning(ctx context.Context, at time.Time, reason string) (int64, error) {
	return s.q.AbandonRunningBackupRuns(ctx, gen.AbandonRunningBackupRunsParams{FinishedAt: db.NullTime(&at), Error: &reason})
}

// SlotClaimed implements Store.
func (s *SQLStore) SlotClaimed(ctx context.Context, slot time.Time) (bool, error) {
	return s.q.BackupSlotClaimed(ctx, db.NullTime(&slot))
}

// InsertAttempt implements Store; the partial unique index on a scheduled
// attempt's slot decides which manager runs it.
func (s *SQLStore) InsertAttempt(ctx context.Context, a NewAttempt) (uuid.UUID, bool, error) {
	id, err := s.q.InsertBackupRun(ctx, gen.InsertBackupRunParams{
		Kind: a.Kind, Slot: db.NullTime(a.Slot), StartedAt: a.StartedAt, ObjectName: nonEmpty(a.ObjectName), ActorID: db.NullUUID(a.Actor),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	return id, true, nil
}

// FinishAttempt implements Store.
func (s *SQLStore) FinishAttempt(ctx context.Context, id uuid.UUID, f Finish) error {
	var size *int64
	if f.State == StateSucceeded {
		size = &f.Size
	}
	return s.q.FinishBackupRun(ctx, gen.FinishBackupRunParams{
		ID: id, State: f.State, FinishedAt: db.NullTime(&f.At), SizeBytes: size, Error: nonEmpty(f.Error),
	})
}

// RecentAttempts implements Store.
func (s *SQLStore) RecentAttempts(ctx context.Context, limit int) ([]Attempt, error) {
	rows, err := s.q.ListRecentBackupRuns(ctx, int32(min(limit, 1<<20))) //nolint:gosec // bounded just above
	if err != nil {
		return nil, err
	}
	out := make([]Attempt, 0, len(rows))
	for _, r := range rows {
		out = append(out, Attempt{
			ID: r.ID, Kind: r.Kind, Slot: db.TimePtr(r.Slot), StartedAt: r.StartedAt, FinishedAt: db.TimePtr(r.FinishedAt),
			State: r.State, ObjectName: deref(r.ObjectName), Size: deref(r.SizeBytes), Error: deref(r.Error),
			Actor: db.UUIDPtr(r.ActorID), ActorName: r.ActorName,
		})
	}
	return out, nil
}

// InstanceAdminEmails returns the address of every enabled instance
// administrator, for the failure mail.
func (s *SQLStore) InstanceAdminEmails(ctx context.Context) ([]string, error) {
	return s.q.ListInstanceAdminEmails(ctx)
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
