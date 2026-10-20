package shared

import (
	"context"
	"fmt"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// withTx runs fn inside one new transaction on d, committing on success and
// rolling back otherwise — repos.SQLStore.ReplaceRepoVaultIdentities's own
// shape (s.d.Begin, gen.New(tx), the caller's statements, an explicit
// Commit, defer Rollback(context.WithoutCancel(ctx)) as a no-op once
// committed), factored out here rather than written inline in each of
// CreateWithProjections, UpdateAndPropagate and DeleteWithProjections.
//
// This is deliberately a plain function over *gen.Queries, not a generic
// one over a definition type: every *.sql file sqlc compiles lands in the
// same internal/db/gen package, so gen.Queries already carries every
// table's methods for every definition kind this package will ever hold —
// repositories today, job templates and check definitions once Tasks 6 and
// 7 land in this same package. There is nothing kind-specific this helper
// needs to know, so there is nothing a type parameter would buy beyond
// what a shared, concrete helper already gives for free: the one thing
// Review Focus 2 cares about — begin, run, commit-or-rollback — written
// once and reused by every kind's own transactional methods, rather than
// reviewed for correctness a second and third time.
func withTx(ctx context.Context, d *db.DB, fn func(ctx context.Context, q *gen.Queries) error) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	if err := fn(ctx, gen.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// propagate calls op once for each of items, in order, stopping at the
// first error. It is the per-organization loop UpdateAndPropagate runs
// (over organization ids) and DeleteWithProjections runs (over whole
// projection rows, since it needs each one's id as well as its
// organization) — and the one Tasks 6 and 7's own equivalents for job
// templates and check definitions will reuse for their own item type:
// always inside withTx's transaction, never on its own, since a loop like
// this has no atomicity of its own the way a single bulk statement would,
// which is exactly Review Focus 2's point. Factoring the loop out does not
// remove the need to call it from inside withTx; it only removes the
// chance that a future caller copies the loop without copying that
// requirement too. Generic over the item type (rather than fixed to
// uuid.UUID) for that second caller, DeleteWithProjections, which loops
// over gen.ListProjectedReposRow, not a bare id.
func propagate[T any](items []T, op func(item T) error) error {
	for _, item := range items {
		if err := op(item); err != nil {
			return err
		}
	}
	return nil
}
