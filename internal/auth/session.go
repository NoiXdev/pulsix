package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// SessionTTL is the cookie session lifetime.
const SessionTTL = 12 * time.Hour

// ErrUnauthenticated is returned when no valid credential is present.
var ErrUnauthenticated = errors.New("unauthenticated")

// ErrSessionLocked is returned by StillValid for a session that is locked
// behind its owner's PIN (backlog 3.93): still authenticated, but not
// allowed to act until it is unlocked.
var ErrSessionLocked = errors.New("session locked")

// Principal identifies the caller of a request.
type Principal struct {
	UserID        uuid.UUID
	SessionID     uuid.UUID
	TokenID       *uuid.UUID
	InstanceAdmin bool
	MFAComplete   bool
	// MFAAt is when this session last proved a second factor, or nil for
	// one that never did (and for every session that predates the column).
	// The account-security actions ask FactorFresh about it.
	MFAAt *time.Time
	// MustChangePassword is the account's forced-change flag, read from the
	// session lookup. It is carried here and enforced elsewhere.
	MustChangePassword bool
	// Locked is true while the session is locked behind the owner's PIN.
	Locked bool
	// PasswordOnly is true for a session that is complete on its password
	// alone because the second factor is optional (PULSIX_MFA_OPTIONAL) and
	// has proven no factor since. Lookup already reads such a session as
	// pending and stale once its account has a factor; while it has none,
	// the session's first enrolment must still prove the new factor
	// (Enrolment.FinishPasskey) so the session stops resting on the password.
	PasswordOnly bool
	// ExpiresAt is when the session's cookie stops authenticating; zero for
	// a bearer token principal, which has no session.
	ExpiresAt time.Time
}

// StepUpWindow is how long a proven second factor keeps the
// account-security actions open — registering or deleting a passkey,
// moving or disabling TOTP (backlog 1.28). Long enough to manage several
// credentials in one sitting, short enough that an unattended screen is
// not an account takeover waiting to happen.
const StepUpWindow = 5 * time.Minute

// FactorFresh reports whether this session proved a second factor recently
// enough for an account-security action. A session that never proved one
// is never fresh — including a bearer token principal, which has no
// session and therefore no factor to have proven.
func (p Principal) FactorFresh(now time.Time) bool {
	return p.MFAAt != nil && now.Sub(*p.MFAAt) < StepUpWindow
}

// Sessions stores cookie sessions as id + hashed secret.
type Sessions struct {
	q   *gen.Queries
	now func() time.Time
}

// NewSessions builds the session store. now is the clock used to compute
// ExpiresAt; pass time.Now in production and an injectable clock in tests so
// expiry can be driven through the public API instead of only through the
// SQL layer's own "expires_at > now()" clause.
func NewSessions(q *gen.Queries, now func() time.Time) *Sessions {
	return &Sessions{q: q, now: now}
}

// Create inserts a session and returns the cookie value "<id>.<secret>".
func (s *Sessions) Create(ctx context.Context, userID uuid.UUID, mfaComplete bool) (uuid.UUID, string, error) {
	r, err := s.create(ctx, userID, mfaComplete, false)
	return r.ID, r.Cookie, err
}

// CreatePasswordOnly inserts a session that is complete on the password
// alone because the second factor is optional (PULSIX_MFA_OPTIONAL) and
// userID has none, and returns its id and cookie value. It is complete and
// fresh only while the account has no factor: once a TOTP secret is
// confirmed or a passkey registered, Lookup reads it as pending and stale
// (GetSession's password_superseded), so a factor that commits after the
// caller checked for one cannot leave it complete.
func (s *Sessions) CreatePasswordOnly(ctx context.Context, userID uuid.UUID) (uuid.UUID, string, error) {
	r, err := s.create(ctx, userID, true, true)
	return r.ID, r.Cookie, err
}

// RenewedSession is a session Replace opened in place of another: its id,
// its cookie value and when that cookie stops authenticating.
type RenewedSession struct {
	ID        uuid.UUID
	Cookie    string
	ExpiresAt time.Time
}

func (s *Sessions) create(ctx context.Context, userID uuid.UUID, mfaComplete, passwordOnly bool) (RenewedSession, error) {
	id := uuid.Must(uuid.NewV7())
	secret, sum, err := newSessionSecret()
	if err != nil {
		return RenewedSession{}, err
	}
	// Postgres keeps microseconds: the expiry is cut to that before it is
	// stored, so what this returns is what /auth/me later reads back.
	expiresAt := s.now().Add(SessionTTL).Truncate(time.Microsecond)
	err = s.q.CreateSession(ctx, gen.CreateSessionParams{
		ID: id, UserID: userID, SecretHash: sum, MfaComplete: mfaComplete,
		ExpiresAt: expiresAt, PasswordOnly: passwordOnly,
	})
	if err != nil {
		return RenewedSession{}, err
	}
	return RenewedSession{ID: id, Cookie: cookieValue(id, secret), ExpiresAt: expiresAt}, nil
}

