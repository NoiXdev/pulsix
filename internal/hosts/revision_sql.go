package hosts

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// BumpRevision moves the host's revision by one, only while ifRevision (when
// non-nil) is still current, and returns the new value. It is the one
// revision bump every host-scoped write outside the hosts row itself goes
// through (tags here, SSH settings in package sshconf). q must be bound to
// the caller's transaction.
//
// A write that matched no row is ErrNotFound (the host is gone) or
// ErrRevisionMismatch (the caller's view is stale); any other failure comes
// back wrapped. Callers in other packages map those two sentinels to their
// own.
func BumpRevision(ctx context.Context, q *gen.Queries, orgID, hostID uuid.UUID, ifRevision *int64) (int64, error) {
	rev, err := q.BumpHostRevision(ctx, gen.BumpHostRevisionParams{ID: hostID, OrgID: orgID, IfRevision: ifRevision})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, missingOrStale(ctx, q, orgID, hostID)
	}
	if err != nil {
		return 0, fmt.Errorf("bump host revision: %w", err)
	}
	return rev, nil
}

// missingOrStale tells apart the two reasons a revision-guarded host write
// matched no row, which the caller needs apart: the host is gone
// (ErrNotFound, 404) or someone saved first and this view is stale
// (ErrRevisionMismatch, 412). Only a read can tell, and it runs on q, the
// caller's transaction.
func missingOrStale(ctx context.Context, q *gen.Queries, orgID, hostID uuid.UUID) error {
	if _, err := q.GetHost(ctx, gen.GetHostParams{OrgID: orgID, ID: hostID}); err != nil {
		return mapErr(err)
	}
	return ErrRevisionMismatch
}
