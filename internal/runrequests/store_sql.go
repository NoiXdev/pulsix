package runrequests

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/jobs"
)

// SQLStore implements Store with sqlc queries.
type SQLStore struct {
	db *db.DB
	q  *gen.Queries
}

// NewSQLStore builds the store on d itself, never on its raw pool, so the
// statements run inside the request's organization scope.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{db: d, q: gen.New(d)} }

// tokenCapLockClass is the first key of the per-token pending cap lock,
// taken in the two-int form pg_advisory_xact_lock(class, hashtext(token)).
// The other advisory locks use the single-bigint form (a distinct lock
// space, see pg_locks.objsubid) and none shares this class, so a token
// lock cannot collide with a fixed key. Arbitrary but must never change.
const tokenCapLockClass = 0x52525443

// Insert implements Store.
func (s *SQLStore) Insert(ctx context.Context, r Request) error {
	return insertRow(ctx, s.q, r)
}

// InsertCapped implements Store: the count and the insert run in one
// transaction under the token's advisory lock, so concurrent creates of one
// token cannot both pass the count.
func (s *SQLStore) InsertCapped(ctx context.Context, r Request, limit int, now time.Time) error {
	if r.RequestedByToken == nil {
		return s.Insert(ctx, r)
	}
	return s.db.WithOrg(ctx, r.OrgID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, int32(tokenCapLockClass), r.RequestedByToken.String()); err != nil {
			return fmt.Errorf("acquire run request cap lock: %w", err)
		}
		q := gen.New(tx)
		n, err := q.CountPendingRunRequests(ctx, gen.CountPendingRunRequestsParams{OrgID: r.OrgID, Now: now, Token: pgUUID(r.RequestedByToken)})
		if err != nil {
			return fmt.Errorf("count pending run requests: %w", err)
		}
		if int(n) >= limit {
			return ErrTooManyPending
		}
		return insertRow(ctx, q, r)
	})
}

func insertRow(ctx context.Context, q *gen.Queries, r Request) error {
	var overrides []byte
	if r.Overrides != nil {
		var err error
		if overrides, err = json.Marshal(r.Overrides); err != nil {
			return fmt.Errorf("encode run request overrides: %w", err)
		}
	}
	var expiresIn *int32
	if r.ExpiresInMinutes > 0 {
		v := int32(min(r.ExpiresInMinutes, MaxExpiresInMinutes)) //nolint:gosec // bounded just now
		expiresIn = &v
	}
	return q.InsertRunRequest(ctx, gen.InsertRunRequestParams{
		ID: r.ID, OrgID: r.OrgID, Kind: string(r.Kind), TemplateID: r.TemplateID, TemplateRevision: r.TemplateRevision,
		HostID: pgUUID(r.HostID), JobID: pgUUID(r.JobID), Reason: r.Reason,
		RequestedByUser: r.RequestedByUser, RequestedByToken: pgUUID(r.RequestedByToken),
		Status: string(cmp.Or(r.Status, StatusPending)), ExpiresAt: pgTime(r.ExpiresAt), CreatedAt: r.CreatedAt, Overrides: overrides,
		SyncJobID: pgUUID(r.SyncJobID), ExpiresInMinutes: expiresIn,
	})
}

// Get implements Store.
func (s *SQLStore) Get(ctx context.Context, orgID, id uuid.UUID) (Request, error) {
	row, err := s.q.GetRunRequest(ctx, gen.GetRunRequestParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, fmt.Errorf("get run request: %w", err)
	}
	return fromRow(row)
}