// Replace renews userID's session oldID (backlog 3.94): it ends oldID and
// opens a new MFA-complete session with mfa_at now and a fresh SessionTTL.
// The caller has just proved the password and a second factor (or a
// passkey) for userID; a renewal never extends a session's lifetime, it
// replaces the session.
//
// It runs inside the caller's instance transaction (Enrolment.RenewSession)
// so the two writes commit together, and it takes the account row first
// (LockLive), in the order an administrator's second-factor reset takes the
// account and then the sessions: opening a complete session stamps the
// account's last_sign_in_at, which in the other order deadlocks with the
// reset (see CompleteVerifiedSession). A renewal queued behind a reset then
// finds its session ended.
//
// oldID that is not a live session of userID, or is locked, is ErrNotFound
// and nothing changes; so is one a concurrent renewal already replaced
// (RevokeReplacedSession matches it once).
//
// passwordOnly marks the new session as complete on the password alone
// (CreatePasswordOnly): the renewal of a factor-less account while the
// second factor is optional.
func (s *Sessions) Replace(ctx context.Context, userID, oldID uuid.UUID, passwordOnly bool) (RenewedSession, error) {
	if err := s.LockLive(ctx, userID, oldID); err != nil {
		return RenewedSession{}, err
	}
	n, err := s.q.RevokeReplacedSession(ctx, gen.RevokeReplacedSessionParams{ID: oldID, UserID: userID})
	if err != nil {
		return RenewedSession{}, fmt.Errorf("end the replaced session: %w", err)
	}
	if n == 0 {
		return RenewedSession{}, ErrNotFound
	}
	r, err := s.create(ctx, userID, true, passwordOnly)
	if err != nil {
		return RenewedSession{}, fmt.Errorf("open the renewed session: %w", err)
	}
	return r, nil
}

// newSessionSecret draws a session secret and its stored SHA-256 hash.
func newSessionSecret() (secret, sum []byte, err error) {
	secret = make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, nil, err
	}
	h := sha256.Sum256(secret)
	return secret, h[:], nil
}

// cookieValue is the session cookie's value, "<id>.<secret>".
func cookieValue(id uuid.UUID, secret []byte) string {
	return id.String() + "." + base64.RawURLEncoding.EncodeToString(secret)
}

// Lookup validates a cookie value.
func (s *Sessions) Lookup(ctx context.Context, cookie string) (Principal, error) {
	idStr, secretStr, ok := strings.Cut(cookie, ".")
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	secret, err := base64.RawURLEncoding.DecodeString(secretStr)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	row, err := s.q.GetSession(ctx, id)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	sum := sha256.Sum256(secret)
	if subtle.ConstantTimeCompare(sum[:], row.SecretHash) != 1 {
		return Principal{}, ErrUnauthenticated
	}
	p := Principal{UserID: row.UserID, SessionID: row.ID, InstanceAdmin: row.InstanceAdmin, MFAComplete: row.MfaComplete, MustChangePassword: row.MustChangePassword, Locked: row.LockedAt.Valid, ExpiresAt: row.ExpiresAt, PasswordOnly: row.PasswordOnly}
	// A password-only session whose account has a factor now is pending
	// and has proven nothing: it must verify that factor (GetSession).
	if row.PasswordSuperseded {
		p.MFAComplete = false
		return p, nil
	}
	if row.MfaAt.Valid {
		at := row.MfaAt.Time
		p.MFAAt = &at
	}
	return p, nil
}

// StillValid reports whether the session id a request was authorised with
// is still valid — not revoked, not expired, its user not disabled — and
// whether it is MFA-complete. A request that runs for a long time (the job
// log stream) asks it periodically, so a sign-out or a deactivation ends it
// (backlog 7.22). A session that no longer resolves is ErrUnauthenticated;
// a locked session is ErrSessionLocked; any other error is the store's.
func (s *Sessions) StillValid(ctx context.Context, id uuid.UUID) (mfaComplete bool, err error) {
	row, err := s.q.GetSession(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrUnauthenticated
	}
	if err != nil {
		return false, fmt.Errorf("look up session: %w", err)
	}
	if row.LockedAt.Valid {
		return false, ErrSessionLocked
	}
	return row.MfaComplete && !row.PasswordSuperseded, nil
}

