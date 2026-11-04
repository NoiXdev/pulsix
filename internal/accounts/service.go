// Package accounts is account administration at the instance level: a
// person changing their own password, an instance admin managing other
// people's accounts (admin.go, actions.go) and telling them about it
// (notices.go), and the password reset flow (reset.go).
// Signing in, sessions and second factors stay in internal/auth; this
// package builds on its password policy and hashing.
package accounts

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Sentinel errors, mapped to HTTP status by the transport layer.
var (
	// ErrInvalidCredentials is a current password that does not verify.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrTemporaryPasswordExpired is a current password that verifies but
	// is a temporary one past its expiry. Only a caller who knows the
	// password ever sees it, so it gives nothing away; the person needs a
	// new temporary password or a reset link.
	ErrTemporaryPasswordExpired = errors.New("the temporary password has expired")
	// ErrSamePassword is a new password equal to the current one.
	ErrSamePassword = errors.New("the new password must differ from the current one")
	// ErrNotFound is an account, or a reset token, that does not exist, or
	// an account the path asking for it does not see. Changing one's own
	// password sees only accounts that are not deactivated, so there a
	// deactivated account is not found. Everywhere else — the instance
	// admin's paths, and the reset flow's lookups by address and by token
	// — a deactivated account is found, and the service decides what that
	// means (a forgotten password sends it nothing, a reset link of it is
	// ErrInvalidResetToken).
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
	// ErrInvalidCursor is a page cursor the account list did not issue.
	ErrInvalidCursor = errors.New("invalid cursor")
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

	// ListAccounts returns up to limit accounts ordered by e-mail,
	// case-insensitively, starting after the address after ("" for the
	// first page). A non-empty query keeps the accounts whose e-mail or
	// display name contains it, case-insensitively.
	ListAccounts(ctx context.Context, query, after string, limit int) ([]AccountRecord, error)
	// GetAccount reads one account; ErrNotFound for an unknown id.
	GetAccount(ctx context.Context, id uuid.UUID) (AccountDetailRecord, error)
	// ListMemberships returns the organizations userID belongs to, each
	// marked with whether actor belongs to it too.
	ListMemberships(ctx context.Context, userID, actor uuid.UUID) ([]Membership, error)
	// LockAccount reads whether the account has a password, whether it is
	// deactivated and whether it must change its password, and locks its
	// row for the rest of the transaction; a deactivated account is found
	// too. ErrNotFound for an unknown id.
	LockAccount(ctx context.Context, id uuid.UUID) (LockedAccount, error)
	// SetTemporaryPassword stores an administrator's temporary password:
	// the hash, the forced change and the expiry. ErrNotFound for an
	// unknown id.
	SetTemporaryPassword(ctx context.Context, id uuid.UUID, hash string, expiresAt time.Time) error
	// RevokeAllSessions ends every session of the account. API tokens are
	// untouched.
	RevokeAllSessions(ctx context.Context, id uuid.UUID) error
	// DeleteSecondFactors removes the account's TOTP secret, recovery
	// codes and passkeys.
	DeleteSecondFactors(ctx context.Context, id uuid.UUID) error
	// Reactivate clears the account's deactivation. ErrNotFound for an
	// unknown id.
	Reactivate(ctx context.Context, id uuid.UUID) error
	// NoticeRecipient reads where an account notice goes and the languages
	// it may be written in. ErrNotFound for an unknown id.
	NoticeRecipient(ctx context.Context, id uuid.UUID) (Recipient, error)

	// AccountByEmail returns the id of the account with this address,
	// matched case-insensitively. ErrNotFound when there is none.
	AccountByEmail(ctx context.Context, email string) (uuid.UUID, error)
	// CountResetTokensSince counts the reset tokens the account was issued
	// at or after since, used or not.
	CountResetTokensSince(ctx context.Context, userID uuid.UUID, since time.Time) (int, error)
	// VoidResetTokens marks every unused reset token of the account used
	// at at.
	VoidResetTokens(ctx context.Context, userID uuid.UUID, at time.Time) error
	// InsertResetToken stores a reset token by its hash.
	InsertResetToken(ctx context.Context, t ResetToken) error
	// ResetTokenOwner returns the account a token hash belongs to, used or
	// not, without locking anything. ErrNotFound for an unknown hash.
	ResetTokenOwner(ctx context.Context, hash []byte) (uuid.UUID, error)
	// LockResetToken locks the token row of hash while it belongs to
	// userID and is unused and unexpired at now; ErrNotFound otherwise.
	// Callers lock the account first (LockAccount): one lock order for
	// every reset path.
	LockResetToken(ctx context.Context, hash []byte, userID uuid.UUID, now time.Time) error
}

// LockedAccount is what an administrative action reads about its target
// under the row lock.
type LockedAccount struct {
	// HasPassword is false while the invitation is still pending.
	HasPassword bool
	Deactivated bool
	// MustChangePassword is set while an administrator's temporary
	// password waits to be replaced.
	MustChangePassword bool
}

// active reports whether the account is in use: its invitation accepted
// and not deactivated. Only such an account is mailed about what happens
// to it.
func (a LockedAccount) active() bool { return a.HasPassword && !a.Deactivated }

// ResetToken is one password reset link as stored: by the SHA-256 of the
// token, never the token itself.
type ResetToken struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Hash      []byte
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Mailer sends the account notices and the reset links.
// *mail.SMTP satisfies it.
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
	// background tracks the reset requests RequestReset hands to the
	// background (Wait).
	background sync.WaitGroup
	// resetSlots bounds the reset requests in flight (maxResetJobs).
	resetSlots chan struct{}
}

// NewService wires the dependencies. publicURL is the instance's public
// address, for links in mails; now and rnd are the clock and the source of
// randomness for expiries and generated secrets.
func NewService(store Store, mailer Mailer, publicURL string, now func() time.Time, rnd io.Reader) *Service {
	return &Service{
		store: store, mailer: mailer, publicURL: publicURL, now: now, rnd: rnd,
		resetSlots: make(chan struct{}, maxResetJobs),
	}
}
