package rules

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// DirtyMarker marks a host's evaluation cursor dirty from outside the
// evaluator's own write path (EvalSQLStore.Apply writes it as a byproduct
// of a successful evaluation; internal/checks' ingest path writes it
// directly on every accepted batch — see the generated MarkHostDirty's own
// doc comment, db/gen/rules.sql.go).
//
// It exists for v0.3b-3's item-decisions API: creating, changing or
// deleting a decision must make the host(s) it affects due again
// immediately, or an operator who ignores a red item would watch it stay
// red until that host's next due time comes round on its own — bounded
// only by its staleness deadline, which can be minutes. "I ignored it and
// nothing happened" reads as broken, so internal/api calls this the moment
// such a write commits, through the same single-row upsert ingest already
// uses, rather than inventing a second mechanism.
//
// A thin wrapper on *db.DB rather than a method on EvalSQLStore: that type
// also carries the checks/items ports the ticker's Load/Apply need
// (NewEvalSQLStore's own signature), none of which this one upsert uses,
// and building a second one just to reach this method would wire ports
// nothing here calls.
type DirtyMarker struct{ d *db.DB }

// NewDirtyMarker builds a DirtyMarker on d.
func NewDirtyMarker(d *db.DB) DirtyMarker { return DirtyMarker{d: d} }

// MarkHostDirty is the exact upsert internal/checks' ingest path issues
// (checks/store_sql.go's own call to the same generated query): one
// INSERT … ON CONFLICT (host_id) DO UPDATE, so calling it for a host with
// no cursor row yet (never evaluated, never ingested into) still succeeds.
func (m DirtyMarker) MarkHostDirty(ctx context.Context, orgID, hostID uuid.UUID) error {
	if err := gen.New(m.d).MarkHostDirty(ctx, gen.MarkHostDirtyParams{OrgID: orgID, HostID: hostID}); err != nil {
		return fmt.Errorf("mark host dirty: %w", err)
	}
	return nil
}
