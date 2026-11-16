package runrequests

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store with sqlc queries.
type SQLStore struct{ q *gen.Queries }

// NewSQLStore builds the store on d itself, never on its raw pool, so the
// statements run inside the request's organization scope.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d)} }

// Insert implements Store.
func (s *SQLStore) Insert(ctx context.Context, r Request) error {
	return s.q.InsertRunRequest(ctx, gen.InsertRunRequestParams{
		ID: r.ID, OrgID: r.OrgID, Kind: string(r.Kind), TemplateID: r.TemplateID, TemplateRevision: r.TemplateRevision,
		HostID: pgUUID(r.HostID), JobID: pgUUID(r.JobID), Reason: r.Reason,
		RequestedByUser: r.RequestedByUser, RequestedByToken: pgUUID(r.RequestedByToken),
		ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt,
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
	return fromRow(row), nil
}

// List implements Store.
func (s *SQLStore) List(ctx context.Context, orgID uuid.UUID) ([]Request, error) {
	rows, err := s.q.ListRunRequests(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list run requests: %w", err)
	}
	out := make([]Request, len(rows))
	for i, row := range rows {
		out[i] = fromRow(row)
	}
	return out, nil
}

// Decide implements Store.
func (s *SQLStore) Decide(ctx context.Context, orgID, id uuid.UUID, to Status, by uuid.UUID, at time.Time) (bool, error) {
	n, err := s.q.DecideRunRequest(ctx, gen.DecideRunRequestParams{
		OrgID: orgID, ID: id, Status: string(to),
		DecidedBy: pgtype.UUID{Bytes: by, Valid: true}, DecidedAt: pgtype.Timestamptz{Time: at, Valid: true},
	})
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// SetRunGroup implements Store.
func (s *SQLStore) SetRunGroup(ctx context.Context, orgID, id, group uuid.UUID) error {
	return s.q.SetRunRequestRunGroup(ctx, gen.SetRunRequestRunGroupParams{OrgID: orgID, ID: id, RunGroupID: pgtype.UUID{Bytes: group, Valid: true}})
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
	return fromRow(row), nil
}

func fromRow(r gen.RunRequest) Request {
	out := Request{
		ID: r.ID, OrgID: r.OrgID, Kind: Kind(r.Kind), TemplateID: r.TemplateID, TemplateRevision: r.TemplateRevision,
		HostID: fromPgUUID(r.HostID), JobID: fromPgUUID(r.JobID), Reason: r.Reason,
		RequestedByUser: r.RequestedByUser, RequestedByToken: fromPgUUID(r.RequestedByToken),
		Status: Status(r.Status), DecidedBy: fromPgUUID(r.DecidedBy), RunGroupID: fromPgUUID(r.RunGroupID),
		ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt,
	}
	if r.DecidedAt.Valid {
		at := r.DecidedAt.Time
		out.DecidedAt = &at
	}
	return out
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
