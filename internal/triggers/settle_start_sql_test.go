package triggers_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/triggers"
)

// TestSettleTwoManagersNeverStartTheSameRunTwice: two managers' tickers
// tick at once; the row one of them is starting is skipped by the other
// (SKIP LOCKED), and a delivery arriving meanwhile opens a new window
// instead of joining the one being started.
func TestSettleTwoManagersNeverStartTheSameRunTwice(t *testing.T) {
	w := newSettleWorld(t, "settle-managers")
	other := w.manager(triggers.NewSQLStore(w.d))
	otherTicker := triggers.NewSettleTicker(other, time.Second, nil, nil)
	c := w.create(t, 5, nil)
	first := w.deliver(t, c, "v1")
	w.clock.Add(5 * time.Second)

	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	w.runs.onEnqueue = func() { once.Do(func() { close(entered); <-release }) }
	var released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	defer unblock()
	done := make(chan error, 1)
	go func() {
		_, err := w.ticker.Tick(ctx)
		done <- err
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("the first manager's tick started nothing: %v", err)
	}
	n, err := otherTicker.Tick(ctx)
	if err != nil || n != 0 {
		t.Fatalf("second manager's tick = %d, %v; want the locked row skipped", n, err)
	}
	late := make(chan sent, 1)
	go func() {
		res, err := send(ctx, other, c, "v2")
		late <- sent{res, err}
	}()
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := <-late
	if got.err != nil {
		t.Fatal(got.err)
	}
	res := got.res
	if runs := w.started(); len(runs) != 1 || runs[0].Overrides.ExtraVars["release"] != "v1" {
		t.Fatalf("runs = %+v", runs)
	}
	if res.Outcome != triggers.OutcomeCollected || *res.PendingID == *first.PendingID {
		t.Fatalf("the late delivery = %+v; want a new window", res)
	}
	if p := w.pending(t, c.Trigger.ID); p == nil || p.ID != *res.PendingID || p.Deliveries != 1 {
		t.Fatalf("pending = %+v", p)
	}
}

// TestSettleRunNowAndDiscard: a person starts a pending run at once, or
// discards it; either answers ErrNothingPending when there is none.
func TestSettleRunNowAndDiscard(t *testing.T) {
	w := newSettleWorld(t, "settle-now")
	c := w.create(t, 600, nil)
	w.deliver(t, c, "v1")
	person := uuid.Must(uuid.NewV7())
	res, err := w.svc.RunPending(w.scoped, w.org, c.Trigger.ID, person)
	if err != nil || res.Outcome != triggers.OutcomeTriggered || res.RunID == nil {
		t.Fatalf("RunPending = %+v, %v", res, err)
	}
	if runs := w.started(); len(runs) != 1 || runs[0].Overrides.ExtraVars["release"] != "v1" || runs[0].Actor == nil || *runs[0].Actor != person {
		t.Fatalf("runs = %+v", runs)
	}
	if e := w.audit.events; len(e) != 1 || e[0].ActorKind != audit.ActorKindUser || *e[0].ActorID != person || e[0].Meta["trigger_id"] != c.Trigger.ID.String() {
		t.Fatalf("run-now audit = %+v", e)
	}
	if ds := w.deliveries(t, c.Trigger.ID); ds[0].JobGroupID == nil || *ds[0].JobGroupID != w.runs.groups[0] {
		t.Fatalf("deliveries = %+v", ds)
	}
	if _, err := w.svc.RunPending(w.scoped, w.org, c.Trigger.ID, uuid.Must(uuid.NewV7())); !errors.Is(err, triggers.ErrNothingPending) {
		t.Fatalf("RunPending with nothing pending = %v", err)
	}

	w.deliver(t, c, "v2")
	if err := w.svc.DiscardPending(w.scoped, w.org, c.Trigger.ID); err != nil {
		t.Fatal(err)
	}
	if p := w.pending(t, c.Trigger.ID); p != nil {
		t.Fatalf("pending after discard = %+v", p)
	}
	if ds := w.deliveries(t, c.Trigger.ID); ds[0].JobGroupID != nil || !strings.Contains(ds[0].Reason, "discarded") {
		t.Fatalf("discarded delivery = %+v", ds[0])
	}
	w.clock.Add(time.Hour)
	w.tick(t)
	if n := len(w.started()); n != 1 {
		t.Fatalf("%d runs after a discard", n)
	}
	if err := w.svc.DiscardPending(w.scoped, w.org, c.Trigger.ID); !errors.Is(err, triggers.ErrNothingPending) {
		t.Fatalf("DiscardPending with nothing pending = %v", err)
	}
	stranger := uuid.Must(uuid.NewV7())
	for name, call := range map[string]func() error{
		"pending": func() error {
			_, err := w.svc.Pending(db.WithOrgScope(ctx, stranger), stranger, c.Trigger.ID)
			return err
		},
		"run": func() error {
			_, err := w.svc.RunPending(db.WithOrgScope(ctx, stranger), stranger, c.Trigger.ID, stranger)
			return err
		},
		"discard": func() error { return w.svc.DiscardPending(db.WithOrgScope(ctx, stranger), stranger, c.Trigger.ID) },
	} {
		if err := call(); !errors.Is(err, triggers.ErrNotFound) {
			t.Fatalf("another org's %s = %v", name, err)
		}
	}
}

