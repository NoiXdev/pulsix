package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// WithInstanceTx is the transaction for the tables that have no
// organization at all — users, sessions, totp_secrets, recovery_codes —
// where the second-factor confirmation composes writes across two stores
// and needs them to land together (backlog 1.26).

// userExists reads through the raw pool: what another session would see
// committed, not what the transaction under test can see itself.
func userExists(t *testing.T, d *db.DB, id uuid.UUID) bool {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(context.Background(), `SELECT count(*) FROM users WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func insertUser(ctx context.Context, d *db.DB, id uuid.UUID, email string) error {
	_, err := d.Exec(ctx, `INSERT INTO users (id, email, display_name) VALUES ($1, $2, $3)`, id, email, email)
	return err
}

// Every statement run through the bound context lands in the one
// transaction: either all of them commit or none of them do.
func TestWithInstanceTxCommitsOrRollsBackEverything(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	boom := errors.New("second write failed")

	for _, tt := range []struct {
		name       string
		failWith   error
		wantCommit bool
	}{
		{name: "success commits both writes", wantCommit: true},
		{name: "a failure rolls both back", failWith: boom},
	} {
		t.Run(tt.name, func(t *testing.T) {
			first, second := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
			err := d.WithInstanceTx(ctx, func(ctx context.Context) error {
				if err := insertUser(ctx, d, first, first.String()+"@example.test"); err != nil {
					return err
				}
				if err := insertUser(ctx, d, second, second.String()+"@example.test"); err != nil {
					return err
				}
				return tt.failWith
			})
			if !errors.Is(err, tt.failWith) {
				t.Fatalf("WithInstanceTx err = %v, want %v", err, tt.failWith)
			}
			for _, id := range []uuid.UUID{first, second} {
				if got := userExists(t, d, id); got != tt.wantCommit {
					t.Errorf("user %s committed = %v, want %v", id, got, tt.wantCommit)
				}
			}
		})
	}
}

// The transaction is not bound to an organization: the org-scoped
// policies' bypass stays active, which is what lets these instance-level
// tables be written at all without inventing an organization for them.
func TestWithInstanceTxCarriesNoOrganization(t *testing.T) {
	d := testdb.Open(t)
	var bound string
	if err := d.WithInstanceTx(context.Background(), func(ctx context.Context) error {
		// COALESCE: an unset setting reads as NULL, and "unset" is
		// precisely what this asserts.
		return d.QueryRow(ctx, `SELECT COALESCE(current_setting('pulsix.org_id', true), '')`).Scan(&bound)
	}); err != nil {
		t.Fatal(err)
	}
	if bound != "" {
		t.Fatalf("pulsix.org_id inside an instance transaction = %q, want it unset", bound)
	}
}

// A context cancelled before the commit writes nothing: the deferred
// rollback runs on a context of its own, so the connection still goes
// back to the pool clean.
func TestWithInstanceTxCancelledContextCommitsNothing(t *testing.T) {
	d := testdb.Open(t)
	ctx, cancel := context.WithCancel(context.Background())
	id := uuid.Must(uuid.NewV7())

	err := d.WithInstanceTx(ctx, func(ctx context.Context) error {
		if err := insertUser(ctx, d, id, id.String()+"@example.test"); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if err == nil {
		t.Fatal("a cancelled context must not report a successful commit")
	}
	if userExists(t, d, id) {
		t.Error("the write committed although the context was cancelled")
	}
	// The pool is still usable afterwards.
	if _, err := d.Pool().Exec(context.Background(), `SELECT 1`); err != nil {
		t.Fatalf("pool after the rollback: %v", err)
	}
}
