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
	id := uuid.Must(uuid.NewV7())
	secret, sum, err := newSessionSecret()
	if err != nil {
		return uuid.Nil, "", err
	}
	err = s.q.CreateSession(ctx, gen.CreateSessionParams{
		ID: id, UserID: userID, SecretHash: sum, MfaComplete: mfaComplete,
		ExpiresAt: s.now().Add(SessionTTL),
	})
	if err != nil {
		return uuid.Nil, "", err
	}
	return id, cookieValue(id, secret), nil
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
	p := Principal{UserID: row.UserID, SessionID: row.ID, InstanceAdmin: row.InstanceAdmin, MFAComplete: row.MfaComplete}
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
// any other error is the store's.
func (s *Sessions) StillValid(ctx context.Context, id uuid.UUID) (mfaComplete bool, err error) {
	row, err := s.q.GetSession(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrUnauthenticated
	}
	if err != nil {
		return false, fmt.Errorf("look up session: %w", err)
	}
	return row.MfaComplete, nil
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

// Revoke ends a session.
func (s *Sessions) Revoke(ctx context.Context, id uuid.UUID) error { return s.q.RevokeSession(ctx, id) }
