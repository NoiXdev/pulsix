package personaltokens

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// GrantError is a refusal of one grant: it names the organization, so the
// caller can word it, and wraps ErrNotMember, ErrAboveRole, ErrBlocked,
// ErrSwitchedOff or ErrInvalid.
type GrantError struct {
	OrgID uuid.UUID
	Err   error
}

func (e *GrantError) Error() string { return fmt.Sprintf("organization %s: %v", e.OrgID, e.Err) }

// Unwrap returns the reason.
func (e *GrantError) Unwrap() error { return e.Err }

func invalid(reason string) error { return fmt.Errorf("%w: %s", ErrInvalid, reason) }

// checkGrant checks one grant the owner adds (was == nil) or changes: not a
// blocked organization, a known cap, a current membership at or above the
// cap, and — unless the cap is lowered — the organization's switch on.
func (s *Service) checkGrant(ctx context.Context, userID uuid.UUID, g GrantInput, was *Grant) error {
	refuse := func(err error) error { return &GrantError{OrgID: g.OrgID, Err: err} }
	if was != nil && was.State == GrantBlocked {
		return refuse(ErrBlocked)
	}
	if !tenancy.Valid(g.Role) {
		return refuse(invalid("unknown role " + string(g.Role)))
	}
	member, err := s.members.MembershipOf(ctx, g.OrgID, userID)
	if errors.Is(err, tenancy.ErrNotMember) {
		return refuse(ErrNotMember)
	}
	if err != nil {
		return fmt.Errorf("read membership: %w", err)
	}
	if tenancy.Lower(member, g.Role) != g.Role {
		return refuse(ErrAboveRole)
	}
	if was != nil && tenancy.Lower(was.Role, g.Role) == g.Role {
		return nil // lowered: allowed even while the switch is off
	}
	on, err := s.store.Allowed(ctx, g.OrgID)
	if err != nil {
		return fmt.Errorf("read personal token switch: %w", err)
	}
	if !on {
		return refuse(ErrSwitchedOff)
	}
	return nil
}

// grantWriteErr names the organization of a grant write the store refused
// because the membership ended meanwhile.
func grantWriteErr(orgID uuid.UUID, err error) error {
	if errors.Is(err, ErrNotMember) {
		return &GrantError{OrgID: orgID, Err: ErrNotMember}
	}
	return fmt.Errorf("store personal token grant: %w", err)
}

// checkName trims a token name and refuses a blank one.
func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", invalid("a name is required")
	}
	return name, nil
}

// checkGrantSet checks a grant set's shape: one to MaxGrantsPerToken
// grants, each organization once.
func checkGrantSet(set []GrantInput) error {
	if len(set) == 0 {
		return invalid("a personal token needs at least one organization")
	}
	if len(set) > MaxGrantsPerToken {
		return invalid(fmt.Sprintf("a personal token holds at most %d organizations", MaxGrantsPerToken))
	}
	seen := map[uuid.UUID]bool{}
	for _, g := range set {
		if seen[g.OrgID] {
			return &GrantError{OrgID: g.OrgID, Err: invalid("the organization is listed twice")}
		}
		seen[g.OrgID] = true
	}
	return nil
}

// checkAreas checks a token's areas the way organization tokens' are
// checked (spec rule 4): an assistant token needs at least one known,
// distinct area, each covered by top — the highest cap; an API token has
// none. It returns the areas in canonical order.
func checkAreas(kind auth.TokenKind, top tenancy.Role, areas []string) ([]string, error) {
	if kind != auth.TokenKindAssistant {
		if len(areas) > 0 {
			return nil, invalid("only assistant tokens have areas")
		}
		return nil, nil
	}
	if len(areas) == 0 {
		return nil, invalid("an assistant token needs at least one area")
	}
	seen := map[string]bool{}
	for _, a := range areas {
		if !tenancy.ValidArea(tenancy.Area(a)) {
			return nil, invalid(fmt.Sprintf("unknown area %q", a))
		}
		if seen[a] {
			return nil, invalid("area " + a + " is listed twice")
		}
		seen[a] = true
		if !tenancy.RoleAllowsArea(top, tenancy.Area(a)) {
			return nil, invalid("area " + a + " needs more than the token's highest role " + string(top) + " allows")
		}
	}
	out := make([]string, 0, len(areas))
	for _, a := range tenancy.Areas() {
		if seen[string(a)] {
			out = append(out, string(a))
		}
	}
	return out, nil
}
