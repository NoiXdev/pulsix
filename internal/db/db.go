// Package db owns the connection pool, migrations and the per-org transaction helper.
package db

import (
	"context"
	"embed"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrationLockKey is the PostgreSQL advisory lock that serializes migrations
// across manager instances. The value is arbitrary but must never change.
const migrationLockKey = 7256

// DB wraps the pgx pool.
type DB struct{ pool *pgxpool.Pool }

// Open connects and pings.
func Open(ctx context.Context, url string) (*DB, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &DB{pool: pool}, nil
}

// OpenAsRole connects like Open, but every connection in the returned pool
// switches to postgresRole via SET ROLE immediately after connecting.
//
// This is how callers get a connection that is actually subject to row-level
// security. The login role encoded in url may be the schema owner or a
// superuser — both are exempt from RLS policies regardless of FORCE ROW
// LEVEL SECURITY — while the role it switches into (pulsix_app) is neither,
// so policies apply to it as designed. Migrations, which need DDL privileges
// pulsix_app does not have, must run through Open instead.
func OpenAsRole(ctx context.Context, url, postgresRole string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse pool config: %w", err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE "+(pgx.Identifier{postgresRole}).Sanitize())
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &DB{pool: pool}, nil
}

// Pool exposes the underlying pool. It deliberately ignores the org scope
// WithOrgScope binds: statements run through it see every organization's
// rows whenever the login role is subject to the bypass policy. It exists
// for migrations, test fixtures and the startup role check — production
// stores are built on *DB itself (gen.New(d)), whose Exec/Query/QueryRow/
// Begin honour the scope, never on this pool.
func (d *DB) Pool() *pgxpool.Pool { return d.pool }

// Close releases the pool.
func (d *DB) Close() { d.pool.Close() }

// Migrate applies all embedded goose migrations under an advisory lock.
func (d *DB) Migrate(ctx context.Context) error {
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	sqlDB := stdlib.OpenDBFromPool(d.pool)
	defer sqlDB.Close() //nolint:errcheck

	// The advisory lock is session-scoped, so it must be taken and released on
	// one pinned connection: a database/sql pool may hand the unlock to a
	// different session, which would leave the lock held. goose migrates on
	// other connections of the same pool, which is fine — the lock coordinates
	// manager instances, not individual statements.
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pin migration connection: %w", err)
	}
	defer conn.Close() //nolint:errcheck
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		// Runs even when ctx is already cancelled; closing conn would release
		// the lock anyway, this makes the release explicit.
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLockKey)
	}()
	if err := goose.UpContext(ctx, sqlDB, "migrations"); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// WithOrg runs fn in a transaction scoped to orgID via the RLS setting.
// fn's own error comes back unchanged (pgx.ErrNoRows included) after the
// transaction is rolled back; success commits.
func (d *DB) WithOrg(ctx context.Context, orgID uuid.UUID, fn func(pgx.Tx) error) error {
	tx, err := d.beginOrg(ctx, orgID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
