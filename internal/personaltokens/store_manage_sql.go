package personaltokens

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// InTx runs fn in one instance-level transaction (db.WithInstanceTx): the
// owner's writes span organizations, so no organization is bound and the
// bypass policy applies to the grant rows.
func (s *SQLStore) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.d.WithInstanceTx(ctx, fn)
}

// CountTokens counts userID's tokens that are not revoked and, inside a
// transaction, holds the user row until it ends (FOR NO KEY UPDATE). The
// lock comes first and the count after it, in a statement of its own, so
// the count sees a token another create committed while this one waited.
func (s *SQLStore) CountTokens(ctx context.Context, userID uuid.UUID) (int, error) {
	if err := s.q.LockPersonalTokenOwner(ctx, userID); err != nil {
		return 0, fmt.Errorf("lock personal token owner: %w", err)
	}
	n, err := s.q.CountPersonalTokens(ctx, userID)
	return int(n), err
}

func personalToken(id, userID uuid.UUID, name, kind string, areas []string, exp, last pgtype.Timestamptz, created time.Time) auth.PersonalToken {
	return auth.PersonalToken{
		ID: id, UserID: userID, Name: name, Kind: auth.TokenKind(kind), Areas: areas,
		ExpiresAt: db.TimePtr(exp), LastUsedAt: db.TimePtr(last), CreatedAt: created,
	}
}

// Token reads userID's token id unless revoked; inside a transaction it
// holds the row until the transaction ends.
func (s *SQLStore) Token(ctx context.Context, userID, id uuid.UUID) (auth.PersonalToken, error) {
	r, err := s.q.GetPersonalToken(ctx, gen.GetPersonalTokenParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.PersonalToken{}, ErrNotFound
	}
	if err != nil {
		return auth.PersonalToken{}, err
	}
	return personalToken(r.ID, r.UserID, r.Name, r.Kind, r.Areas, r.ExpiresAt, r.LastUsedAt, r.CreatedAt), nil
}

// Tokens lists userID's tokens that are not revoked, oldest first.
func (s *SQLStore) Tokens(ctx context.Context, userID uuid.UUID) ([]auth.PersonalToken, error) {
	rows, err := s.q.ListPersonalTokens(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]auth.PersonalToken, len(rows))
	for i, r := range rows {
		out[i] = personalToken(r.ID, r.UserID, r.Name, r.Kind, r.Areas, r.ExpiresAt, r.LastUsedAt, r.CreatedAt)
	}
	return out, nil
}

// UserGrants lists the grants of all of userID's tokens.
func (s *SQLStore) UserGrants(ctx context.Context, userID uuid.UUID) ([]StoredGrant, error) {
	rows, err := s.q.ListPersonalTokenGrantsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]StoredGrant, len(rows))
	for i, r := range rows {
		out[i] = StoredGrant{TokenID: r.TokenID, OrgID: r.OrgID, OrgSlug: r.Slug, OrgName: r.Name, Role: tenancy.Role(r.Role),
			Blocked: r.Blocked, Allowed: r.Allowed, LastUsedAt: db.TimePtr(r.LastUsedAt)}
	}
	return out, nil
}

// TokenGrants lists one token's grants.
func (s *SQLStore) TokenGrants(ctx context.Context, tokenID uuid.UUID) ([]StoredGrant, error) {
	rows, err := s.q.ListPersonalTokenGrantsForToken(ctx, tokenID)
	if err != nil {
		return nil, err
	}
	out := make([]StoredGrant, len(rows))
	for i, r := range rows {
		out[i] = StoredGrant{TokenID: r.TokenID, OrgID: r.OrgID, OrgSlug: r.Slug, OrgName: r.Name, Role: tenancy.Role(r.Role),
			Blocked: r.Blocked, Allowed: r.Allowed, LastUsedAt: db.TimePtr(r.LastUsedAt)}
	}
	return out, nil
}

