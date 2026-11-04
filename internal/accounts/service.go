// Package accounts is account administration at the instance level: a
// person changing their own password, and (in later tasks) an instance
// admin managing other people's accounts and the password reset flow.
// Signing in, sessions and second factors stay in internal/auth; this
// package builds on its password policy and hashing.
package accounts

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"
)

// Sentinel errors, mapped to HTTP status by the transport layer.
var (
	// ErrInvalidCredentials is a current password that does not verify,
	// including an expired temporary one.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrSamePassword is a new password equal to the current one.
	ErrSamePassword = errors.New("the new password must differ from the current one")
	// ErrNotFound is an account that does not exist or is deactivated.
	ErrNotFound = errors.New("not found")
	// ErrForbidden is a caller who is not an instance admin.
	ErrForbidden = errors.New("instance admin required")
	// ErrNotOnOwnAccount is an administrative action aimed at the caller's
	// own account.
	ErrNotOnOwnAccount = errors.New("not on own account")
	// ErrInvitePending is an action that needs a password on an account
	// whose owner has not accepted the invitation yet.
	ErrInvitePending = errors.New("invite pending")
	// ErrMailNotConfigured is an action that needs outbound mail on an
	// instance without it.
	ErrMailNotConfigured = errors.New("mail not configured")
	// ErrInvalidResetToken is a reset token that is unknown, used, expired
	// or belongs to a deactivated account.
	ErrInvalidResetToken = errors.New("invalid or expired reset token")
)

// PasswordState is what a password change reads about the account.
type PasswordState struct {
	// Hash is nil for an account whose invitation is still pending.
	Hash *string
	// TempPasswordExpiresAt is set while the password is a temporary one.
	TempPasswordExpiresAt *time.Time
}

// Store is what the service needs from persistence. Accounts and sessions
// are instance-level, so nothing here is scoped to an organization.
type Store interface {
	// InTx runs fn in one transaction: every store call fn makes with the
	// context it is handed joins it, and they commit together or not at
	// all.
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
	// LockPassword reads an active account's password state and locks the
	// row for the rest of the transaction; ErrNotFound for an unknown or
	// deactivated account.
	LockPassword(ctx context.Context, userID uuid.UUID) (PasswordState, error)
	// SetPassword stores a new password hash. clearMustChange also clears
	// the forced-change flag and the temporary password's expiry.
	// ErrNotFound for an unknown account.
	SetPassword(ctx context.Context, userID uuid.UUID, hash string, clearMustChange bool) error
	// RevokeSessionsExcept ends every session of the user except keep.
	// API tokens are untouched.
	RevokeSessionsExcept(ctx context.Context, userID, keep uuid.UUID) error
}

// Mailer sends the notices and reset links of later tasks. *mail.SMTP
// satisfies it.
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
	Configured(ctx context.Context) (bool, error)
}

// Service implements account administration.
type Service struct {
	store     Store
	mailer    Mailer
	publicURL string
	now       func() time.Time
	rnd       io.Reader
}

// NewService wires the dependencies. publicURL is the instance's public
// address, for links in mails; now and rnd are the clock and the source of
// randomness for expiries and generated secrets.
func NewService(store Store, mailer Mailer, publicURL string, now func() time.Time, rnd io.Reader) *Service {
	return &Service{store: store, mailer: mailer, publicURL: publicURL, now: now, rnd: rnd}
}
