// The two reads the follower needs that are neither route CRUD
// (store_sql.go) nor the ledger (ledger_sql.go): the tenants to walk, and
// the names one message carries. They live in their own file rather than
// being appended to either of those, the same one-responsibility-per-file
// split that put the ledger beside the route store in the first place.
package alertroutes

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// ListOrgIDs returns every organization id — what Follower.Run iterates,
// one tick per tenant. Unscoped by design, like schedule.Retest's own
// pass over the same query: a background ticker belongs to no
// organization until it picks one, and the per-organization statements it
// then runs name org_id themselves (ledger_sql.go's own doc comment on
// why this package does not lean on row-level security inside the tick).
func (s *Store) ListOrgIDs(ctx context.Context) ([]uuid.UUID, error) {
	ids, err := gen.New(s.d).ListOrgIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	return ids, nil
}

// OrgContext returns orgID's slug and name, for the organization a
// message names and the links it carries. It runs on the tick's own
// transaction, like every other statement the tick makes, and is read
// once per tick — only when there is actually something to enqueue.
func (s *Store) OrgContext(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) (slug, name string, err error) {
	org, err := gen.New(tx).GetOrganizationByID(ctx, orgID)
	if err != nil {
		return "", "", fmt.Errorf("get organization %s: %w", orgID, err)
	}
	return org.Slug, org.Name, nil
}
