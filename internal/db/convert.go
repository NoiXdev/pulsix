package db

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// NullUUID converts an optional id to the type sqlc generates for a nullable
// uuid column.
func NullUUID(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

// UUIDPtr reverses NullUUID.
func UUIDPtr(v pgtype.UUID) *uuid.UUID {
	if !v.Valid {
		return nil
	}
	id := uuid.UUID(v.Bytes)
	return &id
}

// NullTime converts an optional time to a nullable timestamptz.
func NullTime(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

// TimePtr reverses NullTime.
func TimePtr(v pgtype.Timestamptz) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time
	return &t
}

// IsUniqueViolation reports a unique constraint violation (SQLSTATE 23505).
func IsUniqueViolation(err error) bool { return pgCode(err) == "23505" }

// IsForeignKeyViolation reports a foreign key violation (SQLSTATE 23503):
// a row that is still referenced, or a reference to a row that does not exist.
func IsForeignKeyViolation(err error) bool { return pgCode(err) == "23503" }

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
