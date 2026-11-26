package triggers_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/triggers"
)

func intp(v int) *int { return &v }

// TestSettleConfigBounds: the settle delay is 0 (off) or 5-3600 seconds;
// the maximum wait, only with a settle delay, is settle-86400 seconds and
// defaults to ten times the settle delay.
func TestSettleConfigBounds(t *testing.T) {
	cases := []struct {
		name        string
		settle      int
		maxWait     *int
		ok          bool
		wantMaxWait time.Duration
	}{
		{"off", 0, nil, true, 0},
		{"lowest", 5, nil, true, 50 * time.Second},
		{"highest", 3600, nil, true, 36000 * time.Second},
		{"below the lowest", 4, nil, false, 0},
		{"negative", -1, nil, false, 0},
		{"above the highest", 3601, nil, false, 0},
		{"max wait without a settle delay", 0, intp(60), false, 0},
		{"max wait below the settle delay", 30, intp(29), false, 0},
		{"max wait equal to the settle delay", 30, intp(30), true, 30 * time.Second},
		{"max wait at its highest", 30, intp(86400), true, 86400 * time.Second},
		{"max wait above its highest", 30, intp(86401), false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			in := w.templateInput()
			in.SettleSeconds, in.MaxWaitSeconds = tc.settle, tc.maxWait
			c, err := w.svc.Create(ctx, w.org, w.actor, in)
			if !tc.ok {
				if !errors.Is(err, triggers.ErrInvalidInput) {
					t.Fatalf("Create = %v, want ErrInvalidInput", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := c.Trigger
			if got.SettleSeconds != tc.settle || (tc.maxWait == nil) != (got.MaxWaitSeconds == nil) || got.MaxWait() != tc.wantMaxWait {
				t.Fatalf("stored settle %d max wait %v (effective %s)", got.SettleSeconds, got.MaxWaitSeconds, got.MaxWait())
			}
			if got.Settle() != time.Duration(tc.settle)*time.Second {
				t.Fatalf("Settle() = %s", got.Settle())
			}
		})
	}
}

// TestSettleAppliesToRepoSyncTriggers: both kinds take a settle delay.
func TestSettleAppliesToRepoSyncTriggers(t *testing.T) {
	w := newWorld(t)
	c, err := w.svc.Create(ctx, w.org, w.actor, triggers.Input{Kind: triggers.KindRepoSync, RepoID: &w.repo, Name: "sync", Enabled: true,
		Preset: triggers.PresetGitLab, SettleSeconds: 30, MaxWaitSeconds: intp(600)})
	if err != nil || c.Trigger.SettleSeconds != 30 || *c.Trigger.MaxWaitSeconds != 600 {
		t.Fatalf("Create = %+v, %v", c.Trigger, err)
	}
}

// fakePending records what the receive path collects; only Collect is
// used by these unit tests (the rest runs against Postgres in
// settle_sql_test.go).
type fakePending struct {
	triggers.PendingStore
	collects   []triggers.Collect
	deliveries []triggers.Delivery
}

func (f *fakePending) Collect(_ context.Context, c triggers.Collect, d triggers.Delivery) (triggers.PendingRun, error) {
	id := c.ID
	if len(f.collects) > 0 {
		id = f.collects[0].ID
	}
	f.collects = append(f.collects, c)
	d.PendingID = &id
	f.deliveries = append(f.deliveries, d)
	return triggers.PendingRun{ID: id, OrgID: c.OrgID, TriggerID: c.TriggerID, FirstAt: c.At, LastAt: c.At,
		DueAt: c.Due, DeadlineAt: c.Deadline, Deliveries: len(f.collects), LastDeliveryID: d.ID}, nil
}

func (f *fakePending) Supersede(context.Context, uuid.UUID, uuid.UUID) (*uuid.UUID, error) {
	return nil, nil
}

func (rw *receiveWorld) withPending(p triggers.PendingStore) {
	rw.svc = triggers.NewService(triggers.Deps{
		Store: rw.rec, Pending: p, Templates: rw.tmpls, Repos: rw.repos, Secrets: rw.secrets(), Scope: rw.scope, Box: fakeBox{},
		Runs: rw.runs, Audit: rw.audit, PublicURL: "https://pulsix.example.test",
		Now: func() time.Time { return time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC) }, Rand: rand.Reader,
	})
}

// TestReceiveWithoutSettleStartsARunAsBefore: settle 0 never touches the
// pending store.
func TestReceiveWithoutSettleStartsARunAsBefore(t *testing.T) {
	rw := newReceiveWorld(t)
	p := &fakePending{}
	rw.withPending(p)
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, rw.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	res, err := rw.svc.Receive(ctx, rw.signed(t, c, `{"ref":"v1"}`))
	if err != nil || res.Outcome != triggers.OutcomeTriggered || len(rw.runs.requests) != 1 || len(p.collects) != 0 {
		t.Fatalf("Receive = %+v, %v; runs %d, collects %d", res, err, len(rw.runs.requests), len(p.collects))
	}
}

// TestReceiveCollectsOnASettlingTrigger: a delivery to a trigger with a
// settle delay starts nothing; its mapped values are kept sealed for the
// collected run, the delivery is recorded as collected against the pending
// run, and on_active does not apply (owner decision 3).
func TestReceiveCollectsOnASettlingTrigger(t *testing.T) {
	rw := newReceiveWorld(t)
	p := &fakePending{}
	rw.withPending(p)
	in := rw.templateInput()
	in.SettleSeconds, in.OnActive = 30, triggers.OnActiveSkip
	in.Mapping.Limit = "$.target"
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	rw.runs.active = true
	res, err := rw.svc.Receive(ctx, rw.signed(t, c, `{"ref":"{{ lookup('pipe','id') }}","target":"web"}`))
	if err != nil || res.Outcome != triggers.OutcomeCollected || res.PendingID == nil || res.DueAt == nil || res.RunID != nil {
		t.Fatalf("Receive = %+v, %v", res, err)
	}
	if len(rw.runs.requests) != 0 || len(rw.rec.deliveries) != 0 {
		t.Fatalf("a collected delivery started a run (%d) or was recorded outside the collect (%d)", len(rw.runs.requests), len(rw.rec.deliveries))
	}
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	got := p.collects[0]
	if got.TriggerID != c.Trigger.ID || got.OrgID != rw.org || !got.At.Equal(now) || !got.Due.Equal(now.Add(30*time.Second)) || !got.Deadline.Equal(now.Add(300*time.Second)) {
		t.Fatalf("collect = %+v", got)
	}
	if !res.DueAt.Equal(got.Due) || *res.PendingID != got.ID {
		t.Fatalf("result %+v does not name the pending run %+v", res, got)
	}
	plain, err := fakeBox{}.OpenAAD(got.Overrides, triggers.PendingAAD(rw.org, c.Trigger.ID))
	if err != nil {
		t.Fatalf("overrides not sealed for the pending run: %v", err)
	}
	var ov templates.Overrides
	if err := json.Unmarshal(plain, &ov); err != nil || ov.ExtraVars["release"] != "{{ lookup('pipe','id') }}" || ov.Limit == nil || *ov.Limit != "web" {
		t.Fatalf("sealed overrides = %s, %v", plain, err)
	}
	d := p.deliveries[0]
	if d.Outcome != triggers.OutcomeCollected || d.PendingID == nil || *d.PendingID != got.ID || d.SourceIP != "140.82.112.5" || d.JobGroupID != nil {
		t.Fatalf("delivery = %+v", d)
	}
}

// TestReceiveCollectsRepoSyncWithoutOverrides: a settling repository sync
// trigger collects with no overrides at all.
func TestReceiveCollectsRepoSyncWithoutOverrides(t *testing.T) {
	rw := newReceiveWorld(t)
	p := &fakePending{}
	rw.withPending(p)
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, triggers.Input{Kind: triggers.KindRepoSync, RepoID: &rw.repo, Name: "sync", Enabled: true,
		Preset: triggers.PresetGitHub, SettleSeconds: 10})
	if err != nil {
		t.Fatal(err)
	}
	res, err := rw.svc.Receive(ctx, rw.signed(t, c, `{}`))
	if err != nil || res.Outcome != triggers.OutcomeCollected || len(rw.runs.requests) != 0 || p.collects[0].Overrides != nil {
		t.Fatalf("Receive = %+v, %v; runs %d, collects %+v", res, err, len(rw.runs.requests), p.collects)
	}
}

