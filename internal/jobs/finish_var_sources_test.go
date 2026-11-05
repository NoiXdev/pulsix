package jobs_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/varsource"
	"github.com/noixdev/pulsix/internal/worker"
)

type sourceCall struct {
	orgID, repoID uuid.UUID
	at            time.Time
	raw           json.RawMessage
}

// recordingSourceSink records every RecordVarSources call.
type recordingSourceSink struct {
	mu    sync.Mutex
	calls []sourceCall
	err   error
}

func (s *recordingSourceSink) RecordVarSources(_ context.Context, orgID, repoID uuid.UUID, at time.Time, raw json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.calls = append(s.calls, sourceCall{orgID, repoID, at, raw})
	return nil
}

func (s *recordingSourceSink) all() []sourceCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sourceCall(nil), s.calls...)
}

// varSourcesFixture is one organization with a repository and a queue
// whose source sink records.
type varSourcesFixture struct {
	d      *db.DB
	org    uuid.UUID
	repoID uuid.UUID
	queue  *jobs.SQLQueue
	store  *jobs.SQLStore
	sink   *recordingSourceSink
	zone   uuid.UUID
}

func newVarSourcesFixture(t *testing.T, slug string) varSourcesFixture {
	t.Helper()
	d := testdb.Open(t)
	org := seedOrg(t, gen.New(d.Pool()), slug)
	repoID := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(db.WithOrgScope(ctx, org),
		`INSERT INTO git_repos (id, org_id, name, url) VALUES ($1, $2, 'infra', 'https://git.example.com/acme/infra.git')`,
		repoID, org); err != nil {
		t.Fatal(err)
	}
	queue := jobs.NewSQLQueue(d, crypto.NewBox(testBoxKey("jobs-var-sources-key")), emptySources{}, emptySources{}, emptySources{}, emptySources{}, time.Now, rand.Reader)
	sink := &recordingSourceSink{}
	queue.SetSourceSink(sink)
	return varSourcesFixture{d: d, org: org, repoID: repoID, queue: queue, store: jobs.NewSQLStore(d), sink: sink, zone: seedZone(t, d, org)}
}

