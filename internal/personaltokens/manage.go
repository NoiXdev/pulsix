package personaltokens

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// GrantInput is one organization the owner grants a token, with its cap.
type GrantInput struct {
	OrgID uuid.UUID
	Role  tenancy.Role
}

// GrantState says whether a grant works today.
type GrantState string

// The grant states: active works; blocked was revoked by an organization
// admin (sticky); off is an unblocked grant in an organization whose switch
// is off — it works again when the switch is turned on.
const (
	GrantActive  GrantState = "active"
	GrantBlocked GrantState = "blocked"
	GrantOff     GrantState = "off"
)

// Grant is one grant as its owner sees it.
type Grant struct {
	OrgID            uuid.UUID
	OrgSlug, OrgName string
	Role             tenancy.Role
	State            GrantState
	LastUsedAt       *time.Time
}

// Token is a personal token with its grants.
type Token struct {
	auth.PersonalToken
	Grants []Grant
}

// NewToken is what Create mints. An empty Kind is an API token.
type NewToken struct {
	Name      string
	Kind      auth.TokenKind
	Areas     []string
	ExpiresAt *time.Time
	Grants    []GrantInput
}

// Change is a partial update: a nil field and auth.ExpiryKeep leave that
// part as it is. Grants, when set, replaces the owner's grant set; blocked
// grants are never the owner's to change and stay as they are.
type Change struct {
	Name      *string
	Areas     *[]string
	Expiry    auth.ExpiryChange
	ExpiresAt *time.Time
	Grants    *[]GrantInput
}

// The kinds of a GrantChange.
const (
	GrantAdded   = "added"
	GrantChanged = "changed"
	GrantRemoved = "removed"
)

// GrantChange is one grant an update added, changed or removed: From is
// empty for an added grant, To for a removed one.
type GrantChange struct {
	OrgID    uuid.UUID
	Kind     string
	From, To tenancy.Role
}

// Create mints a personal token for userID and returns its plaintext (shown
// once, stored only as a hash) and the token as stored. Every grant must
// name a current membership, with a cap no higher than the role there and
// the organization's switch on (spec rules 1, 10).
func (s *Service) Create(ctx context.Context, userID uuid.UUID, in NewToken) (string, Token, error) {
	name, err := checkName(in.Name)
	if err != nil {
		return "", Token{}, err
	}
	kind := in.Kind
	if kind == "" {
		kind = auth.TokenKindAPI
	}
	if kind != auth.TokenKindAPI && kind != auth.TokenKindAssistant {
		return "", Token{}, invalid("unknown token kind")
	}
	if err := checkGrantSet(in.Grants); err != nil {
		return "", Token{}, err
	}
	now := s.now()
	if in.ExpiresAt != nil && !in.ExpiresAt.After(now) {
		return "", Token{}, invalid("the expiry must be in the future")
	}
	var plain string
	var tok Token
	err = s.store.InTx(ctx, func(ctx context.Context) error {
		n, err := s.store.CountTokens(ctx, userID)
		if err != nil {
			return fmt.Errorf("count personal tokens: %w", err)
		}
		if n >= MaxTokensPerUser {
			return ErrLimit
		}
		caps := make([]tenancy.Role, 0, len(in.Grants))
		for _, g := range in.Grants {
			if err := s.checkGrant(ctx, userID, g, nil); err != nil {
				return err
			}
			caps = append(caps, g.Role)
		}
		areas, err := checkAreas(kind, tenancy.Highest(caps...), in.Areas)
		if err != nil {
			return err
		}
		secret, hash, err := auth.MintPersonalSecret()
		if err != nil {
			return fmt.Errorf("mint personal token: %w", err)
		}
		stored, err := s.store.InsertToken(ctx, auth.PersonalToken{
			ID: uuid.Must(uuid.NewV7()), UserID: userID, Name: name, Kind: kind, Areas: areas,
			ExpiresAt: in.ExpiresAt, CreatedAt: now.Truncate(time.Microsecond),
		}, hash)
		if err != nil {
			return fmt.Errorf("store personal token: %w", err)
		}
		for _, g := range in.Grants {
			if err := s.store.InsertGrant(ctx, stored.ID, userID, g.OrgID, g.Role); err != nil {
				return grantWriteErr(g.OrgID, err)
			}
		}
		// Answer the token as a later list reads it: the same precision
		// and the same representation of its times (Review Focus 5).
		read, err := s.store.Token(ctx, userID, stored.ID)
		if err != nil {
			return fmt.Errorf("read personal token: %w", err)
		}
		tok, err = s.withGrants(ctx, read)
		plain = secret
		return err
	})
	if err != nil {
		return "", Token{}, err
	}
	return plain, tok, nil
}

