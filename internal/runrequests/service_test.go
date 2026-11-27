package runrequests_test

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/runrequests"
)

type memStore struct {
	rows      map[uuid.UUID]runrequests.Request
	tokens    map[uuid.UUID]runrequests.TokenRecord
	tokensErr error
	// syncs is what the queue says about each sync job; a job missing
	// here is gone.
	syncs map[uuid.UUID]memSync
	// dueCalls are the staleBefore bounds the sweep asked with.
	dueCalls []time.Time
	// origins is where each follow-up row sits (FollowUpOrigin), by its
	// parent group and template.
	origins map[followUpRowKey]runrequests.FollowUpOrigin
	// decides counts the Decide calls.
	decides int
}

// followUpRowKey is a follow-up row's unique key: its parent run group
// and its template.
type followUpRowKey struct{ parent, template uuid.UUID }

type memSync struct {
	status jobs.Status
	err    string
}

func newMemStore() *memStore {
	return &memStore{rows: map[uuid.UUID]runrequests.Request{}, tokens: map[uuid.UUID]runrequests.TokenRecord{}, syncs: map[uuid.UUID]memSync{}}
}

// effective is the SQL store's status filter: a pending row at or past its
// expiry reads as expired.
func effective(r runrequests.Request, now time.Time) runrequests.Status {
	if r.Status == runrequests.StatusPending && !now.Before(r.ExpiresAt) {
		return runrequests.StatusExpired
	}
	return r.Status
}

func (m *memStore) Insert(_ context.Context, r runrequests.Request) error {
	m.rows[r.ID] = r
	return nil
}

func (m *memStore) InsertCapped(ctx context.Context, r runrequests.Request, limit int, now time.Time) error {
	n, err := m.CountPending(ctx, r.OrgID, r.RequestedByToken, now)
	if err != nil {
		return err
	}
	if n >= limit {
		return runrequests.ErrTooManyPending
	}
	return m.Insert(ctx, r)
}

func (m *memStore) Get(_ context.Context, orgID, id uuid.UUID) (runrequests.Request, error) {
	r, ok := m.rows[id]
	if !ok || r.OrgID != orgID {
		return runrequests.Request{}, runrequests.ErrNotFound
	}
	return r, nil
}

func (m *memStore) List(_ context.Context, orgID uuid.UUID, status *runrequests.Status, now time.Time) ([]runrequests.Request, error) {
	var out []runrequests.Request
	for _, r := range m.rows {
		if r.OrgID == orgID && (status == nil || effective(r, now) == *status) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memStore) CountPending(_ context.Context, orgID uuid.UUID, token *uuid.UUID, now time.Time) (int, error) {
	n := 0
	for _, r := range m.rows {
		waits := token != nil && r.Status == runrequests.StatusWaitingForSync
		if r.OrgID != orgID || (effective(r, now) != runrequests.StatusPending && !waits) {
			continue
		}
		if token != nil && (r.RequestedByToken == nil || *r.RequestedByToken != *token) {
			continue
		}
		n++
	}
	return n, nil
}

func (m *memStore) Tokens(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]runrequests.TokenRecord, error) {
	if m.tokensErr != nil {
		return nil, m.tokensErr
	}
	out := map[uuid.UUID]runrequests.TokenRecord{}
	for _, id := range ids {
		if rec, ok := m.tokens[id]; ok {
			out[id] = rec
		}
	}
	return out, nil
}

func (m *memStore) Decide(_ context.Context, orgID, id uuid.UUID, to runrequests.Status, by uuid.UUID, at time.Time, fingerprint string, approved jobs.ApprovedRun, followUps *runrequests.FollowUpBindings) (bool, error) {
	m.decides++
	r, ok := m.rows[id]
	rejectWaiting := r.Status == runrequests.StatusWaitingForSync && to == runrequests.StatusRejected
	if !ok || r.OrgID != orgID || (r.Status != runrequests.StatusPending && !rejectWaiting) {
		return false, nil
	}
	if r.ExpiresAt.IsZero() {
		r.ExpiresAt = at
	}
	r.Status, r.DecidedBy, r.DecidedAt, r.ApprovedFingerprint, r.PinnedCommit = to, &by, &at, fingerprint, approved.PinnedCommit
	r.EnrollHostIDs, r.FollowUpBindings = approved.EnrollHostIDs, followUps
	if to == runrequests.StatusApproved && (r.Kind == runrequests.KindRun || r.Kind == runrequests.KindRetryFailed) {
		group := r.ID
		r.RunGroupID = &group
	}
	m.rows[id] = r
	return true, nil
}

