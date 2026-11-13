package monitorsettings

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// PurgeSQLStore implements PurgeStore. The organization list and the chunk
// ranges are read outside any organization scope (the bypass policy, the
// posture of the other cross-org tickers); every DELETE runs inside the
// organization's own scope, so row-level security fences it in addition
// to its org_id predicate.
type PurgeSQLStore struct {
	d *db.DB
	q *gen.Queries
}

// NewPurgeSQLStore builds the store on d itself, never on its raw pool.
func NewPurgeSQLStore(d *db.DB) *PurgeSQLStore { return &PurgeSQLStore{d: d, q: gen.New(d)} }

// OrgsWithShorterRetention implements PurgeStore.
func (s *PurgeSQLStore) OrgsWithShorterRetention(ctx context.Context, maxDays int) ([]OrgRetention, error) {
	if maxDays <= 0 {
		return nil, nil
	}
	// Every choice is at most 365, so capping the bound at 366 lists the
	// same organizations and keeps the narrowing to int32 safe.
	rows, err := s.q.ListOrgsWithShorterRetention(ctx, int32(min(maxDays, 366)))
	if err != nil {
		return nil, fmt.Errorf("list organizations with a shorter retention: %w", err)
	}
	out := make([]OrgRetention, len(rows))
	for i, r := range rows {
		out[i] = OrgRetention{OrgID: r.OrgID, Days: int(r.RetentionDays)}
	}
	return out, nil
}

// chunkRangesSQL reads a hypertable's chunk ranges from TimescaleDB's
// catalog view (hand-written: sqlc cannot type it). Only chunks of a
// time-dimension hypertable, so range_start/range_end are set.
const chunkRangesSQL = `
SELECT range_start, range_end
FROM timescaledb_information.chunks
WHERE hypertable_schema = 'public' AND hypertable_name = $1 AND range_start < $2
ORDER BY range_start`

// ChunkRanges implements PurgeStore.
func (s *PurgeSQLStore) ChunkRanges(ctx context.Context, table Table, before time.Time) ([]TimeRange, error) {
	if !knownTable(table) {
		return nil, fmt.Errorf("chunk ranges: unknown table %q", table)
	}
	rows, err := s.d.Query(ctx, chunkRangesSQL, string(table), before)
	if err != nil {
		return nil, fmt.Errorf("list %s chunks: %w", table, err)
	}
	defer rows.Close()
	var out []TimeRange
	for rows.Next() {
		var r TimeRange
		if err := rows.Scan(&r.Start, &r.End); err != nil {
			return nil, fmt.Errorf("scan %s chunk: %w", table, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list %s chunks: %w", table, err)
	}
	return out, nil
}

// DeleteBatch implements PurgeStore with the generated batch deletes
// (internal/db/queries/retention_purge.sql explains the statement shape).
func (s *PurgeSQLStore) DeleteBatch(ctx context.Context, table Table, orgID uuid.UUID, r TimeRange, limit int) (int64, error) {
	scoped := db.WithOrgScope(ctx, orgID)
	lim := int32(min(max(limit, 0), RetentionPurgeBatch))
	var (
		n   int64
		err error
	)
	switch table {
	case TableCheckSamples:
		n, err = s.q.DeleteOrgCheckSamplesBatch(scoped, gen.DeleteOrgCheckSamplesBatchParams{
			OrgID: orgID, RangeStart: r.Start, RangeEnd: r.End, RowLimit: lim,
		})
	case TableCheckStateChanges:
		n, err = s.q.DeleteOrgCheckStateChangesBatch(scoped, gen.DeleteOrgCheckStateChangesBatchParams{
			OrgID: orgID, RangeStart: r.Start, RangeEnd: r.End, RowLimit: lim,
		})
	default:
		return 0, fmt.Errorf("delete batch: unknown table %q", table)
	}
	if err != nil {
		return 0, fmt.Errorf("delete %s batch: %w", table, err)
	}
	return n, nil
}

func knownTable(t Table) bool {
	return t == TableCheckSamples || t == TableCheckStateChanges
}
