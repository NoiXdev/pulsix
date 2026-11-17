package personaltokens

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// SQLStore implements Store with sqlc queries on *db.DB, never on its raw
// pool: under an organization's scope (db.WithOrgScope) row-level security
// confines every grant and switch statement to that organization. Reads
// across one user's grants (the owner's own view) run in an instance-level
// context, where the bypass policy applies, like the token lookup by hash.
type SQLStore struct{ q *gen.Queries }

// NewSQLStore builds the store on d.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d)} }

// InsertToken stores t with the hash of its secret and returns it as
// stored: CreatedAt and ExpiresAt truncated to the database's microsecond
// precision, so what a caller answers equals what a later read answers.
func (s *SQLStore) InsertToken(ctx context.Context, t auth.PersonalToken, secretHash []byte) (auth.PersonalToken, error) {
	t.CreatedAt = t.CreatedAt.Truncate(time.Microsecond)
	exp := pgtype.Timestamptz{}
	if t.ExpiresAt != nil {
		v := t.ExpiresAt.Truncate(time.Microsecond)
		t.ExpiresAt = &v
		exp = pgtype.Timestamptz{Time: v, Valid: true}
	}
	if t.Kind == "" {
		t.Kind = auth.TokenKindAPI
	}
	if err := s.q.InsertPersonalToken(ctx, gen.InsertPersonalTokenParams{
		ID: t.ID, UserID: t.UserID, Name: t.Name, SecretHash: secretHash, ExpiresAt: exp,
		CreatedAt: t.CreatedAt, Kind: string(t.Kind), Areas: t.Areas,
	}); err != nil {
		return auth.PersonalToken{}, err
	}
	return t, nil
}

// InsertGrant grants tokenID orgID at role. The foreign keys refuse a grant
// whose user is not the token's owner or not a member of orgID.
func (s *SQLStore) InsertGrant(ctx context.Context, tokenID, userID, orgID uuid.UUID, role tenancy.Role) error {
	return s.q.InsertPersonalTokenGrant(ctx, gen.InsertPersonalTokenGrantParams{
		TokenID: tokenID, UserID: userID, OrgID: orgID, Role: gen.OrgRole(role),
	})
}

// Cap returns the grant's role and whether it is blocked; ErrNotFound when
// the grant does not exist or row-level security hides it.
func (s *SQLStore) Cap(ctx context.Context, tokenID, orgID uuid.UUID) (tenancy.Role, bool, error) {
	row, err := s.q.GetPersonalTokenGrant(ctx, gen.GetPersonalTokenGrantParams{TokenID: tokenID, OrgID: orgID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrNotFound
	}
	if err != nil {
		return "", false, err
	}
	return tenancy.Role(row.Role), row.Blocked, nil
}

// Allowed reads the organization's switch; no row means allowed.
func (s *SQLStore) Allowed(ctx context.Context, orgID uuid.UUID) (bool, error) {
	return s.q.PersonalTokensAllowed(ctx, orgID)
}

// SetAllowed turns the organization's switch on or off.
func (s *SQLStore) SetAllowed(ctx context.Context, orgID uuid.UUID, on bool) error {
	return s.q.SetPersonalTokensAllowed(ctx, gen.SetPersonalTokensAllowedParams{OrgID: orgID, PersonalTokensAllowed: on})
}

// TouchGrant stamps the database's own now(), at most once a minute; a
// throttled touch is success.
func (s *SQLStore) TouchGrant(ctx context.Context, tokenID, orgID uuid.UUID) error {
	return s.q.TouchPersonalTokenGrant(ctx, gen.TouchPersonalTokenGrantParams{TokenID: tokenID, OrgID: orgID})
}

// BlockGrant is an organization admin's sticky revocation of one grant.
// ErrNotFound when no unblocked grant exists (or row-level security hides
// it). by is the admin; uuid.Nil records no one.
func (s *SQLStore) BlockGrant(ctx context.Context, tokenID, orgID, by uuid.UUID) error {
	blockedBy := pgtype.UUID{Bytes: by, Valid: by != uuid.Nil}
	n, err := s.q.BlockPersonalTokenGrant(ctx, gen.BlockPersonalTokenGrantParams{TokenID: tokenID, OrgID: orgID, BlockedBy: blockedBy})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
