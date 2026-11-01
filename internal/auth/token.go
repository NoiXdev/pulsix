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

// Token is a stored API token (never the secret — only its SHA-256 hash is
// ever persisted; see Create).
type Token struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	OrgID      uuid.UUID
	Name       string
	Role       string
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	CreatedAt  time.Time
}

// TokenScope pins a bearer token to one org and a maximum role. Task 10's
// authorization takes the lower of Role and the caller's actual membership
// role in OrgID — a token can only narrow what its owner can already do,
// never widen it.
type TokenScope struct {
	OrgID uuid.UUID
	Role  string
}

// TokenStore persists API tokens. It mirrors Store and TOTPStore's seam:
// the service never imports gen or pgx directly, so every branch below can
// be driven from a fake in unit tests (see token_test.go) as well as the
// real database (token_store_sql.go, token_store_sql_test.go).
type TokenStore interface {
	// Insert stores t together with the SHA-256 hash of its secret. The
	// plaintext secret itself is never passed to a store.
	Insert(ctx context.Context, t Token, secretHash []byte) error
	// GetByHash looks up the token whose secret hashes to secretHash and is
	// currently usable as of now: not revoked, and not expired. It returns
	// ErrNotFound — never swallowed into a different error, and never
	// swallowing any other error into itself — when no such token exists;
	// any other error (a timeout, a dropped connection, ...) must propagate
	// so Resolve can fail closed instead of treating a store outage as "no
	// such token, therefore anonymous". instanceAdmin mirrors GetSession's
	// join against users.instance_admin.
	GetByHash(ctx context.Context, secretHash []byte, now time.Time) (tok Token, instanceAdmin bool, err error)
	// Usable reports whether the token id is still usable as of now: not
	// revoked, not expired, its owner not disabled — GetByHash's filter,
	// by id.
	Usable(ctx context.Context, id uuid.UUID, now time.Time) (bool, error)
	// ListForOrg returns every non-revoked token belonging to orgID.
	ListForOrg(ctx context.Context, orgID uuid.UUID) ([]Token, error)
	// Revoke marks id revoked, scoped to orgID so a token can never be
	// revoked through an org it does not belong to. It returns ErrNotFound
	// when no matching, not-yet-revoked row exists.
	Revoke(ctx context.Context, orgID, id uuid.UUID) error
	// TouchLastUsed best-effort records when id was last resolved,
	// throttled by the store to at most one write per minute per token
	// (backlog 1.22): the SQL predicate inside the store's UPDATE — not an
	// in-process map — is the source of truth, so the throttle holds
	// identically across managers and a repeated touch inside the window
	// is a no-op, not an error. A store failure here is still bookkeeping,
	// not an authentication decision: Resolve logs it but does not reject
	// an otherwise-valid token over it (see Resolve).
	TouchLastUsed(ctx context.Context, id uuid.UUID, at time.Time) error
}

// Tokens creates and resolves bearer API tokens.
type Tokens struct {
	store TokenStore
	now   func() time.Time
}

// NewTokens wires the store and clock, mirroring NewSessions/NewTOTP/NewPasskeys.
func NewTokens(store TokenStore, now func() time.Time) *Tokens {
	return &Tokens{store: store, now: now}
}

// tokenPrefix marks a value as a Pulsix API token, e.g. for pattern-based
// secret scanning in client logs and vendor credential scanners.
const tokenPrefix = "pxt_"

// tokenSecretBytes is the raw entropy carried by a token's secret half: 32
// bytes (256 bits) from a CSPRNG, the same width Sessions.Create uses for
// its own secret.
const tokenSecretBytes = 32

// Create mints a new token for userID, pinned to orgID and capped at role,
// and returns the plaintext exactly once — the caller (the HTTP handler) is
// the last code in the process that ever sees it. Only its SHA-256 hash is
// persisted, so a compromised database can never be used to recover a
// usable token; Create cannot re-derive the plaintext later, and neither
// can anything else.
//
// The plaintext is "pxt_" followed by the base64url (unpadded) encoding of
// the 32 random secret bytes — 43 characters, nothing else embedded. An
// earlier design embedded the token's id so a lookup could go id-first,
// secret-second, but the secret's hash is already unique (Insert relies on
// the api_tokens.secret_hash UNIQUE constraint the same way Sessions relies
// on sessions.id), so Resolve can and does look up by hash alone; embedding
// an id it never needs would only hand an attacker a working row hint for
// free.
func (t *Tokens) Create(ctx context.Context, userID, orgID uuid.UUID, name, role string, expiresAt *time.Time) (string, Token, error) {
	secret := make([]byte, tokenSecretBytes)
	if _, err := rand.Read(secret); err != nil {
		return "", Token{}, err
	}
	sum := sha256.Sum256(secret)
	tok := Token{ID: uuid.Must(uuid.NewV7()), UserID: userID, OrgID: orgID, Name: name, Role: role, ExpiresAt: expiresAt, CreatedAt: t.now()}
	if err := t.store.Insert(ctx, tok, sum[:]); err != nil {
		return "", Token{}, err
	}
	plain := tokenPrefix + base64.RawURLEncoding.EncodeToString(secret)
	return plain, tok, nil
}

