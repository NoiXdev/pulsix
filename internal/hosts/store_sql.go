package hosts

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store with sqlc queries, mirroring tenancy.SQLStore
// and audit.SQLStore's seam: the service never imports gen or pgx
// directly, so it can be driven from the in-memory fake in
// service_test.go as well as the real database.
//
// pool is used only by QueryHosts (tags_store_sql.go): every other method
// goes through q, the ordinary sqlc-generated path, but QueryHosts' WHERE
// clause is built at runtime from a selector.Selector, which sqlc's
// static-query generation has no way to express — see QueryHosts' own doc
// comment.
type SQLStore struct {
	q    *gen.Queries
	pool *pgxpool.Pool
}

// NewSQLStore wraps the generated queries and the pool they run against.
func NewSQLStore(q *gen.Queries, pool *pgxpool.Pool) *SQLStore { return &SQLStore{q: q, pool: pool} }

// uniqueViolationCode is the PostgreSQL error code for a unique constraint
// violation — zones' (org_id, slug) and hosts' (org_id, name), here.
const uniqueViolationCode = "23505"

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (s *SQLStore) CreateZone(ctx context.Context, z Zone) error {
	_, err := s.q.CreateZone(ctx, gen.CreateZoneParams{ID: z.ID, OrgID: z.OrgID, Slug: z.Slug, Name: z.Name})
	if err != nil {
		if isUniqueViolation(err) {
			return ErrNameTaken
		}
		return err
	}
	return nil
}

func (s *SQLStore) ListZones(ctx context.Context, orgID uuid.UUID) ([]Zone, error) {
	rows, err := s.q.ListZonesForOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Zone, len(rows))
	for i, r := range rows {
		out[i] = toZone(r)
	}
	return out, nil
}

func (s *SQLStore) GetZone(ctx context.Context, orgID, zoneID uuid.UUID) (Zone, error) {
	row, err := s.q.GetZone(ctx, gen.GetZoneParams{ID: zoneID, OrgID: orgID})
	if err != nil {
		return Zone{}, mapErr(err)
	}
	return toZone(row), nil
}

func toZone(r gen.Zone) Zone {
	return Zone{ID: r.ID, OrgID: r.OrgID, Slug: r.Slug, Name: r.Name}
}

func (s *SQLStore) CreateHost(ctx context.Context, h Host) error {
	facts, err := marshalFacts(h.Facts)
	if err != nil {
		return err
	}
	_, err = s.q.CreateHost(ctx, gen.CreateHostParams{
		ID: h.ID, OrgID: h.OrgID, ZoneID: h.ZoneID, Name: h.Name, Address: h.Address,
		Managed: h.Managed, Monitored: h.Monitored, Facts: facts,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return ErrNameTaken
		}
		return err
	}
	return nil
}

func (s *SQLStore) GetHost(ctx context.Context, orgID, hostID uuid.UUID) (Host, error) {
	row, err := s.q.GetHost(ctx, gen.GetHostParams{ID: hostID, OrgID: orgID})
	if err != nil {
		return Host{}, mapErr(err)
	}
	return fromHostRow(row)
}

func (s *SQLStore) UpdateHost(ctx context.Context, h Host) (Host, error) {
	row, err := s.q.UpdateHost(ctx, gen.UpdateHostParams{
		ID: h.ID, OrgID: h.OrgID, ZoneID: h.ZoneID, Name: h.Name, Address: h.Address,
		Managed: h.Managed, Monitored: h.Monitored,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return Host{}, ErrNameTaken
		}
		return Host{}, mapErr(err)
	}
	return fromHostRow(row)
}

