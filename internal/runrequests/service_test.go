package runrequests_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/runrequests"
)

type memStore struct {
	rows map[uuid.UUID]runrequests.Request
}

func newMemStore() *memStore { return &memStore{rows: map[uuid.UUID]runrequests.Request{}} }

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

func (m *memStore) List(_ context.Context, orgID uuid.UUID) ([]runrequests.Request, error) {
	var out []runrequests.Request
	for _, r := range m.rows {
		if r.OrgID == orgID {
			out = append(out, r)
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
	runs    []string // idempotency keys
	retries []uuid.UUID
	actor   uuid.UUID
	err     error
	group   uuid.UUID   // the run group the last enqueue was given
	list    []uuid.UUID // the follow-up list the last enqueue was given
}

func (f *fakeEnqueuer) Run(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ *uuid.UUID, actor uuid.UUID, key string, group uuid.UUID, followUps []uuid.UUID) error {
	if f.err != nil {
		return f.err
	}
	f.runs, f.actor, f.group, f.list = append(f.runs, key), actor, group, followUps
	return nil
}

func (f *fakeEnqueuer) RetryFailed(_ context.Context, _ uuid.UUID, jobID, actor, group uuid.UUID, followUps []uuid.UUID) error {
	if f.err != nil {
		return f.err
	}
	f.retries, f.actor, f.group, f.list = append(f.retries, jobID), actor, group, followUps
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

func TestApproveEnqueuesAsTheApprover(t *testing.T) {
	w := newWiring()
	r := w.create(t)
	_, _, fp, err := w.svc.Get(context.Background(), w.org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	approver := uuid.Must(uuid.NewV7())
	approved, err := w.svc.Approve(context.Background(), w.org, r.ID, approver, fp, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := approved.Request
	if got.Status != runrequests.StatusApproved || got.RunGroupID == nil || *got.RunGroupID != w.enq.group || *got.DecidedBy != approver {
		t.Fatalf("approved = %+v", got)
	}
	if len(w.enq.runs) != 1 || w.enq.runs[0] != "run-request:"+r.ID.String() || w.enq.actor != approver {
		t.Fatalf("enqueue = %+v", w.enq)
	}
	if _, err := w.svc.Approve(context.Background(), w.org, r.ID, approver, fp, nil); !errors.Is(err, runrequests.ErrDecided) {
		t.Fatalf("second approve = %v, want ErrDecided", err)
	}
}

func TestApproveRefusesAChangedPicture(t *testing.T) {
	w := newWiring()
	r := w.create(t)
	_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
	w.pics.p.Limit = "db"
	if _, err := w.svc.Approve(context.Background(), w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); !errors.Is(err, runrequests.ErrChanged) {
		t.Fatalf("approve after a change = %v, want ErrChanged", err)
	}
	if len(w.enq.runs) != 0 {
		t.Fatal("a changed request was enqueued")
	}
	got, _, _, _ := w.svc.Get(context.Background(), w.org, r.ID)
	if got.Status != runrequests.StatusPending {
		t.Fatalf("status after the refusal = %s, want pending", got.Status)
	}
}

func TestApproveRefusesAnExpiredRequest(t *testing.T) {
	w := newWiring()
	r := w.create(t)
	_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
	w.now = w.now.Add(time.Hour)
	if _, err := w.svc.Approve(context.Background(), w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); !errors.Is(err, runrequests.ErrExpired) {
		t.Fatalf("approve after expiry = %v, want ErrExpired", err)
	}
}

func TestApproveRevertsWhenTheEnqueueFails(t *testing.T) {
	w := newWiring()
	r := w.create(t)
	_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
	w.enq.err = errors.New("no worker zone")
	if _, err := w.svc.Approve(context.Background(), w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); err == nil {
		t.Fatal("approve with a failing enqueue succeeded")
	}
	got, _, _, _ := w.svc.Get(context.Background(), w.org, r.ID)
	if got.Status != runrequests.StatusPending || got.DecidedBy != nil {
		t.Fatalf("after the failed enqueue = %+v, want pending again", got)
	}
}

func TestApproveRetryUsesRetryFailed(t *testing.T) {
	w := newWiring()
	job := uuid.Must(uuid.NewV7())
	r, err := w.svc.Create(context.Background(), w.org, runrequests.CreateInput{Kind: runrequests.KindRetryFailed, JobID: &job, Reason: "retry", RequestedByUser: uuid.Must(uuid.NewV7())})
	if err != nil {
		t.Fatal(err)
	}
	if r.TemplateID != w.val.template {
		t.Fatalf("retry request template = %s, want the job's", r.TemplateID)
	}
	_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
	if _, err := w.svc.Approve(context.Background(), w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); err != nil {
		t.Fatal(err)
	}
	if len(w.enq.retries) != 1 || w.enq.retries[0] != job {
		t.Fatalf("retries = %+v", w.enq.retries)
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

func TestFingerprintCoversEveryField(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	base := runrequests.Picture{
		TemplateID: "t", TemplateName: "deploy", TemplateRevision: 1, Limit: "web", OnlyHost: "",
		Playbook: runrequests.PicturePlaybook{Source: "repo", Path: "site.yml"},
		Repo:     &runrequests.PictureRepo{Name: "infra", URL: "https://git.example/infra", Branch: "main", Subpath: "ansible"},
		Targets: []runrequests.PictureHost{
			{ID: "b", Name: "web-2", Address: "10.0.0.2", SSHUser: "root", SSHPort: 22},
			{ID: "a", Name: "web-1", Address: "10.0.0.1", SSHUser: "root", SSHPort: 22, JumpHost: "bastion"},
		},
		SSHCredential:   "deploy-key",
		VaultIdentities: []runrequests.PictureVault{{Label: "noidee", Credential: "vault"}},
		ExtraVars:       map[string]any{"a": 1},
		VarsDigest:      "d1",
	}
	fp := runrequests.Fingerprint(id, base)
	if fp != runrequests.Fingerprint(id, base) {
		t.Fatal("not deterministic")
	}
	reordered := base
	reordered.Targets = []runrequests.PictureHost{base.Targets[1], base.Targets[0]}
	if runrequests.Fingerprint(id, reordered) != fp {
		t.Fatal("host order changes the fingerprint")
	}
	if runrequests.Fingerprint(uuid.Must(uuid.NewV7()), base) == fp {
		t.Fatal("the request id is not covered")
	}
	mutations := map[string]func(p *runrequests.Picture){
		"template name": func(p *runrequests.Picture) { p.TemplateName = "x" },
		"revision":      func(p *runrequests.Picture) { p.TemplateRevision = 2 },
		"limit":         func(p *runrequests.Picture) { p.Limit = "db" },
		"only host":     func(p *runrequests.Picture) { p.OnlyHost = "a" },
		"playbook":      func(p *runrequests.Picture) { p.Playbook.Path = "other.yml" },
		"repo url":      func(p *runrequests.Picture) { r := *p.Repo; r.URL = "https://evil.example/x"; p.Repo = &r },
		"repo branch":   func(p *runrequests.Picture) { r := *p.Repo; r.Branch = "dev"; p.Repo = &r },
		"repo subpath":  func(p *runrequests.Picture) { r := *p.Repo; r.Subpath = ""; p.Repo = &r },
		"target added": func(p *runrequests.Picture) {
			p.Targets = append(append([]runrequests.PictureHost{}, p.Targets...), runrequests.PictureHost{ID: "c"})
		},
		"target address": func(p *runrequests.Picture) {
			p.Targets = append([]runrequests.PictureHost{}, p.Targets...)
			p.Targets[0].Address = "10.9.9.9"
		},
		"ssh credential": func(p *runrequests.Picture) { p.SSHCredential = "other" },
		"vault":          func(p *runrequests.Picture) { p.VaultIdentities = nil },
		"extra vars":     func(p *runrequests.Picture) { p.ExtraVars = map[string]any{"a": 2} },
		"vars digest":    func(p *runrequests.Picture) { p.VarsDigest = "d2" },
		"retry":          func(p *runrequests.Picture) { p.Retry = &runrequests.PictureRetry{JobID: "j"} },
		"follow-ups added": func(p *runrequests.Picture) {
			p.FollowUps = &runrequests.PictureFollowUps{Start: true, Templates: []runrequests.PictureFollowUp{{TemplateID: "a", TemplateName: "a", TemplateRevision: 1}}}
		},
	}
	withFollowUps := base
	withFollowUps.FollowUps = &runrequests.PictureFollowUps{Start: true, Templates: []runrequests.PictureFollowUp{
		{TemplateID: "a", TemplateName: "a", TemplateRevision: 1}, {TemplateID: "b", TemplateName: "b", TemplateRevision: 2},
	}}
	fuFP := runrequests.Fingerprint(id, withFollowUps)
	fuMutations := map[string]func(f *runrequests.PictureFollowUps){
		"start":    func(f *runrequests.PictureFollowUps) { f.Start = false },
		"revision": func(f *runrequests.PictureFollowUps) { f.Templates[0].TemplateRevision = 9 },
		"order": func(f *runrequests.PictureFollowUps) {
			f.Templates[0], f.Templates[1] = f.Templates[1], f.Templates[0]
		},
		"child": func(f *runrequests.PictureFollowUps) {
			f.Templates[0].FollowUps = []runrequests.PictureFollowUp{{TemplateID: "c"}}
		},
		"truncated": func(f *runrequests.PictureFollowUps) { f.Truncated = true },
		"stop":      func(f *runrequests.PictureFollowUps) { f.Templates[1].Stop = runrequests.StopInChain },
	}
	for name, mutate := range fuMutations {
		p := base
		f := *withFollowUps.FollowUps
		f.Templates = append([]runrequests.PictureFollowUp(nil), f.Templates...)
		mutate(&f)
		p.FollowUps = &f
		if runrequests.Fingerprint(id, p) == fuFP {
			t.Errorf("follow-ups %s does not change the fingerprint", name)
		}
	}
	for name, mutate := range mutations {
		p := base
		mutate(&p)
		if runrequests.Fingerprint(id, p) == fp {
			t.Errorf("%s does not change the fingerprint", name)
		}
	}
}

// TestCheckRunClaim: the claim of an approved run passes only while the
// run picture is the one the person approved; a run group of no request
// passes; a request put back to pending refuses its jobs.
func TestCheckRunClaim(t *testing.T) {
	w := newWiring()
	ctx := context.Background()
	if err := w.svc.CheckRunClaim(ctx, w.org, uuid.Must(uuid.NewV7())); err != nil {
		t.Fatalf("a run group of no request = %v, want nil", err)
	}
	r := w.create(t)
	_, _, fp, err := w.svc.Get(ctx, w.org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Approve(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); err != nil {
		t.Fatal(err)
	}
	if w.enq.group != r.ID {
		t.Fatalf("enqueued into run group %s, want the request's id %s", w.enq.group, r.ID)
	}
	if err := w.svc.CheckRunClaim(ctx, w.org, r.ID); err != nil {
		t.Fatalf("an unchanged approved run = %v", err)
	}
	w.pics.p.Limit = "web-*"
	if err := w.svc.CheckRunClaim(ctx, w.org, r.ID); !errors.Is(err, runrequests.ErrChangedAfterApproval) {
		t.Fatalf("a changed approved run = %v, want ErrChangedAfterApproval", err)
	}
	if err := w.store.RevertToPending(ctx, w.org, r.ID); err != nil {
		t.Fatal(err)
	}
	w.pics.p.Limit = "web"
	if err := w.svc.CheckRunClaim(ctx, w.org, r.ID); err == nil {
		t.Fatal("a reverted request's job passed the claim check")
	}

	// Approved in mode A: a follow-up's revision moving, or the setting
	// flipping, between the approval and the claim fails the run.
	followUp := uuid.Must(uuid.NewV7())
	modeA := func() *runrequests.PictureFollowUps {
		return &runrequests.PictureFollowUps{Start: true, Templates: []runrequests.PictureFollowUp{
			{TemplateID: followUp.String(), TemplateName: "listmonk", TemplateRevision: 3},
		}}
	}
	for name, change := range map[string]func(f *runrequests.PictureFollowUps){
		"follow-up revision": func(f *runrequests.PictureFollowUps) { f.Templates[0].TemplateRevision = 4 },
		"setting flipped": func(f *runrequests.PictureFollowUps) {
			f.Start = false
			f.Templates[0].TemplateRevision = 0
		},
	} {
		w.pics.p.FollowUps = modeA()
		r := w.create(t)
		_, _, fp, err := w.svc.Get(ctx, w.org, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.svc.Approve(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); err != nil {
			t.Fatalf("%s: approve = %v", name, err)
		}
		if err := w.svc.CheckRunClaim(ctx, w.org, r.ID); err != nil {
			t.Fatalf("%s: an unchanged approved run = %v", name, err)
		}
		f := modeA()
		change(f)
		w.pics.p.FollowUps = f
		if err := w.svc.CheckRunClaim(ctx, w.org, r.ID); !errors.Is(err, runrequests.ErrChangedAfterApproval) {
			t.Fatalf("%s: claim = %v, want ErrChangedAfterApproval", name, err)
		}
	}
}

// TestApproveSelection: the follow-ups an approval starts come from the
// picture it recomputed and the person's selection, validated before the
// decision.
func TestApproveSelection(t *testing.T) {
	y, z, stopped, foreign := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	modeA := &runrequests.PictureFollowUps{Start: true, Templates: []runrequests.PictureFollowUp{
		{TemplateID: y.String(), TemplateName: "y", TemplateRevision: 1},
		{TemplateID: z.String(), TemplateName: "z", TemplateRevision: 2},
		{TemplateID: stopped.String(), TemplateName: "s", Stop: runrequests.StopInChain},
	}}
	modeB := &runrequests.PictureFollowUps{Templates: []runrequests.PictureFollowUp{
		{TemplateID: y.String(), TemplateName: "y"}, {TemplateID: z.String(), TemplateName: "z"},
	}}
	truncated := &runrequests.PictureFollowUps{Start: true, Truncated: true, Templates: modeA.Templates[:2]}
	sel := func(ids ...uuid.UUID) *[]uuid.UUID { return &ids }
	cases := []struct {
		name      string
		followUps *runrequests.PictureFollowUps
		selection *[]uuid.UUID
		want      []uuid.UUID // nil with wantErr
		wantErr   error
	}{
		{"mode B omitted", modeB, nil, []uuid.UUID{}, nil},
		{"mode B empty", modeB, sel(), []uuid.UUID{}, nil},
		{"mode B selected", modeB, sel(y), nil, runrequests.ErrInvalidInput},
		{"mode A omitted", modeA, nil, []uuid.UUID{y, z}, nil},
		{"mode A empty", modeA, sel(), []uuid.UUID{}, nil},
		{"mode A subset", modeA, sel(z), []uuid.UUID{z}, nil},
		{"mode A picture order", modeA, sel(z, y), []uuid.UUID{y, z}, nil},
		{"mode A foreign", modeA, sel(y, foreign), nil, runrequests.ErrInvalidInput},
		{"mode A stop-marked", modeA, sel(stopped), nil, runrequests.ErrInvalidInput},
		{"truncated omitted", truncated, nil, []uuid.UUID{}, nil},
		{"truncated empty", truncated, sel(), []uuid.UUID{}, nil},
		{"truncated selected", truncated, sel(y), nil, runrequests.ErrInvalidInput},
		{"no follow-ups omitted", nil, nil, []uuid.UUID{}, nil},
		{"no follow-ups selected", nil, sel(y), nil, runrequests.ErrInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWiring()
			w.pics.p.FollowUps = tc.followUps
			r := w.create(t)
			_, _, fp, err := w.svc.Get(context.Background(), w.org, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			got, err := w.svc.Approve(context.Background(), w.org, r.ID, uuid.Must(uuid.NewV7()), fp, tc.selection)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("approve = %v, want %v", err, tc.wantErr)
				}
				if len(w.enq.runs) != 0 {
					t.Fatal("a refused selection was enqueued")
				}
				if after, _, _, _ := w.svc.Get(context.Background(), w.org, r.ID); after.Status != runrequests.StatusPending {
					t.Fatalf("status after the refusal = %s, want pending", after.Status)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if w.enq.list == nil || !slices.Equal(w.enq.list, tc.want) {
				t.Fatalf("enqueued follow-ups = %v (nil %t), want %v", w.enq.list, w.enq.list == nil, tc.want)
			}
			started := make([]uuid.UUID, len(got.Started))
			for i, n := range got.Started {
				started[i] = uuid.MustParse(n.TemplateID)
			}
			if !slices.Equal(started, tc.want) {
				t.Fatalf("Started = %v, want %v", started, tc.want)
			}
			if got.FollowUps != tc.followUps {
				t.Fatalf("FollowUps = %+v, want the picture's", got.FollowUps)
			}
		})
	}

	t.Run("a changed fingerprint wins", func(t *testing.T) {
		w := newWiring()
		w.pics.p.FollowUps = modeB
		r := w.create(t)
		_, _, fp, _ := w.svc.Get(context.Background(), w.org, r.ID)
		w.pics.p.FollowUps = modeA
		if _, err := w.svc.Approve(context.Background(), w.org, r.ID, uuid.Must(uuid.NewV7()), fp, sel(foreign)); !errors.Is(err, runrequests.ErrChanged) {
			t.Fatalf("approve = %v, want ErrChanged", err)
		}
	})
}