// InTx runs fn and, when it fails, puts the rows back as they were: the
// SQL store's transaction rollback.
func (m *memStore) LatestReviews(_ context.Context, orgID uuid.UUID, kind runrequests.Kind, targets []uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
	out := map[uuid.UUID]uuid.UUID{}
	for _, r := range m.rows {
		for _, t := range targets {
			if r.OrgID == orgID && r.Kind == kind && r.TargetID != nil && *r.TargetID == t {
				out[t] = r.ID
			}
		}
	}
	return out, nil
}

func (m *memStore) InTx(ctx context.Context, _ uuid.UUID, fn func(context.Context) error) error {
	saved := maps.Clone(m.rows)
	if err := fn(ctx); err != nil {
		m.rows = saved
		return err
	}
	return nil
}

func (m *memStore) RevertToPending(_ context.Context, orgID, id uuid.UUID) error {
	r := m.rows[id]
	r.Status, r.DecidedBy, r.DecidedAt, r.ApprovedFingerprint, r.PinnedCommit = runrequests.StatusPending, nil, nil, "", ""
	r.EnrollHostIDs, r.FollowUpBindings = nil, nil
	m.rows[id] = r
	return nil
}

func (m *memStore) FollowUpOrigin(_ context.Context, orgID, parentGroup, templateID uuid.UUID) (runrequests.FollowUpOrigin, error) {
	o, ok := m.origins[followUpRowKey{parentGroup, templateID}]
	if r, found := m.rows[o.RequestID]; !ok || !found || r.OrgID != orgID {
		return runrequests.FollowUpOrigin{}, runrequests.ErrNotFound
	}
	return o, nil
}

func (m *memStore) ByRunGroup(_ context.Context, orgID, group uuid.UUID) (runrequests.Request, error) {
	for _, r := range m.rows {
		if r.OrgID == orgID && r.RunGroupID != nil && *r.RunGroupID == group {
			return r, nil
		}
	}
	return runrequests.Request{}, runrequests.ErrNotFound
}

func (m *memStore) LockStatusByRunGroup(ctx context.Context, orgID, group uuid.UUID) (runrequests.Status, error) {
	r, err := m.ByRunGroup(ctx, orgID, group)
	return r.Status, err
}

func (m *memStore) waitingRows(keep func(runrequests.Waiting) bool) []runrequests.Waiting {
	var out []runrequests.Waiting
	for _, r := range m.rows {
		if r.Status != runrequests.StatusWaitingForSync {
			continue
		}
		job := m.syncs[*r.SyncJobID]
		w := runrequests.Waiting{OrgID: r.OrgID, ID: r.ID, CreatedAt: r.CreatedAt, ExpiresInMinutes: r.ExpiresInMinutes,
			SyncJobID: *r.SyncJobID, SyncStatus: job.status, SyncError: job.err}
		if keep(w) {
			out = append(out, w)
		}
	}
	return out
}

func (m *memStore) ListWaitingFor(_ context.Context, syncJob uuid.UUID) ([]runrequests.Waiting, error) {
	return m.waitingRows(func(w runrequests.Waiting) bool { return w.SyncJobID == syncJob }), nil
}

// ListDueWaiting is the SQL store's filter: the sync ended or is gone, or
// the request waited since before staleBefore.
func (m *memStore) ListDueWaiting(_ context.Context, staleBefore time.Time) ([]runrequests.Waiting, error) {
	m.dueCalls = append(m.dueCalls, staleBefore)
	return m.waitingRows(func(w runrequests.Waiting) bool {
		return w.SyncStatus == "" || w.SyncStatus.Terminal() || !w.CreatedAt.After(staleBefore)
	}), nil
}

func (m *memStore) EndWait(_ context.Context, orgID, id uuid.UUID, to runrequests.Status, expiresAt time.Time, syncError string) (bool, error) {
	r, ok := m.rows[id]
	if !ok || r.OrgID != orgID || r.Status != runrequests.StatusWaitingForSync {
		return false, nil
	}
	r.Status, r.ExpiresAt, r.SyncError = to, expiresAt, syncError
	m.rows[id] = r
	return true, nil
}

type fakePictures struct {
	p runrequests.Picture
	// live is each follow-up template's binding now (BindFollowUp), and
	// within what the last BindFollowUp of each was given.
	live   map[uuid.UUID]runrequests.PictureFollowUp
	within map[uuid.UUID][]uuid.UUID
}

func (f *fakePictures) Picture(context.Context, uuid.UUID, runrequests.Request) (runrequests.Picture, error) {
	return f.p, nil
}

func (f *fakePictures) BindFollowUp(_ context.Context, _, _, templateID uuid.UUID, within []uuid.UUID) (runrequests.PictureFollowUp, error) {
	if f.within == nil {
		f.within = map[uuid.UUID][]uuid.UUID{}
	}
	f.within[templateID] = within
	return f.live[templateID], nil
}