// Resolve validates a plaintext bearer token and reports the principal and
// scope it grants.
//
// There is no manual secret-vs-stored-secret comparison here to make
// constant-time the way Sessions.Lookup's subtle.ConstantTimeCompare
// is (Sessions looks a row up by its non-secret id first, then must compare
// the caller's secret against that row's hash by hand): Resolve looks a
// token up *by* the hash of the caller's secret — GetByHash's equality
// check against secret_hash (a unique, effectively-random 256-bit digest)
// is the entire comparison, done once, and an attacker cannot use its
// timing to learn anything about which digest is "closer" to correct the
// way they could against a byte-by-byte secret comparison.
//
// A malformed or truncated plain (wrong prefix, invalid base64, wrong
// length) is rejected by the prefix check and the decode step below,
// neither of which can panic on arbitrary input.
func (t *Tokens) Resolve(ctx context.Context, plain string) (Principal, TokenScope, error) {
	if !strings.HasPrefix(plain, tokenPrefix) {
		return Principal{}, TokenScope{}, ErrUnauthenticated
	}
	secret, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(plain, tokenPrefix))
	if err != nil {
		return Principal{}, TokenScope{}, ErrUnauthenticated
	}
	sum := sha256.Sum256(secret)
	tok, admin, err := t.store.GetByHash(ctx, sum[:], t.now())
	switch {
	case errors.Is(err, ErrNotFound):
		// Unknown, revoked, or expired: GetByHash's contract folds all
		// three into ErrNotFound (see TokenStore.GetByHash), and none of
		// them is distinguishable to the caller of a bearer credential
		// anyway — the same reasoning Sessions.Lookup applies to a bad
		// cookie.
		return Principal{}, TokenScope{}, ErrUnauthenticated
	case err != nil:
		// A genuine store failure (timeout, connection reset, ...) must
		// not be folded into "no such token": that would let a database
		// hiccup look identical to an invalid credential, and — worse —
		// BearerMiddleware would have no way to tell "reject this token"
		// apart from "the store is unavailable, fail closed" if Resolve
		// itself already discarded the distinction.
		return Principal{}, TokenScope{}, err
	}
	if err := t.store.TouchLastUsed(ctx, tok.ID, t.now()); err != nil {
		// Best-effort bookkeeping: a write failure here must not turn an
		// otherwise-valid, already-verified token into a rejected request.
		slog.ErrorContext(ctx, "token touch failed", "err", err, "token_id", tok.ID)
	}
	id := tok.ID
	// A token is not a browser session with its own second-factor step-up:
	// it was itself minted only by an already MFA-complete caller (see
	// token_handlers.go), so a resolved bearer principal counts as
	// MFA-complete from the start.
	principal := Principal{UserID: tok.UserID, TokenID: &id, InstanceAdmin: admin, MFAComplete: true}
	return principal, TokenScope{OrgID: tok.OrgID, Role: tok.Role}, nil
}

// StillUsable reports whether the token id a request was authorised with
// is still usable. A request that runs for a long time (the job log
// stream) asks it periodically, so a revocation ends it (backlog 7.22).
func (t *Tokens) StillUsable(ctx context.Context, id uuid.UUID) (bool, error) {
	return t.store.Usable(ctx, id, t.now())
}

// List returns every non-revoked token for orgID.
func (t *Tokens) List(ctx context.Context, orgID uuid.UUID) ([]Token, error) {
	return t.store.ListForOrg(ctx, orgID)
}

// Revoke revokes id, scoped to orgID.
func (t *Tokens) Revoke(ctx context.Context, orgID, id uuid.UUID) error {
	return t.store.Revoke(ctx, orgID, id)
}
