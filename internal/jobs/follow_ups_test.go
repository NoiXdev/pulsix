package jobs_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

func id7() uuid.UUID { return uuid.Must(uuid.NewV7()) }

type planCall struct {
	org, group, template uuid.UUID
	override             *[]uuid.UUID
	chain                jobs.ChainContext
	jobsAtCall           int
}

type copyCall struct {
	org, from, to uuid.UUID
	by            *uuid.UUID
	jobsAtCall    int
}

// fakePlanner records the planning calls and how many jobs the store held
// when each arrived.
type fakePlanner struct {
	store   *fakeStore
	plans   []planCall
	copies  []copyCall
	planErr error
	copyErr error
}

func (f *fakePlanner) count() int {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	return len(f.store.jobs)
}

func (f *fakePlanner) PlanFollowUps(_ context.Context, org, group, tpl uuid.UUID, override *[]uuid.UUID, c jobs.ChainContext) error {
	f.plans = append(f.plans, planCall{org, group, tpl, override, c, f.count()})
	return f.planErr
}

func (f *fakePlanner) CopyFollowUps(_ context.Context, org, from, to uuid.UUID, by *uuid.UUID) error {
	f.copies = append(f.copies, copyCall{org, from, to, by, f.count()})
	return f.copyErr
}

type chainFixture struct {
	org, tpl, zone, h1, h2, h3 uuid.UUID
	store                      *fakeStore
	res                        *fakeResolver
	tmpl                       *fakeTemplates
	planner                    *fakePlanner
	svc                        *jobs.Service
}

func newChainFixture(t *testing.T) chainFixture {
	t.Helper()
	f := chainFixture{org: id7(), tpl: id7(), zone: id7(), h1: id7(), h2: id7(), h3: id7(), store: newFakeStore()}
	f.tmpl = &fakeTemplates{byID: map[uuid.UUID]jobs.TemplateRow{
		f.tpl: {ID: f.tpl, OrgID: f.org, Scope: json.RawMessage(`{}`)},
	}}
	f.res = &fakeResolver{targets: []jobs.ZoneTargets{{ZoneID: f.zone, HostIDs: []uuid.UUID{f.h1, f.h2}}}}
	f.svc = newService(f.store, &fakeQueue{}, f.tmpl, f.res, &fakeHosts{})
	f.planner = &fakePlanner{store: f.store}
	f.svc.SetFollowUpPlanner(f.planner)
	return f
}

func (f chainFixture) addTemplate() uuid.UUID {
	id := id7()
	f.tmpl.byID[id] = jobs.TemplateRow{ID: id, OrgID: f.org, Scope: json.RawMessage(`{}`)}
	return id
}

