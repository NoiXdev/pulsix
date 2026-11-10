package accounts

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

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

// ReadPassword implements Store.
func (s *SQLStore) ReadPassword(ctx context.Context, userID uuid.UUID) (PasswordState, error) {
	row, err := s.q.GetUserPassword(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return PasswordState{}, ErrNotFound
	}
	if err != nil {
		return PasswordState{}, fmt.Errorf("get user password: %w", err)
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

// ListAccounts implements Store.
func (s *SQLStore) ListAccounts(ctx context.Context, query, after string, limit int) ([]AccountRecord, error) {
	rows, err := s.q.ListAccounts(ctx, gen.ListAccountsParams{Query: query, After: after, Lim: int32(min(limit, math.MaxInt32))}) //nolint:gosec // bounded just above
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	out := make([]AccountRecord, len(rows))
	for i, r := range rows {
		out[i] = AccountRecord{
			ID: r.ID, Email: r.Email, DisplayName: r.DisplayName, InstanceAdmin: r.InstanceAdmin,
			HasPassword: r.HasPassword, MustChangePassword: r.MustChangePassword, HasSecondFactor: r.HasSecondFactor,
			DisabledAt: db.TimePtr(r.DisabledAt), LastSignInAt: db.TimePtr(r.LastSignInAt), OrgCount: int(r.OrgCount),
		}
	}
	return out, nil
}

// GetAccount implements Store.
func (s *SQLStore) GetAccount(ctx context.Context, id uuid.UUID) (AccountDetailRecord, error) {
	r, err := s.q.GetAccount(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccountDetailRecord{}, ErrNotFound
	}
	if err != nil {
		return AccountDetailRecord{}, fmt.Errorf("get account: %w", err)
	}
	factors := Factors{TOTP: r.Totp, Passkeys: int(r.Passkeys), RecoveryCodesLeft: int(r.RecoveryCodesLeft)}
	return AccountDetailRecord{
		AccountRecord: AccountRecord{
			ID: r.ID, Email: r.Email, DisplayName: r.DisplayName, InstanceAdmin: r.InstanceAdmin,
			HasPassword: r.HasPassword, MustChangePassword: r.MustChangePassword,
			HasSecondFactor: factors.TOTP || factors.Passkeys > 0,
			DisabledAt:      db.TimePtr(r.DisabledAt), LastSignInAt: db.TimePtr(r.LastSignInAt), OrgCount: int(r.OrgCount),
		},
		CreatedAt:             r.CreatedAt,
		TempPasswordExpiresAt: db.TimePtr(r.TempPasswordExpiresAt),
		Factors:               factors,
		ActiveSessions:        int(r.ActiveSessions),
	}, nil
}

// ListMemberships implements Store.
func (s *SQLStore) ListMemberships(ctx context.Context, userID, actor uuid.UUID) ([]Membership, error) {
	rows, err := s.q.ListAccountMemberships(ctx, gen.ListAccountMembershipsParams{UserID: userID, ActorID: actor})
	if err != nil {
		return nil, fmt.Errorf("list memberships: %w", err)
	}
	out := make([]Membership, len(rows))
	for i, r := range rows {
		out[i] = Membership{OrgID: r.OrgID, Slug: r.Slug, Name: r.Name, Role: r.Role, ActorIsMember: r.ActorIsMember}
	}
	return out, nil
}

// LockAccount implements Store.
func (s *SQLStore) LockAccount(ctx context.Context, id uuid.UUID) (LockedAccount, error) {
	row, err := s.q.LockAccount(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return LockedAccount{}, ErrNotFound
	}
	if err != nil {
		return LockedAccount{}, fmt.Errorf("lock account: %w", err)
	}
	return LockedAccount{HasPassword: row.HasPassword, Deactivated: row.Deactivated, MustChangePassword: row.MustChangePassword}, nil
}

// HasSecondFactor implements Store.
func (s *SQLStore) HasSecondFactor(ctx context.Context, id uuid.UUID) (bool, error) {
	has, err := s.q.AccountHasSecondFactor(ctx, id)
	if err != nil {
		return false, fmt.Errorf("read second factors: %w", err)
	}
	return has, nil
}

// SetTemporaryPassword implements Store.
func (s *SQLStore) SetTemporaryPassword(ctx context.Context, id uuid.UUID, hash string, expiresAt time.Time) error {
	n, err := s.q.SetTemporaryPassword(ctx, gen.SetTemporaryPasswordParams{ID: id, PasswordHash: hash, ExpiresAt: expiresAt})
	if err != nil {
		return fmt.Errorf("set temporary password: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeAllSessions implements Store, with tenancy's query for the same
// thing (deactivation ends every session too).
func (s *SQLStore) RevokeAllSessions(ctx context.Context, id uuid.UUID) error {
	if err := s.q.RevokeSessionsForUser(ctx, id); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	return nil
}

// DeleteSecondFactors implements Store.
func (s *SQLStore) DeleteSecondFactors(ctx context.Context, id uuid.UUID) error {
	if err := s.q.DeleteSecondFactors(ctx, id); err != nil {
		return fmt.Errorf("delete second factors: %w", err)
	}
	return nil
}

// Reactivate implements Store.
func (s *SQLStore) Reactivate(ctx context.Context, id uuid.UUID) error {
	n, err := s.q.ReactivateUser(ctx, id)
	if err != nil {
		return fmt.Errorf("reactivate user: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// NoticeRecipient implements Store.
func (s *SQLStore) NoticeRecipient(ctx context.Context, id uuid.UUID) (Recipient, error) {
	r, err := s.q.GetNoticeRecipient(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Recipient{}, ErrNotFound
	}
	if err != nil {
		return Recipient{}, fmt.Errorf("get notice recipient: %w", err)
	}
	return Recipient{Email: r.Email, Locale: r.Locale, DefaultLocale: r.DefaultLocale}, nil
}

// AccountByEmail implements Store.
func (s *SQLStore) AccountByEmail(ctx context.Context, email string) (uuid.UUID, error) {
	id, err := s.q.GetAccountIDByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("get account by email: %w", err)
	}
	return id, nil
}

// CountResetTokensSince implements Store.
func (s *SQLStore) CountResetTokensSince(ctx context.Context, userID uuid.UUID, since time.Time) (int, error) {
	n, err := s.q.CountResetTokensSince(ctx, gen.CountResetTokensSinceParams{UserID: userID, Since: since})
	if err != nil {
		return 0, fmt.Errorf("count reset tokens: %w", err)
	}
	return int(n), nil
}

// VoidResetTokens implements Store.
func (s *SQLStore) VoidResetTokens(ctx context.Context, userID uuid.UUID, at time.Time) error {
	if err := s.q.VoidResetTokens(ctx, gen.VoidResetTokensParams{UserID: userID, At: at}); err != nil {
		return fmt.Errorf("void reset tokens: %w", err)
	}
	return nil
}

// InsertResetToken implements Store.
func (s *SQLStore) InsertResetToken(ctx context.Context, t ResetToken) error {
	if err := s.q.InsertResetToken(ctx, gen.InsertResetTokenParams{
		ID: t.ID, UserID: t.UserID, TokenHash: t.Hash, CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt,
	}); err != nil {
		return fmt.Errorf("insert reset token: %w", err)
	}
	return nil
}

// ResetTokenOwner implements Store.
func (s *SQLStore) ResetTokenOwner(ctx context.Context, hash []byte) (uuid.UUID, error) {
	id, err := s.q.ResetTokenOwner(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("reset token owner: %w", err)
	}
	return id, nil
}

// LockResetToken implements Store.
func (s *SQLStore) LockResetToken(ctx context.Context, hash []byte, userID uuid.UUID, now time.Time) error {
	_, err := s.q.LockResetToken(ctx, gen.LockResetTokenParams{TokenHash: hash, UserID: userID, Now: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock reset token: %w", err)
	}
	return nil
}
