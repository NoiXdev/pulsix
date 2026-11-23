package runrequests_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/chain"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/templates"
)

func id() uuid.UUID { return uuid.Must(uuid.NewV7()) }

type fakeTemplates map[uuid.UUID]templates.Template

func (f fakeTemplates) Get(_ context.Context, _, tid uuid.UUID) (templates.Template, error) {
	t, ok := f[tid]
	if !ok {
		return templates.Template{}, templates.ErrNotFound
	}
	return t, nil
}

type fakeSettings struct{ on bool }

func (f fakeSettings) StartsFollowUps(context.Context, uuid.UUID) (bool, error) { return f.on, nil }

type fakeFollowUpRuns struct {
	rows     []chain.FollowUp
	clearErr error
	calls    *[]string
}

func (f fakeFollowUpRuns) ListForGroup(context.Context, uuid.UUID, uuid.UUID) ([]chain.FollowUp, error) {
	return f.rows, nil
}

func (f fakeFollowUpRuns) ClearUnstarted(context.Context, uuid.UUID, uuid.UUID) error {
	*f.calls = append(*f.calls, "clear")
	return f.clearErr
}

type fakeJobRunner struct {
	calls       *[]string
	group       uuid.UUID
	retryErr    error
	enqueued    *jobs.RunRequest
	retryList   *[]uuid.UUID
	retryCalled bool
}

