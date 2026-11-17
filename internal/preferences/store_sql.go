package preferences

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore is the sqlc-backed Store.
type SQLStore struct{ q *gen.Queries }

func NewSQLStore(q *gen.Queries) *SQLStore { return &SQLStore{q: q} }

func (s *SQLStore) Get(ctx context.Context, userID uuid.UUID) (Prefs, error) {
	row, err := s.q.GetUserPreferences(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Prefs{}, ErrNotFound
	}
	if err != nil {
		return Prefs{}, err
	}
	return decode(row.Density, row.PageSize, row.Lists)
}

func (s *SQLStore) Put(ctx context.Context, userID uuid.UUID, p Prefs) (Prefs, error) {
	lists, err := json.Marshal(p.Lists)
	if err != nil {
		return Prefs{}, fmt.Errorf("encode lists: %w", err)
	}
	row, err := s.q.UpsertUserPreferences(ctx, gen.UpsertUserPreferencesParams{
		UserID: userID, Density: string(p.Density), PageSize: int32(p.PageSize), Lists: lists,
	})
	if err != nil {
		return Prefs{}, err
	}
	return decode(row.Density, row.PageSize, row.Lists)
}

func decode(density string, pageSize int32, raw []byte) (Prefs, error) {
	lists := map[string]List{}
	if err := json.Unmarshal(raw, &lists); err != nil {
		return Prefs{}, fmt.Errorf("decode lists: %w", err)
	}
	return Prefs{Density: Density(density), PageSize: int(pageSize), Lists: lists}, nil
}
