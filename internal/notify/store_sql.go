package notify

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore is the org-scoped notification store.
type SQLStore struct {
	q *gen.Queries
}

// NewSQLStore builds the store on *db.DB so every statement runs in the
// request's org scope.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d)} }

func (st *SQLStore) CreateChannel(ctx context.Context, c Channel) (Channel, error) {
	row, err := st.q.CreateNotificationChannel(ctx, gen.CreateNotificationChannelParams{
		ID: c.ID, OrgID: c.OrgID, Kind: string(c.Kind), Name: c.Name, Config: c.Config, BodyJson: c.BodyJSON, Enabled: c.Enabled,
		CreatedBy: db.NullUUID(c.CreatedBy),
	})
	if db.IsUniqueViolation(err) {
		return Channel{}, ErrNameTaken
	}
	if err != nil {
		return Channel{}, fmt.Errorf("create notification channel: %w", err)
	}
	return toChannel(row), nil
}

func (st *SQLStore) GetChannel(ctx context.Context, orgID, id uuid.UUID) (Channel, error) {
	row, err := st.q.GetNotificationChannel(ctx, gen.GetNotificationChannelParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	if err != nil {
		return Channel{}, fmt.Errorf("get notification channel: %w", err)
	}
	return toChannel(row), nil
}

func (st *SQLStore) ListChannels(ctx context.Context, orgID uuid.UUID) ([]Channel, error) {
	rows, err := st.q.ListNotificationChannels(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list notification channels: %w", err)
	}
	out := make([]Channel, len(rows))
	for i, r := range rows {
		out[i] = toChannel(r)
	}
	return out, nil
}

func (st *SQLStore) UpdateChannel(ctx context.Context, c Channel, revision int64) (Channel, error) {
	row, err := st.q.UpdateNotificationChannel(ctx, gen.UpdateNotificationChannelParams{
		Name: c.Name, Config: c.Config, BodyJson: c.BodyJSON, Enabled: c.Enabled, OrgID: c.OrgID, ID: c.ID, Revision: revision,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, gerr := st.GetChannel(ctx, c.OrgID, c.ID); gerr != nil {
			return Channel{}, gerr
		}
		return Channel{}, ErrRevisionMismatch
	}
	if db.IsUniqueViolation(err) {
		return Channel{}, ErrNameTaken
	}
	if err != nil {
		return Channel{}, fmt.Errorf("update notification channel: %w", err)
	}
	return toChannel(row), nil
}

func (st *SQLStore) DeleteChannel(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := st.q.DeleteNotificationChannel(ctx, gen.DeleteNotificationChannelParams{OrgID: orgID, ID: id})
	if err != nil {
		return fmt.Errorf("delete notification channel: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func toChannel(r gen.NotificationChannel) Channel {
	return Channel{
		ID: r.ID, OrgID: r.OrgID, Kind: Kind(r.Kind), Name: r.Name, Enabled: r.Enabled, Config: r.Config, BodyJSON: r.BodyJson,
		CreatedBy: db.UUIDPtr(r.CreatedBy), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Revision: r.Revision,
	}
}
