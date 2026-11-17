package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/personaltokens"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// PersonalTokenAccess is what requireOrg, the job stream and the assistant
// endpoint need from personal tokens (backlog 3.100): the grant's cap for
// one organization and the bookkeeping of its use. *personaltokens.Service
// satisfies it.
type PersonalTokenAccess interface {
	// Cap is the grant's role cap for orgID: personaltokens.ErrNoGrant
	// without a live grant, personaltokens.ErrSwitchedOff while the
	// organization's switch is off; any other error is a failure.
	Cap(ctx context.Context, tokenID, orgID uuid.UUID) (tenancy.Role, error)
	// TouchGrant records the grant's use, best effort.
	TouchGrant(ctx context.Context, tokenID, orgID uuid.UUID)
}

// The refusals of a token for an organization.
const (
	tokenOtherOrg       = "token is scoped to another organization"
	personalTokensOffIn = "personal tokens are turned off for this organization"
)

// tokenRefusal is tokenRole's answer when the token may not act in the
// organization at all: a 403 with detail for a request, the end of a live
// stream. Every other error tokenRole returns is a failure to decide.
type tokenRefusal struct{ detail string }

func (e tokenRefusal) Error() string { return e.detail }

// isTokenRefusal reports whether err is tokenRole's refusal, and its detail.
func isTokenRefusal(err error) (string, bool) {
	var r tokenRefusal
	if errors.As(err, &r) {
		return r.detail, true
	}
	return "", false
}

// tokenRole narrows member (the caller's membership role in orgID) by the
// request's bearer token: an organization token → Lower(member, its role),
// or a refusal when it was minted for another organization; a personal
// token → Lower(member, the grant's cap) (spec rule 2), or a refusal
// without a live grant (the same detail as another organization's token)
// or while the organization's switch is off. A refusal is a tokenRefusal;
// anything else is a failure to decide and must fail closed.
func tokenRole(ctx context.Context, deps Deps, ts auth.TokenScope, tokenID uuid.UUID, orgID uuid.UUID, member tenancy.Role) (tenancy.Role, error) {
	if !ts.Personal {
		if ts.OrgID != orgID {
			return "", tokenRefusal{tokenOtherOrg}
		}
		return tenancy.Lower(member, tenancy.Role(ts.Role)), nil
	}
	limit, err := deps.PersonalTokens.Cap(ctx, tokenID, orgID)
	switch {
	case errors.Is(err, personaltokens.ErrNoGrant):
		return "", tokenRefusal{tokenOtherOrg}
	case errors.Is(err, personaltokens.ErrSwitchedOff):
		return "", tokenRefusal{personalTokensOffIn}
	case err != nil:
		return "", fmt.Errorf("personal token grant: %w", err)
	}
	return tenancy.Lower(member, limit), nil
}