// TestReceiveRefusesASettlingTriggerWithoutAPendingStore: a wiring slip is
// an internal error, never a silently immediate run.
func TestReceiveRefusesASettlingTriggerWithoutAPendingStore(t *testing.T) {
	rw := newReceiveWorld(t)
	in := rw.templateInput()
	in.SettleSeconds = 30
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rw.svc.Receive(ctx, rw.signed(t, c, `{"ref":"v1"}`)); err == nil || len(rw.runs.requests) != 0 {
		t.Fatalf("Receive = %v, runs %d", err, len(rw.runs.requests))
	}
}

// writeBackFails is a pending store whose run-now starts the run and then
// fails to write the row back (the run's enqueue already committed).
type writeBackFails struct {
	*fakePending
	run triggers.DueRun
}

func (f *writeBackFails) FirePending(ctx context.Context, _, _ uuid.UUID, fire triggers.FireFunc) (triggers.Firing, error) {
	return fire(ctx, f.run), errors.New("write-back failed")
}

// TestRunPendingAnswersTheStartedRunWhenTheWriteBackFails: the run was
// enqueued, so "Run now" says so; the row stays and the ticker replays
// the same run group (final review 7). A failure before any run started
// is still an error.
func TestRunPendingAnswersTheStartedRunWhenTheWriteBackFails(t *testing.T) {
	rw := newReceiveWorld(t)
	p := &writeBackFails{fakePending: &fakePending{}}
	rw.withPending(p)
	in := rw.templateInput()
	in.SettleSeconds = 30
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	p.run = triggers.DueRun{Pending: triggers.PendingRun{ID: uuid.Must(uuid.NewV7()), OrgID: rw.org, TriggerID: c.Trigger.ID, Deliveries: 1}, Trigger: c.Trigger}
	res, err := rw.svc.RunPending(ctx, rw.org, c.Trigger.ID, rw.actor)
	if err != nil || res.Outcome != triggers.OutcomeTriggered || res.RunID == nil || len(rw.runs.requests) != 1 {
		t.Fatalf("RunPending = %+v, %v; runs %d", res, err, len(rw.runs.requests))
	}
	p.run.Trigger.Enabled = false
	if _, err := rw.svc.RunPending(ctx, rw.org, c.Trigger.ID, rw.actor); err == nil {
		t.Fatal("a failed write-back with no run started answered success")
	}
}
