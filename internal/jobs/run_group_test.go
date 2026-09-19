package jobs

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// recordingGroupSink records every finished run group; err is returned
// from each call.
type recordingGroupSink struct {
	mu    sync.Mutex
	calls []RunGroupResult
	err   error
}

func (s *recordingGroupSink) RunGroupFinished(_ context.Context, r RunGroupResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, r)
	return s.err
}

func (s *recordingGroupSink) snapshot() []RunGroupResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RunGroupResult(nil), s.calls...)
}

type groupWorld struct {
	d    *db.DB
	org  uuid.UUID
	q    *SQLQueue
	sink *recordingGroupSink
}

func newGroupWorld(t *testing.T, slug string) *groupWorld {
	t.Helper()
	d := testdb.Open(t)
	org := uuid.Must(uuid.NewV7())
	if _, err := gen.New(d.Pool()).CreateOrganization(context.Background(), gen.CreateOrganizationParams{ID: org, Slug: slug, Name: slug}); err != nil {
		t.Fatal(err)
	}
	q := NewSQLQueue(d, nil, nil, nil, nil, nil, time.Now, rand.Reader)
	sink := &recordingGroupSink{}
	q.SetRunGroupSink(sink)
	return &groupWorld{d: d, org: org, q: q, sink: sink}
}

// runGroup inserts one scheduled run group with one job per zone, each
// targeting one host and already running.
func (w *groupWorld) runGroup(t *testing.T, zones int) (uuid.UUID, uuid.UUID, []Job) {
	t.Helper()
	group, schedule, tmpl := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	var out []Job
	for i := 0; i < zones; i++ {
		j := Job{
			ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ZoneID: testZone(t, w.d, w.org), Kind: KindRun, TemplateID: &tmpl,
			HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}, Status: StatusQueued, RunGroupID: group,
			Trigger: TriggerSchedule, TriggerRef: &schedule, CreatedAt: time.Now().Add(time.Duration(i) * time.Millisecond),
		}
		if err := NewSQLStore(w.d).CreateJob(db.WithOrgScope(context.Background(), w.org), j, nil); err != nil {
			t.Fatal(err)
		}
		out = append(out, j)
	}
	for _, j := range out {
		if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET status = 'running', started_at = now() WHERE id = $1`, j.ID); err != nil {
			t.Fatal(err)
		}
	}
	return group, schedule, out
}

func TestRunGroupReportedOnceWhenItsLastJobFinishes(t *testing.T) {
	w := newGroupWorld(t, "rungroup-once")
	group, schedule, js := w.runGroup(t, 2)
	// The worker writes JSON null for an empty host list (a nil slice).
	if err := w.q.Finish(context.Background(), js[0].ID, StatusSucceeded,
		map[string]any{"changed_hosts": []string{js[0].HostIDs[0].String()}, "failed_hosts": nil}, ""); err != nil {
		t.Fatal(err)
	}
	if got := w.sink.snapshot(); len(got) != 0 {
		t.Fatalf("reported before the group finished: %+v", got)
	}
	if err := w.q.Finish(context.Background(), js[1].ID, StatusFailed,
		map[string]any{"changed_hosts": nil, "unreachable_hosts": []string{js[1].HostIDs[0].String()}}, "exited with code 4"); err != nil {
		t.Fatal(err)
	}
	got := w.sink.snapshot()
	if len(got) != 1 {
		t.Fatalf("reports = %d, want 1", len(got))
	}
	r := got[0]
	if r.GroupID != group || r.OrgID != w.org || r.FirstJobID != js[0].ID || r.Status != StatusFailed || r.Jobs != 2 ||
		r.Hosts != 2 || r.HostsChanged != 1 || r.HostsFailed != 1 || r.HostsOK() != 1 ||
		r.Trigger != TriggerSchedule || r.TriggerRef == nil || *r.TriggerRef != schedule || r.StartedAt == nil || r.FinishedAt == nil {
		t.Fatalf("result = %+v", r)
	}
	// A repeated finish is the idempotent no-op and reports nothing more.
	if err := w.q.Finish(context.Background(), js[1].ID, StatusFailed, nil, ""); err != nil {
		t.Fatal(err)
	}
	if n := len(w.sink.snapshot()); n != 1 {
		t.Fatalf("reports after a repeated finish = %d", n)
	}
}

func TestConcurrentFinishesReportTheGroupOnce(t *testing.T) {
	w := newGroupWorld(t, "rungroup-race")
	for round := 0; round < 5; round++ {
		_, _, js := w.runGroup(t, 3)
		var wg sync.WaitGroup
		for _, j := range js {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := w.q.Finish(context.Background(), j.ID, StatusSucceeded, nil, ""); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if n := len(w.sink.snapshot()); n != round+1 {
			t.Fatalf("round %d: reports = %d, want %d", round, n, round+1)
		}
	}
}

func TestCancellingTheLastQueuedJobCompletesTheGroup(t *testing.T) {
	w := newGroupWorld(t, "rungroup-cancel")
	_, _, js := w.runGroup(t, 2)
	if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET status = 'queued', started_at = NULL WHERE id = $1`, js[1].ID); err != nil {
		t.Fatal(err)
	}
	if err := w.q.Finish(context.Background(), js[0].ID, StatusSucceeded, nil, ""); err != nil {
		t.Fatal(err)
	}
	scoped := db.WithOrgScope(context.Background(), w.org)
	if status, err := NewSQLStore(w.d).CancelJob(scoped, w.org, js[1].ID); err != nil || status != StatusCancelled {
		t.Fatalf("cancel = %s, %v", status, err)
	}
	w.q.ClearTerminalPayload(scoped, js[1].ID)
	got := w.sink.snapshot()
	if len(got) != 1 || got[0].Status != StatusCancelled {
		t.Fatalf("reports = %+v, want one cancelled group", got)
	}
}

