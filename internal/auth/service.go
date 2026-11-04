package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/locale"
)

// User is the service-level view of a user row. DisabledAt is non-nil once
// an instance admin has deactivated the account (tenancy.Service.DeactivateUser)
// — Login checks it below, and auth.Passkeys.FinishLogin checks the same
// field on its own copy of User (Finding 2, Task 10 fix round 1).
type User struct {
	ID            uuid.UUID
	Email         string
	DisplayName   string
	PasswordHash  *string
	InstanceAdmin bool
	DisabledAt    *time.Time
	// Locale is the user's own GUI language, nil until they choose one
	// (the instance default then applies — see internal/locale).
	Locale *string
	// TempPasswordExpiresAt is set while the password is a temporary one an
	// instance admin issued; from that moment on Login refuses it.
	TempPasswordExpiresAt *time.Time
}

// Sentinel errors mapped to HTTP status by the transport layer.
var (
	ErrNotFound           = errors.New("not found")
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrInvalidInput is a request value the service refuses, e.g. a GUI
	// language the product does not ship. The transport maps it to 422.
	ErrInvalidInput = errors.New("invalid input")
)

// Store is what the auth service needs from persistence.
type Store interface {
	GetUserByEmail(ctx context.Context, email string) (User, error)
	GetUserByID(ctx context.Context, id uuid.UUID) (User, error)
	// SetLocale stores the user's GUI language; nil clears it.
	SetLocale(ctx context.Context, id uuid.UUID, l *string) error
}

// SessionStore is the subset of *Sessions the service uses (fakeable in tests).
type SessionStore interface {
	Create(ctx context.Context, userID uuid.UUID, mfaComplete bool) (uuid.UUID, string, error)
	Revoke(ctx context.Context, id uuid.UUID) error
}

// MFAChecker reports whether a user must complete a second factor.
type MFAChecker interface {
	Enabled(ctx context.Context, userID uuid.UUID) (bool, error)
}

// NoMFA is the checker used before TOTP exists.
type NoMFA struct{}

// Enabled always returns false.
func (NoMFA) Enabled(context.Context, uuid.UUID) (bool, error) { return false, nil }

// AnyEnabled combines several MFAChecker so a second factor is present when
// any one of them reports one — e.g. a user with only a passkey, no TOTP,
// must still be treated as having a second factor at password login
// (Finding 2, fix round 1): otherwise a stolen password alone produced an
// MFA-complete session, which then satisfied every route gated on
// requireMFAComplete, including the passkey management routes themselves.
//
// It fails closed: an error from any checker propagates immediately,
// without consulting the remaining checkers, rather than being treated as
// "not enabled" and possibly masked by a later checker reporting true — a
// checker outage must never silently downgrade to password-only login, the
// same reasoning TOTP.Enabled's own doc comment gives for its single
// checker.
func AnyEnabled(checkers ...MFAChecker) MFAChecker {
	return anyEnabledCheckers(checkers)
}

type anyEnabledCheckers []MFAChecker

func (cs anyEnabledCheckers) Enabled(ctx context.Context, userID uuid.UUID) (bool, error) {
	for _, c := range cs {
		enabled, err := c.Enabled(ctx, userID)
		if err != nil {
			return false, err
		}
		if enabled {
			return true, nil
		}
	}
	return false, nil
}

// dummyHash is a fixed, pre-computed argon2id PHC string (produced once by
// HashPassword and pasted here as a literal — never recomputed at init or
// at request time) used only to burn the same argon2id cost as a real
// verification when the account being logged into doesn't exist or has no
// password set. Without this, GetUserByEmail's fast miss (an index lookup)
// versus VerifyPassword's ~100ms hit on a real account leaks account
// existence through response timing even though the status code and body
// are identical.
const dummyHash = "$argon2id$v=19$m=65536,t=3,p=2$FgFA3V8EY11Cbdml3GComQ$RSomihcBPginroKnLtdpkmg16uKCzPaskdOYWE1B7L4"

// verifyPassword is a package-level indirection over VerifyPassword so
// white-box tests (package auth) can substitute a spy to observe that the
// dummy-hash path was actually taken, without relying on flaky wall-clock
// timing assertions.
var verifyPassword = VerifyPassword

// Service implements login, logout and "who am I".
type Service struct {
	store    Store
	sessions SessionStore
	mfa      MFAChecker
	now      func() time.Time
}