func (f *fakeJobRunner) RunTargets(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

func (f *fakeJobRunner) RetryTargets(context.Context, uuid.UUID, uuid.UUID) (uuid.UUID, uuid.UUID, []uuid.UUID, error) {
	return uuid.Nil, f.group, nil, f.retryErr
}

func (f *fakeJobRunner) EnqueueRunWith(_ context.Context, _, _ uuid.UUID, req jobs.RunRequest) ([]jobs.Job, error) {
	*f.calls = append(*f.calls, "enqueue")
	f.enqueued = &req
	return nil, nil
}

func (f *fakeJobRunner) RetryFailedInto(_ context.Context, _, _ uuid.UUID, _ *uuid.UUID, _ uuid.UUID, fu *[]uuid.UUID) ([]jobs.Job, error) {
	*f.calls = append(*f.calls, "retry")
	f.retryList, f.retryCalled = fu, true
	return nil, nil
}

type fakeCreds struct{}

func (fakeCreds) Get(context.Context, uuid.UUID, uuid.UUID) (credentials.Credential, error) {
	return credentials.Credential{}, errors.New("none")
}

type resolverFx struct {
	res   *runrequests.Resolver
	jobs  *fakeJobRunner
	calls []string
	org   uuid.UUID
}

func newResolverFx(tmpls fakeTemplates, on bool, rows []chain.FollowUp, clearErr error) *resolverFx {
	fx := &resolverFx{org: id()}
	fx.jobs = &fakeJobRunner{calls: &fx.calls, group: id()}
	fx.res = runrequests.NewResolver(runrequests.Sources{
		Templates: tmpls, Jobs: fx.jobs, Credentials: fakeCreds{}, Vars: &runVars{}, VarsKey: testVarsKey,
		Settings: fakeSettings{on}, FollowUpRuns: fakeFollowUpRuns{rows: rows, clearErr: clearErr, calls: &fx.calls},
	})
	return fx
}

func TestPictureFollowUpsOfARun(t *testing.T) {
	x, y := id(), id()
	tmpls := fakeTemplates{
		x: {ID: x, Name: "X", FollowUps: []templates.FollowUp{{TemplateID: y, Name: "Y"}}},
		y: {ID: y, Name: "Y", Revision: 4},
	}
	req := runrequests.Request{Kind: runrequests.KindRun, TemplateID: x}

	p, err := newResolverFx(tmpls, false, nil, nil).res.Picture(context.Background(), id(), req)
	if err != nil {
		t.Fatal(err)
	}
	if p.FollowUps == nil || p.FollowUps.Start || len(p.FollowUps.Templates) != 1 ||
		p.FollowUps.Templates[0].TemplateID != y.String() || p.FollowUps.Templates[0].TemplateName != "Y" ||
		p.FollowUps.Templates[0].TemplateRevision != 0 {
		t.Fatalf("setting off: %+v", p.FollowUps)
	}

	p, err = newResolverFx(tmpls, true, nil, nil).res.Picture(context.Background(), id(), req)
	if err != nil {
		t.Fatal(err)
	}
	if p.FollowUps == nil || !p.FollowUps.Start || len(p.FollowUps.Templates) != 1 || p.FollowUps.Templates[0].TemplateRevision != 4 {
		t.Fatalf("setting on: %+v", p.FollowUps)
	}

	tmpls[x] = templates.Template{ID: x, Name: "X"}
	p, err = newResolverFx(tmpls, true, nil, nil).res.Picture(context.Background(), id(), req)
	if err != nil {
		t.Fatal(err)
	}
	if p.FollowUps != nil {
		t.Fatalf("no follow-ups: %+v", p.FollowUps)
	}
}

// countingTemplates counts the reads per template id, found or not.
type countingTemplates struct {
	fakeTemplates
	reads map[uuid.UUID]int
}

func (c countingTemplates) Get(ctx context.Context, org, tid uuid.UUID) (templates.Template, error) {
	c.reads[tid]++
	return c.fakeTemplates.Get(ctx, org, tid)
}

func TestPictureReadsEachFollowUpTemplateOnce(t *testing.T) {
	// A diamond X -> {Y, Z}, Y -> W, Z -> W, and both W's name a deleted G.
	x, y, z, w, gone := id(), id(), id(), id(), id()
	src := countingTemplates{reads: map[uuid.UUID]int{}, fakeTemplates: fakeTemplates{
		x: {ID: x, Name: "X", FollowUps: []templates.FollowUp{{TemplateID: y, Name: "Y"}, {TemplateID: z, Name: "Z"}}},
		y: {ID: y, Name: "Y", FollowUps: []templates.FollowUp{{TemplateID: w, Name: "W"}}},
		z: {ID: z, Name: "Z", FollowUps: []templates.FollowUp{{TemplateID: w, Name: "W"}}},
		w: {ID: w, Name: "W", Revision: 7, FollowUps: []templates.FollowUp{{TemplateID: gone, Name: "G"}}},
	}}
	fx := &resolverFx{org: id()}
	fx.jobs = &fakeJobRunner{calls: &fx.calls, group: id()}
	fx.res = runrequests.NewResolver(runrequests.Sources{
		Templates: src, Jobs: fx.jobs, Credentials: fakeCreds{}, Vars: &runVars{}, VarsKey: testVarsKey,
		Settings: fakeSettings{true}, FollowUpRuns: fakeFollowUpRuns{calls: &fx.calls},
	})

	p, err := fx.res.Picture(context.Background(), fx.org, runrequests.Request{Kind: runrequests.KindRun, TemplateID: x})
	if err != nil {
		t.Fatal(err)
	}
	if p.FollowUps == nil || len(p.FollowUps.Templates) != 2 {
		t.Fatalf("picture: %+v", p.FollowUps)
	}
	for i, branch := range p.FollowUps.Templates {
		if len(branch.FollowUps) != 1 || branch.FollowUps[0].TemplateID != w.String() || branch.FollowUps[0].TemplateRevision != 7 ||
			len(branch.FollowUps[0].FollowUps) != 1 || branch.FollowUps[0].FollowUps[0].Stop != runrequests.StopDeleted {
			t.Fatalf("branch %d: %+v", i, branch)
		}
	}
	if src.reads[w] != 1 || src.reads[gone] != 1 {
		t.Fatalf("reads: W %d, deleted %d, want 1 each", src.reads[w], src.reads[gone])
	}
}

func TestPictureFollowUpsOfARetry(t *testing.T) {
	a, x, y, z, gone := id(), id(), id(), id(), id()
	tmpls := fakeTemplates{
		x: {ID: x, Name: "X"},
		// Y lists A, which is on the retry row's own path [a, x]: it must stop as in_chain.
		y: {ID: y, Name: "Y", Revision: 2, FollowUps: []templates.FollowUp{{TemplateID: a, Name: "A"}}},
		z: {ID: z, Name: "Z", Revision: 3},
		a: {ID: a, Name: "A", Revision: 1},
	}
	rows := []chain.FollowUp{
		{TemplateID: y, TemplateName: "Y", Depth: 2, Path: []uuid.UUID{a, x}},
		{TemplateID: gone, TemplateName: "", Depth: 2, Path: []uuid.UUID{a, x}},
		{TemplateID: z, TemplateName: "Z", Depth: 10, Path: []uuid.UUID{a, x}},
	}
	job := id()
	req := runrequests.Request{Kind: runrequests.KindRetryFailed, TemplateID: x, JobID: &job}

	fx := newResolverFx(tmpls, true, rows, nil)
	p, err := fx.res.Picture(context.Background(), id(), req)
	if err != nil {
		t.Fatal(err)
	}
	if p.FollowUps == nil || len(p.FollowUps.Templates) != 3 {
		t.Fatalf("picture: %+v", p.FollowUps)
	}
	if got := p.FollowUps.Templates[0]; got.TemplateID != y.String() || got.TemplateRevision != 2 || got.Stop != "" ||
		len(got.FollowUps) != 1 || got.FollowUps[0].TemplateID != a.String() || got.FollowUps[0].Stop != runrequests.StopInChain {
		t.Fatalf("first: %+v", got)
	}
	if got := p.FollowUps.Templates[1]; got.TemplateID != gone.String() || got.Stop != runrequests.StopDeleted {
		t.Fatalf("second: %+v", got)
	}
	if got := p.FollowUps.Templates[2]; got.TemplateID != z.String() || got.Stop != runrequests.StopTooDeep {
		t.Fatalf("third: %+v", got)
	}

	fx = newResolverFx(tmpls, true, rows, nil)
	fx.jobs.retryErr = errors.New("nothing to retry")
	p, err = fx.res.Picture(context.Background(), id(), req)
	if err != nil {
		t.Fatal(err)
	}
	if p.FollowUps != nil {
		t.Fatalf("retry targets error: %+v", p.FollowUps)
	}
}

func TestRunClearsUnstartedFollowUpsFirst(t *testing.T) {
	y := id()
	tpl, actor := id(), id()
	for _, tc := range []struct {
		name string
		in   []uuid.UUID
		want []uuid.UUID
	}{{"nil", nil, []uuid.UUID{}}, {"chosen", []uuid.UUID{y}, []uuid.UUID{y}}} {
		t.Run("run "+tc.name, func(t *testing.T) {
			fx := newResolverFx(nil, false, nil, nil)
			if err := fx.res.Run(context.Background(), fx.org, tpl, nil, actor, "k", id(), tc.in); err != nil {
				t.Fatal(err)
			}
			if len(fx.calls) != 2 || fx.calls[0] != "clear" || fx.calls[1] != "enqueue" {
				t.Fatalf("order: %v", fx.calls)
			}
			got := fx.jobs.enqueued.FollowUps
			if got == nil || *got == nil || len(*got) != len(tc.want) || (len(tc.want) == 1 && (*got)[0] != y) {
				t.Fatalf("follow-ups: %v", got)
			}
		})
		t.Run("retry "+tc.name, func(t *testing.T) {
			fx := newResolverFx(nil, false, nil, nil)
			if err := fx.res.RetryFailed(context.Background(), fx.org, id(), actor, id(), tc.in); err != nil {
				t.Fatal(err)
			}
			if len(fx.calls) != 2 || fx.calls[0] != "clear" || fx.calls[1] != "retry" {
				t.Fatalf("order: %v", fx.calls)
			}
			got := fx.jobs.retryList
			if got == nil || *got == nil || len(*got) != len(tc.want) || (len(tc.want) == 1 && (*got)[0] != y) {
				t.Fatalf("follow-ups: %v", got)
			}
		})
	}
}

func TestClearErrorAbortsTheEnqueue(t *testing.T) {
	boom := errors.New("boom")
	fx := newResolverFx(nil, false, nil, boom)
	if err := fx.res.Run(context.Background(), fx.org, id(), nil, id(), "k", id(), nil); !errors.Is(err, boom) {
		t.Fatalf("run: %v", err)
	}
	if err := fx.res.RetryFailed(context.Background(), fx.org, id(), id(), id(), nil); !errors.Is(err, boom) {
		t.Fatalf("retry: %v", err)
	}
	if fx.jobs.enqueued != nil || fx.jobs.retryCalled {
		t.Fatal("enqueued despite the clear error")
	}
}
