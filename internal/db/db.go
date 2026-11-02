// Package db owns the connection pool, migrations and the per-org transaction helper.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	neturl "net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// WithApplicationName returns connString with its application_name set to
// name, replacing one already there, so the sessions it opens show up under
// that name in pg_stat_activity. connString may be a postgres:// URL or a
// keyword/value string.
func WithApplicationName(connString, name string) (string, error) {
	if strings.HasPrefix(connString, "postgres://") || strings.HasPrefix(connString, "postgresql://") {
		u, err := neturl.Parse(connString)
		if err != nil {
			return "", fmt.Errorf("parse database url: %w", err)
		}
		q := u.Query()
		q.Set("application_name", name)
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	if _, err := pgconn.ParseConfig(connString); err != nil {
		return "", fmt.Errorf("parse database connection string: %w", err)
	}
	// In the keyword/value form the last occurrence of a keyword wins.
	quoted := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(name)
	return connString + " application_name='" + quoted + "'", nil
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
	return d.withMigrationLock(ctx, func(sqlDB *sql.DB, _ *sql.Conn) error {
		if err := goose.UpContext(ctx, sqlDB, "migrations"); err != nil {
			return fmt.Errorf("apply migrations: %w", err)
		}
		return nil
	})
}

// MigrateTo applies the embedded migrations up to and including version,
// under the same advisory lock as Migrate. It never migrates down: a
// database already past version is left as it is.
func (d *DB) MigrateTo(ctx context.Context, version int64) error {
	return d.withMigrationLock(ctx, func(sqlDB *sql.DB, _ *sql.Conn) error {
		if err := goose.UpToContext(ctx, sqlDB, "migrations", version); err != nil {
			return fmt.Errorf("apply migrations up to %d: %w", version, err)
		}
		return nil
	})
}

// SchemaVersion returns the goose version the database is at, 0 when no
// migration has ever run. It reads under the migration lock, so it never
// sees a migration half applied by another manager.
func (d *DB) SchemaVersion(ctx context.Context) (int64, error) {
	var version int64
	err := d.withMigrationLock(ctx, func(_ *sql.DB, conn *sql.Conn) error {
		var err error
		version, err = ReadSchemaVersion(ctx, sqlRowQuerier{conn})
		return err
	})
	return version, err
}

// RowQuerier is the one method ReadSchemaVersion needs; pgx.Tx, *pgx.Conn
// and *pgxpool.Pool all have it.
type RowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ReadSchemaVersion returns the goose version recorded in q's database, 0
// when goose's table does not exist. A version counts when its latest row
// says applied, so a migration rolled back with goose down does not count.
// It takes no lock: callers inside a snapshot (the backup dump) read the
// version that belongs to the rows they read.
func ReadSchemaVersion(ctx context.Context, q RowQuerier) (int64, error) {
	var exists bool
	if err := q.QueryRow(ctx, `SELECT to_regclass('public.goose_db_version') IS NOT NULL`).Scan(&exists); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if !exists {
		return 0, nil
	}
	var version int64
	if err := q.QueryRow(ctx, `
		SELECT COALESCE(max(version_id), 0) FROM (
			SELECT DISTINCT ON (version_id) version_id, is_applied
			FROM public.goose_db_version
			ORDER BY version_id, id DESC
		) latest WHERE is_applied`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

// sqlRowQuerier adapts a pinned database/sql connection to RowQuerier.
type sqlRowQuerier struct{ conn *sql.Conn }

func (s sqlRowQuerier) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return s.conn.QueryRowContext(ctx, query, args...)
}

// withMigrationLock runs fn while holding the migration advisory lock.
//
// The advisory lock is session-scoped, so it must be taken and released on
// one pinned connection: a database/sql pool may hand the unlock to a
// different session, which would leave the lock held. goose migrates on
// other connections of the same pool, which is fine — the lock coordinates
// manager instances, not individual statements.
func (d *DB) withMigrationLock(ctx context.Context, fn func(sqlDB *sql.DB, conn *sql.Conn) error) error {
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	sqlDB := stdlib.OpenDBFromPool(d.pool)
	defer sqlDB.Close() //nolint:errcheck

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
	return fn(sqlDB, conn)
}

// WithOrg runs fn in a transaction scoped to orgID via the RLS setting.
// fn's own error comes back unchanged (pgx.ErrNoRows included) after the
// transaction is rolled back; success commits.
//
// Inside WithWriteTx (a write transaction bound to ctx) fn runs on a
// savepoint of that transaction instead, so the outer transaction decides
// what persists.
func (d *DB) WithOrg(ctx context.Context, orgID uuid.UUID, fn func(pgx.Tx) error) error {
	if b, ok := txOf(ctx); ok && b.write {
		sp, err := b.tx.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin savepoint: %w", err)
		}
		defer sp.Rollback(ctx) //nolint:errcheck // no-op once released
		if err := fn(sp); err != nil {
			return err
		}
		return sp.Commit(ctx)
	}
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