// List returns userID's personal tokens that are not revoked, expired ones
// included, oldest first, each with its grants.
func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]Token, error) {
	toks, err := s.store.Tokens(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list personal tokens: %w", err)
	}
	rows, err := s.store.UserGrants(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list personal token grants: %w", err)
	}
	byToken := map[uuid.UUID][]Grant{}
	for _, r := range rows {
		byToken[r.TokenID] = append(byToken[r.TokenID], toGrant(r))
	}
	out := make([]Token, len(toks))
	for i, t := range toks {
		out[i] = Token{PersonalToken: t, Grants: byToken[t.ID]}
	}
	return out, nil
}

// Update applies ch to userID's token id and returns it before and after,
// with the grants it added, changed or removed (for the organizations'
// feeds). An expired token cannot be changed (auth.ErrTokenExpired). A grant
// the owner adds or changes is checked like one at Create, except that
// lowering a cap needs no switch; an unchanged grant is kept as it is.
// Listing a blocked organization is ErrBlocked; blocked grants survive.
func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, ch Change) (before, after Token, changes []GrantChange, err error) {
	if ch.Name == nil && ch.Areas == nil && ch.Expiry == auth.ExpiryKeep && ch.Grants == nil {
		return Token{}, Token{}, nil, invalid("nothing to change")
	}
	err = s.store.InTx(ctx, func(ctx context.Context) error {
		cur, err := s.store.Token(ctx, userID, id)
		if err != nil {
			return err
		}
		if before, err = s.withGrants(ctx, cur); err != nil {
			return err
		}
		next, err := s.applyFields(cur, ch)
		if err != nil {
			return err
		}
		caps, err := s.planGrants(ctx, userID, before.Grants, ch.Grants, &changes)
		if err != nil {
			return err
		}
		if ch.Areas != nil || ch.Grants != nil {
			if next.Areas, err = checkAreas(next.Kind, tenancy.Highest(caps...), next.Areas); err != nil {
				return err
			}
		}
		updated, err := s.store.UpdateToken(ctx, next)
		if err != nil {
			return fmt.Errorf("update personal token: %w", err)
		}
		if err := s.writeGrants(ctx, userID, id, changes); err != nil {
			return err
		}
		after, err = s.withGrants(ctx, updated)
		return err
	})
	if err != nil {
		return Token{}, Token{}, nil, err
	}
	return before, after, changes, nil
}

// applyFields returns cur with ch's name, areas and expiry applied and
// checked. The areas' fit to the caps is checked by the caller.
func (s *Service) applyFields(cur auth.PersonalToken, ch Change) (auth.PersonalToken, error) {
	now := s.now()
	if cur.ExpiresAt != nil && !cur.ExpiresAt.After(now) {
		return cur, auth.ErrTokenExpired
	}
	next := cur
	if ch.Name != nil {
		name, err := checkName(*ch.Name)
		if err != nil {
			return cur, err
		}
		next.Name = name
	}
	if ch.Areas != nil {
		next.Areas = *ch.Areas
	}
	switch ch.Expiry {
	case auth.ExpiryKeep:
	case auth.ExpiryClear:
		next.ExpiresAt = nil
	case auth.ExpirySet:
		if ch.ExpiresAt == nil {
			return cur, invalid("an expiry is required")
		}
		if !ch.ExpiresAt.After(now) {
			return cur, invalid("the expiry must be in the future")
		}
		v := ch.ExpiresAt.Truncate(time.Microsecond)
		next.ExpiresAt = &v
	default:
		return cur, invalid("unknown expiry change")
	}
	return next, nil
}