// PatchCapabilities applies a partial capabilities update in one atomic
// statement — see PatchHostCapabilities's own comment (internal/db/queries/
// hosts.sql) for why this must not be a read-modify-write.
func (s *SQLStore) PatchCapabilities(ctx context.Context, orgID, hostID uuid.UUID, managed, monitored *bool) (Host, error) {
	row, err := s.q.PatchHostCapabilities(ctx, gen.PatchHostCapabilitiesParams{
		ID: hostID, OrgID: orgID, Managed: managed, Monitored: monitored,
	})
	if err != nil {
		return Host{}, mapErr(err)
	}
	return fromHostRow(row)
}

func (s *SQLStore) DeleteHost(ctx context.Context, orgID, hostID uuid.UUID) error {
	n, err := s.q.DeleteHost(ctx, gen.DeleteHostParams{ID: hostID, OrgID: orgID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// OrgsWithoutDefaultZone lists every organization with no zone named
// DefaultZoneSlug — see OrgsWithoutDefaultZone's own comment (internal/db/
// queries/hosts.sql) and Service.ReconcileDefaultZones for how this is
// used.
func (s *SQLStore) OrgsWithoutDefaultZone(ctx context.Context) ([]uuid.UUID, error) {
	return s.q.OrgsWithoutDefaultZone(ctx, DefaultZoneSlug)
}

// ListHosts decodes cursor, runs the query for one extra row beyond limit
// (the standard way to learn "is there a next page" without a second COUNT
// query — see audit.SQLStore.List/page for the same technique), and
// re-encodes the cursor for the row after the last one returned.
func (s *SQLStore) ListHosts(ctx context.Context, orgID uuid.UUID, f ListFilter, cursor string, limit int) ([]Host, string, error) {
	cursorName, cursorID, err := DecodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	rows, err := s.q.ListHosts(ctx, gen.ListHostsParams{
		OrgID: orgID, ZoneID: uuidPtrToPg(f.ZoneID), Managed: f.Managed, Monitored: f.Monitored,
		Query:      nilIfEmpty(f.Query),
		CursorName: cursorNameToPg(cursor, cursorName), CursorID: cursorIDToPg(cursor, cursorID),
		RowLimit: int32(limit + 1), //nolint:gosec // limit is clamped to MaxListLimit by Service.ListHosts before it ever reaches here
	})
	if err != nil {
		return nil, "", err
	}
	hosts := make([]Host, len(rows))
	for i, r := range rows {
		h, err := fromHostRow(r)
		if err != nil {
			return nil, "", err
		}
		hosts[i] = h
	}
	next := ""
	if len(hosts) > limit {
		last := hosts[limit-1]
		next = EncodeCursor(last.Name, last.ID)
		hosts = hosts[:limit]
	}
	return hosts, next, nil
}

func fromHostRow(r gen.Host) (Host, error) {
	var facts map[string]any
	if len(r.Facts) > 0 {
		if err := json.Unmarshal(r.Facts, &facts); err != nil {
			return Host{}, err
		}
	}
	return Host{
		ID: r.ID, OrgID: r.OrgID, ZoneID: r.ZoneID, Name: r.Name, Address: r.Address,
		Managed: r.Managed, Monitored: r.Monitored, Facts: facts,
		LastSeenAt: fromPgTimestamptz(r.LastSeenAt), CreatedAt: r.CreatedAt,
	}, nil
}

func marshalFacts(m map[string]any) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

func fromPgTimestamptz(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

func uuidPtrToPg(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// cursorNameToPg and cursorIDToPg return an unset pgtype value for an empty
// cursor string (the "first page" case — DecodeCursor already reported
// cursorName/cursorID as the zero value for that case, but the SQL query
// tests for "cursor param IS NULL", so an unset pgtype.Valid/*string nil is
// what actually disables the WHERE clause's cursor condition, not a
// zero-but-valid empty string), mirroring audit.SQLStore's
// cursorToPg/cursorIDToPg.
func cursorNameToPg(cursor, name string) *string {
	if cursor == "" {
		return nil
	}
	return &name
}

func cursorIDToPg(cursor string, id uuid.UUID) pgtype.UUID {
	if cursor == "" {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: id, Valid: true}
}
