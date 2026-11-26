package runrequests_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/runrequests"
)

// waitForLockWait waits until a statement whose text contains name waits
// for a lock in the test database, or done delivers first (the statement
// did not wait). It looks through a superuser connection of its own: the
// application role cannot read other sessions' activity.
func waitForLockWait(t *testing.T, name string, done <-chan error) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%' || $1 || '%'`, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("%s did not wait for the lock (err %v)", name, err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("%s never waited for a lock", name)
}

// TestConfirmWithdrawnReadsTheRequestUnderItsLock is backlog 2.127 (5) at
// the store: ConfirmWithdrawn, inside a transaction, holds an approval of
// the request back until that transaction ends; and when an approval's
// write came first, it waits for it and reads the request approved.
func TestConfirmWithdrawnReadsTheRequestUnderItsLock(t *testing.T) {
	d := testdb.Open(t)
	f := newSQLFixture(t, d, "runreq-confirm-withdrawn")
	store := runrequests.NewSQLStore(d)
	svc := runrequests.NewService(store, nil, nil, nil, nil, nil, time.Now, uuid.New)
	now := time.Now().UTC().Truncate(time.Microsecond)
	r := runrequests.Request{
		ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Kind: runrequests.KindRun, TemplateID: f.template,
		Reason: "x", RequestedByUser: f.user, Status: runrequests.StatusPending, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}
	if err := store.Insert(f.ctx, r); err != nil {
		t.Fatal(err)
	}
	approve := func(ctx context.Context) error {
		ok, err := store.Decide(ctx, f.org, r.ID, runrequests.StatusApproved, f.user, now, "fp", jobs.ApprovedRun{})
		if err == nil && !ok {
			t.Error("the approval found the request decided")
		}
		return err
	}
	// A first approval whose enqueue failed: the run group stays.
	if err := approve(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.RevertToPending(f.ctx, f.org, r.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.ConfirmWithdrawn(f.ctx, f.org, uuid.Must(uuid.NewV7())); err != nil || !got {
		t.Fatalf("ConfirmWithdrawn of a group no request owns = %v, %v; want true", got, err)
	}

	// The confirmation locks first: the approval waits for its transaction.
	approved := make(chan error, 1)
	err := store.InTx(f.ctx, f.org, func(ctx context.Context) error {
		withdrawn, err := svc.ConfirmWithdrawn(ctx, f.org, r.ID)
		if err != nil || !withdrawn {
			t.Fatalf("ConfirmWithdrawn of the pending request = %v, %v; want true", withdrawn, err)
		}
		go func() { approved <- approve(f.ctx) }()
		waitForLockWait(t, "DecideRunRequest", approved)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-approved; err != nil {
		t.Fatal(err)
	}
	if withdrawn, err := svc.ConfirmWithdrawn(f.ctx, f.org, r.ID); err != nil || withdrawn {
		t.Fatalf("ConfirmWithdrawn of the approved request = %v, %v; want false", withdrawn, err)
	}

	// The approval writes first: the confirmation waits and reads it.
	if err := store.RevertToPending(f.ctx, f.org, r.ID); err != nil {
		t.Fatal(err)
	}
	type answer struct {
		withdrawn bool
		err       error
	}
	confirmed := make(chan answer, 1)
	waited := make(chan error, 1)
	err = store.InTx(f.ctx, f.org, func(ctx context.Context) error {
		if err := approve(ctx); err != nil {
			return err
		}
		go func() {
			var a answer
			a.err = store.InTx(f.ctx, f.org, func(ctx context.Context) error {
				var err error
				a.withdrawn, err = svc.ConfirmWithdrawn(ctx, f.org, r.ID)
				return err
			})
			confirmed <- a
			waited <- a.err
		}()
		waitForLockWait(t, "LockRunRequestStatusByRunGroup", waited)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if a := <-confirmed; a.err != nil || a.withdrawn {
		t.Fatalf("ConfirmWithdrawn that waited for an approval = %v, %v; want false", a.withdrawn, a.err)
	}
}
