package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// SQLStore implements Store on *db.DB, so every statement runs inside the
// request's organization scope. Schemas and vars are stored as jsonb.
type SQLStore struct {
	db *db.DB
	q  *gen.Queries
}

// NewSQLStore builds the store.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{db: d, q: gen.New(d)} }

// treeLockAAD namespaces the per-organization advisory lock key from any
// other package's (int64 hashtext keys share one 32-bit keyspace per
// PostgreSQL session/session-less xact lock; see also
// sshconf.chainLockAAD, internal/schedule.AdvisoryKeyRetest and
// db.migrationLockKey, none of which collide with a string-derived key).
const treeLockAAD = "inventory-tree:"

// WithTreeLock runs fn against a Store bound to one transaction that holds
// orgID's group-tree advisory lock for the whole call (backlog 2.15): the
// lock is acquired before fn's first read, so the tree read, the
// cycle/depth check and the write fn performs all see one consistent
// snapshot and commit atomically. A concurrent WithTreeLock call for the
// same organization blocks until this transaction commits or rolls back
// (pg_advisory_xact_lock releases automatically then), which is what turns
// two racing group moves that would jointly form a cycle into one that
// succeeds and one that is rejected against the first's already-committed
// state, instead of both succeeding against a stale tree.
func (s *SQLStore) WithTreeLock(ctx context.Context, orgID uuid.UUID, fn func(Store) error) error {
	return s.db.WithOrg(ctx, orgID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, treeLockAAD+orgID.String()); err != nil {
			return fmt.Errorf("acquire inventory tree lock: %w", err)
		}
		return fn(&SQLStore{db: s.db, q: gen.New(tx)})
	})
}

// WithReadTx delegates to db.DB.WithReadTx: see its doc comment. This
// package's own read methods (ListGroups, ListMemberships, …) need no
// change to take part — they already run through s.q, which is bound to
// whatever ctx carries, ambient transaction included.
func (s *SQLStore) WithReadTx(ctx context.Context, orgID uuid.UUID, fn func(ctx context.Context) error) error {
	return s.db.WithReadTx(ctx, orgID, fn)
}

func (s *SQLStore) CreateGroup(ctx context.Context, g Group) (Group, error) {
	sc, vars, err := encodeGroup(g)
	if err != nil {
		return Group{}, err
	}
	row, err := s.q.CreateInventoryGroup(ctx, gen.CreateInventoryGroupParams{
		ID: g.ID, OrgID: g.OrgID, ParentID: db.NullUUID(g.ParentID), Name: g.Name, Description: g.Description,
		Schema: sc, Vars: vars, BastionID: db.NullUUID(g.BastionID),
	})
	if err != nil {
		return Group{}, mapWriteErr(err)
	}
	return toGroup(row)
}

func (s *SQLStore) GetGroup(ctx context.Context, orgID, id uuid.UUID) (Group, error) {
	row, err := s.q.GetInventoryGroup(ctx, gen.GetInventoryGroupParams{OrgID: orgID, ID: id})
	if err != nil {
		return Group{}, mapErr(err)
	}
	return toGroup(row)
}

func (s *SQLStore) ListGroups(ctx context.Context, orgID uuid.UUID) ([]Group, error) {
	rows, err := s.q.ListInventoryGroups(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Group, len(rows))
	for i, r := range rows {
		if out[i], err = toGroup(r); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *SQLStore) UpdateGroup(ctx context.Context, g Group) (Group, error) {
	sc, vars, err := encodeGroup(g)
	if err != nil {
		return Group{}, err
	}
	row, err := s.q.UpdateInventoryGroup(ctx, gen.UpdateInventoryGroupParams{
		ParentID: db.NullUUID(g.ParentID), Name: g.Name, Description: g.Description, Schema: sc, Vars: vars,
		BastionID: db.NullUUID(g.BastionID), OrgID: g.OrgID, ID: g.ID, Revision: g.Revision,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, getErr := s.q.GetInventoryGroup(ctx, gen.GetInventoryGroupParams{OrgID: g.OrgID, ID: g.ID}); getErr != nil {
			return Group{}, mapErr(getErr)
		}
		return Group{}, ErrRevisionMismatch
	}
	if err != nil {
		return Group{}, mapWriteErr(err)
	}
	return toGroup(row)
}

func (s *SQLStore) DeleteGroup(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.q.DeleteInventoryGroup(ctx, gen.DeleteInventoryGroupParams{OrgID: orgID, ID: id})
	if err != nil {
		if db.IsForeignKeyViolation(err) {
			return ErrHasChildren
		}
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) MemberCounts(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID]int, error) {
	rows, err := s.q.CountInventoryMembers(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]int, len(rows))
	for _, r := range rows {
		out[r.GroupID] = int(r.Members)
	}
	return out, nil
}

func (s *SQLStore) PutMembership(ctx context.Context, m Membership) (Membership, error) {
	vars, err := json.Marshal(m.Vars)
	if err != nil {
		return Membership{}, fmt.Errorf("encode membership vars: %w", err)
	}
	row, err := s.q.UpsertInventoryMembership(ctx, gen.UpsertInventoryMembershipParams{GroupID: m.GroupID, HostID: m.HostID, OrgID: m.OrgID, Vars: vars})
	if err != nil {
		return Membership{}, mapWriteErr(err)
	}
	return toMembership(row.GroupID, row.HostID, row.OrgID, row.Vars, row.Revision, row.CreatedAt, row.UpdatedAt, "", "")
}

func (s *SQLStore) UpdateMembership(ctx context.Context, m Membership) (Membership, error) {
	vars, err := json.Marshal(m.Vars)
	if err != nil {
		return Membership{}, fmt.Errorf("encode membership vars: %w", err)
	}
	row, err := s.q.UpdateInventoryMembership(ctx, gen.UpdateInventoryMembershipParams{Vars: vars, OrgID: m.OrgID, GroupID: m.GroupID, HostID: m.HostID, Revision: m.Revision})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, getErr := s.GetMembership(ctx, m.OrgID, m.GroupID, m.HostID); getErr != nil {
			return Membership{}, getErr
		}
		return Membership{}, ErrRevisionMismatch
	}
	if err != nil {
		return Membership{}, err
	}
	return toMembership(row.GroupID, row.HostID, row.OrgID, row.Vars, row.Revision, row.CreatedAt, row.UpdatedAt, "", "")
}

