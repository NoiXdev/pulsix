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

// boundTxKey is the context key WithReadTx and WithWriteTx bind their
// transaction under.
type boundTxKey struct{}

// boundTx is one transaction bound to a context; write distinguishes
// WithWriteTx's read-write transaction (where Begin and WithOrg nest as
// savepoints) from WithReadTx's shared read-only one.
type boundTx struct {
	tx    pgx.Tx
	write bool
}

// WithReadTx runs fn against a context bound to one read-only, repeatable
// read transaction scoped to orgID (backlog 2.16): every statement any
// store built on *DB executes through the ctx fn receives — regardless of
// which package built that store — runs against this same transaction
// instead of each opening one of its own, so a caller that composes reads
// across several domain stores (the inventory renderer's groups,
// memberships, hosts, SSH settings, jump hosts and credentials) sees one
// consistent snapshot instead of one per statement. This works because
// every store in this codebase is built on the same *DB (gen.New(d)) and
// never touches the pool directly (see Pool's doc comment) — Exec, Query
// and QueryRow all check for this binding first, before the ordinary
// per-statement org-scoped transaction Exec's own doc comment describes.
//
// The transaction is REPEATABLE READ and READ ONLY: every read-path store
// call the renderer makes is a SELECT, and READ ONLY both documents that
// and lets Postgres reject an accidental write instead of silently
// executing it against a snapshot no later reader will ever see. fn's
// error rolls the transaction back and propagates unchanged; success
// commits (a plain COMMIT, matching WithOrg, even though nothing was
// written).
func (d *DB) WithReadTx(ctx context.Context, orgID uuid.UUID, fn func(ctx context.Context) error) error {
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin read transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed
	if _, err := tx.Exec(ctx, `SELECT set_config('pulsix.org_id', $1, true)`, orgID.String()); err != nil {
		return fmt.Errorf("bind read transaction to organization: %w", err)
	}
	if err := fn(context.WithValue(ctx, boundTxKey{}, boundTx{tx: tx})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// WithWriteTx runs fn against a context bound to one read-write
// transaction scoped to orgID: every statement any store built on *DB
// executes through the ctx fn receives runs inside this one transaction,
// so a caller that composes writes across several domain stores (the job
// queue's finish: the terminal status, the connection-test row, the
// repository's sync state, the rotated credential and the payload wipe)
// commits all of them or none of them. A store that opens its own
// transaction inside — Begin, or WithOrg — gets a savepoint nested in the
// bound transaction, so its own Commit never ends the outer one early.
//
// fn's error rolls everything back and propagates unchanged; a context
// cancelled before the commit rolls everything back too (the commit fails
// on the cancelled context, and the deferred rollback releases the
// connection). Success commits.
func (d *DB) WithWriteTx(ctx context.Context, orgID uuid.UUID, fn func(ctx context.Context) error) error {
	tx, err := d.beginOrg(ctx, orgID)
	if err != nil {
		return err
	}
	// The rollback must run even when ctx is already cancelled, so the
	// connection goes back to the pool clean.
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	if err := fn(context.WithValue(ctx, boundTxKey{}, boundTx{tx: tx, write: true})); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("commit write transaction: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit write transaction: %w", err)
	}
	return nil
}

// WithInstanceTx runs fn inside one read-write transaction that is NOT
// bound to an organization, for the instance-level tables that have no
// org_id at all: users, sessions, totp_secrets, recovery_codes,
// webauthn_ceremonies and the settings store. Every store built on this
// *DB picks the transaction up from the context exactly as it does for
// WithWriteTx, so a caller can compose writes across two of them — the
// second-factor confirmation writes the secret, its recovery codes and
// the session's upgrade, and either all three land or none do.
//
// It deliberately does not set `pulsix.org_id`: leaving it unset is what
// keeps the org_isolation_bypass policy active, and an organization-owned
// table has no business in this transaction in the first place. Use
// WithWriteTx for anything that touches one.
func (d *DB) WithInstanceTx(ctx context.Context, fn func(ctx context.Context) error) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin instance transaction: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	if err := fn(context.WithValue(ctx, boundTxKey{}, boundTx{tx: tx, write: true})); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("commit instance transaction: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit instance transaction: %w", err)
	}
	return nil
}

// txOf reports the transaction WithReadTx or WithWriteTx bound to ctx, if
// any.
func txOf(ctx context.Context) (boundTx, bool) {
	b, ok := ctx.Value(boundTxKey{}).(boundTx)
	return b, ok
}

// Exec runs sql on the pool, or — when ctx carries an org scope — inside a
// transaction bound to that organization. Together with Query, QueryRow and
// Begin it makes *DB a drop-in gen.DBTX: stores build their sqlc queries
// with gen.New(d) and never see the pool itself.
func (d *DB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if b, ok := txOf(ctx); ok {
		return b.tx.Exec(ctx, sql, args...)
	}
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
	if b, ok := txOf(ctx); ok {
		return b.tx.QueryRow(ctx, sql, args...)
	}
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
	if b, ok := txOf(ctx); ok {
		return b.tx.Query(ctx, sql, args...)
	}
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
	if b, ok := txOf(ctx); ok {
		if b.write {
			// Inside WithWriteTx a store's own transaction is a savepoint:
			// its Commit releases the savepoint, its Rollback undoes only
			// its own statements, and the outer transaction decides what
			// persists.
			return b.tx.Begin(ctx)
		}
		// No production caller does this today (the renderer's read path
		// never opens its own nested transaction) — TestWithReadTxBeginReturns
		// TheSharedTransaction (scope_test.go) is what exercises this branch.
		// A future caller gets the SAME shared transaction, not a new one —
		// committing or rolling it back ends WithReadTx's transaction early
		// for every other store still reading through it.
		return b.tx, nil
	}
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
