package runrequests

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TokenState is whether the token that made a request can still ask for
// runs (backlog 2.120). It is a hint for the person deciding, never a
// block: approving starts the run as the approver, whatever the token.
type TokenState string

// The token states.
const (
	TokenActive  TokenState = "active"
	TokenRevoked TokenState = "revoked"
	TokenExpired TokenState = "expired"
	// TokenDeleted means the token row is gone (requested_by_token has no
	// foreign key); its name is gone with it.
	TokenDeleted TokenState = "deleted"
)

// TokenRecord is what the store knows of a requesting token. Revoked covers
// everything that stops the token in the organization other than its
// expiry (a revocation, a blocked or missing personal token grant, a
// deactivated owner); ExpiresAt is compared with the service's clock.
type TokenRecord struct {
	Name      string
	Revoked   bool
	ExpiresAt *time.Time
}

func (rec TokenRecord) state(now time.Time) TokenState {
	switch {
	case rec.Revoked:
		return TokenRevoked
	case rec.ExpiresAt != nil && !now.Before(*rec.ExpiresAt):
		return TokenExpired
	default:
		return TokenActive
	}
}

// withTokens fills TokenName and TokenState of rs from one store read.
func (s *Service) withTokens(ctx context.Context, orgID uuid.UUID, rs []*Request) error {
	var ids []uuid.UUID
	seen := map[uuid.UUID]bool{}
	for _, r := range rs {
		if r.RequestedByToken != nil && !seen[*r.RequestedByToken] {
			seen[*r.RequestedByToken] = true
			ids = append(ids, *r.RequestedByToken)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	recs, err := s.store.Tokens(ctx, orgID, ids)
	if err != nil {
		return fmt.Errorf("read the requesting tokens: %w", err)
	}
	now := s.now()
	for _, r := range rs {
		if r.RequestedByToken == nil {
			continue
		}
		rec, ok := recs[*r.RequestedByToken]
		if !ok {
			r.TokenName, r.TokenState = "", TokenDeleted
			continue
		}
		r.TokenName, r.TokenState = rec.Name, rec.state(now)
	}
	return nil
}
