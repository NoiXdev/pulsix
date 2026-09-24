package auth

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLTokenStore implements TokenStore with sqlc queries. Unlike
// SQLTOTPStore it needs no transaction — every operation below is a single
// statement — so, like SQLStore and SQLPasskeyStore, it only holds
// *gen.Queries.
type SQLTokenStore struct{ q *gen.Queries }

// NewSQLTokenStore builds the store on d itself, never on its raw pool, so
// every statement runs inside the organization bound to the request context
// (db.WithOrgScope) and row-level security confines it to that
// organization's tokens. Resolving a presented token by its hash runs before
// any organization is known and so stays instance-level by design.
func NewSQLTokenStore(d *db.DB) *SQLTokenStore { return &SQLTokenStore{q: gen.New(d)} }

func (s *SQLTokenStore) Insert(ctx context.Context, t Token, secretHash []byte) error {
	return s.q.InsertAPIToken(ctx, gen.InsertAPITokenParams{
		ID: t.ID, UserID: t.UserID, OrgID: t.OrgID, Name: t.Name, Role: gen.OrgRole(t.Role),
		SecretHash: secretHash, ExpiresAt: toPgTimestamptz(t.ExpiresAt),
	})
}

// GetByHash maps pgx.ErrNoRows to ErrNotFound and nothing else: the query
// itself already applies the revoked/expiry filter (see tokens.sql), so a
// row scan failure that is not "no rows" is a genuine infrastructure
// problem that must propagate, not be folded into "unknown token" (see
// TokenStore.GetByHash and Resolve's fail-closed handling of this error).
//
// now is passed into the query itself (tokens.sql's expires_at > $2), not
// discarded in favor of the database's own now(): the interface's contract
// is "usable as of now", and the caller's injected clock is what "now"
// means throughout Tokens — the in-memory fake honours it the same way
// (see token_test.go's TestTokenExpiry and this file's
// TestSQLTokenStoreGetByHashUsesInjectedNow).
func (s *SQLTokenStore) GetByHash(ctx context.Context, secretHash []byte, now time.Time) (Token, bool, error) {
	row, err := s.q.GetAPITokenByHash(ctx, gen.GetAPITokenByHashParams{SecretHash: secretHash, Now: now})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Token{}, false, ErrNotFound
		}
		return Token{}, false, err
	}
	return Token{
		ID: row.ID, UserID: row.UserID, OrgID: row.OrgID, Name: row.Name, Role: string(row.Role),
		ExpiresAt: fromPgTimestamptz(row.ExpiresAt), LastUsedAt: fromPgTimestamptz(row.LastUsedAt), CreatedAt: row.CreatedAt,
	}, row.InstanceAdmin, nil
}

func (s *SQLTokenStore) ListForOrg(ctx context.Context, orgID uuid.UUID) ([]Token, error) {
	rows, err := s.q.ListAPITokensForOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Token, len(rows))
	for i, row := range rows {
		out[i] = Token{
			ID: row.ID, UserID: row.UserID, OrgID: row.OrgID, Name: row.Name, Role: string(row.Role),
			ExpiresAt: fromPgTimestamptz(row.ExpiresAt), LastUsedAt: fromPgTimestamptz(row.LastUsedAt), CreatedAt: row.CreatedAt,
		}
	}
	return out, nil
}

// Revoke returns ErrNotFound when no matching, not-yet-revoked row exists
// for id within orgID, so a caller can never mistake "nothing happened" for
// success — the same guarantee SQLPasskeyStore.DeleteCredential gives.
func (s *SQLTokenStore) Revoke(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.q.RevokeAPIToken(ctx, gen.RevokeAPITokenParams{ID: id, OrgID: orgID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchLastUsed writes last_used_at at most once per minute per token:
// the UPDATE's own WHERE clause (tokens.sql, backlog 1.22) only matches
// rows whose last_used_at is still NULL or older than the database's own
// now() minus one minute, so repeated resolves inside the window affect
// zero rows. The throttle lives in the statement rather than an in-process
// map — it then holds identically across managers and needs no
// synchronization here — and a zero-row outcome is success (the touch was
// throttled), not an error. The caller-supplied at still supplies the
// written value whenever the predicate admits a write. The comparison is
// between a value this manager wrote and the database's own now(), so
// what it needs is not one shared clock but bounded drift between them:
// a manager running seconds ahead or behind still throttles to roughly a
// minute, which is all the throttle promises.
func (s *SQLTokenStore) TouchLastUsed(ctx context.Context, id uuid.UUID, at time.Time) error {
	return s.q.TouchAPIToken(ctx, gen.TouchAPITokenParams{ID: id, LastUsedAt: pgtype.Timestamptz{Time: at, Valid: true}})
}

func toPgTimestamptz(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func fromPgTimestamptz(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}