type fakeEnqueuer struct {
	runs      []string // idempotency keys
	retries   []uuid.UUID
	retryKeys []string
	actor     uuid.UUID
	err       error
	group     uuid.UUID   // the run group the last enqueue was given
	list      []uuid.UUID // the follow-up list the last enqueue was given
	request   runrequests.Request
	retryPin  string // the commit the last retry was pinned to
	// retryApproved is what the last retry was given from its approval.
	retryApproved jobs.ApprovedRun
	// checkErr is what CheckReplay answers; checkKeys the keys it was
	// asked with.
	checkErr  error
	checkKeys []string
}

func (f *fakeEnqueuer) CheckReplay(_ context.Context, _ uuid.UUID, _ runrequests.Request, key string) error {
	f.checkKeys = append(f.checkKeys, key)
	return f.checkErr
}

func (f *fakeEnqueuer) Run(_ context.Context, _ uuid.UUID, r runrequests.Request, actor uuid.UUID, key string, group uuid.UUID, followUps []uuid.UUID) error {
	if f.err != nil {
		return f.err
	}
	f.runs, f.actor, f.group, f.list, f.request = append(f.runs, key), actor, group, followUps, r
	return nil
}

func (f *fakeEnqueuer) RetryFailed(_ context.Context, _ uuid.UUID, r runrequests.Request, actor uuid.UUID, key string, group uuid.UUID, followUps []uuid.UUID, approved jobs.ApprovedRun) error {
	if f.err != nil {
		return f.err
	}
	f.retries, f.retryKeys, f.actor, f.group, f.list, f.retryPin = append(f.retries, *r.JobID), append(f.retryKeys, key), actor, group, followUps, approved.PinnedCommit
	f.retryApproved = approved
	f.request = r
	return nil
}

type fakeValidator struct {
	err       error
	template  uuid.UUID
	group     uuid.UUID              // the retried job's run group
	overrides *runrequests.Overrides // what the last ValidateRun was given
}

func (f *fakeValidator) ValidateRun(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ *uuid.UUID, ov *runrequests.Overrides) (int64, error) {
	f.overrides = ov
	return 7, f.err
}

func (f *fakeValidator) ValidateRetry(context.Context, uuid.UUID, uuid.UUID) (uuid.UUID, uuid.UUID, int64, error) {
	return f.template, f.group, 7, f.err
}

func (f *fakeValidator) RetryRunOverrides(_ context.Context, _, _ uuid.UUID, ov *runrequests.Overrides) (*jobs.RunOverrides, error) {
	if ov == nil {
		return nil, nil
	}
	return &jobs.RunOverrides{ExtraVars: ov.ExtraVars, Untrusted: true}, nil
}

// fakeSyncer starts the sync of a sync_first request: job of repo, or
// nil for a template without a repository.
type fakeSyncer struct {
	job      *uuid.UUID
	repo     uuid.UUID
	queued   bool // the sync was already queued
	err      error
	calls    int
	template uuid.UUID
	actor    uuid.UUID
}

func (f *fakeSyncer) SyncFirst(_ context.Context, _, templateID, actor uuid.UUID) (*runrequests.SyncStart, error) {
	f.calls++
	f.template, f.actor = templateID, actor
	if f.err != nil || f.job == nil {
		return nil, f.err
	}
	return &runrequests.SyncStart{JobID: *f.job, RepoID: f.repo, AlreadyQueued: f.queued}, nil
}

type wiring struct {
	svc   *runrequests.Service
	store *memStore
	pics  *fakePictures
	enq   *fakeEnqueuer
	val   *fakeValidator
	syncs *fakeSyncer
	auto  *fakeAutomation
	now   time.Time
	org   uuid.UUID
}

func newWiring() *wiring {
	w := &wiring{
		store: newMemStore(), pics: &fakePictures{p: runrequests.Picture{TemplateName: "deploy", Limit: "web"}},
		enq: &fakeEnqueuer{group: uuid.Must(uuid.NewV7())}, val: &fakeValidator{template: uuid.Must(uuid.NewV7())}, syncs: &fakeSyncer{},
		auto: &fakeAutomation{template: uuid.Must(uuid.NewV7())},
		now:  time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC), org: uuid.Must(uuid.NewV7()),
	}
	w.svc = runrequests.NewService(w.store, w.pics, w.enq, w.val, w.syncs, w.auto, func() time.Time { return w.now }, func() uuid.UUID { return uuid.Must(uuid.NewV7()) })
	return w
}

