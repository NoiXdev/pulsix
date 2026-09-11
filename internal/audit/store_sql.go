package audit

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store with sqlc queries, the same shape SQLTokenStore
// and SQLPasskeyStore use in package auth: a thin adapter around
// *gen.Queries with no transaction of its own, since every operation below
// is a single statement.
type SQLStore struct{ q *gen.Queries }

// NewSQLStore builds the store on d itself, never on its raw pool, so every
// statement runs inside the organization bound to the request context
// (db.WithOrgScope) and row-level security confines it to that
// organization's events.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d)} }

// Insert persists e. Meta is marshaled to JSON explicitly (rather than
// relying on pgx's own encoding of map[string]any) so a nil map — the
// common case, most events carry no extra metadata — becomes the column's
// own default-shaped "{}" instead of JSON null, matching the NOT NULL
// DEFAULT '{}' constraint on audit_events.meta.
func (s *SQLStore) Insert(ctx context.Context, e Event) error {
	meta, err := marshalMeta(e.Meta)
	if err != nil {
		return err
	}
	return s.q.InsertAuditEvent(ctx, gen.InsertAuditEventParams{
		ID: e.ID, OrgID: uuidToPg(e.OrgID), ActorID: uuidToPg(e.ActorID), ActorKind: string(e.ActorKind),
		Action: e.Action, ObjectType: e.ObjectType, ObjectID: e.ObjectID, Summary: e.Summary,
		Meta: meta, At: e.At,
	})
}

// List returns orgID's activity feed.
func (s *SQLStore) List(ctx context.Context, orgID uuid.UUID, filter Filter, cursor string, limit int) ([]Event, string, error) {
	cursorAt, cursorID, err := DecodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	rows, err := s.q.ListAuditEventsForOrg(ctx, gen.ListAuditEventsForOrgParams{
		OrgID: pgtype.UUID{Bytes: orgID, Valid: true}, ActorID: uuidToPg(filter.ActorID),
		ObjectType: nilIfEmpty(filter.ObjectType), Since: timeToPg(filter.Since),
		CursorAt: cursorToPg(cursor, cursorAt), CursorID: cursorIDToPg(cursor, cursorID),
		RowLimit: int32(limit + 1), //nolint:gosec // limit is clamped to MaxListLimit by Recorder before it ever reaches here
	})
	if err != nil {
		return nil, "", err
	}
	return page(rows, limit)
}

// ListForActor returns actorID's own events across every organization.
func (s *SQLStore) ListForActor(ctx context.Context, actorID uuid.UUID, filter Filter, cursor string, limit int) ([]Event, string, error) {
	cursorAt, cursorID, err := DecodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	rows, err := s.q.ListAuditEventsForActor(ctx, gen.ListAuditEventsForActorParams{
		ActorID:    pgtype.UUID{Bytes: actorID, Valid: true},
		ObjectType: nilIfEmpty(filter.ObjectType), Since: timeToPg(filter.Since),
		CursorAt: cursorToPg(cursor, cursorAt), CursorID: cursorIDToPg(cursor, cursorID),
		RowLimit: int32(limit + 1), //nolint:gosec // limit is clamped to MaxListLimit by Recorder before it ever reaches here
	})
	if err != nil {
		return nil, "", err
	}
	return page(rows, limit)
}

// page turns up to limit+1 rows (the query's own RowLimit — see List and
// ListForActor) into a page of at most limit events plus the next cursor:
// fetching one extra row is the standard way to learn "is there a next
// page" without a second COUNT query, and costs nothing here since limit is
// already bounded by Recorder's clampLimit.
func page(rows []gen.AuditEvent, limit int) ([]Event, string, error) {
	events := make([]Event, len(rows))
	for i, r := range rows {
		e, err := fromRow(r)
		if err != nil {
			return nil, "", err
		}
		events[i] = e
	}
	next := ""
	if len(events) > limit {
		last := events[limit-1]
		next = EncodeCursor(last.At, last.ID)
		events = events[:limit]
	}
	return events, next, nil
}

func fromRow(r gen.AuditEvent) (Event, error) {
	var meta map[string]any
	if len(r.Meta) > 0 {
		if err := json.Unmarshal(r.Meta, &meta); err != nil {
			return Event{}, err
		}
	}
	return Event{
		ID: r.ID, OrgID: pgToUUID(r.OrgID), ActorID: pgToUUID(r.ActorID), ActorKind: ActorKind(r.ActorKind),
		Action: r.Action, ObjectType: r.ObjectType, ObjectID: r.ObjectID, Summary: r.Summary, Meta: meta, At: r.At,
	}, nil
}

func marshalMeta(m map[string]any) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

func uuidToPg(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

func pgToUUID(id pgtype.UUID) *uuid.UUID {
	if !id.Valid {
		return nil
	}
	v := uuid.UUID(id.Bytes)
	return &v
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func timeToPg(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

// cursorToPg and cursorIDToPg return an unset pgtype value for an empty
// cursor string (the "first page" case — DecodeCursor already reported
// cursorAt/cursorID as the zero value for that case, but the SQL query
// tests for "cursor param IS NULL", so an unset pgtype.Valid is what
// actually disables the WHERE clause's cursor condition, not a
// zero-but-valid timestamp).
func cursorToPg(cursor string, at time.Time) pgtype.Timestamptz {
	if cursor == "" {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: at, Valid: true}
}

func cursorIDToPg(cursor string, id uuid.UUID) pgtype.UUID {
	if cursor == "" {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: id, Valid: true}
}