// TestSettleRecordsAFailedStart: a collected run the jobs service refuses
// is dropped and its deliveries record the failure.
func TestSettleRecordsAFailedStart(t *testing.T) {
	w := newSettleWorld(t, "settle-fail")
	c := w.create(t, 5, nil)
	w.deliver(t, c, "v1")
	w.runs.err = jobs.ErrEmptyScope
	w.clock.Add(5 * time.Second)
	w.tick(t)
	if p := w.pending(t, c.Trigger.ID); p != nil {
		t.Fatalf("pending after a failed start = %+v", p)
	}
	ds := w.deliveries(t, c.Trigger.ID)
	if ds[0].Outcome != triggers.OutcomeFailed || ds[0].Reason != "the template's scope resolves to no host" {
		t.Fatalf("delivery = %+v", ds[0])
	}
}

// TestSettleRepoSyncStartsOneSync: a settling repository sync trigger
// starts one sync for its collected deliveries.
func TestSettleRepoSyncStartsOneSync(t *testing.T) {
	w := newSettleWorld(t, "settle-sync")
	c, err := w.svc.Create(w.scoped, w.org, uuid.Must(uuid.NewV7()), triggers.Input{Kind: triggers.KindRepoSync, RepoID: &w.repo, Name: "sync",
		Enabled: true, Preset: triggers.PresetPulsix, RateLimit: triggers.RateLimit{Count: 1000, Per: "1s"}, SettleSeconds: 5})
	if err != nil {
		t.Fatal(err)
	}
	w.deliver(t, c, "a")
	w.deliver(t, c, "b")
	w.clock.Add(5 * time.Second)
	w.tick(t)
	runs := w.started()
	if len(runs) != 1 || runs[0].Overrides != nil || *runs[0].TriggerRef != c.Trigger.ID {
		t.Fatalf("syncs = %+v", runs)
	}
	for _, d := range w.deliveries(t, c.Trigger.ID) {
		if d.JobGroupID == nil || *d.JobGroupID != w.runs.groups[0] {
			t.Fatalf("delivery = %+v", d)
		}
	}
}

// TestSettleRunNowReportsAFailedOrDroppedStart: run-now answers the
// failure the jobs service gave, and a disabled trigger's pending run is
// dropped instead of started.
func TestSettleRunNowReportsAFailedOrDroppedStart(t *testing.T) {
	w := newSettleWorld(t, "settle-now-fail")
	c := w.create(t, 600, nil)
	w.deliver(t, c, "v1")
	w.runs.err = jobs.ErrEmptyScope
	if res, err := w.svc.RunPending(w.scoped, w.org, c.Trigger.ID, uuid.Must(uuid.NewV7())); err != nil || res.Outcome != triggers.OutcomeFailed {
		t.Fatalf("RunPending = %+v, %v", res, err)
	}
	w.runs.err = nil
	w.deliver(t, c, "v2")
	if _, err := w.svc.Disable(w.scoped, w.org, c.Trigger.ID); err != nil {
		t.Fatal(err)
	}
	if res, err := w.svc.RunPending(w.scoped, w.org, c.Trigger.ID, uuid.Must(uuid.NewV7())); err != nil || res.Outcome != triggers.OutcomeDisabled || len(w.started()) != 0 {
		t.Fatalf("RunPending on a disabled trigger = %+v, %v (runs %d)", res, err, len(w.started()))
	}
}

// TestSettleTickerRunTicksUntilCancelled: Run ticks, sleeps the tick
// through the injected sleep and stops when that reports the context
// ended.
func TestSettleTickerRunTicksUntilCancelled(t *testing.T) {
	w := newSettleWorld(t, "settle-run")
	c := w.create(t, 5, nil)
	w.deliver(t, c, "v1")
	w.clock.Add(5 * time.Second)
	runCtx, cancel := context.WithCancel(ctx)
	var slept []time.Duration
	sleep := func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		cancel()
		return context.Canceled
	}
	triggers.NewSettleTicker(w.svc, 0, sleep, nil).Run(runCtx)
	if len(w.started()) != 1 || len(slept) != 1 || slept[0] != triggers.DefaultSettleTick {
		t.Fatalf("runs %d, slept %v", len(w.started()), slept)
	}
}