func TestValidateChainTrigger(t *testing.T) {
	ref := id7()
	chain := &jobs.ChainContext{RootTrigger: jobs.TriggerManual, Depth: 1}
	none := []uuid.UUID{}
	cases := []struct {
		name string
		req  jobs.RunRequest
		ok   bool
	}{
		{"chain with ref and context", jobs.RunRequest{Trigger: jobs.TriggerChain, TriggerRef: &ref, Chain: chain}, true},
		{"chain without ref", jobs.RunRequest{Trigger: jobs.TriggerChain, Chain: chain}, false},
		{"chain without context", jobs.RunRequest{Trigger: jobs.TriggerChain, TriggerRef: &ref}, false},
		{"context on manual", jobs.RunRequest{Trigger: jobs.TriggerManual, Chain: chain}, false},
		{"context on schedule", jobs.RunRequest{Trigger: jobs.TriggerSchedule, TriggerRef: &ref, Chain: chain}, false},
		{"follow-ups on manual", jobs.RunRequest{Trigger: jobs.TriggerManual, FollowUps: &none}, true},
		{"follow-ups on schedule", jobs.RunRequest{Trigger: jobs.TriggerSchedule, TriggerRef: &ref, FollowUps: &none}, false},
		{"follow-ups on chain", jobs.RunRequest{Trigger: jobs.TriggerChain, TriggerRef: &ref, Chain: chain, FollowUps: &none}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newChainFixture(t)
			_, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, c.req)
			if c.ok && err != nil {
				t.Fatalf("err = %v", err)
			}
			if !c.ok && !errors.Is(err, jobs.ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestOnlyHostsIntersectsZones(t *testing.T) {
	f := newChainFixture(t)
	z2, h9 := id7(), id7()
	f.res.targets = []jobs.ZoneTargets{
		{ZoneID: f.zone, HostIDs: []uuid.UUID{f.h1, f.h2}},
		{ZoneID: z2, HostIDs: []uuid.UUID{f.h3}},
	}
	out, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, jobs.RunRequest{
		Trigger: jobs.TriggerManual, OnlyHosts: []uuid.UUID{f.h2, f.h3, h9},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("jobs = %d, want 2", len(out))
	}
	got := map[uuid.UUID][]uuid.UUID{}
	for _, j := range out {
		got[j.ZoneID] = j.HostIDs
	}
	if !slices.Equal(got[f.zone], []uuid.UUID{f.h2}) || !slices.Equal(got[z2], []uuid.UUID{f.h3}) {
		t.Fatalf("zones = %v, want {h2} and {h3}", got)
	}
}

func TestOnlyHostsNoneLeft(t *testing.T) {
	f := newChainFixture(t)
	_, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, jobs.RunRequest{
		Trigger: jobs.TriggerManual, OnlyHosts: []uuid.UUID{id7()},
	})
	if !errors.Is(err, jobs.ErrHostNotInScope) {
		t.Fatalf("err = %v, want ErrHostNotInScope", err)
	}
	f.res.targets = nil
	_, err = f.svc.EnqueueRunWith(ctx, f.org, f.tpl, jobs.RunRequest{
		Trigger: jobs.TriggerManual, OnlyHosts: []uuid.UUID{f.h1},
	})
	if !errors.Is(err, jobs.ErrHostNotInScope) {
		t.Fatalf("empty scope: err = %v, want ErrHostNotInScope", err)
	}
}

func TestEnqueuePlansBeforeJobs(t *testing.T) {
	f := newChainFixture(t)
	actor := id7()
	out, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, jobs.RunRequest{Trigger: jobs.TriggerManual, Actor: &actor})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.planner.plans) != 1 {
		t.Fatalf("plans = %d", len(f.planner.plans))
	}
	p := f.planner.plans[0]
	if p.jobsAtCall != 0 {
		t.Fatalf("store held %d jobs when planning ran, want 0", p.jobsAtCall)
	}
	if p.org != f.org || p.template != f.tpl || p.group != out[0].RunGroupID || p.override != nil {
		t.Fatalf("plan call = %+v", p)
	}
	c := p.chain
	if c.RootTrigger != jobs.TriggerManual || c.RootRef != nil || c.Depth != 1 ||
		!slices.Equal(c.Path, []uuid.UUID{f.tpl}) || c.ConfiguredBy == nil || *c.ConfiguredBy != actor {
		t.Fatalf("chain = %+v", c)
	}
}

func TestEnqueueChainContextExtends(t *testing.T) {
	f := newChainFixture(t)
	a, ref := id7(), id7()
	root := id7()
	_, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, jobs.RunRequest{
		Trigger: jobs.TriggerChain, TriggerRef: &ref,
		Chain: &jobs.ChainContext{RootTrigger: jobs.TriggerSchedule, RootRef: &root, Depth: 2, Path: []uuid.UUID{a}},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := f.planner.plans[0].chain
	if c.Depth != 3 || !slices.Equal(c.Path, []uuid.UUID{a, f.tpl}) || c.RootTrigger != jobs.TriggerSchedule || *c.RootRef != root {
		t.Fatalf("chain = %+v", c)
	}
}

func TestEnqueueRejectsBadOverride(t *testing.T) {
	f := newChainFixture(t)
	other := f.addTemplate()
	many := make([]uuid.UUID, 0, 11)
	for range 11 {
		many = append(many, f.addTemplate())
	}
	cases := map[string][]uuid.UUID{
		"self":      {f.tpl},
		"duplicate": {other, other},
		"eleven":    many,
		"unknown":   {id7()},
	}
	for name, ids := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, jobs.RunRequest{Trigger: jobs.TriggerManual, FollowUps: &ids})
			if !errors.Is(err, jobs.ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
	if len(f.planner.plans) != 0 || len(f.store.jobs) != 0 {
		t.Fatal("a rejected override must plan nothing and enqueue nothing")
	}
	ids := []uuid.UUID{other}
	if _, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, jobs.RunRequest{Trigger: jobs.TriggerManual, FollowUps: &ids}); err != nil {
		t.Fatal(err)
	}
	if o := f.planner.plans[0].override; o == nil || !slices.Equal(*o, ids) {
		t.Fatalf("override = %v", o)
	}
}

func TestEnqueuePlannerErrorFailsEnqueue(t *testing.T) {
	f := newChainFixture(t)
	f.planner.planErr = errors.New("boom")
	if _, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, jobs.RunRequest{Trigger: jobs.TriggerManual}); err == nil {
		t.Fatal("want error")
	}
	if len(f.store.jobs) != 0 {
		t.Fatal("no job may exist after a planner error")
	}
}