func TestRunGroupSinkErrorDoesNotFailTheFinish(t *testing.T) {
	w := newGroupWorld(t, "rungroup-sinkerr")
	w.sink.err = errors.New("outbox down")
	_, _, js := w.runGroup(t, 1)
	if err := w.q.Finish(context.Background(), js[0].ID, StatusSucceeded, nil, ""); err != nil {
		t.Fatalf("Finish = %v, want nil although the sink failed", err)
	}
	var status string
	if err := w.d.Pool().QueryRow(context.Background(), `SELECT status FROM jobs WHERE id = $1`, js[0].ID).Scan(&status); err != nil || status != "succeeded" {
		t.Fatalf("status = %s, %v", status, err)
	}
}

func TestNonRunJobsAreNotReported(t *testing.T) {
	w := newGroupWorld(t, "rungroup-kinds")
	j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ZoneID: testZone(t, w.d, w.org), Kind: KindAdhoc,
		HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}, Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), Trigger: TriggerManual, CreatedAt: time.Now()}
	if err := NewSQLStore(w.d).CreateJob(db.WithOrgScope(context.Background(), w.org), j, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET status = 'running' WHERE id = $1`, j.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.q.Finish(context.Background(), j.ID, StatusSucceeded, nil, ""); err != nil {
		t.Fatal(err)
	}
	if n := len(w.sink.snapshot()); n != 0 {
		t.Fatalf("an ad-hoc job was reported as a run group (%d)", n)
	}
}

func TestGroupSummariesAggregateStatusAndTiming(t *testing.T) {
	w := newGroupWorld(t, "rungroup-summary")
	failedGroup, _, fj := w.runGroup(t, 2)
	runningGroup, _, rj := w.runGroup(t, 2)
	queuedGroup, _, qj := w.runGroup(t, 1)
	if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET status = 'queued', started_at = NULL WHERE id = $1`, qj[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := w.q.Finish(context.Background(), fj[0].ID, StatusSucceeded, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := w.q.Finish(context.Background(), fj[1].ID, StatusError, nil, "boom"); err != nil {
		t.Fatal(err)
	}
	if err := w.q.Finish(context.Background(), rj[0].ID, StatusSucceeded, nil, ""); err != nil {
		t.Fatal(err)
	}
	got, err := NewSQLStore(w.d).GroupSummaries(db.WithOrgScope(context.Background(), w.org), w.org, []uuid.UUID{failedGroup, runningGroup, queuedGroup, uuid.Must(uuid.NewV7())})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("summaries = %d, want 3", len(got))
	}
	if g := got[failedGroup]; g.Status != StatusFailed || g.FirstJobID != fj[0].ID || g.FinishedAt == nil || g.Duration() <= 0 {
		t.Fatalf("failed group = %+v", g)
	}
	if g := got[runningGroup]; g.Status != StatusRunning || g.Duration() != 0 {
		t.Fatalf("running group = %+v", g)
	}
	if g := got[queuedGroup]; g.Status != StatusQueued || g.StartedAt != nil {
		t.Fatalf("queued group = %+v", g)
	}
}

func TestCancelledRunGroupCheckIgnoresOtherJobs(t *testing.T) {
	w := newGroupWorld(t, "rungroup-cancel-kinds")
	scoped := db.WithOrgScope(context.Background(), w.org)
	// An unknown job is logged and ignored.
	w.q.recordCancelledRunGroup(scoped, uuid.Must(uuid.NewV7()))
	// A cancelled ad-hoc job is no run group.
	j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ZoneID: testZone(t, w.d, w.org), Kind: KindAdhoc,
		HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}, Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), Trigger: TriggerManual, CreatedAt: time.Now()}
	if err := NewSQLStore(w.d).CreateJob(scoped, j, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSQLStore(w.d).CancelJob(scoped, w.org, j.ID); err != nil {
		t.Fatal(err)
	}
	w.q.ClearTerminalPayload(scoped, j.ID)
	// Without a sink nothing is checked at all.
	w.q.SetRunGroupSink(nil)
	_, _, js := w.runGroup(t, 1)
	if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET status = 'queued', started_at = NULL WHERE id = $1`, js[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSQLStore(w.d).CancelJob(scoped, w.org, js[0].ID); err != nil {
		t.Fatal(err)
	}
	w.q.ClearTerminalPayload(scoped, js[0].ID)
	if n := len(w.sink.snapshot()); n != 0 {
		t.Fatalf("reports = %d, want none", n)
	}
}