// TestSettleTurnedOffSupersedesTheLeftoverWithALaterDelivery: once the
// settle delay is off, a delivery that arrives before the leftover pending
// run started discards it before starting its own run, so the older values
// never start after the newer ones; the leftover's deliveries name the run
// that superseded them.
func TestSettleTurnedOffSupersedesTheLeftoverWithALaterDelivery(t *testing.T) {
	w := newSettleWorld(t, "settle-supersede")
	c := w.create(t, 600, nil)
	ref := c.Trigger.ID
	active := w.insertJob(t, &ref, "running")
	w.deliver(t, c, "v1")
	in := w.input(0, nil)
	in.Name = c.Trigger.Name
	if _, err := w.svc.Update(w.scoped, w.org, c.Trigger.ID, c.Trigger.Revision, in); err != nil {
		t.Fatal(err)
	}
	if res := w.deliver(t, c, "v2"); res.Outcome != triggers.OutcomeTriggered {
		t.Fatalf("delivery with settle off = %+v", res)
	}
	if p := w.pending(t, c.Trigger.ID); p != nil {
		t.Fatalf("leftover pending run = %+v", p)
	}
	w.setJobStatus(t, active, "succeeded")
	w.clock.Add(time.Hour)
	w.tick(t)
	if runs := w.started(); len(runs) != 1 || runs[0].Overrides.ExtraVars["release"] != "v2" {
		t.Fatalf("runs = %+v", runs)
	}
	ds := w.deliveries(t, c.Trigger.ID)
	old := ds[len(ds)-1]
	if old.Outcome != triggers.OutcomeCollected || old.JobGroupID != nil || !strings.Contains(old.Reason, "superseded by run "+w.runs.groups[0].String()) {
		t.Fatalf("leftover delivery = %+v", old)
	}
}

// TestSettleCrashReplayStartsNoSecondRun: when a collected run was already
// started under the window's idempotency key (its bookkeeping lost), the
// next tick replays it without a second webhook.run audit and counts only
// the deliveries from before that start into it; the later ones open a new
// window, which starts with their values once it settled (here in the same
// tick: its quiet window ended with the old one's).
func TestSettleCrashReplayStartsNoSecondRun(t *testing.T) {
	w := newSettleWorld(t, "settle-replay")
	c := w.create(t, 5, nil)
	first := w.deliver(t, c, "v1")
	group := uuid.Must(uuid.NewV7())
	w.runs.keyed = map[string]jobs.Job{triggers.SettleIdempotencyKey(*first.PendingID): {
		ID: uuid.Must(uuid.NewV7()), OrgID: w.org, RunGroupID: group, CreatedAt: w.clock.Now().Add(time.Second)}}
	w.clock.Add(3 * time.Second)
	w.deliver(t, c, "v2")
	w.clock.Add(5 * time.Second)
	w.tick(t)
	runs := w.started()
	if len(runs) != 1 || runs[0].Overrides.ExtraVars["release"] != "v2" || runs[0].RunGroupID == *first.PendingID {
		t.Fatalf("runs = %+v; want only the new window's run", runs)
	}
	if e := w.audit.events; len(e) != 1 || e[0].Meta["pending_id"] != runs[0].RunGroupID.String() {
		t.Fatalf("audit = %+v; want the new window's run only", e)
	}
	ds := w.deliveries(t, c.Trigger.ID) // newest first
	if *ds[0].PendingID != runs[0].RunGroupID || ds[0].JobGroupID == nil || *ds[0].JobGroupID != w.runs.groups[0] ||
		*ds[1].PendingID != *first.PendingID || ds[1].JobGroupID == nil || *ds[1].JobGroupID != group {
		t.Fatalf("deliveries = %+v", ds)
	}
	if p := w.pending(t, c.Trigger.ID); p != nil {
		t.Fatalf("pending = %+v", p)
	}
}

