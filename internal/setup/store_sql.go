package setup

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// setupLockKey is the PostgreSQL advisory lock SQLStore.InTx takes for the
// duration of its transaction. It is what actually makes Complete's "no
// user exists yet" check atomic against a concurrent caller: two
// transactions racing to complete setup both try to acquire this lock, the
// second blocks until the first commits or rolls back, and only then runs
// its own AdminExists check — by which point the first transaction's
// result (an admin now existing, or not, if it failed) is visible. This
// mirrors db.Migrate's own use of a fixed advisory lock key to serialize
// migrations across manager instances; the value is arbitrary but must
// never change, and must never collide with db.go's migrationLockKey
// (7256).
const setupLockKey = 84231

// SQLStore implements Store with sqlc queries, mirroring auth.SQLStore and
// tenancy.SQLStore's seam. Unlike those two, it also holds the database
// handle directly (not just *gen.Queries): InTx needs to begin its own
// transaction, which a bare *gen.Queries has no way to do.
//
// Everything here is instance-level — first-run setup creates the first
// organization, it does not act inside one — so it runs without an
// organization bound to the context and row-level security's bypass policy
// applies by design. It is still built on *db.DB rather than the raw pool,
// like every store, so that property is a fact about the caller's context
// and never about how the store happened to be wired.
type SQLStore struct {
	db *db.DB
	q  *gen.Queries
}

// NewSQLStore wraps d. Non-transactional methods (AdminExists,
// AdminMFAEnrolled, LoadSetting — used directly by Status and LoadSMTP,
// which run outside Complete's transaction) query through d's normal
// connection reuse; InTx opens a dedicated transaction for Complete and
// SaveSMTP-inside-Complete's writes.
func NewSQLStore(d *db.DB) *SQLStore {
	return &SQLStore{db: d, q: gen.New(d)}
}

// InTx begins a transaction, takes setupLockKey as an advisory lock scoped
// to it (released automatically at commit or rollback — pg_advisory_xact_lock,
// not the session-scoped pg_advisory_lock db.Migrate uses, so there is no
// separate unlock step to forget), and runs fn against a Store bound to
// that transaction. fn's error rolls the transaction back and propagates
// unchanged; success commits.
func (s *SQLStore) InTx(ctx context.Context, fn func(Store) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed; the error path already returns its own error

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, setupLockKey); err != nil {
		return fmt.Errorf("acquire setup lock: %w", err)
	}

	txStore := &SQLStore{db: s.db, q: gen.New(tx)}
	if err := fn(txStore); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func (s *SQLStore) AdminExists(ctx context.Context) (bool, error) {
	return s.q.InstanceAdminExists(ctx)
}

func (s *SQLStore) AdminMFAEnrolled(ctx context.Context) (bool, error) {
	return s.q.InstanceAdminMFAEnrolled(ctx)
}

func (s *SQLStore) CreateAdmin(ctx context.Context, u AdminUser) error {
	hash := u.PasswordHash
	_, err := s.q.CreateInstanceAdmin(ctx, gen.CreateInstanceAdminParams{
		ID: u.ID, Email: u.Email, DisplayName: u.DisplayName, PasswordHash: &hash,
	})
	return err
}

func (s *SQLStore) CreateOrg(ctx context.Context, o tenancy.Org) error {
	_, err := s.q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: o.ID, Slug: o.Slug, Name: o.Name})
	return err
}

func (s *SQLStore) AddMembership(ctx context.Context, orgID, userID uuid.UUID, role tenancy.Role) error {
	return s.q.AddMembership(ctx, gen.AddMembershipParams{OrgID: orgID, UserID: userID, Role: gen.OrgRole(role)})
}

func (s *SQLStore) SaveSetting(ctx context.Context, key string, value []byte) error {
	return s.q.UpsertInstanceSetting(ctx, gen.UpsertInstanceSettingParams{Key: key, Value: value})
}

func (s *SQLStore) LoadSetting(ctx context.Context, key string) ([]byte, error) {
	row, err := s.q.GetInstanceSetting(ctx, key)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return row.Value, nil
}