// planGrants checks a new grant set against the current grants and records
// what changes in *changes. It returns the caps of the unblocked grants the
// token holds afterwards. A nil set keeps the grants.
func (s *Service) planGrants(ctx context.Context, userID uuid.UUID, current []Grant, set *[]GrantInput, changes *[]GrantChange) ([]tenancy.Role, error) {
	if set == nil {
		var caps []tenancy.Role
		for _, g := range current {
			if g.State != GrantBlocked {
				caps = append(caps, g.Role)
			}
		}
		return caps, nil
	}
	if err := checkGrantSet(*set); err != nil {
		return nil, err
	}
	existing := map[uuid.UUID]Grant{}
	for _, g := range current {
		existing[g.OrgID] = g
	}
	listed := map[uuid.UUID]bool{}
	for _, in := range *set {
		listed[in.OrgID] = true
	}
	// Blocked grants stay on the token, so they count toward its limit.
	total := len(*set)
	for _, g := range current {
		if g.State == GrantBlocked && !listed[g.OrgID] {
			total++
		}
	}
	if total > MaxGrantsPerToken {
		return nil, invalid(fmt.Sprintf("a personal token holds at most %d organizations, blocked ones included", MaxGrantsPerToken))
	}
	caps := make([]tenancy.Role, 0, len(*set))
	for _, in := range *set {
		caps = append(caps, in.Role)
		ex, ok := existing[in.OrgID]
		if ok && ex.Role == in.Role && ex.State != GrantBlocked {
			continue
		}
		var was *Grant
		if ok {
			was = &ex
		}
		if err := s.checkGrant(ctx, userID, in, was); err != nil {
			return nil, err
		}
		if ok {
			*changes = append(*changes, GrantChange{OrgID: in.OrgID, Kind: GrantChanged, From: ex.Role, To: in.Role})
		} else {
			*changes = append(*changes, GrantChange{OrgID: in.OrgID, Kind: GrantAdded, To: in.Role})
		}
	}
	for _, g := range current {
		if !listed[g.OrgID] && g.State != GrantBlocked {
			*changes = append(*changes, GrantChange{OrgID: g.OrgID, Kind: GrantRemoved, From: g.Role})
		}
	}
	return caps, nil
}

// writeGrants stores the planned grant changes.
func (s *Service) writeGrants(ctx context.Context, userID, tokenID uuid.UUID, changes []GrantChange) error {
	for _, c := range changes {
		var err error
		switch c.Kind {
		case GrantAdded:
			err = s.store.InsertGrant(ctx, tokenID, userID, c.OrgID, c.To)
		case GrantChanged:
			err = s.store.SetGrantRole(ctx, tokenID, c.OrgID, c.To)
		case GrantRemoved:
			err = s.store.DeleteGrant(ctx, tokenID, c.OrgID)
		}
		if errors.Is(err, ErrNotFound) {
			// The unblocked grant was there when read: an admin blocked it
			// (or the membership ended) since. A conflict, never a 404 for
			// a token that exists.
			return &GrantError{OrgID: c.OrgID, Err: ErrBlocked}
		}
		if err != nil {
			return grantWriteErr(c.OrgID, err)
		}
	}
	return nil
}

// Revoke ends userID's token id in every organization at once and returns
// it as it was, with its grants, for the organizations' feeds.
func (s *Service) Revoke(ctx context.Context, userID, id uuid.UUID) (Token, error) {
	var tok Token
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		cur, err := s.store.Token(ctx, userID, id)
		if err != nil {
			return err
		}
		if tok, err = s.withGrants(ctx, cur); err != nil {
			return err
		}
		if err := s.store.RevokeToken(ctx, userID, id); err != nil {
			return fmt.Errorf("revoke personal token: %w", err)
		}
		return nil
	})
	if err != nil {
		return Token{}, err
	}
	return tok, nil
}

// withGrants reads t's grants.
func (s *Service) withGrants(ctx context.Context, t auth.PersonalToken) (Token, error) {
	rows, err := s.store.TokenGrants(ctx, t.ID)
	if err != nil {
		return Token{}, fmt.Errorf("read personal token grants: %w", err)
	}
	out := Token{PersonalToken: t, Grants: make([]Grant, len(rows))}
	for i, r := range rows {
		out.Grants[i] = toGrant(r)
	}
	return out, nil
}

func toGrant(r StoredGrant) Grant {
	state := GrantActive
	switch {
	case r.Blocked:
		state = GrantBlocked
	case !r.Allowed:
		state = GrantOff
	}
	return Grant{OrgID: r.OrgID, OrgSlug: r.OrgSlug, OrgName: r.OrgName, Role: r.Role, State: state, LastUsedAt: r.LastUsedAt}
}
