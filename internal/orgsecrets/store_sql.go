package orgsecrets

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store on org_secrets. It runs on *db.DB, so every
// statement runs inside the context's organization scope: the API binds
// the request's organization and the probe runner the host's, so every
// read runs under org_isolation, and each statement also filters by
// org_id.
type SQLStore struct{ q *gen.Queries }

// NewSQLStore builds the store on d itself, never on its raw pool.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d)} }

// Upsert implements Store.
func (s *SQLStore) Upsert(ctx context.Context, orgID uuid.UUID, name string, sealed []byte, fieldNames []string, at time.Time) (Stored, error) {
	r, err := s.q.UpsertOrgSecret(ctx, gen.UpsertOrgSecretParams{
		OrgID: orgID, Name: name, FieldsSealed: sealed, FieldNames: fieldNames, At: at,
	})
	if err != nil {
		return Stored{}, err
	}
	return Stored{Name: r.Name, FieldNames: r.FieldNames, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}, nil
}

// Insert implements Store: no row back means the name was taken.
func (s *SQLStore) Insert(ctx context.Context, orgID uuid.UUID, name string, sealed []byte, fieldNames []string, at time.Time) (Stored, bool, error) {
	r, err := s.q.InsertOrgSecret(ctx, gen.InsertOrgSecretParams{
		OrgID: orgID, Name: name, FieldsSealed: sealed, FieldNames: fieldNames, At: at,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Stored{}, false, nil
	}
	if err != nil {
		return Stored{}, false, err
	}
	return Stored{Name: r.Name, FieldNames: r.FieldNames, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}, true, nil
}

// List implements Store.
func (s *SQLStore) List(ctx context.Context, orgID uuid.UUID) ([]Stored, error) {
	rows, err := s.q.ListOrgSecrets(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Stored, len(rows))
	for i, r := range rows {
		out[i] = Stored{Name: r.Name, FieldNames: r.FieldNames, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	}
	return out, nil
}

// Exists implements Store.
func (s *SQLStore) Exists(ctx context.Context, orgID uuid.UUID, name string) (bool, error) {
	return s.q.OrgSecretExists(ctx, gen.OrgSecretExistsParams{OrgID: orgID, Name: name})
}

// Sealed implements Store.
func (s *SQLStore) Sealed(ctx context.Context, orgID uuid.UUID, name string) ([]byte, error) {
	sealed, err := s.q.GetOrgSecretSealed(ctx, gen.GetOrgSecretSealedParams{OrgID: orgID, Name: name})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get sealed secret: %w", err)
	}
	return sealed, nil
}

// Delete implements Store.
func (s *SQLStore) Delete(ctx context.Context, orgID uuid.UUID, name string) (bool, error) {
	n, err := s.q.DeleteOrgSecret(ctx, gen.DeleteOrgSecretParams{OrgID: orgID, Name: name})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
