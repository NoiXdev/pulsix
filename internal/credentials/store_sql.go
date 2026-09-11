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

func (s *SQLStore) Get(ctx context.Context, orgID, id uuid.UUID) (Credential, error) {
	row, err := s.q.GetCredential(ctx, gen.GetCredentialParams{OrgID: orgID, ID: id})
	if err != nil {
		return Credential{}, mapErr(err)
	}
	return toCredential(row), nil
}

func (s *SQLStore) List(ctx context.Context, orgID uuid.UUID) ([]Credential, error) {
	rows, err := s.q.ListCredentials(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Credential, len(rows))
	for i, r := range rows {
		out[i] = toCredential(r)
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
