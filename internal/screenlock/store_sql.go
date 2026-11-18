package screenlock

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore is the sqlc-backed Store.
type SQLStore struct{ q *gen.Queries }

func NewSQLStore(q *gen.Queries) *SQLStore { return &SQLStore{q: q} }

func (s *SQLStore) PINState(ctx context.Context, userID uuid.UUID) (string, *int, bool, error) {
	row, err := s.q.GetLockPIN(ctx, userID)
	if err != nil {
		return "", nil, false, err
	}
	hash := ""
	if row.LockPinHash != nil {
		hash = *row.LockPinHash
	}
	var digits *int
	if row.LockPinDigits != nil {
		n := int(*row.LockPinDigits)
		digits = &n
	}
	return hash, digits, row.LockPinPromptedAt.Valid, nil
}

func (s *SQLStore) SetPIN(ctx context.Context, userID uuid.UUID, hash string, digits int) error {
	d := int16(digits)
	return s.q.SetLockPIN(ctx, gen.SetLockPINParams{UserID: userID, LockPinHash: &hash, LockPinDigits: &d})
}

func (s *SQLStore) RemovePIN(ctx context.Context, userID uuid.UUID) error {
	return s.q.RemoveLockPIN(ctx, userID)
}

func (s *SQLStore) MarkPrompted(ctx context.Context, userID uuid.UUID) error {
	return s.q.MarkLockPINPrompted(ctx, userID)
}

func (s *SQLStore) LockSession(ctx context.Context, sessionID uuid.UUID) (bool, error) {
	n, err := s.q.LockSession(ctx, sessionID)
	return n > 0, err
}

func (s *SQLStore) ClaimAttempt(ctx context.Context, sessionID uuid.UUID) (int, bool, error) {
	n, err := s.q.ClaimUnlockAttempt(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return int(n), true, nil
}

func (s *SQLStore) UnlockSession(ctx context.Context, sessionID uuid.UUID) error {
	_, err := s.q.UnlockSession(ctx, sessionID)
	return err
}

func (s *SQLStore) SessionState(ctx context.Context, sessionID uuid.UUID) (SessionState, bool, error) {
	row, err := s.q.GetSession(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionState{}, false, nil
	}
	if err != nil {
		return SessionState{}, false, err
	}
	return SessionState{Locked: row.LockedAt.Valid, Failures: int(row.UnlockFailures)}, true, nil
}

func (s *SQLStore) RevokeSession(ctx context.Context, sessionID uuid.UUID) error {
	return s.q.RevokeSession(ctx, sessionID)
}
