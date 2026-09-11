package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// orgScopeKey is the context key WithOrgScope stores the bound organization
// under.
type orgScopeKey struct{}

// WithOrgScope returns a child of ctx that binds every statement run through
// a *DB to orgID's row-level-security scope: each one executes inside a
// transaction that first sets pulsix.org_id to orgID (SET LOCAL semantics,
// see WithOrg), so the org_isolation policy on every org-owned table admits
// only orgID's rows and org_isolation_bypass never applies — independently
// of whether the statement's own text filters by org_id at all.
//
// The HTTP layer binds this once per org-scoped request (internal/api's
// orgScopeMiddleware), before any handler runs, so every store operation a
// handler triggers is covered without the store having to remember
// anything. uuid.Nil is a valid binding and means "no organization": every
// org-owned table then looks empty, which is what an org-scoped request for
// an organization that does not exist should see.
//
// A context without a binding is instance-level — logging in by e-mail,
// listing a user's organizations, first-run setup, reconciling default
// zones — and runs directly on the pool, where the bypass policy applies by
// design.
func WithOrgScope(ctx context.Context, orgID uuid.UUID) context.Context {
	return context.WithValue(ctx, orgScopeKey{}, orgID)
}

// OrgScope reports the organization ctx is bound to by WithOrgScope, if any.
func OrgScope(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(orgScopeKey{}).(uuid.UUID)
	return id, ok
}

// Exec runs sql on the pool, or — when ctx carries an org scope — inside a
// transaction bound to that organization. Together with Query, QueryRow and
// Begin it makes *DB a drop-in gen.DBTX: stores build their sqlc queries
// with gen.New(d) and never see the pool itself.
func (d *DB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	orgID, ok := OrgScope(ctx)
	if !ok {
		return d.pool.Exec(ctx, sql, args...)
	}
	var tag pgconn.CommandTag
	err := d.WithOrg(ctx, orgID, func(tx pgx.Tx) error {
		var err error
		tag, err = tx.Exec(ctx, sql, args...)
		return err
	})
	return tag, err
}

// QueryRow is Exec's single-row counterpart. When ctx carries an org scope,
// the statement only runs once the caller calls Scan, inside its own
// org-bound transaction, and Scan's error (pgx.ErrNoRows included) comes
// back unchanged.
func (d *DB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	orgID, ok := OrgScope(ctx)
	if !ok {
		return d.pool.QueryRow(ctx, sql, args...)
	}
	return scopedRow{d: d, ctx: ctx, orgID: orgID, sql: sql, args: args}
}

// Query is Exec's multi-row counterpart. When ctx carries an org scope, the
// returned rows hold an org-bound transaction open until they are closed —
// by the caller, or implicitly once Next reports the last row — which then
// commits it (or rolls it back if reading the rows failed).
func (d *DB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	orgID, ok := OrgScope(ctx)
	if !ok {
		return d.pool.Query(ctx, sql, args...)
	}
	tx, err := d.beginOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return &scopedRows{Rows: rows, tx: tx, ctx: ctx}, nil
}

// Begin starts a transaction for a store that needs several statements to
// commit together. When ctx carries an org scope, the transaction is bound
// to that organization before it is handed back.
func (d *DB) Begin(ctx context.Context) (pgx.Tx, error) {
	orgID, ok := OrgScope(ctx)
	if !ok {
		return d.pool.Begin(ctx)
	}
	return d.beginOrg(ctx, orgID)
}

// beginOrg starts a transaction and binds it to orgID. The setting is local
// to the transaction (set_config's third argument), so it can never leak
// onto the pooled connection once the transaction ends.
func (d *DB) beginOrg(ctx context.Context, orgID uuid.UUID) (pgx.Tx, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin org-scoped transaction: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('pulsix.org_id', $1, true)`, orgID.String()); err != nil {
		_ = tx.Rollback(ctx)
		return nil, fmt.Errorf("bind transaction to organization: %w", err)
	}
	return tx, nil
}

// scopedRow defers QueryRow's statement to Scan, so the whole statement —
// including reading its one row — runs inside one org-bound transaction.
type scopedRow struct {
	d     *DB
	ctx   context.Context
	orgID uuid.UUID
	sql   string
	args  []any
}

func (r scopedRow) Scan(dest ...any) error {
	return r.d.WithOrg(r.ctx, r.orgID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.ctx, r.sql, r.args...).Scan(dest...)
	})
}

// scopedRows wraps an org-bound transaction's rows and ends the transaction
// exactly once, when the rows close: commit after a clean read, rollback
// after a failed one. A commit failure is reported through Err, which every
// caller already checks after its Next loop.
type scopedRows struct {
	pgx.Rows
	tx     pgx.Tx
	ctx    context.Context
	closed bool
	err    error
}

func (r *scopedRows) Next() bool {
	if r.Rows.Next() {
		return true
	}
	r.Close()
	return false
}

func (r *scopedRows) Close() {
	if r.closed {
		return
	}
	r.closed = true
	r.Rows.Close()
	if r.Rows.Err() != nil {
		_ = r.tx.Rollback(r.ctx)
		return
	}
	if err := r.tx.Commit(r.ctx); err != nil {
		r.err = fmt.Errorf("commit org-scoped query: %w", err)
	}
}

// Err reports the read error, if any, or else the commit error: the two
// are exclusive, since a failed read rolls back instead of committing.
func (r *scopedRows) Err() error {
	if r.err != nil {
		return r.err
	}
	return r.Rows.Err()
}
