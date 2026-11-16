package chain_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/chain"
	"github.com/noixdev/pulsix/internal/jobs"
)

type dismissRuns struct {
	job     jobs.Job
	getErr  error
	summary *jobs.GroupSummary
}

func (r dismissRuns) Get(_ context.Context, _, id uuid.UUID) (jobs.Job, error) {
	if r.getErr != nil {
		return jobs.Job{}, r.getErr
	}
	return r.job, nil
}

func (r dismissRuns) GroupSummaries(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]jobs.GroupSummary, error) {
	out := map[uuid.UUID]jobs.GroupSummary{}
	if r.summary != nil {
		out[ids[0]] = *r.summary
	}
	return out, nil
}

type dismissCall struct {
	org, group uuid.UUID
	reason     string
	now        time.Time
}

type dismissStore struct{ calls []dismissCall }

func (s *dismissStore) Dismiss(_ context.Context, org, group uuid.UUID, reason string, now time.Time) (int, error) {
	s.calls = append(s.calls, dismissCall{org, group, reason, now})
	return 2, nil
}

func TestDismissFollowUpsOfAnActiveRun(t *testing.T) {
	org, job, group := newID(), newID(), newID()
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	store := &dismissStore{}
	d := chain.NewDismisser(dismissRuns{job: jobs.Job{ID: job, RunGroupID: group}, summary: &jobs.GroupSummary{GroupID: group, Status: jobs.StatusRunning}},
		store, func() time.Time { return at })
	n, err := d.DismissFollowUps(ctx, org, job, "Ada")
	if err != nil || n != 2 {
		t.Fatalf("dismissed = %d, %v", n, err)
	}
	if want := (dismissCall{org, group, "dismissed by Ada", at}); len(store.calls) != 1 || store.calls[0] != want {
		t.Fatalf("store calls = %+v, want %+v", store.calls, want)
	}
}

func TestDismissFollowUpsRefusesAFinishedRun(t *testing.T) {
	group := newID()
	for name, sum := range map[string]*jobs.GroupSummary{
		"finished": {GroupID: group, Status: jobs.StatusSucceeded},
		"no group": nil,
	} {
		store := &dismissStore{}
		d := chain.NewDismisser(dismissRuns{job: jobs.Job{RunGroupID: group}, summary: sum}, store, time.Now)
		if _, err := d.DismissFollowUps(ctx, newID(), newID(), "Ada"); !errors.Is(err, chain.ErrRunFinished) || len(store.calls) != 0 {
			t.Fatalf("%s: err = %v, store calls = %d", name, err, len(store.calls))
		}
	}
}

func TestDismissFollowUpsPassesTheJobLookupErrorOn(t *testing.T) {
	store := &dismissStore{}
	d := chain.NewDismisser(dismissRuns{getErr: jobs.ErrNotFound}, store, time.Now)
	if _, err := d.DismissFollowUps(ctx, newID(), newID(), "Ada"); !errors.Is(err, jobs.ErrNotFound) || len(store.calls) != 0 {
		t.Fatalf("err = %v, store calls = %d", err, len(store.calls))
	}
}
