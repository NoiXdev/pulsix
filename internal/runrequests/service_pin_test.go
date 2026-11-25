package runrequests_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/runrequests"
)

const (
	commitX = "0123456789abcdef0123456789abcdef01234567"
	commitY = "fedcba9876543210fedcba9876543210fedcba98"
)

// TestApproveRefusesWhileTheCommitIsBlocked (spec 2026-11-25 A, D3): while
// the picture says why its commit cannot be pinned, an approval is
// refused with every reason, whatever fingerprint it sends; nothing is
// decided or enqueued.
func TestApproveRefusesWhileTheCommitIsBlocked(t *testing.T) {
	for name, tc := range map[string]struct {
		blocked []string
		want    []string
	}{
		"sync running": {[]string{runrequests.BlockedSyncRunning}, []string{"a sync of the repository is queued or running"}},
		"never synced": {[]string{repos.CommitNeverSynced}, []string{"never been synced"}},
		"not reported": {[]string{repos.CommitNotReported}, []string{"did not report its commit"}},
		"unrecorded":   {[]string{repos.CommitSyncUnrecorded}, []string{"did not record what it fetched"}},
		"repo changed and sync running": {
			[]string{repos.CommitRepoChanged, runrequests.BlockedSyncRunning},
			[]string{"URL, branch or subpath changed after its last sync", "queued or running"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWiring()
			ctx := context.Background()
			w.pics.p.Commit = &runrequests.PictureCommit{SHA: commitX, Blocked: tc.blocked}
			r := w.create(t)
			_, _, fp, err := w.svc.Get(ctx, w.org, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = w.svc.Approve(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil)
			if !errors.Is(err, runrequests.ErrCommitBlocked) {
				t.Fatalf("Approve = %v, want ErrCommitBlocked", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("message %q does not say %q", err, want)
				}
			}
			// A stale fingerprint is answered with the block too: it says
			// what to do.
			if _, err := w.svc.Approve(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()), "stale", nil); !errors.Is(err, runrequests.ErrCommitBlocked) {
				t.Fatalf("Approve with a stale fingerprint = %v, want ErrCommitBlocked", err)
			}
			if got := w.store.rows[r.ID]; got.Status != runrequests.StatusPending || len(w.enq.runs) != 0 {
				t.Fatalf("after a refused approval: %s, %d runs", got.Status, len(w.enq.runs))
			}
		})
	}
}

// TestApprovePinsTheCommitItShowed: the approval stores the picture's
// commit on the request and hands it to the enqueue; a run without a
// repository has none to pin.
func TestApprovePinsTheCommitItShowed(t *testing.T) {
	ctx := context.Background()
	t.Run("run", func(t *testing.T) {
		w := newWiring()
		w.pics.p.Commit = &runrequests.PictureCommit{SHA: commitX}
		r := w.create(t)
		_, _, fp, _ := w.svc.Get(ctx, w.org, r.ID)
		got, err := w.svc.Approve(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.Request.PinnedCommit != commitX || w.store.rows[r.ID].PinnedCommit != commitX || w.enq.request.PinnedCommit != commitX {
			t.Fatalf("pins: approved %q, stored %q, enqueued %q; want %s", got.Request.PinnedCommit, w.store.rows[r.ID].PinnedCommit, w.enq.request.PinnedCommit, commitX)
		}
	})
	t.Run("retry", func(t *testing.T) {
		w := newWiring()
		w.pics.p.Commit = &runrequests.PictureCommit{SHA: commitX}
		job := uuid.Must(uuid.NewV7())
		r, err := w.svc.Create(ctx, w.org, runrequests.CreateInput{Kind: runrequests.KindRetryFailed, JobID: &job, Reason: "retry", RequestedByUser: uuid.Must(uuid.NewV7())})
		if err != nil {
			t.Fatal(err)
		}
		_, _, fp, _ := w.svc.Get(ctx, w.org, r.ID)
		if _, err := w.svc.Approve(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); err != nil {
			t.Fatal(err)
		}
		if w.enq.retryPin != commitX || w.store.rows[r.ID].PinnedCommit != commitX {
			t.Fatalf("retry pinned to %q, stored %q; want %s", w.enq.retryPin, w.store.rows[r.ID].PinnedCommit, commitX)
		}
	})
	t.Run("editor playbook", func(t *testing.T) {
		w := newWiring()
		r := w.create(t)
		_, _, fp, _ := w.svc.Get(ctx, w.org, r.ID)
		if _, err := w.svc.Approve(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); err != nil {
			t.Fatal(err)
		}
		if w.store.rows[r.ID].PinnedCommit != "" || w.enq.request.PinnedCommit != "" {
			t.Fatalf("an unpinnable run was pinned to %q", w.store.rows[r.ID].PinnedCommit)
		}
	})
}

// TestCheckRunClaimComparesTheStoredCommit: a job of an approved run is
// claimed only while it is pinned to the commit the approval stored.
func TestCheckRunClaimComparesTheStoredCommit(t *testing.T) {
	ctx := context.Background()
	w := newWiring()
	w.pics.p.Commit = &runrequests.PictureCommit{SHA: commitX}
	r := w.create(t)
	_, _, fp, _ := w.svc.Get(ctx, w.org, r.ID)
	if _, err := w.svc.Approve(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.CheckRunClaim(ctx, w.org, r.ID, commitX); err != nil {
		t.Fatalf("the pinned job's claim = %v", err)
	}
	for _, other := range []string{"", commitY} {
		err := w.svc.CheckRunClaim(ctx, w.org, r.ID, other)
		if !errors.Is(err, runrequests.ErrChangedAfterApproval) || errors.Is(err, jobs.ErrApprovalWithdrawn) {
			t.Fatalf("a job pinned to %q = %v, want ErrChangedAfterApproval", other, err)
		}
	}
}