func (s *SQLStore) GetMembership(ctx context.Context, orgID, groupID, hostID uuid.UUID) (Membership, error) {
	row, err := s.q.GetInventoryMembership(ctx, gen.GetInventoryMembershipParams{OrgID: orgID, GroupID: groupID, HostID: hostID})
	if err != nil {
		return Membership{}, mapErr(err)
	}
	return toMembership(row.GroupID, row.HostID, row.OrgID, row.Vars, row.Revision, row.CreatedAt, row.UpdatedAt, "", "")
}

func (s *SQLStore) DeleteMembership(ctx context.Context, orgID, groupID, hostID uuid.UUID) error {
	n, err := s.q.DeleteInventoryMembership(ctx, gen.DeleteInventoryMembershipParams{OrgID: orgID, GroupID: groupID, HostID: hostID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) ListMemberships(ctx context.Context, orgID uuid.UUID) ([]Membership, error) {
	rows, err := s.q.ListInventoryMemberships(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Membership, len(rows))
	for i, r := range rows {
		if out[i], err = toMembership(r.GroupID, r.HostID, r.OrgID, r.Vars, r.Revision, r.CreatedAt, r.UpdatedAt, "", ""); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *SQLStore) ListGroupMembers(ctx context.Context, orgID, groupID uuid.UUID) ([]Membership, error) {
	rows, err := s.q.ListGroupMembers(ctx, gen.ListGroupMembersParams{OrgID: orgID, GroupID: groupID})
	if err != nil {
		return nil, err
	}
	out := make([]Membership, len(rows))
	for i, r := range rows {
		if out[i], err = toMembership(r.GroupID, r.HostID, r.OrgID, r.Vars, r.Revision, r.CreatedAt, r.UpdatedAt, r.HostName, ""); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *SQLStore) ListHostMemberships(ctx context.Context, orgID, hostID uuid.UUID) ([]Membership, error) {
	rows, err := s.q.ListHostMemberships(ctx, gen.ListHostMembershipsParams{OrgID: orgID, HostID: hostID})
	if err != nil {
		return nil, err
	}
	out := make([]Membership, len(rows))
	for i, r := range rows {
		if out[i], err = toMembership(r.GroupID, r.HostID, r.OrgID, r.Vars, r.Revision, r.CreatedAt, r.UpdatedAt, "", r.GroupName); err != nil {
			return nil, err
		}
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
		return fmt.Errorf("%w: a referenced group, host or jump host does not exist", ErrInvalidInput)
	default:
		return err
	}
}

func encodeGroup(g Group) ([]byte, []byte, error) {
	sc, err := json.Marshal(g.Schema)
	if err != nil {
		return nil, nil, fmt.Errorf("encode group schema: %w", err)
	}
	vars := g.Vars
	if vars == nil {
		vars = map[string]any{}
	}
	v, err := json.Marshal(vars)
	if err != nil {
		return nil, nil, fmt.Errorf("encode group vars: %w", err)
	}
	return sc, v, nil
}

func toGroup(r gen.InventoryGroup) (Group, error) {
	g := Group{
		ID: r.ID, OrgID: r.OrgID, ParentID: db.UUIDPtr(r.ParentID), Name: r.Name, Description: r.Description,
		BastionID: db.UUIDPtr(r.BastionID), Revision: r.Revision, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if err := json.Unmarshal(r.Schema, &g.Schema); err != nil {
		return Group{}, fmt.Errorf("decode schema of group %s: %w", r.ID, err)
	}
	if g.Schema.Fields == nil {
		g.Schema.Fields = []schema.Field{}
	}
	if err := json.Unmarshal(r.Vars, &g.Vars); err != nil {
		return Group{}, fmt.Errorf("decode vars of group %s: %w", r.ID, err)
	}
	if g.Vars == nil {
		g.Vars = map[string]any{}
	}
	return g, nil
}

func toMembership(groupID, hostID, orgID uuid.UUID, rawVars []byte, revision int64, created, updated time.Time, hostName, groupName string) (Membership, error) {
	m := Membership{GroupID: groupID, HostID: hostID, OrgID: orgID, Revision: revision, CreatedAt: created, UpdatedAt: updated, HostName: hostName, GroupName: groupName}
	if err := json.Unmarshal(rawVars, &m.Vars); err != nil {
		return Membership{}, fmt.Errorf("decode membership vars: %w", err)
	}
	if m.Vars == nil {
		m.Vars = map[string]any{}
	}
	return m, nil
}