func (w *wiring) create(t *testing.T) runrequests.Request {
	t.Helper()
	tok := uuid.Must(uuid.NewV7())
	r, err := w.svc.Create(context.Background(), w.org, runrequests.CreateInput{
		Kind: runrequests.KindRun, TemplateID: uuid.Must(uuid.NewV7()), Reason: "set up web",
		RequestedByUser: uuid.Must(uuid.NewV7()), RequestedByToken: &tok,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCreateValidatesAndExpiresAfterAnHour(t *testing.T) {
	w := newWiring()
	r := w.create(t)
	if r.Status != runrequests.StatusPending || r.TemplateRevision != 7 || !r.ExpiresAt.Equal(w.now.Add(time.Hour)) {
		t.Fatalf("created = %+v", r)
	}
	w.now = w.now.Add(time.Hour - time.Second)
	if got, _, _, err := w.svc.Get(context.Background(), w.org, r.ID); err != nil || got.Status != runrequests.StatusPending {
		t.Fatalf("just before the hour = %+v %v", got, err)
	}
	w.now = w.now.Add(time.Second)
	if got, _, _, err := w.svc.Get(context.Background(), w.org, r.ID); err != nil || got.Status != runrequests.StatusExpired {
		t.Fatalf("at the hour = %+v %v", got, err)
	}

	w.val.err = runrequests.ErrInvalidInput
	if _, err := w.svc.Create(context.Background(), w.org, runrequests.CreateInput{Kind: runrequests.KindRun, TemplateID: uuid.Must(uuid.NewV7()), Reason: "x", RequestedByUser: uuid.Must(uuid.NewV7())}); !errors.Is(err, runrequests.ErrInvalidInput) {
		t.Fatalf("create with a failing validation = %v", err)
	}
}

func TestCreateNeedsAReason(t *testing.T) {
	w := newWiring()
	if _, err := w.svc.Create(context.Background(), w.org, runrequests.CreateInput{Kind: runrequests.KindRun, TemplateID: uuid.Must(uuid.NewV7()), RequestedByUser: uuid.Must(uuid.NewV7())}); !errors.Is(err, runrequests.ErrInvalidInput) {
		t.Fatalf("create without a reason = %v", err)
	}
}

func TestReject(t *testing.T) {
	w := newWiring()
	r := w.create(t)
	got, err := w.svc.Reject(context.Background(), w.org, r.ID, uuid.Must(uuid.NewV7()))
	if err != nil || got.Status != runrequests.StatusRejected {
		t.Fatalf("reject = %+v %v", got, err)
	}
	if _, err := w.svc.Reject(context.Background(), w.org, r.ID, uuid.Must(uuid.NewV7())); !errors.Is(err, runrequests.ErrDecided) {
		t.Fatalf("second reject = %v", err)
	}
}

func TestCreatedByToken(t *testing.T) {
	w := newWiring()
	r := w.create(t)
	_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
	if _, err := w.svc.Approve(context.Background(), w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil, false); err != nil {
		t.Fatal(err)
	}
	if ok, err := w.svc.CreatedByToken(context.Background(), w.org, w.enq.group, *r.RequestedByToken); err != nil || !ok {
		t.Fatalf("own run group = %v %v", ok, err)
	}
	if ok, _ := w.svc.CreatedByToken(context.Background(), w.org, w.enq.group, uuid.Must(uuid.NewV7())); ok {
		t.Fatal("another token's run group counted as its own")
	}
	if ok, _ := w.svc.CreatedByToken(context.Background(), w.org, uuid.Must(uuid.NewV7()), *r.RequestedByToken); ok {
		t.Fatal("an unrelated run group counted")
	}
}

func TestListFiltersByEffectiveStatus(t *testing.T) {
	w := newWiring()
	old := w.create(t)
	w.now = w.now.Add(30 * time.Minute)
	fresh := w.create(t)
	w.now = w.now.Add(30 * time.Minute) // old has reached its hour, fresh has not
	ctx := context.Background()

	all, err := w.svc.List(ctx, w.org, nil)
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %+v %v", all, err)
	}
	for _, tc := range []struct {
		status runrequests.Status
		want   uuid.UUID
	}{{runrequests.StatusExpired, old.ID}, {runrequests.StatusPending, fresh.ID}} {
		st := tc.status
		got, err := w.svc.List(ctx, w.org, &st)
		if err != nil || len(got) != 1 || got[0].ID != tc.want || got[0].Status != tc.status {
			t.Fatalf("%s = %+v %v", tc.status, got, err)
		}
	}
	if got, err := w.svc.List(ctx, uuid.Must(uuid.NewV7()), nil); err != nil || len(got) != 0 {
		t.Fatalf("another organization = %+v %v", got, err)
	}
}
