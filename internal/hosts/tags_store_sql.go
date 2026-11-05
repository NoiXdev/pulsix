package hosts

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/hosts/selector"
)

func (s *SQLStore) ListHostTags(ctx context.Context, orgID, hostID uuid.UUID) ([]Tag, error) {
	rows, err := s.q.ListHostTags(ctx, gen.ListHostTagsParams{OrgID: orgID, HostID: hostID})
	if err != nil {
		return nil, err
	}
	out := make([]Tag, len(rows))
	for i, r := range rows {
		out[i] = Tag{Key: r.Key, Value: r.Value, Source: TagSource(r.Source)}
	}
	return out, nil
}

// ReplaceManualTags bumps the host's revision (under ifRevision, when one is
// given), then deletes every existing "manual" row for the host and inserts
// tags in its place, inside one transaction bound to the organization — a
// partial write (some new tags inserted, others not, or the delete applied
// but the insert failing) would leave the host with neither its old manual
// tag set nor its new one, which is worse than either. The bump goes first
// so a stale revision or an unknown host fails before any tag is touched.
// It returns the host's new revision.
func (s *SQLStore) ReplaceManualTags(ctx context.Context, orgID, hostID uuid.UUID, tags []Tag, ifRevision *int64) (int64, error) {
	var rev int64
	err := s.db.WithOrg(ctx, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		var err error
		if rev, err = bumpRevision(ctx, q, orgID, hostID, ifRevision); err != nil {
			return err
		}
		return replaceManualTags(ctx, q, orgID, hostID, tags)
	})
	if err != nil {
		return 0, err
	}
	return rev, nil
}

// bumpRevision moves the host's revision by one, only while ifRevision (when
// non-nil) is still current, and returns the new value. No row back means
// either answer UpdateHostWith distinguishes the same way: the host is gone
// (ErrNotFound) or the caller's view is stale (ErrRevisionMismatch); only a
// read can tell them apart. q must be bound to the caller's transaction.
func bumpRevision(ctx context.Context, q *gen.Queries, orgID, hostID uuid.UUID, ifRevision *int64) (int64, error) {
	rev, err := q.BumpHostRevision(ctx, gen.BumpHostRevisionParams{ID: hostID, OrgID: orgID, IfRevision: ifRevision})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, getErr := q.GetHost(ctx, gen.GetHostParams{OrgID: orgID, ID: hostID}); getErr != nil {
			return 0, mapErr(getErr)
		}
		return 0, ErrRevisionMismatch
	}
	if err != nil {
		return 0, mapErr(err)
	}
	return rev, nil
}

// replaceManualTags is the one "replace manual tags" write: it deletes the
// host's manual rows and inserts tags, on q, which must be bound to the
// caller's transaction (ReplaceManualTags' own, or a host write's).
func replaceManualTags(ctx context.Context, q *gen.Queries, orgID, hostID uuid.UUID, tags []Tag) error {
	if err := q.DeleteManualHostTags(ctx, gen.DeleteManualHostTagsParams{OrgID: orgID, HostID: hostID}); err != nil {
		return fmt.Errorf("clear manual tags: %w", err)
	}
	for _, t := range tags {
		if err := q.InsertHostTag(ctx, gen.InsertHostTagParams{
			HostID: hostID, OrgID: orgID, Key: t.Key, Value: t.Value, Source: string(t.Source),
		}); err != nil {
			return fmt.Errorf("insert tag %q: %w", t.Key, err)
		}
	}
	return nil
}

func (s *SQLStore) ListTagKeyValueCounts(ctx context.Context, orgID uuid.UUID) ([]TagValueCount, error) {
	rows, err := s.q.ListTagKeyValueCounts(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]TagValueCount, len(rows))
	for i, r := range rows {
		out[i] = TagValueCount{Key: r.Key, Value: r.Value, Count: int(r.Count)}
	}
	return out, nil
}

// queryHostsColumns is the exact column list and order QueryHosts selects.
// The Scan call below must list its destinations in this same order — the
// two are not mechanically linked (unlike sqlc's generated code, this query
// is hand-written, see QueryHosts' own doc comment), so a change to one
// requires the matching change to the other.
const queryHostsColumns = `h.id, h.org_id, h.zone_id, h.name, h.address, h.managed, h.monitored, h.facts, h.last_seen_at, h.created_at, h.updated_at, h.revision`

// QueryHosts returns every host in orgID whose *merged* tags (a "manual"
// tag overriding a "fact" tag on the same key — see Service.SetTags' own
// doc comment) match sel.
//
// This is the one place in this package that cannot go through sqlc: sel's
// WHERE fragment (selector.Selector.SQL) is built at runtime from whatever
// expression the caller parsed, so the query text itself varies per call —
// exactly what sqlc's static, compile-time query generation cannot express.
// It runs directly against s.db instead, which binds it to the request's
// organization exactly like the generated queries (db.WithOrgScope).
//
// selector.Selector.SQL's own doc comment is explicit that its fragment
// matches *any* row in host_tags with the given key — it knows nothing
// about "manual" vs. "fact" precedence, the same way Matches only ever sees
// a plain map. Getting the merged semantics Matches expects therefore has
// to happen here, not in the selector package: a leading CTE named
// "host_tags" — the same name as the real table — pre-computes, per host
// and key, whichever row wins (the manual one if both exist, else the
// fact one). A non-recursive CTE cannot see itself, so inside its own body
// "FROM host_tags" still means the physical table; only the main query
// that follows sees the CTE instead. This is standard, valid SQL scoping,
// not a hack specific to pgx or this schema, and it is what lets sel.SQL's
// fragment — which only ever says "FROM host_tags t" — resolve to the
// merged view without the selector package needing to know that view
// exists. TestSQLStoreQueryHostsMergesFactAndManual (store_sql_test.go)
// proves this actually holds against a real database, and
// TestSQLStoreQueryHostsAgreesWithMatches proves it for a wide matrix of
// hosts and selectors, not just one hand-picked case.
func (s *SQLStore) QueryHosts(ctx context.Context, orgID uuid.UUID, sel selector.Selector) ([]Host, error) {
	// org_id is $1 in both the CTE and the outer WHERE, so the selector
	// fragment's own placeholders must start after it — offset 1, giving
	// $2 as sel's first placeholder (see selector.Selector.SQL's own doc
	// comment on offset).
	where, args := sel.SQL(1)

	query := `WITH host_tags AS (
	SELECT DISTINCT ON (host_id, key) host_id, key, value
	FROM host_tags
	WHERE org_id = $1
	ORDER BY host_id, key, (source = 'manual') DESC
)
SELECT ` + queryHostsColumns + `
FROM hosts h
WHERE h.org_id = $1`
	if where != "" {
		query += " AND (" + where + ")"
	}
	query += " ORDER BY h.name ASC, h.id ASC"

	allArgs := make([]any, 0, len(args)+1)
	allArgs = append(allArgs, orgID)
	allArgs = append(allArgs, args...)

	rows, err := s.db.Query(ctx, query, allArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Host
	for rows.Next() {
		var r gen.Host
		if err := rows.Scan(&r.ID, &r.OrgID, &r.ZoneID, &r.Name, &r.Address, &r.Managed, &r.Monitored,
			&r.Facts, &r.LastSeenAt, &r.CreatedAt, &r.UpdatedAt, &r.Revision); err != nil {
			return nil, err
		}
		h, err := fromHostRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