// UpdateToken stores t's name, areas and expiry (truncated to the
// database's microseconds) and returns the token as stored.
func (s *SQLStore) UpdateToken(ctx context.Context, t auth.PersonalToken) (auth.PersonalToken, error) {
	exp := pgtype.Timestamptz{}
	if t.ExpiresAt != nil {
		exp = pgtype.Timestamptz{Time: t.ExpiresAt.Truncate(time.Microsecond), Valid: true}
	}
	r, err := s.q.UpdatePersonalToken(ctx, gen.UpdatePersonalTokenParams{
		ID: t.ID, UserID: t.UserID, Name: t.Name, ExpiresAt: exp, Areas: t.Areas,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.PersonalToken{}, ErrNotFound
	}
	if err != nil {
		return auth.PersonalToken{}, err
	}
	return personalToken(r.ID, r.UserID, r.Name, r.Kind, r.Areas, r.ExpiresAt, r.LastUsedAt, r.CreatedAt), nil
}

// rowsOrNotFound turns an :execrows result into ErrNotFound when nothing
// matched.
func rowsOrNotFound(n int64, err error) error {
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetGrantRole changes an unblocked grant's cap.
func (s *SQLStore) SetGrantRole(ctx context.Context, tokenID, orgID uuid.UUID, role tenancy.Role) error {
	return rowsOrNotFound(s.q.SetPersonalTokenGrantRole(ctx, gen.SetPersonalTokenGrantRoleParams{
		TokenID: tokenID, OrgID: orgID, Role: gen.OrgRole(role),
	}))
}

// DeleteGrant removes an unblocked grant.
func (s *SQLStore) DeleteGrant(ctx context.Context, tokenID, orgID uuid.UUID) error {
	return rowsOrNotFound(s.q.DeletePersonalTokenGrant(ctx, gen.DeletePersonalTokenGrantParams{TokenID: tokenID, OrgID: orgID}))
}

// RevokeToken revokes userID's token id with the database's own clock.
func (s *SQLStore) RevokeToken(ctx context.Context, userID, id uuid.UUID) error {
	return rowsOrNotFound(s.q.RevokePersonalToken(ctx, gen.RevokePersonalTokenParams{ID: id, UserID: userID}))
}

// LiveGrants counts tokenID's unblocked grants. It reads across
// organizations, so it fails closed when ctx is bound to one: row-level
// security would hide the others and turn two grants into one.
func (s *SQLStore) LiveGrants(ctx context.Context, tokenID uuid.UUID) (uuid.UUID, int, error) {
	if bound, ok := db.OrgScope(ctx); ok {
		return uuid.Nil, 0, fmt.Errorf("count grants of %s bound to %s: %w", tokenID, bound, errForeignScope)
	}
	r, err := s.q.CountLivePersonalTokenGrants(ctx, tokenID)
	if err != nil {
		return uuid.Nil, 0, err
	}
	if r.N != 1 {
		return uuid.Nil, int(r.N), nil
	}
	return r.OrgID, 1, nil
}

func orgEntry(tokenID uuid.UUID, name, kind string, owner uuid.UUID, ownerName, ownerEmail string, role gen.OrgRole,
	blocked, revoked, ownerBlocked bool, last pgtype.Timestamptz, created time.Time, exp pgtype.Timestamptz) OrgEntry {
	return OrgEntry{TokenID: tokenID, Name: name, Kind: auth.TokenKind(kind), OwnerID: owner, OwnerName: ownerName,
		OwnerEmail: ownerEmail, Role: tenancy.Role(role), Blocked: blocked, TokenRevoked: revoked, OwnerBlocked: ownerBlocked, LastUsedAt: db.TimePtr(last),
		CreatedAt: created, ExpiresAt: db.TimePtr(exp)}
}

// OrgEntries lists orgID's grants of tokens that are not revoked. ctx must
// be bound to orgID: row-level security then admits only its grants.
func (s *SQLStore) OrgEntries(ctx context.Context, orgID uuid.UUID) ([]OrgEntry, error) {
	rows, err := s.q.ListPersonalTokenGrantsForOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]OrgEntry, len(rows))
	for i, r := range rows {
		out[i] = orgEntry(r.TokenID, r.Name, r.Kind, r.UserID, r.DisplayName, r.Email, r.Role, r.Blocked, r.TokenRevoked, r.OwnerBlocked, r.LastUsedAt, r.CreatedAt, r.ExpiresAt)
	}
	return out, nil
}

// OrgEntry is one of OrgEntries; ErrNotFound when there is none.
func (s *SQLStore) OrgEntry(ctx context.Context, orgID, tokenID uuid.UUID) (OrgEntry, error) {
	r, err := s.q.GetPersonalTokenGrantForOrg(ctx, gen.GetPersonalTokenGrantForOrgParams{OrgID: orgID, TokenID: tokenID})
	if errors.Is(err, pgx.ErrNoRows) {
		return OrgEntry{}, ErrNotFound
	}
	if err != nil {
		return OrgEntry{}, err
	}
	return orgEntry(r.TokenID, r.Name, r.Kind, r.UserID, r.DisplayName, r.Email, r.Role, r.Blocked, r.TokenRevoked, r.OwnerBlocked, r.LastUsedAt, r.CreatedAt, r.ExpiresAt), nil
}

// OrgBlocked reports whether an admin blocked any grant of userID in orgID,
// of any token of theirs, revoked ones included. It runs at instance level
// or bound to orgID; bound to another organization it fails closed.
func (s *SQLStore) OrgBlocked(ctx context.Context, userID, orgID uuid.UUID) (bool, error) {
	if err := refuseForeignScope(ctx, orgID); err != nil {
		return false, fmt.Errorf("read personal token block: %w", err)
	}
	return s.q.PersonalTokenOrgBlocked(ctx, gen.PersonalTokenOrgBlockedParams{UserID: userID, OrgID: orgID})
}

// UnblockOwner deletes every blocked grant of userID in orgID; ErrNotFound
// when there is none.
func (s *SQLStore) UnblockOwner(ctx context.Context, orgID, userID uuid.UUID) error {
	return rowsOrNotFound(s.q.UnblockPersonalTokenOwner(ctx, gen.UnblockPersonalTokenOwnerParams{OrgID: orgID, UserID: userID}))
}