// MarkMFAComplete upgrades a session after a successful second factor and
// returns its new cookie value: the upgrade replaces the session's secret,
// so the cookie it had before no longer authenticates (backlog 7.19).
// Without that, a pending session planted in a victim's browser (by
// someone who knows the password and can set the victim's cookie) was
// upgraded by the victim's own second factor and stayed usable by the
// planter. The caller must send the new value as the session cookie.
func (s *Sessions) MarkMFAComplete(ctx context.Context, id uuid.UUID) (string, error) {
	secret, sum, err := newSessionSecret()
	if err != nil {
		return "", err
	}
	n, err := s.q.MarkSessionMFAComplete(ctx, gen.MarkSessionMFACompleteParams{ID: id, SecretHash: sum})
	if err != nil {
		return "", err
	}
	if n == 0 {
		// Nothing was updated: the session is gone (revoked, reaped, or
		// never existed). Reporting success here would let the caller
		// believe a second factor had been completed for a session that
		// cannot carry it.
		return "", ErrNotFound
	}
	return cookieValue(id, secret), nil
}

// RefreshPasswordProof is MarkMFAComplete for the password step-up of
// PULSIX_MFA_OPTIONAL: it refreshes the complete session id's proof and
// replaces its secret, keeping it password-only, and returns the new cookie
// value. Whether the account still has no second factor is decided in the
// same statement (RefreshPasswordProof), so a factor that is there when it
// runs refuses it with ErrPasswordNotTheFactor and nothing changes. The
// caller has checked that the session is live (LockLive); a session that
// matches nothing here has a factor or is not complete.
func (s *Sessions) RefreshPasswordProof(ctx context.Context, id uuid.UUID) (string, error) {
	secret, sum, err := newSessionSecret()
	if err != nil {
		return "", err
	}
	n, err := s.q.RefreshPasswordProof(ctx, gen.RefreshPasswordProofParams{ID: id, SecretHash: sum})
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", ErrPasswordNotTheFactor
	}
	return cookieValue(id, secret), nil
}

// LockLive is the first step of finishing a second-factor enrolment, or of
// completing a verified session (CompleteVerifiedSession, backlog 7.118),
// inside an instance transaction (backlog 7.109). It takes userID's account row in
// key-share mode, which an administrator's second-factor reset needs
// exclusively before it removes the factors and ends the sessions, and which
// still lets a concurrent enrolment of the same account stamp the row when
// it completes its session (LockUserForEnrolment). Then it checks that
// sessionID is still a live session of userID: not revoked, not expired, its
// account not deactivated. A request resolves its session before it reads
// its body, so the Principal it carries can be older than a reset that
// committed in between; this re-check under the lock is what refuses it.
//
// A session that is no longer live, or belongs to another account, is
// ErrNotFound — the same answer MarkMFAComplete gives for an ended session.
// Outside a transaction the lock ends with the statement, so callers run
// this and their writes in one (TxRunner).
func (s *Sessions) LockLive(ctx context.Context, userID, sessionID uuid.UUID) error {
	if err := s.LockAccount(ctx, userID); err != nil {
		return err
	}
	row, err := s.q.GetSession(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("look up session: %w", err)
	}
	if row.UserID != userID {
		return ErrNotFound
	}
	return nil
}

// LockAccount takes userID's account row in key-share mode for the rest of
// the enclosing instance transaction (LockUserForEnrolment), the lock an
// administrator's second-factor reset needs exclusively before it removes
// the factors and ends the sessions (backlog 7.109). A write that depends on
// a factor the reset removes takes it first and re-checks that factor in a
// statement of its own afterwards: LockLive for an enrolment, which re-checks
// its session, and a passkey login, which re-checks its credential before it
// opens a session. An unknown account is ErrNotFound.
func (s *Sessions) LockAccount(ctx context.Context, userID uuid.UUID) error {
	if _, err := s.q.LockUserForEnrolment(ctx, userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock the account: %w", err)
	}
	return nil
}

// LockAccountForRemoval takes userID's account row exclusively (FOR UPDATE)
// for the rest of the enclosing instance transaction
// (LockUserForFactorRemoval), before the owner removes a second factor
// (backlog 7.116). Unlike LockAccount's key share it conflicts with a
// passkey login's lock, so a login waiting on it re-reads its credential
// after the removal committed, and with a second removal of the same
// account, so the two last-factor checks cannot both pass. An unknown
// account is ErrNotFound.
func (s *Sessions) LockAccountForRemoval(ctx context.Context, userID uuid.UUID) error {
	if _, err := s.q.LockUserForFactorRemoval(ctx, userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock the account for a factor removal: %w", err)
	}
	return nil
}

// Revoke ends a session.
func (s *Sessions) Revoke(ctx context.Context, id uuid.UUID) error { return s.q.RevokeSession(ctx, id) }
