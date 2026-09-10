package auth

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLTOTPStore implements TOTPStore with sqlc queries.
//
// Unlike SQLStore, it holds the pool itself rather than only a
// *gen.Queries: ConfirmEnrol needs to open a transaction (pool.Begin), and
// *gen.Queries has no way to do that on its own — it only ever runs
// against whatever DBTX (pool or an existing tx) it was built with.
type SQLTOTPStore struct {
	q    *gen.Queries
	pool *pgxpool.Pool
}

// NewSQLTOTPStore wraps pool.
func NewSQLTOTPStore(pool *pgxpool.Pool) *SQLTOTPStore {
	return &SQLTOTPStore{q: gen.New(pool), pool: pool}
}

func (s *SQLTOTPStore) UpsertTOTPSecret(ctx context.Context, userID uuid.UUID, secretSealed []byte) error {
	return s.q.UpsertTOTPSecret(ctx, gen.UpsertTOTPSecretParams{UserID: userID, SecretSealed: secretSealed})
}

func (s *SQLTOTPStore) GetTOTPSecret(ctx context.Context, userID uuid.UUID) (TOTPSecretRow, error) {
	row, err := s.q.GetTOTPSecret(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TOTPSecretRow{}, ErrNoTOTPSecret
		}
		return TOTPSecretRow{}, err
	}
	return TOTPSecretRow{SecretSealed: row.SecretSealed, Confirmed: row.ConfirmedAt.Valid}, nil
}

func (s *SQLTOTPStore) SetTOTPLastStep(ctx context.Context, userID uuid.UUID, step int64) (bool, error) {
	n, err := s.q.SetTOTPLastStep(ctx, gen.SetTOTPLastStepParams{UserID: userID, LastUsedStep: step})
	return n == 1, err
}

func (s *SQLTOTPStore) ReplaceRecoveryCodes(ctx context.Context, userID uuid.UUID) error {
	return s.q.ReplaceRecoveryCodes(ctx, userID)
}

func (s *SQLTOTPStore) DeleteTOTP(ctx context.Context, userID uuid.UUID) error {
	return s.q.DeleteTOTP(ctx, userID)
}

func (s *SQLTOTPStore) UseRecoveryCode(ctx context.Context, userID uuid.UUID, codeHash []byte) (bool, error) {
	n, err := s.q.UseRecoveryCode(ctx, gen.UseRecoveryCodeParams{UserID: userID, CodeHash: codeHash})
	return n == 1, err
}

// ConfirmEnrol marks the secret confirmed and replaces the recovery codes
// in a single transaction. Without this, three autocommitted statements
// (ConfirmTOTP, ReplaceRecoveryCodes, then one InsertRecoveryCode per
// code) could leave confirmed_at set — MFA now required at login — while a
// failure partway through the inserts means the caller never receives
// usable recovery codes: a confirmed factor with no recovery path.
func (s *SQLTOTPStore) ConfirmEnrol(ctx context.Context, userID uuid.UUID, recoveryCodeHashes [][]byte) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds; the error path is what matters and is already returned below.

	q := s.q.WithTx(tx)
	if err := q.ConfirmTOTP(ctx, userID); err != nil {
		return err
	}
	if err := q.ReplaceRecoveryCodes(ctx, userID); err != nil {
		return err
	}
	for _, hash := range recoveryCodeHashes {
		if err := q.InsertRecoveryCode(ctx, gen.InsertRecoveryCodeParams{UserID: userID, CodeHash: hash}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
