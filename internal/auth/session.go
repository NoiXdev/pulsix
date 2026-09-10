package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

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
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return uuid.Nil, "", err
	}
	sum := sha256.Sum256(secret)
	err := s.q.CreateSession(ctx, gen.CreateSessionParams{
		ID: id, UserID: userID, SecretHash: sum[:], MfaComplete: mfaComplete,
		ExpiresAt: s.now().Add(SessionTTL),
	})
	if err != nil {
		return uuid.Nil, "", err
	}
	return id, id.String() + "." + base64.RawURLEncoding.EncodeToString(secret), nil
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
	return Principal{UserID: row.UserID, SessionID: row.ID, InstanceAdmin: row.InstanceAdmin, MFAComplete: row.MfaComplete}, nil
}

// MarkMFAComplete upgrades a session after a successful second factor.
func (s *Sessions) MarkMFAComplete(ctx context.Context, id uuid.UUID) error {
	return s.q.MarkSessionMFAComplete(ctx, id)
}

// Revoke ends a session.
func (s *Sessions) Revoke(ctx context.Context, id uuid.UUID) error { return s.q.RevokeSession(ctx, id) }