// TestSettleCrashReplayKeepsALaterWindowWaiting: the deliveries after the
// replayed start form a window with their own quiet period.
func TestSettleCrashReplayKeepsALaterWindowWaiting(t *testing.T) {
	w := newSettleWorld(t, "settle-replay-wait")
	c := w.create(t, 5, nil)
	first := w.deliver(t, c, "v1")
	w.runs.keyed = map[string]jobs.Job{triggers.SettleIdempotencyKey(*first.PendingID): {
		ID: uuid.Must(uuid.NewV7()), OrgID: w.org, RunGroupID: uuid.Must(uuid.NewV7()), CreatedAt: w.clock.Now().Add(time.Second)}}
	w.clock.Add(2 * time.Second)
	w.deliver(t, c, "v2")
	if _, err := w.svc.RunPending(w.scoped, w.org, c.Trigger.ID, uuid.Must(uuid.NewV7())); err != nil {
		t.Fatal(err)
	}
	p := w.pending(t, c.Trigger.ID)
	if n := len(w.started()); n != 0 || p == nil || p.ID == *first.PendingID || p.Deliveries != 1 ||
		!p.FirstAt.Equal(w.clock.Now()) || !p.DueAt.Equal(w.clock.Now().Add(5*time.Second)) {
		t.Fatalf("runs %d, pending after the replay = %+v", n, p)
	}
}

// TestSettleRepoSyncJoinsAQueuedSyncAndReplaysItsOwn: a settled sync
// joins the repository's queued sync instead of queuing another, and a
// window whose sync was already started (bookkeeping lost) gets that sync
// back instead of a second one.
func TestSettleRepoSyncJoinsAQueuedSyncAndReplaysItsOwn(t *testing.T) {
	w := newSettleWorld(t, "settle-sync-once")
	c, err := w.svc.Create(w.scoped, w.org, uuid.Must(uuid.NewV7()), triggers.Input{Kind: triggers.KindRepoSync, RepoID: &w.repo, Name: "sync",
		Enabled: true, Preset: triggers.PresetPulsix, RateLimit: triggers.RateLimit{Count: 1000, Per: "1s"}, SettleSeconds: 5})
	if err != nil {
		t.Fatal(err)
	}
	queued := jobs.Job{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, RunGroupID: uuid.Must(uuid.NewV7())}
	w.runs.queuedSync = &queued
	w.deliver(t, c, "a")
	w.clock.Add(5 * time.Second)
	w.tick(t)
	if n := len(w.started()); n != 0 || len(w.audit.events) != 0 {
		t.Fatalf("joining a queued sync started %d, audited %+v", n, w.audit.events)
	}
	if d := w.deliveries(t, c.Trigger.ID)[0]; d.JobGroupID == nil || *d.JobGroupID != queued.RunGroupID {
		t.Fatalf("delivery = %+v", d)
	}

	w.runs.queuedSync = nil
	second := w.deliver(t, c, "b")
	own := jobs.Job{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, RunGroupID: *second.PendingID, CreatedAt: w.clock.Now().Add(time.Second)}
	w.runs.syncs = map[uuid.UUID]jobs.Job{*second.PendingID: own}
	w.clock.Add(5 * time.Second)
	w.tick(t)
	if n := len(w.started()); n != 0 || len(w.audit.events) != 0 {
		t.Fatalf("a replayed sync started %d, audited %+v", n, w.audit.events)
	}
	if d := w.deliveries(t, c.Trigger.ID)[0]; d.JobGroupID == nil || *d.JobGroupID != own.RunGroupID {
		t.Fatalf("delivery = %+v", d)
	}
}

// TestSettleDeliveryIsNotHeldByAnotherTriggersStart: the ticker commits
// each pending run on its own, so a delivery to a trigger whose run just
// started is not held while the ticker starts another trigger's run.
func TestSettleDeliveryIsNotHeldByAnotherTriggersStart(t *testing.T) {
	w := newSettleWorld(t, "settle-per-row")
	a, b := w.create(t, 5, nil), w.create(t, 5, nil)
	firstA := w.deliver(t, a, "a1")
	w.clock.Add(time.Second)
	w.deliver(t, b, "b1")
	w.clock.Add(5 * time.Second)

	entered, release := make(chan struct{}), make(chan struct{})
	var calls int
	var released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	defer unblock()
	w.runs.onEnqueue = func() {
		calls++ // the ticker enqueues one at a time
		if calls == 2 {
			close(entered)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := w.ticker.Tick(ctx)
		done <- err
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("the tick never reached the second trigger: %v", err)
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	res, err := send(bounded, w.svc, a, "a2")
	cancel()
	unblock()
	if err != nil || res.Outcome != triggers.OutcomeCollected || *res.PendingID == *firstA.PendingID {
		t.Fatalf("delivery during the tick = %+v, %v; want a new window without waiting", res, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := len(w.started()); n != 2 {
		t.Fatalf("%d runs, want 2", n)
	}
}
