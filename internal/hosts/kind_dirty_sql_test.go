package hosts_test

import (
	"context"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/rules"
)

// TestKindChangeIsMarkedAfterCommitWithoutBlockingTheEvaluator (backlog
// 3.57, fix round 1): the dirty mark of a kind change runs after the host
// update committed, never inside its transaction.
//
// The evaluator holds a host's cursor row for the whole evaluation and,
// inside it, takes FOR KEY SHARE on the hosts row (the foreign keys of the
// states and alerts it inserts). A PATCH that renames the host updates a
// key column (UNIQUE (org_id, name)), which conflicts with KEY SHARE; had
// the mark run inside the update's transaction, that transaction would
// hold the renamed row and wait for the cursor while the evaluator holds
// the cursor and waits for the row — a deadlock. Here the "evaluator"
// holds the cursor throughout: the rename and kind change must commit
// anyway, the evaluator's KEY SHARE must be granted at once, and once it
// commits the mark lands and leaves the host due.
func TestKindChangeIsMarkedAfterCommitWithoutBlockingTheEvaluator(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := hosts.NewSQLStore(d)
	marker := rules.NewDirtyMarker(d)
	svc := hosts.NewService(store, time.Now, hosts.WithEvalDirty(marker))
	org := seedOrg(t, q, "hostkind-order-org")
	zone := seedZone(t, store, org, "fra")
	h, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "laptop", ZoneID: zone.ID})
	if err != nil {
		t.Fatal(err)
	}
	// A clean cursor: evaluated after its last mark.
	if err := marker.MarkHostDirty(ctx, org, h.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(ctx, `UPDATE check_eval_cursor SET evaluated_at = dirty_at + interval '1 microsecond' WHERE host_id = $1`, h.ID); err != nil {
		t.Fatal(err)
	}

	// The evaluator claims the host.
	evalTx, err := d.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = evalTx.Rollback(context.Background()) }()
	if _, err := evalTx.Exec(ctx, `SELECT host_id FROM check_eval_cursor WHERE host_id = $1 FOR UPDATE`, h.ID); err != nil {
		t.Fatal(err)
	}

	// The PATCH: rename and kind change in one write.
	done := make(chan error, 1)
	go func() {
		_, err := svc.UpdateHost(ctx, org, h.ID, h.Revision, hosts.HostInput{Name: "laptop-renamed", ZoneID: zone.ID, Kind: hosts.KindClient})
		done <- err
	}()

	// It commits while the evaluator still holds the cursor.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var name, kind string
		if err := d.Pool().QueryRow(ctx, `SELECT name, kind FROM hosts WHERE id = $1`, h.ID).Scan(&name, &kind); err != nil {
			t.Fatal(err)
		}
		if name == "laptop-renamed" && kind == "client" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the rename and kind change did not commit while the evaluator held the cursor: the update waits for the cursor inside its transaction")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The evaluator's foreign-key lock on the host row is granted at once.
	if _, err := evalTx.Exec(ctx, `SET LOCAL lock_timeout = '5s'`); err != nil {
		t.Fatal(err)
	}
	if _, err := evalTx.Exec(ctx, `SELECT 1 FROM hosts WHERE id = $1 FOR KEY SHARE`, h.ID); err != nil {
		t.Fatalf("evaluator KEY SHARE on the host = %v, want granted", err)
	}
	// It finishes as AdvanceEvalCursor does: evaluated_at is its own start.
	if _, err := evalTx.Exec(ctx, `UPDATE check_eval_cursor SET evaluated_at = now() WHERE host_id = $1`, h.ID); err != nil {
		t.Fatal(err)
	}
	if err := evalTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("UpdateHost = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("UpdateHost did not return after the evaluator released the cursor")
	}
	var due bool
	if err := d.Pool().QueryRow(ctx, `SELECT dirty_at > evaluated_at FROM check_eval_cursor WHERE host_id = $1`, h.ID).Scan(&due); err != nil {
		t.Fatal(err)
	}
	if !due {
		t.Fatal("after the kind change the host is not due, want dirty_at later than the evaluation that read the old kind")
	}
}
