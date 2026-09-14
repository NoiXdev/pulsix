package ca

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store with sqlc queries on *db.DB, the same way the
// credentials store is built.
type SQLStore struct{ q *gen.Queries }

// NewSQLStore builds the store.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d)} }

// Load returns the sealed CA blob; found is false before the first Save.
func (s *SQLStore) Load(ctx context.Context) ([]byte, bool, error) {
	sealed, err := s.q.GetInstanceCA(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("load instance CA: %w", err)
	}
	return sealed, true, nil
}

// Save replaces the sealed CA blob of the singleton row.
func (s *SQLStore) Save(ctx context.Context, sealed []byte) error {
	if err := s.q.UpsertInstanceCA(ctx, sealed); err != nil {
		return fmt.Errorf("save instance CA: %w", err)
	}
	return nil
}
