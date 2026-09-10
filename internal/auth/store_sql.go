package auth

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store with sqlc queries.
type SQLStore struct{ q *gen.Queries }

// NewSQLStore wraps the generated queries.
func NewSQLStore(q *gen.Queries) *SQLStore { return &SQLStore{q: q} }

func (s *SQLStore) GetUserByEmail(ctx context.Context, email string) (User, error) {
	row, err := s.q.GetUserByEmail(ctx, email)
	return toUser(row), mapErr(err)
}

func (s *SQLStore) GetUserByID(ctx context.Context, id uuid.UUID) (User, error) {
	row, err := s.q.GetUserByID(ctx, id)
	return toUser(row), mapErr(err)
}

func toUser(r gen.User) User {
	return User{ID: r.ID, Email: r.Email, DisplayName: r.DisplayName, PasswordHash: r.PasswordHash, InstanceAdmin: r.InstanceAdmin}
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