// NewService wires the dependencies. now is the clock a temporary
// password's expiry is measured against.
func NewService(store Store, sessions SessionStore, mfa MFAChecker, now func() time.Time) *Service {
	return &Service{store: store, sessions: sessions, mfa: mfa, now: now}
}

// LoginResult is what the transport needs to answer a login. A password
// never completes a session on its own — a second factor is mandatory — so
// exactly one of the two flags is always set: MFARequired when the account
// has a second factor to verify (POST /auth/totp/verify, or a passkey), and
// EnrolmentRequired when it has none yet and must enrol one before the
// session can do anything else.
type LoginResult struct {
	Cookie            string
	MFARequired       bool
	EnrolmentRequired bool
}

// Login verifies the password and opens a session.
//
// A disabled account (u.DisabledAt != nil, set by
// tenancy.Service.DeactivateUser) is refused here with the same
// ErrInvalidCredentials a wrong password gets, not a distinct error: a
// dedicated "account disabled" response would let a caller use the login
// endpoint to probe which addresses belong to disabled accounts, the same
// account-enumeration concern dummyHash already exists to close for
// "account doesn't exist" and "account has no password". The check runs
// only after a successful password verification (never before, and never
// skipped for a wrong password) specifically so a disabled account's login
// attempt costs exactly the same argon2id verification time as any other
// outcome and reveals nothing through response timing either (Finding 2,
// Task 10 fix round 1).
//
// An expired temporary password (u.TempPasswordExpiresAt not after now) is
// refused the same way and at the same point, for the same two reasons: it
// reveals nothing about the account, and it costs the same time.
//
// This is deliberately not the only place deactivation is enforced:
// Sessions.Lookup and Tokens.Resolve both refuse a disabled user's row at
// the query level too (see auth.sql's GetSession and tokens.sql's
// GetAPITokenByHash), so an already-issued session or bearer token stops
// working immediately as well, and so does any future entry point that
// resolves a principal through either of those — not only the ones that
// call Login or FinishLogin to mint a fresh credential.
func (s *Service) Login(ctx context.Context, email, password string) (LoginResult, error) {
	u, err := s.store.GetUserByEmail(ctx, email)
	if err != nil || u.PasswordHash == nil {
		// Spend the same argon2id cost a real verification would, so a
		// missing account (or one without a password) takes the same time
		// as a wrong-password hit on a real one.
		_, _ = verifyPassword(dummyHash, password)
		return LoginResult{}, ErrInvalidCredentials
	}
	ok, err := verifyPassword(*u.PasswordHash, password)
	if err != nil {
		return LoginResult{}, fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return LoginResult{}, ErrInvalidCredentials
	}
	if u.DisabledAt != nil {
		return LoginResult{}, ErrInvalidCredentials
	}
	if u.TempPasswordExpiresAt != nil && !s.now().Before(*u.TempPasswordExpiresAt) {
		return LoginResult{}, ErrInvalidCredentials
	}
	hasFactor, err := s.mfa.Enabled(ctx, u.ID)
	if err != nil {
		return LoginResult{}, fmt.Errorf("check mfa: %w", err)
	}
	// Never MFA-complete: an account with a factor must verify it, and an
	// account without one must enrol one first (the API's
	// requireEnrolmentSession admits exactly that and nothing else).
	_, cookie, err := s.sessions.Create(ctx, u.ID, false)
	if err != nil {
		return LoginResult{}, fmt.Errorf("create session: %w", err)
	}
	return LoginResult{Cookie: cookie, MFARequired: hasFactor, EnrolmentRequired: !hasFactor}, nil
}

// Logout revokes the caller's session.
func (s *Service) Logout(ctx context.Context, p Principal) error {
	return s.sessions.Revoke(ctx, p.SessionID)
}

// Me returns the caller's user.
func (s *Service) Me(ctx context.Context, p Principal) (User, error) {
	return s.store.GetUserByID(ctx, p.UserID)
}

// SetLocale stores the user's own GUI language, or clears it with nil so
// the instance default applies again. Anything but a shipped language is
// ErrInvalidInput.
func (s *Service) SetLocale(ctx context.Context, userID uuid.UUID, l *string) error {
	if l != nil && !locale.Valid(*l) {
		return fmt.Errorf("%w: unsupported language %q", ErrInvalidInput, *l)
	}
	return s.store.SetLocale(ctx, userID, l)
}
