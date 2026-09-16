package credentials

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store with sqlc queries on *db.DB, so every statement
// runs inside the request's organization scope.
type SQLStore struct{ q *gen.Queries }

// NewSQLStore builds the store.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d)} }

func (s *SQLStore) Create(ctx context.Context, c Credential, sealed []byte) (Credential, error) {
	row, err := s.q.CreateCredential(ctx, gen.CreateCredentialParams{
		ID: c.ID, OrgID: c.OrgID, Name: c.Name, Kind: string(c.Kind), Username: c.Username,
		PublicKey: c.PublicKey, Fingerprint: c.Fingerprint, KeyType: c.KeyType, Secret: sealed,
		ExpiresAt: db.NullTime(c.ExpiresAt), CreatedBy: db.NullUUID(c.CreatedBy),
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return Credential{}, ErrNameTaken
		}
		return Credential{}, err
	}
	return toCredential(row), nil
}

// Get reads one credential's metadata. The generated query (see
// GetCredential's comment in credentials.sql) selects every column except
// the sealed secret, so there is no ciphertext to leave out here — unlike
// the pre-2.17 shape, where toCredential's job was to drop a Secret field
// the row already carried.
func (s *SQLStore) Get(ctx context.Context, orgID, id uuid.UUID) (Credential, error) {
	row, err := s.q.GetCredential(ctx, gen.GetCredentialParams{OrgID: orgID, ID: id})
	if err != nil {
		return Credential{}, mapErr(err)
	}
	return toCredentialMeta(row), nil
}

// List reads every credential's metadata, same narrowing as Get (backlog
// 2.17): see ListCredentials' comment in credentials.sql.
func (s *SQLStore) List(ctx context.Context, orgID uuid.UUID) ([]Credential, error) {
	rows, err := s.q.ListCredentials(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Credential, len(rows))
	for i, r := range rows {
		out[i] = toCredentialMeta(gen.GetCredentialRow(r))
	}
	return out, nil
}

func (s *SQLStore) Update(ctx context.Context, c Credential, sealed []byte) (Credential, error) {
	row, err := s.q.UpdateCredential(ctx, gen.UpdateCredentialParams{
		OrgID: c.OrgID, ID: c.ID, Name: c.Name, Username: c.Username, ExpiresAt: db.NullTime(c.ExpiresAt),
		PublicKey: c.PublicKey, Fingerprint: c.Fingerprint, KeyType: c.KeyType, Secret: sealed,
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return Credential{}, ErrNameTaken
		}
		return Credential{}, mapErr(err)
	}
	return toCredential(row), nil
}

func (s *SQLStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.q.DeleteCredential(ctx, gen.DeleteCredentialParams{OrgID: orgID, ID: id})
	if err != nil {
		if db.IsForeignKeyViolation(err) {
			return ErrInUse
		}
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) Usage(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID]Usage, error) {
	rows, err := s.q.ListCredentialUsage(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]Usage, len(rows))
	for _, r := range rows {
		out[r.ID] = Usage{Bastions: int(r.Bastions), Hosts: int(r.Hosts)}
	}
	return out, nil
}

// LoadSecret reads kind and sealed bytes only — the generated query is the
// narrowest possible select (see GetCredentialSecret's comment in
// credentials.sql), so no metadata column can ride along.
func (s *SQLStore) LoadSecret(ctx context.Context, orgID, id uuid.UUID) (Kind, []byte, error) {
	row, err := s.q.GetCredentialSecret(ctx, gen.GetCredentialSecretParams{OrgID: orgID, ID: id})
	if err != nil {
		return "", nil, mapErr(err)
	}
	return Kind(row.Kind), row.Secret, nil
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func toCredential(r gen.Credential) Credential {
	return Credential{
		ID: r.ID, OrgID: r.OrgID, Name: r.Name, Kind: Kind(r.Kind), Username: r.Username,
		PublicKey: r.PublicKey, Fingerprint: r.Fingerprint, KeyType: r.KeyType,
		ExpiresAt: db.TimePtr(r.ExpiresAt), CreatedBy: db.UUIDPtr(r.CreatedBy),
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// toCredentialMeta is toCredential's counterpart for the secret-less
// GetCredentialRow shape (backlog 2.17): Get and List both scan into this
// row type (or its field-identical ListCredentialsRow sibling, converted
// at the call site), which has no Secret field to ignore in the first
// place — the narrowing happens in SQL, not in Go.
func toCredentialMeta(r gen.GetCredentialRow) Credential {
	return Credential{
		ID: r.ID, OrgID: r.OrgID, Name: r.Name, Kind: Kind(r.Kind), Username: r.Username,
		PublicKey: r.PublicKey, Fingerprint: r.Fingerprint, KeyType: r.KeyType,
		ExpiresAt: db.TimePtr(r.ExpiresAt), CreatedBy: db.UUIDPtr(r.CreatedBy),
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}
