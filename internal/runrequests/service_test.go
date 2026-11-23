package runrequests_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/runrequests"
)

type memStore struct {
	rows      map[uuid.UUID]runrequests.Request
	tokens    map[uuid.UUID]runrequests.TokenRecord
	tokensErr error
}

func newMemStore() *memStore {
	return &memStore{rows: map[uuid.UUID]runrequests.Request{}, tokens: map[uuid.UUID]runrequests.TokenRecord{}}
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
		if r.OrgID != orgID || effective(r, now) != runrequests.StatusPending {
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

func (m *memStore) Decide(_ context.Context, orgID, id uuid.UUID, to runrequests.Status, by uuid.UUID, at time.Time, fingerprint string) (bool, error) {
	r, ok := m.rows[id]
	if !ok || r.OrgID != orgID || r.Status != runrequests.StatusPending {
		return false, nil
	}
	r.Status, r.DecidedBy, r.DecidedAt, r.ApprovedFingerprint = to, &by, &at, fingerprint
	if to == runrequests.StatusApproved {
		group := r.ID
		r.RunGroupID = &group
	}
	m.rows[id] = r
	return true, nil
}

func (m *memStore) RevertToPending(_ context.Context, orgID, id uuid.UUID) error {
	r := m.rows[id]
	r.Status, r.DecidedBy, r.DecidedAt, r.ApprovedFingerprint = runrequests.StatusPending, nil, nil, ""
	m.rows[id] = r
	return nil
}

func (m *memStore) ByRunGroup(_ context.Context, orgID, group uuid.UUID) (runrequests.Request, error) {
	for _, r := range m.rows {
		if r.OrgID == orgID && r.RunGroupID != nil && *r.RunGroupID == group {
			return r, nil
		}
	}
	return runrequests.Request{}, runrequests.ErrNotFound
}

type fakePictures struct{ p runrequests.Picture }

func (f *fakePictures) Picture(context.Context, uuid.UUID, runrequests.Request) (runrequests.Picture, error) {
	return f.p, nil
}

type fakeEnqueuer struct {
	runs      []string // idempotency keys
	retries   []uuid.UUID
	retryKeys []string
	actor     uuid.UUID
	err       error
	group     uuid.UUID   // the run group the last enqueue was given
	list      []uuid.UUID // the follow-up list the last enqueue was given
}

func (f *fakeEnqueuer) Run(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ *uuid.UUID, actor uuid.UUID, key string, group uuid.UUID, followUps []uuid.UUID) error {
	if f.err != nil {
		return f.err
	}
	f.runs, f.actor, f.group, f.list = append(f.runs, key), actor, group, followUps
	return nil
}

func (f *fakeEnqueuer) RetryFailed(_ context.Context, _ uuid.UUID, jobID, actor uuid.UUID, key string, group uuid.UUID, followUps []uuid.UUID) error {
	if f.err != nil {
		return f.err
	}
	f.retries, f.retryKeys, f.actor, f.group, f.list = append(f.retries, jobID), append(f.retryKeys, key), actor, group, followUps
	return nil
}

type fakeValidator struct {
	err      error
	template uuid.UUID
}

func (f *fakeValidator) ValidateRun(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID) (int64, error) {
	return 7, f.err
}

func (f *fakeValidator) ValidateRetry(context.Context, uuid.UUID, uuid.UUID) (uuid.UUID, int64, error) {
	return f.template, 7, f.err
}

type wiring struct {
	svc   *runrequests.Service
	store *memStore
	pics  *fakePictures
	enq   *fakeEnqueuer
	val   *fakeValidator
	now   time.Time
	org   uuid.UUID
}

func newWiring() *wiring {
	w := &wiring{
		store: newMemStore(), pics: &fakePictures{p: runrequests.Picture{TemplateName: "deploy", Limit: "web"}},
		enq: &fakeEnqueuer{group: uuid.Must(uuid.NewV7())}, val: &fakeValidator{template: uuid.Must(uuid.NewV7())},
		now: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC), org: uuid.Must(uuid.NewV7()),
	}
	w.svc = runrequests.NewService(w.store, w.pics, w.enq, w.val, func() time.Time { return w.now }, func() uuid.UUID { return uuid.Must(uuid.NewV7()) })
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
	if _, err := w.svc.Approve(context.Background(), w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); err != nil {
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