func TestEnqueuePlansWithClaimedGroup(t *testing.T) {
	f := newChainFixture(t)
	req := jobs.RunRequest{Trigger: jobs.TriggerManual, IdempotencyKey: "k"}
	first, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, req); err != nil {
		t.Fatal(err)
	}
	for i, p := range f.planner.plans {
		if p.group != first[0].RunGroupID {
			t.Fatalf("plan %d group = %s, want the claimed %s", i, p.group, first[0].RunGroupID)
		}
	}
	if len(f.planner.plans) != 2 {
		t.Fatalf("plans = %d, want 2", len(f.planner.plans))
	}
}

func TestRetryCopiesFollowUps(t *testing.T) {
	f := newChainFixture(t)
	orig, grp, actor := id7(), id7(), id7()
	f.store.jobs[orig] = storedJob{j: jobs.Job{
		ID: orig, OrgID: f.org, Kind: jobs.KindRun, Status: jobs.StatusFailed,
		TemplateID: &f.tpl, HostIDs: []uuid.UUID{f.h1}, RunGroupID: grp,
		Stats: map[string]any{jobs.StatsFailedHosts: []any{f.h1.String()}},
	}}
	retried, err := f.svc.RetryFailed(ctx, f.org, orig, &actor)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.planner.copies) != 1 || len(f.planner.plans) != 0 {
		t.Fatalf("copies = %d plans = %d", len(f.planner.copies), len(f.planner.plans))
	}
	c := f.planner.copies[0]
	if c.org != f.org || c.from != grp || c.to != retried[0].RunGroupID || c.by == nil || *c.by != actor {
		t.Fatalf("copy = %+v, retry group %s", c, retried[0].RunGroupID)
	}
	if c.jobsAtCall != 1 {
		t.Fatalf("store held %d jobs at copy, want only the original", c.jobsAtCall)
	}

	f.planner.copyErr = errors.New("boom")
	before := len(f.store.jobs)
	if _, err := f.svc.RetryFailed(ctx, f.org, orig, &actor); err == nil {
		t.Fatal("want error")
	}
	if len(f.store.jobs) != before {
		t.Fatal("a planner error must fail the retry before any job is created")
	}
}

