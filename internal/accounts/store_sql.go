package accounts

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store with sqlc queries.
type SQLStore struct {
	q *gen.Queries
	d *db.DB
}

// NewSQLStore builds the store on d, so a transaction InTx binds to the
// context reaches every query.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d), d: d} }

// InTx implements Store with one instance-level transaction
// (db.WithInstanceTx): users and sessions carry no organization.
func (s *SQLStore) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.d.WithInstanceTx(ctx, fn)
}

// LockPassword implements Store.
func (s *SQLStore) LockPassword(ctx context.Context, userID uuid.UUID) (PasswordState, error) {
	row, err := s.q.LockUserPassword(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return PasswordState{}, ErrNotFound
	}
	if err != nil {
		return PasswordState{}, fmt.Errorf("lock user password: %w", err)
	}
	return PasswordState{Hash: row.PasswordHash, TempPasswordExpiresAt: db.TimePtr(row.TempPasswordExpiresAt)}, nil
}

// SetPassword implements Store.
func (s *SQLStore) SetPassword(ctx context.Context, userID uuid.UUID, hash string, clearMustChange bool) error {
	n, err := s.q.SetUserPassword(ctx, gen.SetUserPasswordParams{ID: userID, PasswordHash: hash, ClearMustChange: clearMustChange})
	if err != nil {
		return fmt.Errorf("set user password: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeSessionsExcept implements Store.
func (s *SQLStore) RevokeSessionsExcept(ctx context.Context, userID, keep uuid.UUID) error {
	if err := s.q.RevokeUserSessionsExcept(ctx, gen.RevokeUserSessionsExceptParams{UserID: userID, KeepID: keep}); err != nil {
		return fmt.Errorf("revoke user sessions: %w", err)
	}
	return nil
}