// List implements Store.
func (s *SQLStore) List(ctx context.Context, orgID uuid.UUID, status *Status, now time.Time) ([]Request, error) {
	var st *string
	if status != nil {
		v := string(*status)
		st = &v
	}
	rows, err := s.q.ListRunRequests(ctx, gen.ListRunRequestsParams{OrgID: orgID, Status: st, Now: now})
	if err != nil {
		return nil, fmt.Errorf("list run requests: %w", err)
	}
	out := make([]Request, len(rows))
	for i, row := range rows {
		if out[i], err = fromRow(row); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CountPending implements Store.
func (s *SQLStore) CountPending(ctx context.Context, orgID uuid.UUID, token *uuid.UUID, now time.Time) (int, error) {
	n, err := s.q.CountPendingRunRequests(ctx, gen.CountPendingRunRequestsParams{OrgID: orgID, Now: now, Token: pgUUID(token)})
	if err != nil {
		return 0, fmt.Errorf("count pending run requests: %w", err)
	}
	return int(n), nil
}

// Tokens implements Store.
func (s *SQLStore) Tokens(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]TokenRecord, error) {
	out := make(map[uuid.UUID]TokenRecord, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.q.ListRunRequestTokens(ctx, gen.ListRunRequestTokensParams{OrgID: orgID, Ids: ids})
	if err != nil {
		return nil, fmt.Errorf("list run request tokens: %w", err)
	}
	for _, row := range rows {
		rec := TokenRecord{Name: row.Name, Revoked: row.Revoked}
		if row.ExpiresAt.Valid {
			at := row.ExpiresAt.Time
			rec.ExpiresAt = &at
		}
		out[row.ID] = rec
	}
	return out, nil
}

// Decide implements Store.
func (s *SQLStore) Decide(ctx context.Context, orgID, id uuid.UUID, to Status, by uuid.UUID, at time.Time, fingerprint, pinnedCommit string) (bool, error) {
	var fp, pin *string
	if fingerprint != "" {
		fp = &fingerprint
	}
	if pinnedCommit != "" {
		pin = &pinnedCommit
	}
	n, err := s.q.DecideRunRequest(ctx, gen.DecideRunRequestParams{
		OrgID: orgID, ID: id, Status: string(to),
		DecidedBy: pgtype.UUID{Bytes: by, Valid: true}, DecidedAt: pgtype.Timestamptz{Time: at, Valid: true},
		ApprovedFingerprint: fp, PinnedCommit: pin,
	})
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// RevertToPending implements Store.
func (s *SQLStore) RevertToPending(ctx context.Context, orgID, id uuid.UUID) error {
	return s.q.RevertRunRequest(ctx, gen.RevertRunRequestParams{OrgID: orgID, ID: id})
}

// ByRunGroup implements Store.
func (s *SQLStore) ByRunGroup(ctx context.Context, orgID, group uuid.UUID) (Request, error) {
	row, err := s.q.GetRunRequestByRunGroup(ctx, gen.GetRunRequestByRunGroupParams{OrgID: orgID, RunGroupID: pgtype.UUID{Bytes: group, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, fmt.Errorf("get run request by run group: %w", err)
	}
	return fromRow(row)
}

// ListWaiting implements Store.
func (s *SQLStore) ListWaiting(ctx context.Context, syncJob *uuid.UUID) ([]Waiting, error) {
	rows, err := s.q.ListWaitingRunRequests(ctx, pgUUID(syncJob))
	if err != nil {
		return nil, fmt.Errorf("list run requests waiting for their sync: %w", err)
	}
	out := make([]Waiting, len(rows))
	for i, row := range rows {
		w := Waiting{OrgID: row.OrgID, ID: row.ID, CreatedAt: row.CreatedAt}
		if row.SyncJobID.Valid {
			w.SyncJobID = row.SyncJobID.Bytes
		}
		if row.ExpiresInMinutes != nil {
			w.ExpiresInMinutes = int(*row.ExpiresInMinutes)
		}
		if row.SyncStatus != nil {
			w.SyncStatus = jobs.Status(*row.SyncStatus)
		}
		if row.SyncError != nil {
			w.SyncError = *row.SyncError
		}
		out[i] = w
	}
	return out, nil
}

// EndWait implements Store.
func (s *SQLStore) EndWait(ctx context.Context, orgID, id uuid.UUID, to Status, expiresAt time.Time, syncError string) (bool, error) {
	var reason *string
	if syncError != "" {
		reason = &syncError
	}
	n, err := s.q.EndRunRequestWait(ctx, gen.EndRunRequestWaitParams{
		OrgID: orgID, ID: id, Status: string(to), ExpiresAt: pgTime(expiresAt), SyncError: reason,
	})
	if err != nil {
		return false, fmt.Errorf("end a run request's wait for its sync: %w", err)
	}
	return n == 1, nil
}

func fromRow(r gen.RunRequest) (Request, error) {
	out := Request{
		ID: r.ID, OrgID: r.OrgID, Kind: Kind(r.Kind), TemplateID: r.TemplateID, TemplateRevision: r.TemplateRevision,
		HostID: fromPgUUID(r.HostID), JobID: fromPgUUID(r.JobID), Reason: r.Reason,
		RequestedByUser: r.RequestedByUser, RequestedByToken: fromPgUUID(r.RequestedByToken),
		Status: Status(r.Status), DecidedBy: fromPgUUID(r.DecidedBy), RunGroupID: fromPgUUID(r.RunGroupID),
		CreatedAt: r.CreatedAt, SyncJobID: fromPgUUID(r.SyncJobID),
	}
	if r.ExpiresAt.Valid {
		out.ExpiresAt = r.ExpiresAt.Time
	}
	if r.ExpiresInMinutes != nil {
		out.ExpiresInMinutes = int(*r.ExpiresInMinutes)
	}
	if r.SyncError != nil {
		out.SyncError = *r.SyncError
	}
	if r.DecidedAt.Valid {
		at := r.DecidedAt.Time
		out.DecidedAt = &at
	}
	if r.ApprovedFingerprint != nil {
		out.ApprovedFingerprint = *r.ApprovedFingerprint
	}
	if r.PinnedCommit != nil {
		out.PinnedCommit = *r.PinnedCommit
	}
	if len(r.Overrides) > 0 {
		var ov Overrides
		dec := json.NewDecoder(bytes.NewReader(r.Overrides))
		dec.UseNumber() // extra vars keep their numbers exact
		if err := dec.Decode(&ov); err != nil {
			return Request{}, fmt.Errorf("decode run request %s overrides: %w", r.ID, err)
		}
		out.Overrides = &ov
	}
	return out, nil
}

func pgUUID(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

func fromPgUUID(id pgtype.UUID) *uuid.UUID {
	if !id.Valid {
		return nil
	}
	v := uuid.UUID(id.Bytes)
	return &v
}

// pgTime is NULL for the zero time: a request waiting for its sync has no
// expiry yet.
func pgTime(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}