// TestApprovedRunRequestPlansFollowUpsInItsGroup: an approved run request
// (spec 2026-11-16 assistant access) enqueues into its own id as the run
// group, as a manual run or as a retry. Its follow-ups must be planned, or
// copied, for that group, the one the run-group sink later finishes, and
// the chain's root is the manual trigger run_follow_ups.root_trigger
// accepts.
func TestApprovedRunRequestPlansFollowUpsInItsGroup(t *testing.T) {
	t.Run("run", func(t *testing.T) {
		f := newChainFixture(t)
		request, actor := id7(), id7()
		got, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, jobs.RunRequest{
			Trigger: jobs.TriggerManual, Actor: &actor, IdempotencyKey: "run-request:" + request.String(), RunGroupID: request,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 0 || got[0].RunGroupID != request {
			t.Fatalf("jobs = %+v, want them in the request's group %s", got, request)
		}
		if len(f.planner.plans) != 1 {
			t.Fatalf("plans = %d, want 1", len(f.planner.plans))
		}
		p := f.planner.plans[0]
		if p.group != request || p.template != f.tpl || p.chain.RootTrigger != jobs.TriggerManual || p.chain.Depth != 1 ||
			p.chain.ConfiguredBy == nil || *p.chain.ConfiguredBy != actor {
			t.Fatalf("plan = %+v, want the request's group, a manual root and the actor", p)
		}
	})
	t.Run("retry", func(t *testing.T) {
		f := newChainFixture(t)
		orig, grp, request, actor := id7(), id7(), id7(), id7()
		f.store.jobs[orig] = storedJob{j: jobs.Job{
			ID: orig, OrgID: f.org, Kind: jobs.KindRun, Status: jobs.StatusFailed,
			TemplateID: &f.tpl, HostIDs: []uuid.UUID{f.h1}, RunGroupID: grp,
			Stats: map[string]any{jobs.StatsFailedHosts: []any{f.h1.String()}},
		}}
		got, err := f.svc.RetryFailedInto(ctx, f.org, orig, &actor, request)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 0 || got[0].RunGroupID != request {
			t.Fatalf("jobs = %+v, want them in the request's group %s", got, request)
		}
		if len(f.planner.copies) != 1 || len(f.planner.plans) != 0 {
			t.Fatalf("copies = %d plans = %d", len(f.planner.copies), len(f.planner.plans))
		}
		if c := f.planner.copies[0]; c.from != grp || c.to != request || c.by == nil || *c.by != actor {
			t.Fatalf("copy = %+v, want from %s to the request's group %s", c, grp, request)
		}
	})
}

type countSink struct {
	calls int
	err   error
}

func (c *countSink) RunGroupFinished(context.Context, jobs.RunGroupResult) error {
	c.calls++
	return c.err
}

func TestRunGroupSinksCallsAllAndJoinsErrors(t *testing.T) {
	e1, e2 := errors.New("one"), errors.New("two")
	a, b, c := &countSink{err: e1}, &countSink{}, &countSink{err: e2}
	err := jobs.RunGroupSinks{a, b, c}.RunGroupFinished(ctx, jobs.RunGroupResult{})
	if a.calls != 1 || b.calls != 1 || c.calls != 1 {
		t.Fatalf("calls = %d %d %d", a.calls, b.calls, c.calls)
	}
	if !errors.Is(err, e1) || !errors.Is(err, e2) {
		t.Fatalf("err = %v", err)
	}
	if err := (jobs.RunGroupSinks{b}).RunGroupFinished(ctx, jobs.RunGroupResult{}); err != nil {
		t.Fatal(err)
	}
}

func TestEnqueueOverrideAcceptsNotReadyTemplate(t *testing.T) {
	f := newChainFixture(t)
	other := f.addTemplate()
	ids := []uuid.UUID{other}
	src := &notReadyTemplates{fakeTemplates: f.tmpl, notReady: other}
	// Fallback path: a lookup that answers ErrTemplateNotReady still counts as existing.
	svc2 := jobs.NewService(f.store, &fakeQueue{}, src, f.res, &fakeHosts{}, nil, fakeSealer{}, time.Now, rand.Reader)
	svc2.SetFollowUpPlanner(f.planner)
	if _, err := svc2.EnqueueRunWith(ctx, f.org, f.tpl, jobs.RunRequest{Trigger: jobs.TriggerManual, FollowUps: &ids}); err != nil {
		t.Fatalf("not-ready follow-up must be accepted: %v", err)
	}
}

func TestEnqueueOverrideWrapsLookupErrorWithID(t *testing.T) {
	f := newChainFixture(t)
	other := f.addTemplate()
	ids := []uuid.UUID{other}
	boom := errors.New("boom")
	src := &notReadyTemplates{fakeTemplates: f.tmpl, failID: other, failErr: boom}
	svc := jobs.NewService(f.store, &fakeQueue{}, src, f.res, &fakeHosts{}, nil, fakeSealer{}, time.Now, rand.Reader)
	svc.SetFollowUpPlanner(f.planner)
	_, err := svc.EnqueueRunWith(ctx, f.org, f.tpl, jobs.RunRequest{Trigger: jobs.TriggerManual, FollowUps: &ids})
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "check follow-up template "+other.String()) {
		t.Fatalf("err = %v", err)
	}
}

// notReadyTemplates answers ErrTemplateNotReady for one id and a custom
// error for another, like a lookup over unregistered playbooks.
type notReadyTemplates struct {
	*fakeTemplates
	notReady, failID uuid.UUID
	failErr          error
}

func (n *notReadyTemplates) TemplateLookup(c context.Context, org, id uuid.UUID) (jobs.TemplateRow, error) {
	switch id {
	case n.notReady:
		return jobs.TemplateRow{}, jobs.ErrTemplateNotReady
	case n.failID:
		return jobs.TemplateRow{}, n.failErr
	}
	return n.fakeTemplates.TemplateLookup(c, org, id)
}

func TestStartedRunGroupReportsAKeyedEnqueue(t *testing.T) {
	f := newChainFixture(t)
	ref := id7()
	req := jobs.RunRequest{Trigger: jobs.TriggerChain, TriggerRef: &ref, Chain: &jobs.ChainContext{RootTrigger: jobs.TriggerManual, Depth: 1}, IdempotencyKey: "chain:x"}
	if got, err := f.svc.StartedRunGroup(ctx, f.org, "chain:x"); err != nil || got != nil {
		t.Fatalf("before = %v, %v", got, err)
	}
	created, err := f.svc.EnqueueRunWith(ctx, f.org, f.tpl, req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.StartedRunGroup(ctx, f.org, "chain:x")
	if err != nil || got == nil || *got != created[0].RunGroupID {
		t.Fatalf("after = %v, %v, want %s", got, err, created[0].RunGroupID)
	}
}
