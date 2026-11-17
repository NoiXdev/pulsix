package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

// personalTokenPrefix marks a personal token (backlog 3.100). A prefix of
// its own keeps the secret lookup one indexed read: Resolve knows from the
// prefix which table to ask, instead of trying both.
const personalTokenPrefix = "pxp_"

// PersonalToken is a stored personal token: it belongs to a user, not to an
// organization, and acts only in the organizations granted on it (the
// grants live in internal/personaltokens). Never the secret — only its
// SHA-256 hash is ever persisted.
type PersonalToken struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	Name       string
	Kind       TokenKind
	Areas      []string
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	CreatedAt  time.Time
}

// MintPersonalSecret returns a new personal token secret and the SHA-256
// hash to store: "pxp_" followed by the unpadded base64url encoding of 32
// CSPRNG bytes (47 characters), the same shape as an organization token's.
// The plaintext is shown once; nothing can derive it from the hash.
func MintPersonalSecret() (plain string, hash []byte, err error) {
	secret := make([]byte, tokenSecretBytes)
	if _, err := rand.Read(secret); err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(secret)
	return personalTokenPrefix + base64.RawURLEncoding.EncodeToString(secret), sum[:], nil
}

// resolvePersonal is Resolve for a "pxp_" credential. It only authenticates
// the token: which organization it may act in, and with which role, is
// decided per request against its grants (internal/api's requireOrg), since
// the bearer middleware runs before the organization is known.
//
// The principal never carries InstanceAdmin, whatever the owner is
// (spec rule 3): the store does not even read the flag.
func (t *Tokens) resolvePersonal(ctx context.Context, plain string) (Principal, TokenScope, error) {
	secret, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(plain, personalTokenPrefix))
	if err != nil || len(secret) != tokenSecretBytes {
		return Principal{}, TokenScope{}, ErrUnauthenticated
	}
	sum := sha256.Sum256(secret)
	tok, err := t.store.GetPersonalByHash(ctx, sum[:], t.now())
	switch {
	case errors.Is(err, ErrNotFound):
		return Principal{}, TokenScope{}, ErrUnauthenticated
	case err != nil:
		// Fail closed, as for an organization token: an outage is not "no
		// such token".
		return Principal{}, TokenScope{}, err
	}
	if err := t.store.TouchPersonalLastUsed(ctx, tok.ID); err != nil {
		slog.ErrorContext(ctx, "personal token touch failed", "err", err, "token_id", tok.ID)
	}
	id := tok.ID
	principal := Principal{UserID: tok.UserID, TokenID: &id, MFAComplete: true}
	return principal, TokenScope{Personal: true, Kind: tok.Kind, Areas: tok.Areas}, nil
}
