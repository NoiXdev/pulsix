package auth

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLTOTPStore implements TOTPStore with sqlc queries.
//
// Unlike SQLStore, it holds the database handle itself rather than only a
// *gen.Queries: ConfirmEnrol needs to open a transaction (db.Begin), and
// *gen.Queries has no way to do that on its own — it only ever runs
// against whatever DBTX (the handle or an existing tx) it was built with.
type SQLTOTPStore struct {
	q  *gen.Queries
	db *db.DB
}

// NewSQLTOTPStore wraps d. Like every store, it is built on *db.DB rather
// than its raw pool, so it honours an organization bound to the context
// (db.WithOrgScope) should it ever run inside one.
func NewSQLTOTPStore(d *db.DB) *SQLTOTPStore {
	return &SQLTOTPStore{q: gen.New(d), db: d}
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
	return TOTPSecretRow{
		SecretSealed:  row.SecretSealed,
		Confirmed:     row.ConfirmedAt.Valid,
		PendingSealed: row.PendingSecretSealed,
	}, nil
}

func (s *SQLTOTPStore) SetTOTPLastStep(ctx context.Context, userID uuid.UUID, step int64) (bool, error) {
	n, err := s.q.SetTOTPLastStep(ctx, gen.SetTOTPLastStepParams{UserID: userID, LastUsedStep: step})
	return n == 1, err
}

// ClaimFactorAttempt implements TOTPStore with one conditional UPDATE: the
// count and the lock move together, under the row's lock, so a burst of
// parallel guesses cannot all pass a check made before any of them was
// counted. From the factorAttemptsFree-th attempt on, each one that gets
// through locks the account for factorLockBase, doubled per further
// attempt and capped at factorLockMax (the exponent is capped as well, so
// the arithmetic stays in range however long an attack runs).
func (s *SQLTOTPStore) ClaimFactorAttempt(ctx context.Context, userID uuid.UUID, now time.Time) (bool, error) {
	n, err := s.q.ClaimFactorAttempt(ctx, gen.ClaimFactorAttemptParams{
		FreeAttempts: factorAttemptsFree,
		Now:          now,
		BaseSecs:     factorLockBase.Seconds(),
		MaxSecs:      factorLockMax.Seconds(),
		UserID:       userID,
	})
	return n == 1, err
}

// ClearFactorAttempts implements TOTPStore.
func (s *SQLTOTPStore) ClearFactorAttempts(ctx context.Context, userID uuid.UUID) error {
	return s.q.ClearFactorAttempts(ctx, userID)
}

func (s *SQLTOTPStore) ReplaceRecoveryCodes(ctx context.Context, userID uuid.UUID) error {
	return s.q.ReplaceRecoveryCodes(ctx, userID)
}

func (s *SQLTOTPStore) DeleteTOTP(ctx context.Context, userID uuid.UUID) error {
	return s.q.DeleteTOTP(ctx, userID)
}

// CountUnusedRecoveryCodes implements TOTPStore.
func (s *SQLTOTPStore) CountUnusedRecoveryCodes(ctx context.Context, userID uuid.UUID) (int, error) {
	n, err := s.q.CountUnusedRecoveryCodes(ctx, userID)
	return int(n), err
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
// ReplaceRecoveryCodesWith implements TOTPStore. One transaction, so an
// account is never left between sets: either the old codes are gone and
// the new ones are in, or nothing moved. The TOTP secret is untouched —
// this swaps the way back in, not the factor itself.
func (s *SQLTOTPStore) ReplaceRecoveryCodesWith(ctx context.Context, userID uuid.UUID, recoveryCodeHashes [][]byte) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds; the error path is returned below.

	q := s.q.WithTx(tx)
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

func (s *SQLTOTPStore) ConfirmEnrol(ctx context.Context, userID uuid.UUID, recoveryCodeHashes [][]byte) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds; the error path is what matters and is already returned below.

	q := s.q.WithTx(tx)
	n, err := q.ConfirmTOTP(ctx, userID)
	if err != nil {
		return err
	}
	if n == 0 {
		// Nothing was in flight: no enrolment to confirm (or a concurrent
		// call already promoted it).
		return ErrNoTOTPSecret
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