// runningJob is a running job of kind; a sync_repo job names the
// fixture's repository.
func (f varSourcesFixture) runningJob(t *testing.T, kind jobs.Kind) jobs.Job {
	t.Helper()
	j := newJob(f.org, f.zone, kind, jobs.StatusQueued)
	var payload json.RawMessage
	if kind == jobs.KindSyncRepo {
		payload = json.RawMessage(`{"repo_id":"` + f.repoID.String() + `"}`)
	}
	j.Payload = payload
	if err := f.store.CreateJob(db.WithOrgScope(ctx, f.org), j, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Exec(ctx, `UPDATE jobs SET status = 'running' WHERE id = $1`, j.ID); err != nil {
		t.Fatal(err)
	}
	return j
}

func (f varSourcesFixture) jobStatus(t *testing.T, id uuid.UUID) jobs.Status {
	t.Helper()
	j, err := f.store.GetJob(db.WithOrgScope(ctx, f.org), f.org, id)
	if err != nil {
		t.Fatal(err)
	}
	return j.Status
}

func (f varSourcesFixture) storedStats(t *testing.T, id uuid.UUID) map[string]any {
	t.Helper()
	var raw []byte
	if err := f.d.Pool().QueryRow(ctx, `SELECT stats FROM jobs WHERE id = $1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stats map[string]any
	if err := json.Unmarshal(raw, &stats); err != nil {
		t.Fatal(err)
	}
	return stats
}

// workerStats is a sync result's stats as the gRPC layer hands them to the
// queue: the worker's map, encoded and decoded again (grpcsrv.parseStats).
func workerStats(t *testing.T, repoID uuid.UUID, report *varsource.Report) (map[string]any, json.RawMessage) {
	t.Helper()
	stats := map[string]any{"repo_id": repoID.String(), "requirements_installed": true}
	var encoded json.RawMessage
	if report != nil {
		var err error
		if encoded, err = json.Marshal(report); err != nil {
			t.Fatal(err)
		}
		stats[worker.VarSourcesStatsKey] = encoded
	}
	data, err := json.Marshal(stats)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded, encoded
}

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("decode %s: %v", a, err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return reflect.DeepEqual(va, vb)
}

func testReport() *varsource.Report {
	return &varsource.Report{Commit: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", Playbooks: []varsource.Playbook{
		{Path: "site.yml", Fields: nil, SetInPlaybook: []string{}, Roles: []varsource.RoleUse{}, Files: []varsource.FileUse{}, Notes: []varsource.Note{}},
	}, Notes: []varsource.Note{}}
}

func TestFinishSyncStripsVarSourcesFromStats(t *testing.T) {
	f := newVarSourcesFixture(t, "jobs-var-sources-strip-org")
	j := f.runningJob(t, jobs.KindSyncRepo)
	stats, encoded := workerStats(t, f.repoID, testReport())

	if err := f.queue.Finish(ctx, j.ID, jobs.StatusSucceeded, stats, ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	stored := f.storedStats(t, j.ID)
	if _, kept := stored[worker.VarSourcesStatsKey]; kept {
		t.Fatalf("jobs.stats = %v, want no %s", stored, worker.VarSourcesStatsKey)
	}
	if stored["repo_id"] != f.repoID.String() || stored["requirements_installed"] != true {
		t.Fatalf("jobs.stats = %v, want the rest of the stats kept", stored)
	}
	if _, still := stats[worker.VarSourcesStatsKey]; !still {
		t.Fatal("Finish must not modify the caller's stats map")
	}
	calls := f.sink.all()
	if len(calls) != 1 || calls[0].orgID != f.org || calls[0].repoID != f.repoID || calls[0].at.IsZero() {
		t.Fatalf("sink calls = %+v", calls)
	}
	if !sameJSON(t, calls[0].raw, encoded) {
		t.Fatalf("sink raw = %s, want the report %s", calls[0].raw, encoded)
	}
}

func TestFinishSyncWithoutVarSourcesMarksUnread(t *testing.T) {
	f := newVarSourcesFixture(t, "jobs-var-sources-unread-org")
	j := f.runningJob(t, jobs.KindSyncRepo)
	stats, _ := workerStats(t, f.repoID, nil)

	if err := f.queue.Finish(ctx, j.ID, jobs.StatusSucceeded, stats, ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	calls := f.sink.all()
	if len(calls) != 1 || calls[0].raw != nil || calls[0].repoID != f.repoID {
		t.Fatalf("sink calls = %+v, want one with a nil report (an older worker)", calls)
	}
}

func TestFinishRecordsVarSourcesOnlyForASucceededSync(t *testing.T) {
	f := newVarSourcesFixture(t, "jobs-var-sources-only-org")

	for _, status := range []jobs.Status{jobs.StatusFailed, jobs.StatusCancelled} {
		j := f.runningJob(t, jobs.KindSyncRepo)
		stats, _ := workerStats(t, f.repoID, testReport())
		if err := f.queue.Finish(ctx, j.ID, status, stats, "boom"); err != nil {
			t.Fatalf("Finish(%s): %v", status, err)
		}
		if _, kept := f.storedStats(t, j.ID)[worker.VarSourcesStatsKey]; kept {
			t.Fatalf("a %s sync stored the variable sources in jobs.stats", status)
		}
	}
	j := f.runningJob(t, jobs.KindConnectionTest)
	if err := f.queue.Finish(ctx, j.ID, jobs.StatusSucceeded, map[string]any{worker.VarSourcesStatsKey: "x"}, ""); err != nil {
		t.Fatalf("Finish(connection_test): %v", err)
	}
	if got := f.storedStats(t, j.ID)[worker.VarSourcesStatsKey]; got != "x" {
		t.Fatalf("a non-sync job's stats = %v, want them untouched", got)
	}
	if calls := f.sink.all(); len(calls) != 0 {
		t.Fatalf("sink calls = %+v, want none", calls)
	}

	// The repository gone meanwhile (the sync sink says so): the sources
	// are not recorded either.
	f.queue.SetSyncSink(&recordingSink{err: jobs.ErrNotFound})
	j = f.runningJob(t, jobs.KindSyncRepo)
	stats, _ := workerStats(t, f.repoID, testReport())
	if err := f.queue.Finish(ctx, j.ID, jobs.StatusSucceeded, stats, ""); err != nil {
		t.Fatalf("Finish(gone repo): %v", err)
	}
	if calls := f.sink.all(); len(calls) != 0 {
		t.Fatalf("sink calls for a gone repository = %+v, want none", calls)
	}
}

func TestFinishSyncSourceSinkFailureFailsTheFinish(t *testing.T) {
	f := newVarSourcesFixture(t, "jobs-var-sources-fail-org")
	f.sink.err = errors.New("connection reset")
	j := f.runningJob(t, jobs.KindSyncRepo)
	stats, _ := workerStats(t, f.repoID, testReport())
	if err := f.queue.Finish(ctx, j.ID, jobs.StatusSucceeded, stats, ""); err == nil {
		t.Fatal("Finish = nil, want the sink failure (the finish is atomic)")
	}
	if got := f.jobStatus(t, j.ID); got != jobs.StatusRunning {
		t.Fatalf("job after the failed finish = %s, want running", got)
	}
}

type applyCall struct {
	orgID, repoID uuid.UUID
	commit        string
	deadline      time.Duration
}

// committedApplier records each apply and what a fresh connection sees of
// the repository's record at that moment.
type committedApplier struct {
	d     *db.DB
	mu    sync.Mutex
	calls []applyCall
	err   error
}

func (a *committedApplier) ApplyRepoSources(ctx context.Context, orgID, repoID uuid.UUID) error {
	var commit string
	if err := a.d.Pool().QueryRow(context.Background(), `SELECT commit_sha FROM repo_var_sources WHERE repo_id = $1`, repoID).Scan(&commit); err != nil {
		commit = "<none: " + err.Error() + ">"
	}
	var left time.Duration
	if dl, ok := ctx.Deadline(); ok {
		left = time.Until(dl)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, applyCall{orgID, repoID, commit, left})
	return a.err
}

func (a *committedApplier) all() []applyCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]applyCall(nil), a.calls...)
}

func TestFinishSyncAppliesSourcesAfterCommit(t *testing.T) {
	f := newVarSourcesFixture(t, "jobs-var-sources-apply-org")
	f.queue.SetSourceSink(repos.NewService(repos.NewSQLStore(f.d), nil, nil))
	applier := &committedApplier{d: f.d, err: errors.New("apply failed")}
	f.queue.SetSourceApplier(applier)

	j := f.runningJob(t, jobs.KindSyncRepo)
	report := testReport()
	stats, _ := workerStats(t, f.repoID, report)
	if err := f.queue.Finish(ctx, j.ID, jobs.StatusSucceeded, stats, ""); err != nil {
		t.Fatalf("Finish = %v, want nil (an apply failure never fails the sync)", err)
	}
	if got := f.jobStatus(t, j.ID); got != jobs.StatusSucceeded {
		t.Fatalf("job = %s, want succeeded", got)
	}
	calls := applier.all()
	if len(calls) != 1 || calls[0].orgID != f.org || calls[0].repoID != f.repoID {
		t.Fatalf("apply calls = %+v, want one for the repository", calls)
	}
	if calls[0].commit != report.Commit {
		t.Fatalf("the applier saw commit %q, want the committed %q", calls[0].commit, report.Commit)
	}
	if calls[0].deadline <= 0 || calls[0].deadline > 30*time.Second {
		t.Fatalf("apply deadline in %v, want a bound of at most 30s", calls[0].deadline)
	}

	// No apply when the sync sent no sources, failed, or was retried.
	if err := f.queue.Finish(ctx, j.ID, jobs.StatusSucceeded, stats, ""); err != nil {
		t.Fatal(err)
	}
	old := f.runningJob(t, jobs.KindSyncRepo)
	oldStats, _ := workerStats(t, f.repoID, nil)
	if err := f.queue.Finish(ctx, old.ID, jobs.StatusSucceeded, oldStats, ""); err != nil {
		t.Fatal(err)
	}
	failed := f.runningJob(t, jobs.KindSyncRepo)
	if err := f.queue.Finish(ctx, failed.ID, jobs.StatusFailed, stats, "git fetch: timeout"); err != nil {
		t.Fatal(err)
	}
	if calls := applier.all(); len(calls) != 1 {
		t.Fatalf("apply calls = %+v, want only the first", calls)
	}
}
