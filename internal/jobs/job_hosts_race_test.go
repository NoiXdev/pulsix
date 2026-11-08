package jobs_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
)

// TestCreateJobSkipsAHostDeletedMeanwhile (backlog 7.90): a host whose DELETE
// is still open when the run is created is skipped like a host deleted just
// before, instead of failing the link insert on job_hosts' foreign key. The
// test is deterministic: it holds the DELETE open, waits until the CreateJob
// statement is blocked on the lock of the host's row, then commits the delete.
func TestCreateJobSkipsAHostDeletedMeanwhile(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, gen.New(d.Pool()), "jobs-link-race-org")
	store := jobs.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	zone := seedZone(t, d, org)
	hostA, hostB := seedHost(t, d, org, zone, "a"), seedHost(t, d, org, zone, "b")

	// The delete stays open until commit or the cleanup rolls it back. The
	// superuser also does the polling: pulsix_app cannot see other sessions'
	// wait events.
	owner := superuser(t)
	tx, err := owner.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var blockerPID int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	var txOnce sync.Once
	release := func(commit bool) error {
		var err error
		txOnce.Do(func() {
			bg := context.Background()
			if commit {
				err = tx.Commit(bg)
			} else {
				err = tx.Rollback(bg)
			}
		})
		return err
	}
	// Registered right after Begin, so a failing DELETE cannot leave the
	// transaction open and hang pool.Close.
	t.Cleanup(func() { _ = release(false) })
	if _, err := tx.Exec(ctx, `DELETE FROM hosts WHERE id = $1`, hostA); err != nil {
		t.Fatal(err)
	}

	j := newJob(org, zone, jobs.KindRun, jobs.StatusQueued)
	j.HostIDs = []uuid.UUID{hostA, hostB}
	var createErr error
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		createErr = store.CreateJob(scoped, j, nil)
	}()
	// Whatever happens, free the lock and let the statement end before the
	// pools close, so a failing test cannot hang the package.
	t.Cleanup(func() {
		_ = release(false)
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
		}
	})

	// The poll matches the blocker, not query text: a backend waiting on a lock
	// held by the open delete's own backend, which is exactly CreateJob's
	// statement on the host's row.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var blocked bool
		if err := owner.Pool().QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			  WHERE datname = current_database() AND wait_event_type = 'Lock'
			    AND $1 = ANY(pg_blocking_pids(pid)))`, blockerPID,
		).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case <-finished:
			t.Fatalf("CreateJob finished without waiting for the open delete: %v", createErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("CreateJob never blocked on the host's row lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := release(true); err != nil {
		t.Fatalf("commit the delete: %v", err)
	}

	select {
	case <-finished:
		if createErr != nil {
			t.Fatalf("CreateJob after the host was deleted meanwhile: %v", createErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CreateJob did not return after the delete committed")
	}

	links := jobHostLinks(t, d.Pool(), org)
	if len(links) != 1 || links[0].job != j.ID || links[0].host != hostB {
		t.Fatalf("links = %v, want only (%s, %s)", links, j.ID, hostB)
	}
	got, err := store.GetJob(scoped, org, j.ID)
	if err != nil || len(got.HostIDs) != 2 || got.HostIDs[0] != hostA || got.HostIDs[1] != hostB {
		t.Fatalf("run = %+v, %v; want host_ids kept as given %v", got, err, j.HostIDs)
	}
}
