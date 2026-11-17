// Package personaltokens holds a user's personal API and assistant tokens'
// grants (backlog 3.100, spec docs/superpowers/specs/2026-11-17-personal-
// tokens-design.md): a personal token acts only in the organizations granted
// on it, each with a role cap, never above the user's current membership
// role there. The token itself (its secret, kind, areas and expiry) is
// resolved by internal/auth; this package answers, per request, what the
// token may do in one organization, and manages the tokens: the owner's
// create, edit and revoke (manage.go) and an organization's view of the
// grants it holds, an admin's sticky revocation and the organization's
// switch (org.go).
package personaltokens

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/tenancy"
)

var (
	// ErrNotFound is the store's answer for a row that does not exist.
	ErrNotFound = errors.New("personal token not found")
	// ErrNoGrant: the token holds no live grant for the organization —
	// none at all, or one an organization admin revoked.
	ErrNoGrant = errors.New("the token holds no grant for this organization")
	// ErrSwitchedOff: the organization turned personal tokens off.
	ErrSwitchedOff = errors.New("personal tokens are turned off for this organization")
	// ErrLimit: the user already holds MaxTokensPerUser personal tokens.
	ErrLimit = fmt.Errorf("a user holds at most %d personal tokens", MaxTokensPerUser)
	// ErrBlocked: an organization admin revoked the token's grant there,
	// and the block sticks until an admin lifts it.
	ErrBlocked = errors.New("an organization admin revoked this token's grant there")
	// ErrAboveRole: a grant's cap is above the user's role in the
	// organization. It is refused, never silently lowered.
	ErrAboveRole = errors.New("the role cap is above your role in the organization")
	// ErrNotMember: a grant names an organization the user is not a member of.
	ErrNotMember = errors.New("you are not a member of the organization")
	// ErrInvalid is wrapped with the reason a token or change is refused.
	ErrInvalid = errors.New("invalid personal token")
)

// The limits (spec rule 10).
const (
	MaxTokensPerUser  = 50
	MaxGrantsPerToken = 50
)

// Store keeps personal tokens and their grants. Under an organization's
// database scope row-level security confines every grant read to that
// organization: the organization methods (OrgEntries, OrgEntry, BlockGrant,
// UnblockGrant, SetAllowed, and Cap/Allowed/TouchGrant at use) run in that
// scope. The owner's methods (CountTokens, Token, Tokens, UserGrants,
// TokenGrants, the writes and LiveGrants) span every organization a token is
// granted in, so they run at instance level, with no organization bound.
type Store interface {
	// Cap returns the grant of tokenID for orgID and whether an
	// organization admin blocked it; ErrNotFound when there is none.
	Cap(ctx context.Context, tokenID, orgID uuid.UUID) (role tenancy.Role, blocked bool, err error)
	// Allowed reads the organization's switch; no row means allowed. It
	// must run bound to orgID or at instance level (see SQLStore.Allowed).
	Allowed(ctx context.Context, orgID uuid.UUID) (bool, error)
	// TouchGrant records the grant's last use with the database's own
	// clock, at most once a minute.
	TouchGrant(ctx context.Context, tokenID, orgID uuid.UUID) error

	// InTx runs fn in one instance-level transaction: every store call
	// made with the context fn receives commits with it or not at all.
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
	// CountTokens counts userID's tokens that are not revoked (expired
	// ones included). Inside InTx it holds the user until the commit, so
	// two creates cannot both pass the limit.
	CountTokens(ctx context.Context, userID uuid.UUID) (int, error)
	// InsertToken stores t with its secret's hash and returns it as stored.
	InsertToken(ctx context.Context, t auth.PersonalToken, secretHash []byte) (auth.PersonalToken, error)
	// InsertGrant grants tokenID orgID at role; ErrNotMember when userID is
	// not (or no longer) a member of orgID.
	InsertGrant(ctx context.Context, tokenID, userID, orgID uuid.UUID, role tenancy.Role) error
	// Token is userID's token id unless revoked (expired or not);
	// ErrNotFound otherwise. Inside InTx it holds the token until the commit.
	Token(ctx context.Context, userID, id uuid.UUID) (auth.PersonalToken, error)
	// Tokens lists userID's tokens that are not revoked, oldest first.
	Tokens(ctx context.Context, userID uuid.UUID) ([]auth.PersonalToken, error)
	// UserGrants lists the grants of all of userID's tokens, blocked ones
	// included, ordered by organization.
	UserGrants(ctx context.Context, userID uuid.UUID) ([]StoredGrant, error)
	// TokenGrants is UserGrants for one token.
	TokenGrants(ctx context.Context, tokenID uuid.UUID) ([]StoredGrant, error)
	// UpdateToken stores t's name, areas and expiry for the token t.ID of
	// t.UserID and returns it as stored; ErrNotFound when revoked or not theirs.
	UpdateToken(ctx context.Context, t auth.PersonalToken) (auth.PersonalToken, error)
	// SetGrantRole changes an unblocked grant's cap; ErrNotFound otherwise.
	SetGrantRole(ctx context.Context, tokenID, orgID uuid.UUID, role tenancy.Role) error
	// DeleteGrant removes an unblocked grant; ErrNotFound otherwise.
	DeleteGrant(ctx context.Context, tokenID, orgID uuid.UUID) error
	// RevokeToken revokes userID's token id; ErrNotFound when already
	// revoked or not theirs.
	RevokeToken(ctx context.Context, userID, id uuid.UUID) error
	// OrgEntries lists orgID's grants of tokens that are not revoked.
	OrgEntries(ctx context.Context, orgID uuid.UUID) ([]OrgEntry, error)
	// OrgEntry is one of them; ErrNotFound when there is none.
	OrgEntry(ctx context.Context, orgID, tokenID uuid.UUID) (OrgEntry, error)
	// BlockGrant blocks an unblocked grant; ErrNotFound otherwise. by is
	// the admin.
	BlockGrant(ctx context.Context, tokenID, orgID, by uuid.UUID) error
	// UnblockGrant deletes a blocked grant; ErrNotFound otherwise.
	UnblockGrant(ctx context.Context, tokenID, orgID uuid.UUID) error
	// SetAllowed turns the organization's switch on or off.
	SetAllowed(ctx context.Context, orgID uuid.UUID, on bool) error
	// LiveGrants counts tokenID's unblocked grants; orgID is the one
	// organization when n == 1, uuid.Nil otherwise.
	LiveGrants(ctx context.Context, tokenID uuid.UUID) (orgID uuid.UUID, n int, err error)
}

