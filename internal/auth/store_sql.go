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

func (s *SQLStore) SetLocale(ctx context.Context, id uuid.UUID, l *string) error {
	n, err := s.q.SetUserLocale(ctx, gen.SetUserLocaleParams{ID: id, Locale: l})
	if err != nil {
		return mapErr(err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func toUser(r gen.User) User {
	return User{
		ID: r.ID, Email: r.Email, DisplayName: r.DisplayName, PasswordHash: r.PasswordHash, InstanceAdmin: r.InstanceAdmin,
		DisabledAt: fromPgTimestamptz(r.DisabledAt), Locale: r.Locale,
		TempPasswordExpiresAt: fromPgTimestamptz(r.TempPasswordExpiresAt),
	}
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
