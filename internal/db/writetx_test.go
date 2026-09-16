package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// zoneExists reports whether a zone row with id exists, read through the
// raw pool (bypass policy), i.e. what any other session sees committed.
func zoneExists(t *testing.T, d *db.DB, id uuid.UUID) bool {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(context.Background(), `SELECT count(*) FROM zones WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// insertZone inserts one zone through d with ctx — whatever binding ctx
// carries decides which transaction it lands in.
func insertZone(ctx context.Context, d *db.DB, orgID, id uuid.UUID, slug string) error {
	_, err := d.Exec(ctx, `INSERT INTO zones (id, org_id, slug, name) VALUES ($1, $2, $3, $3)`, id, orgID, slug)
	return err
}

// TestWithWriteTxCommitsOrRollsBackEverything: every statement any store
// runs through the bound ctx — Exec, QueryRow, Query, Begin (a savepoint)
// and WithOrg (a savepoint) — lands in the one transaction, which commits
// all of it or none of it.
func TestWithWriteTxCommitsOrRollsBackEverything(t *testing.T) {
	d := testdb.Open(t)
	orgA, _ := seedTwoOrgs(t, d)
	ctx := context.Background()
	boom := errors.New("sink failed")

	tests := []struct {
		name       string
		failWith   error
		wantCommit bool
	}{
		{name: "success commits every statement", wantCommit: true},
		{name: "callback error rolls back every statement", failWith: boom},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			viaExec, viaBegin, viaWithOrg := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
			var pids []int32
			err := d.WithWriteTx(ctx, orgA, func(ctx context.Context) error {
				var pid int32
				if err := d.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
					return err
				}
				pids = append(pids, pid)
				if err := insertZone(ctx, d, orgA, viaExec, "wtx-exec-"+string(rune('a'+i))); err != nil {
					return err
				}
				// A store that opens its own transaction gets a savepoint:
				// committing it must not end the outer transaction.
				tx, err := d.Begin(ctx)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO zones (id, org_id, slug, name) VALUES ($1, $2, $3, $3)`,
					viaBegin, orgA, "wtx-begin-"+string(rune('a'+i))); err != nil {
					return err
				}
				if err := tx.Commit(ctx); err != nil {
					return err
				}
				if err := d.WithOrg(ctx, orgA, func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, `INSERT INTO zones (id, org_id, slug, name) VALUES ($1, $2, $3, $3)`,
						viaWithOrg, orgA, "wtx-withorg-"+string(rune('a'+i)))
					return err
				}); err != nil {
					return err
				}
				if err := d.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
					return err
				}
				pids = append(pids, pid)
				// Nothing is visible to any other session before commit.
				if zoneExists(t, d, viaExec) || zoneExists(t, d, viaBegin) || zoneExists(t, d, viaWithOrg) {
					t.Fatal("a write inside WithWriteTx is visible before the transaction committed")
				}
				return tt.failWith
			})
			if !errors.Is(err, tt.failWith) {
				t.Fatalf("WithWriteTx = %v, want %v", err, tt.failWith)
			}
			if len(pids) != 2 || pids[0] != pids[1] {
				t.Fatalf("backend pids = %v, want one connection throughout", pids)
			}
			for name, id := range map[string]uuid.UUID{"Exec": viaExec, "Begin": viaBegin, "WithOrg": viaWithOrg} {
				if got := zoneExists(t, d, id); got != tt.wantCommit {
					t.Errorf("row written via %s exists = %v, want %v", name, got, tt.wantCommit)
				}
			}
		})
	}
}

// TestWithWriteTxBindsTheOrganization: the transaction is org-scoped, so a
// write into another organization is refused by row-level security and a
// read sees only the bound organization.
func TestWithWriteTxBindsTheOrganization(t *testing.T) {
	d := testdb.Open(t)
	orgA, orgB := seedTwoOrgs(t, d)
	ctx := context.Background()

	err := d.WithWriteTx(ctx, orgA, func(ctx context.Context) error {
		if seen := membershipOrgs(ctx, t, d); len(seen) != 1 || seen[0] != orgA {
			t.Fatalf("read inside WithWriteTx saw %v, want only %s", seen, orgA)
		}
		return insertZone(ctx, d, orgB, uuid.Must(uuid.NewV7()), "wtx-foreign")
	})
	if err == nil {
		t.Fatal("WithWriteTx admitted a write into another organization")
	}
}

// TestWithWriteTxCancelledContextCommitsNothing: a context cancelled
// mid-callback leaves nothing behind — the statements already executed
// roll back with the transaction.
func TestWithWriteTxCancelledContextCommitsNothing(t *testing.T) {
	d := testdb.Open(t)
	orgA, _ := seedTwoOrgs(t, d)
	ctx, cancel := context.WithCancel(context.Background())
	id := uuid.Must(uuid.NewV7())

	err := d.WithWriteTx(ctx, orgA, func(ctx context.Context) error {
		if err := insertZone(ctx, d, orgA, id, "wtx-cancel"); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if err == nil {
		t.Fatal("WithWriteTx reported success after its context was cancelled before commit")
	}
	if zoneExists(t, d, id) {
		t.Fatal("a cancelled WithWriteTx committed its write")
	}
}
