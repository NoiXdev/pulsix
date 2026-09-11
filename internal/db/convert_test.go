package db_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/noixdev/pulsix/internal/db"
)

func TestNullConversions(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	if got := db.UUIDPtr(db.NullUUID(&id)); got == nil || *got != id {
		t.Fatalf("uuid round trip = %v", got)
	}
	if db.NullUUID(nil).Valid || db.UUIDPtr(db.NullUUID(nil)) != nil {
		t.Fatal("nil uuid must stay NULL")
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if got := db.TimePtr(db.NullTime(&now)); got == nil || !got.Equal(now) {
		t.Fatalf("time round trip = %v", got)
	}
	if db.NullTime(nil).Valid || db.TimePtr(db.NullTime(nil)) != nil {
		t.Fatal("nil time must stay NULL")
	}
}

func TestPgErrorClassification(t *testing.T) {
	unique := fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "23505"})
	fk := &pgconn.PgError{Code: "23503"}
	if !db.IsUniqueViolation(unique) || db.IsUniqueViolation(fk) {
		t.Fatal("IsUniqueViolation")
	}
	if !db.IsForeignKeyViolation(fk) || db.IsForeignKeyViolation(unique) {
		t.Fatal("IsForeignKeyViolation")
	}
	if db.IsUniqueViolation(errors.New("plain")) || db.IsForeignKeyViolation(nil) {
		t.Fatal("non-pg errors must not match")
	}
}