// StoredGrant is one grant row with what the owner's view shows of its
// organization: slug, name and the organization's switch.
type StoredGrant struct {
	TokenID, OrgID   uuid.UUID
	OrgSlug, OrgName string
	Role             tenancy.Role
	Blocked          bool
	Allowed          bool
	LastUsedAt       *time.Time
}

// Members reads a user's current membership role (tenancy.Service).
type Members interface {
	MembershipOf(ctx context.Context, orgID, userID uuid.UUID) (tenancy.Role, error)
}

// Service manages personal tokens and answers what one may do in one
// organization.
type Service struct {
	store   Store
	members Members
	now     func() time.Time
}

// NewService wires the store, the membership lookup and the clock.
func NewService(store Store, members Members, now func() time.Time) *Service {
	return &Service{store: store, members: members, now: now}
}

// Cap is the role cap of tokenID's grant for orgID: no grant or a blocked
// one is ErrNoGrant, the organization's switch off is ErrSwitchedOff. Any
// other error is the store's and propagates, so the caller fails closed.
// The grant is checked first, so a token with no business in orgID learns
// nothing about its switch.
func (s *Service) Cap(ctx context.Context, tokenID, orgID uuid.UUID) (tenancy.Role, error) {
	role, blocked, err := s.store.Cap(ctx, tokenID, orgID)
	if errors.Is(err, ErrNotFound) || (err == nil && blocked) {
		return "", ErrNoGrant
	}
	if err != nil {
		return "", fmt.Errorf("read grant: %w", err)
	}
	on, err := s.store.Allowed(ctx, orgID)
	if err != nil {
		return "", fmt.Errorf("read personal token switch: %w", err)
	}
	if !on {
		return "", ErrSwitchedOff
	}
	return role, nil
}

// EffectiveRole is what tokenID may do in orgID for its owner userID: the
// lower of the grant's cap and the user's current membership role (spec
// rule 2). Not a member is tenancy.ErrNotMember; otherwise Cap's errors.
func (s *Service) EffectiveRole(ctx context.Context, tokenID, orgID, userID uuid.UUID) (tenancy.Role, error) {
	member, err := s.members.MembershipOf(ctx, orgID, userID)
	if err != nil {
		return "", err
	}
	limit, err := s.Cap(ctx, tokenID, orgID)
	if err != nil {
		return "", err
	}
	return tenancy.Lower(member, limit), nil
}

// TouchGrant records the grant's use. It is bookkeeping, never an
// authorization decision: a failure is logged, not returned.
func (s *Service) TouchGrant(ctx context.Context, tokenID, orgID uuid.UUID) {
	if err := s.store.TouchGrant(ctx, tokenID, orgID); err != nil {
		slog.ErrorContext(ctx, "personal token grant touch failed", "err", err, "token_id", tokenID, "org_id", orgID)
	}
}
