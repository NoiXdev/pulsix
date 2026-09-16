package sshconf

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store on *db.DB. The composite foreign keys in
// migration 00014 make a reference into another organization fail as a
// foreign key violation, reported here as ErrInvalidInput; the service
// normally rejects it earlier with a clearer message.
type SQLStore struct {
	db *db.DB
	q  *gen.Queries
}

// NewSQLStore builds the store.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{db: d, q: gen.New(d)} }

// chainLockAAD namespaces this package's per-organization advisory lock key
// (see inventory.treeLockAAD for the sibling group-tree lock; the two never
// need to block each other, since bastions and groups are separate graphs).
const chainLockAAD = "bastion-chain:"

// WithChainLock runs fn against a Store bound to one transaction that holds
// orgID's jump-host chain advisory lock for the whole call (backlog 2.15):
// a chain read inside fn followed by a write inside the same fn call is
// atomic against any concurrent WithChainLock call for the same
// organization, which is what turns two racing chain edits that would
// jointly form a cycle (or a too-long chain) into one that succeeds and one
// that is rejected against the first's already-committed state.
func (s *SQLStore) WithChainLock(ctx context.Context, orgID uuid.UUID, fn func(Store) error) error {
	return s.db.WithOrg(ctx, orgID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, chainLockAAD+orgID.String()); err != nil {
			return fmt.Errorf("acquire bastion chain lock: %w", err)
		}
		return fn(&SQLStore{db: s.db, q: gen.New(tx)})
	})
}

func (s *SQLStore) CreateBastion(ctx context.Context, b Bastion) (Bastion, error) {
	row, err := s.q.CreateBastion(ctx, gen.CreateBastionParams{
		ID: b.ID, OrgID: b.OrgID, Name: b.Name, Address: b.Address, Port: int32(b.Port), Username: b.Username,
		CredentialID: db.NullUUID(b.CredentialID), ViaBastionID: db.NullUUID(b.ViaBastionID),
	})
	if err != nil {
		return Bastion{}, mapWriteErr(err)
	}
	return toBastion(row), nil
}

func (s *SQLStore) GetBastion(ctx context.Context, orgID, id uuid.UUID) (Bastion, error) {
	row, err := s.q.GetBastion(ctx, gen.GetBastionParams{OrgID: orgID, ID: id})
	if err != nil {
		return Bastion{}, mapErr(err)
	}
	return toBastion(row), nil
}

func (s *SQLStore) ListBastions(ctx context.Context, orgID uuid.UUID) ([]Bastion, error) {
	rows, err := s.q.ListBastions(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Bastion, len(rows))
	for i, r := range rows {
		out[i] = toBastion(r)
	}
	return out, nil
}

func (s *SQLStore) UpdateBastion(ctx context.Context, b Bastion) (Bastion, error) {
	row, err := s.q.UpdateBastion(ctx, gen.UpdateBastionParams{
		OrgID: b.OrgID, ID: b.ID, Name: b.Name, Address: b.Address, Port: int32(b.Port), Username: b.Username,
		CredentialID: db.NullUUID(b.CredentialID), ViaBastionID: db.NullUUID(b.ViaBastionID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Bastion{}, ErrNotFound
		}
		return Bastion{}, mapWriteErr(err)
	}
	return toBastion(row), nil
}

func (s *SQLStore) DeleteBastion(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.q.DeleteBastion(ctx, gen.DeleteBastionParams{OrgID: orgID, ID: id})
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

func (s *SQLStore) SetZoneBastion(ctx context.Context, orgID, zoneID uuid.UUID, bastionID *uuid.UUID) error {
	n, err := s.q.SetZoneBastion(ctx, gen.SetZoneBastionParams{OrgID: orgID, ID: zoneID, BastionID: db.NullUUID(bastionID)})
	if err != nil {
		return mapWriteErr(err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) ZoneBastions(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
	rows, err := s.q.ListZoneBastions(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]uuid.UUID, len(rows))
	for _, r := range rows {
		if id := db.UUIDPtr(r.BastionID); id != nil {
			out[r.ID] = *id
		}
	}
	return out, nil
}

func (s *SQLStore) GetHostSSH(ctx context.Context, orgID, hostID uuid.UUID) (HostSSH, error) {
	row, err := s.q.GetHostSSH(ctx, gen.GetHostSSHParams{OrgID: orgID, HostID: hostID})
	if err != nil {
		return HostSSH{}, mapErr(err)
	}
	return toHostSSH(row), nil
}

func (s *SQLStore) PutHostSSH(ctx context.Context, h HostSSH) (HostSSH, error) {
	row, err := s.q.UpsertHostSSH(ctx, gen.UpsertHostSSHParams{
		HostID: h.HostID, OrgID: h.OrgID, Username: h.Username, Port: int32(h.Port),
		CredentialID: db.NullUUID(h.CredentialID), BastionMode: string(h.BastionMode), BastionID: db.NullUUID(h.BastionID),
	})
	if err != nil {
		return HostSSH{}, mapWriteErr(err)
	}
	return toHostSSH(row), nil
}

func (s *SQLStore) ListHostSSH(ctx context.Context, orgID uuid.UUID) ([]HostSSH, error) {
	rows, err := s.q.ListHostSSH(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]HostSSH, len(rows))
	for i, r := range rows {
		out[i] = toHostSSH(r)
	}
	return out, nil
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func mapWriteErr(err error) error {
	switch {
	case db.IsUniqueViolation(err):
		return ErrNameTaken
	case db.IsForeignKeyViolation(err):
		return ErrInvalidInput
	default:
		return err
	}
}

func toBastion(r gen.Bastion) Bastion {
	return Bastion{
		ID: r.ID, OrgID: r.OrgID, Name: r.Name, Address: r.Address, Port: int(r.Port), Username: r.Username,
		CredentialID: db.UUIDPtr(r.CredentialID), ViaBastionID: db.UUIDPtr(r.ViaBastionID),
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func toHostSSH(r gen.HostSsh) HostSSH {
	return HostSSH{
		HostID: r.HostID, OrgID: r.OrgID, Username: r.Username, Port: int(r.Port),
		CredentialID: db.UUIDPtr(r.CredentialID), BastionMode: BastionMode(r.BastionMode), BastionID: db.UUIDPtr(r.BastionID),
	}
}
