package runrequests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// OpenReview implements ReviewStore: the review of kind for target that is
// pending and unexpired at now; ErrNotFound when there is none.
func (s *SQLStore) OpenReview(ctx context.Context, orgID uuid.UUID, kind Kind, target uuid.UUID, now time.Time) (Request, error) {
	row, err := s.q.GetOpenReviewRequest(ctx, gen.GetOpenReviewRequestParams{OrgID: orgID, Kind: string(kind), TargetID: target, Now: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, fmt.Errorf("get open review: %w", err)
	}
	return fromRow(row)
}

// RefreshReview implements ReviewStore: the pending review id shows change
// and expires at expiresAt.
func (s *SQLStore) RefreshReview(ctx context.Context, orgID, id uuid.UUID, change ReviewChange, expiresAt time.Time, templateRevision int64) error {
	payload, err := json.Marshal(change)
	if err != nil {
		return fmt.Errorf("encode review: %w", err)
	}
	if _, err := s.q.RefreshReviewRequest(ctx, gen.RefreshReviewRequestParams{
		OrgID: orgID, ID: id, Payload: payload, ExpiresAt: pgTime(expiresAt), TemplateRevision: templateRevision,
	}); err != nil {
		return fmt.Errorf("refresh review: %w", err)
	}
	return nil
}

// CloseReviews implements ReviewStore: the open reviews of kind for target
// become approved by by (uuid.Nil: nobody recorded) at at.
func (s *SQLStore) CloseReviews(ctx context.Context, orgID uuid.UUID, kind Kind, target, by uuid.UUID, at time.Time) error {
	decidedBy := pgtype.UUID{}
	if by != uuid.Nil {
		decidedBy = pgtype.UUID{Bytes: by, Valid: true}
	}
	if _, err := s.q.CloseReviewRequests(ctx, gen.CloseReviewRequestsParams{
		OrgID: orgID, Kind: string(kind), TargetID: target, DecidedBy: decidedBy, DecidedAt: pgtype.Timestamptz{Time: at, Valid: true},
	}); err != nil {
		return fmt.Errorf("close reviews: %w", err)
	}
	return nil
}

// LatestReviews implements ReviewStore: the newest review of kind of each
// of targets that has one, by target.
func (s *SQLStore) LatestReviews(ctx context.Context, orgID uuid.UUID, kind Kind, targets []uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
	out := map[uuid.UUID]uuid.UUID{}
	if len(targets) == 0 {
		return out, nil
	}
	rows, err := s.q.LatestReviewRequests(ctx, gen.LatestReviewRequestsParams{OrgID: orgID, Kind: string(kind), TargetIds: targets})
	if err != nil {
		return nil, fmt.Errorf("list latest reviews: %w", err)
	}
	for _, r := range rows {
		if r.TargetID.Valid {
			out[uuid.UUID(r.TargetID.Bytes)] = r.ID
		}
	}
	return out, nil
}
