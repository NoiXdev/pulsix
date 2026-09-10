package auth

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLPasskeyStore implements PasskeyStore with sqlc queries.
type SQLPasskeyStore struct{ q *gen.Queries }

// NewSQLPasskeyStore wraps the generated queries.
func NewSQLPasskeyStore(q *gen.Queries) *SQLPasskeyStore { return &SQLPasskeyStore{q: q} }

func (s *SQLPasskeyStore) ListCredentials(ctx context.Context, userID uuid.UUID) ([]Credential, error) {
	rows, err := s.q.ListPasskeys(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Credential, len(rows))
	for i, r := range rows {
		out[i] = toCredential(r)
	}
	return out, nil
}

func (s *SQLPasskeyStore) AddCredential(ctx context.Context, userID uuid.UUID, name string, c Credential) error {
	// transports is NOT NULL; a nil []string (an authenticator that
	// reported no transports) must become "{}", not a NULL that violates
	// the column constraint — the column's own DEFAULT only applies when
	// the column is omitted from the INSERT, not when an explicit NULL is
	// bound to it.
	transports := c.Transports
	if transports == nil {
		transports = []string{}
	}
	return s.q.InsertPasskey(ctx, gen.InsertPasskeyParams{
		ID: c.ID, UserID: userID, Name: name, CredentialID: c.CredentialID, PublicKey: c.PublicKey,
		Attestation: c.Attestation, Transports: transports, SignCount: int64(c.SignCount), BackupEligible: c.BackupEligible,
	})
}

func (s *SQLPasskeyStore) UpdateCredentialCounter(ctx context.Context, credentialID []byte, counter uint32) error {
	return s.q.UpdatePasskeyCounter(ctx, gen.UpdatePasskeyCounterParams{CredentialID: credentialID, SignCount: int64(counter)})
}

// DeleteCredential returns ErrNotFound when no row matched id owned by
// userID, so a caller can never mistake "nothing happened" for success.
func (s *SQLPasskeyStore) DeleteCredential(ctx context.Context, userID, id uuid.UUID) error {
	n, err := s.q.DeletePasskey(ctx, gen.DeletePasskeyParams{ID: id, UserID: userID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLPasskeyStore) FindUserByCredentialID(ctx context.Context, credentialID []byte) (uuid.UUID, error) {
	row, err := s.q.GetPasskeyByCredentialID(ctx, credentialID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrNotFound
		}
		return uuid.Nil, err
	}
	return row.UserID, nil
}

func (s *SQLPasskeyStore) PutCeremony(ctx context.Context, id uuid.UUID, kind string, userID *uuid.UUID, data []byte, expiresAt time.Time) error {
	return s.q.PutCeremony(ctx, gen.PutCeremonyParams{
		ID: id, Kind: kind, UserID: toPgUUID(userID), Data: data, ExpiresAt: expiresAt,
	})
}

// TakeCeremony maps pgx.ErrNoRows — an unknown, already-taken, or expired
// ceremony id, all of which the DELETE ... RETURNING query treats alike —
// to ErrNotFound.
func (s *SQLPasskeyStore) TakeCeremony(ctx context.Context, id uuid.UUID) (string, *uuid.UUID, []byte, error) {
	row, err := s.q.TakeCeremony(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, nil, ErrNotFound
		}
		return "", nil, nil, err
	}
	return row.Kind, fromPgUUID(row.UserID), row.Data, nil
}

func toCredential(r gen.Passkey) Credential {
	c := Credential{
		ID: r.ID, Name: r.Name, CredentialID: r.CredentialID, PublicKey: r.PublicKey, Attestation: r.Attestation,
		Transports: r.Transports, SignCount: uint32(r.SignCount), BackupEligible: r.BackupEligible, CreatedAt: r.CreatedAt,
	}
	if r.LastUsedAt.Valid {
		t := r.LastUsedAt.Time
		c.LastUsedAt = &t
	}
	return c
}

// toPgUUID converts our nil-able *uuid.UUID (nil for a login ceremony, no
// user known yet) to the pgtype.UUID sqlc generates for the nullable
// webauthn_ceremonies.user_id column.
func toPgUUID(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

func fromPgUUID(id pgtype.UUID) *uuid.UUID {
	if !id.Valid {
		return nil
	}
	u := uuid.UUID(id.Bytes)
	return &u
}
