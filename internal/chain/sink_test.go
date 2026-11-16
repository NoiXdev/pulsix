package chain_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/chain"
	"github.com/noixdev/pulsix/internal/jobs"
)

func TestSinkSucceededMarksDue(t *testing.T) {
	e := newEnv(t)
	b := e.template("b")
	a := e.template("a", b)
	group, h1 := newID(), newID()
	e.plan(group, a, nil)
	e.job(group, "succeeded", []uuid.UUID{h1}, map[string][]uuid.UUID{"ok_hosts": {h1}})
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	sink := chain.NewSink(e.store, func() time.Time { return now }, nil)
	if err := sink.RunGroupFinished(e.scoped, jobs.RunGroupResult{OrgID: e.org, GroupID: group, Status: jobs.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	r := e.rows(group)[0]
	if r.State != chain.StateDue || !slices.Equal(r.HostIDs, []uuid.UUID{h1}) || r.NextAttemptAt == nil || !r.NextAttemptAt.Equal(now) {
		t.Fatalf("row = %+v", r)
	}
}

func TestSinkFailedAndCancelledSkip(t *testing.T) {
	for _, tc := range []struct {
		status jobs.Status
		reason string
	}{
		{jobs.StatusFailed, "the run failed"},
		{jobs.StatusError, "the run failed"},
		{jobs.StatusCancelled, "the run was cancelled"},
	} {
		e := newEnv(t)
		b := e.template("b")
		a := e.template("a", b)
		group := newID()
		e.plan(group, a, nil)
		sink := chain.NewSink(e.store, time.Now, nil)
		if err := sink.RunGroupFinished(e.scoped, jobs.RunGroupResult{OrgID: e.org, GroupID: group, Status: tc.status}); err != nil {
			t.Fatal(err)
		}
		r := e.rows(group)[0]
		if r.State != chain.StateSkipped || r.Reason != tc.reason || len(r.HostIDs) != 0 || r.NextAttemptAt != nil {
			t.Fatalf("%s: row = %+v", tc.status, r)
		}
	}
}

func TestSinkPlayedOnNoHost(t *testing.T) {
	e := newEnv(t)
	b := e.template("b")
	a := e.template("a", b)
	group, h1 := newID(), newID()
	e.plan(group, a, nil)
	e.job(group, "succeeded", []uuid.UUID{h1}, map[string][]uuid.UUID{})
	sink := chain.NewSink(e.store, time.Now, nil)
	if err := sink.RunGroupFinished(e.scoped, jobs.RunGroupResult{OrgID: e.org, GroupID: group, Status: jobs.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	if r := e.rows(group)[0]; r.State != chain.StateSkipped || r.Reason != "the run played on no host" {
		t.Fatalf("row = %+v", r)
	}
}

func TestSinkWithoutFollowUpsIsANoop(t *testing.T) {
	e := newEnv(t)
	sink := chain.NewSink(e.store, time.Now, nil)
	if err := sink.RunGroupFinished(e.scoped, jobs.RunGroupResult{OrgID: e.org, GroupID: newID(), Status: jobs.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
}

func TestSweepResolvesGroupWithoutJobs(t *testing.T) {
	e := newEnv(t)
	b := e.template("b")
	a := e.template("a", b)
	group := newID()
	e.plan(group, a, nil)
	later := time.Now().Add(2 * time.Minute)
	n, err := e.store.SweepStale(ctx, later, 50)
	if err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	if r := e.rows(group)[0]; r.State != chain.StateSkipped || r.Reason != "the run did not start" {
		t.Fatalf("row = %+v", r)
	}
}

func TestSweepResolvesFinishedGroupsLikeTheSink(t *testing.T) {
	e := newEnv(t)
	b := e.template("b")
	a := e.template("a", b)
	okGroup, failGroup, cancelGroup, h1 := newID(), newID(), newID(), newID()
	for _, g := range []uuid.UUID{okGroup, failGroup, cancelGroup} {
		e.plan(g, a, nil)
	}
	e.job(okGroup, "succeeded", []uuid.UUID{h1}, map[string][]uuid.UUID{"changed_hosts": {h1}})
	e.job(failGroup, "succeeded", nil, nil)
	e.job(failGroup, "error", nil, nil)
	e.job(cancelGroup, "succeeded", nil, nil)
	e.job(cancelGroup, "cancelled", nil, nil)
	if n, err := e.store.SweepStale(ctx, time.Now().Add(2*time.Minute), 50); err != nil || n != 3 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	if r := e.rows(okGroup)[0]; r.State != chain.StateDue || !slices.Equal(r.HostIDs, []uuid.UUID{h1}) {
		t.Fatalf("ok row = %+v", r)
	}
	if r := e.rows(failGroup)[0]; r.State != chain.StateSkipped || r.Reason != "the run failed" {
		t.Fatalf("failed row = %+v", r)
	}
	if r := e.rows(cancelGroup)[0]; r.State != chain.StateSkipped || r.Reason != "the run was cancelled" {
		t.Fatalf("cancelled row = %+v", r)
	}
}

func TestSweepIgnoresActiveGroupAndFreshRows(t *testing.T) {
	e := newEnv(t)
	b := e.template("b")
	a := e.template("a", b)
	active, fresh := newID(), newID()
	e.plan(active, a, nil)
	e.job(active, "running", nil, nil)
	e.plan(fresh, a, nil)
	// the active group's row is old enough; the fresh group has no job but
	// was planned less than a minute before the sweep.
	if _, err := e.d.Pool().Exec(ctx, `UPDATE run_follow_ups SET created_at = now() - interval '1 hour' WHERE parent_group_id = $1`, active); err != nil {
		t.Fatal(err)
	}
	n, err := e.store.SweepStale(ctx, time.Now(), 50)
	if err != nil || n != 0 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	for _, g := range []uuid.UUID{active, fresh} {
		if r := e.rows(g)[0]; r.State != chain.StatePending {
			t.Fatalf("row = %+v, want pending", r)
		}
	}
}

// TestSinkJoinsTheFinishingTransaction runs the sink inside a bound write
// transaction like the queue does: the resolution commits with it, and a
// rolled-back finish takes the resolution with it.
func TestSinkJoinsTheFinishingTransaction(t *testing.T) {
	e := newEnv(t)
	b := e.template("b")
	a := e.template("a", b)
	group, h1 := newID(), newID()
	e.plan(group, a, nil)
	e.job(group, "succeeded", []uuid.UUID{h1}, map[string][]uuid.UUID{"ok_hosts": {h1}})
	sink := chain.NewSink(e.store, time.Now, nil)
	res := jobs.RunGroupResult{OrgID: e.org, GroupID: group, Status: jobs.StatusSucceeded}

	errRollback := errSentinel("rollback")
	err := e.d.WithWriteTx(ctx, e.org, func(tctx context.Context) error {
		if err := sink.RunGroupFinished(tctx, res); err != nil {
			return err
		}
		return errRollback
	})
	if err != errRollback {
		t.Fatalf("err = %v", err)
	}
	if r := e.rows(group)[0]; r.State != chain.StatePending {
		t.Fatalf("row after rollback = %+v, want pending", r)
	}
	if err := e.d.WithWriteTx(ctx, e.org, func(tctx context.Context) error { return sink.RunGroupFinished(tctx, res) }); err != nil {
		t.Fatal(err)
	}
	if r := e.rows(group)[0]; r.State != chain.StateDue || !slices.Equal(r.HostIDs, []uuid.UUID{h1}) {
		t.Fatalf("row after commit = %+v", r)
	}
}

type errSentinel string

func (e errSentinel) Error() string { return string(e) }
