package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
)

// TestGroupSummariesStatusBranchesAndScoping completes the coverage of
// GroupSummaries' hand-written SQL, which the final review's CI section
// asked for: internal/jobs sits on its 90 % floor with no headroom, and
// this is the obvious place to buy some (backlog 2.81 already noted the
// gap). TestGroupSummariesAggregateStatusAndTiming covers queued, running
// and failed; the succeeded and cancelled aggregates, the org filter and
// the empty-input short circuit were untested.
func TestGroupSummariesStatusBranchesAndScoping(t *testing.T) {
	w := newGroupWorld(t, "rungroup-summary-branches")
	ctx := context.Background()
	scoped := db.WithOrgScope(ctx, w.org)
	st := NewSQLStore(w.d)

	okGroup, _, okJobs := w.runGroup(t, 2)
	for _, j := range okJobs {
		if err := w.q.Finish(ctx, j.ID, StatusSucceeded, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	// A cancelled member outranks the succeeded ones, but not a failure.
	mixedGroup, _, mixedJobs := w.runGroup(t, 2)
	if err := w.q.Finish(ctx, mixedJobs[0].ID, StatusSucceeded, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := w.q.Finish(ctx, mixedJobs[1].ID, StatusCancelled, nil, ""); err != nil {
		t.Fatal(err)
	}

	got, err := st.GroupSummaries(scoped, w.org, []uuid.UUID{okGroup, mixedGroup})
	if err != nil {
		t.Fatal(err)
	}
	if g := got[okGroup]; g.Status != StatusSucceeded || g.FirstJobID != okJobs[0].ID || g.Duration() <= 0 {
		t.Fatalf("all-succeeded group = %+v", g)
	}
	if g := got[mixedGroup]; g.Status != StatusCancelled {
		t.Fatalf("group with a cancelled member = %s, want cancelled", g.Status)
	}

	// The query is org-scoped: another organization never sees the group.
	other := uuid.Must(uuid.NewV7())
	if out, err := st.GroupSummaries(db.WithOrgScope(ctx, other), other, []uuid.UUID{okGroup, mixedGroup}); err != nil || len(out) != 0 {
		t.Fatalf("another org saw %d summary/summaries, %v", len(out), err)
	}
	// No ids at all is an empty answer, never a query.
	if out, err := st.GroupSummaries(scoped, w.org, nil); err != nil || len(out) != 0 {
		t.Fatalf("empty input = %v, %v", out, err)
	}
}

// TestStoreReadsReportDatabaseFailures pins that every store read and
// write reports a database failure instead of a zero value — the error
// branches none of the happy-path tests reach. A cancelled context is the
// cheapest faithful stand-in for a database that is gone: the statement
// never reaches Postgres and pgx returns the context error, exactly as the
// caller would see for a dropped connection.
func TestStoreReadsReportDatabaseFailures(t *testing.T) {
	w := newGroupWorld(t, "store-db-failures")
	st := NewSQLStore(w.d)
	id := uuid.Must(uuid.NewV7())
	dead, cancel := context.WithCancel(db.WithOrgScope(context.Background(), w.org))
	cancel()

	if _, err := st.GroupSummaries(dead, w.org, []uuid.UUID{id}); err == nil {
		t.Error("GroupSummaries reported no error on a dead connection")
	}
	// GetJob's mapErr passes a non-ErrNoRows failure through unchanged,
	// rather than turning every failure into "not found".
	if _, err := st.GetJob(dead, w.org, id); err == nil || errorIs(err, ErrNotFound) {
		t.Errorf("GetJob = %v, want the database failure itself, not ErrNotFound", err)
	}
	if _, err := st.CancelJob(dead, w.org, id); err == nil || errorIs(err, ErrNotRunning) {
		t.Errorf("CancelJob = %v, want the database failure itself", err)
	}
	if _, err := st.ListJobEvents(dead, w.org, id, 0); err == nil {
		t.Error("ListJobEvents reported no error on a dead connection")
	}
	if err := st.RecordSyncResult(dead, w.org, id, time.Now(), "", "", ""); err == nil || errorIs(err, ErrNotFound) {
		t.Errorf("RecordSyncResult = %v, want the database failure itself", err)
	}
	if _, err := st.HasActiveTemplateJob(dead, w.org, id); err == nil {
		t.Error("HasActiveTemplateJob reported no error on a dead connection")
	}
	if _, err := st.HasActiveRepoSync(dead, w.org, id); err == nil {
		t.Error("HasActiveRepoSync reported no error on a dead connection")
	}
	if err := st.CreateRotationGroup(dead, w.org, id, id, nil, nil, nil); err == nil {
		t.Error("CreateRotationGroup reported no error on a dead connection")
	}
	if _, _, err := st.ListJobs(dead, w.org, Filter{}, "", 10); err == nil {
		t.Error("ListJobs reported no error on a dead connection")
	}
	if err := st.CreateJobGroup(dead, w.org, nil, nil); err == nil {
		t.Error("CreateJobGroup reported no error on a dead connection")
	}
	if err := st.CreateJob(dead, Job{ID: id, OrgID: w.org, Kind: KindRun, Status: StatusQueued, CreatedAt: time.Now()}, nil); err == nil {
		t.Error("CreateJob reported no error on a dead connection")
	}
}

// errorIs is errors.Is, named so the assertions above read as prose.
func errorIs(err, target error) bool { return errors.Is(err, target) }
