package triggers_test

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/triggers"
)

// fakeClock is the settle tests' clock: the receive path and the ticker
// read the same one, and a test moves it on by hand.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// settleWorld drives the real store with a fake clock, fake runs and the
// ticker's step hook (Tick): no sleeps anywhere.
type settleWorld struct {
	d      *db.DB
	org    uuid.UUID
	tmpl   uuid.UUID
	repo   uuid.UUID
	scoped context.Context
	clock  *fakeClock
	runs   *fakeRuns
	audit  *fakeAudit
	svc    *triggers.Service
	ticker *triggers.SettleTicker
}

func newSettleWorld(t *testing.T, slug string) *settleWorld {
	t.Helper()
	d := testdb.Open(t)
	org, tmpl := seedTemplate(t, d, slug)
	repo, err := gen.New(d.Pool()).CreateRepo(ctx, gen.CreateRepoParams{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "infra", Url: "https://git.example.com/infra.git", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	w := &settleWorld{d: d, org: org, tmpl: tmpl, repo: repo.ID, scoped: db.WithOrgScope(ctx, org),
		clock: &fakeClock{t: time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)}, runs: &fakeRuns{}, audit: &fakeAudit{}}
	w.svc = w.manager(triggers.NewSQLStore(d))
	w.ticker = triggers.NewSettleTicker(w.svc, time.Second, nil, nil)
	return w
}

// manager is one manager's triggers service over st: two of them on one
// database are two managers.
func (w *settleWorld) manager(st *triggers.SQLStore) *triggers.Service {
	return triggers.NewService(triggers.Deps{
		Store: st, Pending: st, Box: fakeBox{}, Runs: w.runs, Audit: w.audit, Now: w.clock.Now, Rand: rand.Reader,
		Templates: fakeTemplates{byID: map[uuid.UUID]templates.Template{w.tmpl: {ID: w.tmpl, OrgID: w.org, Name: "deploy"}}},
		Repos:     fakeRepos{byID: map[uuid.UUID]repos.Repo{w.repo: {ID: w.repo, OrgID: w.org}}},
	})
}

func (w *settleWorld) input(settle int, maxWait *int) triggers.Input {
	return triggers.Input{Kind: triggers.KindTemplateRun, TemplateID: &w.tmpl, Name: "deploy-" + uuid.NewString()[:8], Enabled: true,
		Preset: triggers.PresetPulsix, Mapping: triggers.Mapping{ExtraVars: map[string]string{"release": "$.ref"}},
		RateLimit: triggers.RateLimit{Count: 1000, Per: "1s"}, SettleSeconds: settle, MaxWaitSeconds: maxWait}
}

func (w *settleWorld) create(t *testing.T, settle int, maxWait *int) triggers.Created {
	t.Helper()
	c, err := w.svc.Create(w.scoped, w.org, uuid.Must(uuid.NewV7()), w.input(settle, maxWait))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// send delivers ref to c through svc; safe to call from any goroutine.
func send(ctx context.Context, svc *triggers.Service, c triggers.Created, ref string) (triggers.Result, error) {
	body := []byte(`{"ref":"` + ref + `"}`)
	h := http.Header{}
	h.Set(c.Trigger.SignatureHeader(), triggers.Sign(c.Trigger.Preset, c.Secret, "", body))
	return svc.Receive(ctx, triggers.Request{TriggerID: c.Trigger.ID.String(), Kind: c.Trigger.Kind, Body: body, Header: h.Get,
		SourceIP: netip.MustParseAddr("192.0.2.10")})
}

func (w *settleWorld) deliver(t *testing.T, c triggers.Created, ref string) triggers.Result {
	t.Helper()
	res, err := send(ctx, w.svc, c, ref)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

type sent struct {
	res triggers.Result
	err error
}

func (w *settleWorld) tick(t *testing.T) {
	t.Helper()
	if _, err := w.ticker.Tick(ctx); err != nil {
		t.Fatal(err)
	}
}

func (w *settleWorld) started() []jobs.RunRequest {
	w.runs.mu.Lock()
	defer w.runs.mu.Unlock()
	return append([]jobs.RunRequest(nil), w.runs.requests...)
}

func (w *settleWorld) pending(t *testing.T, id uuid.UUID) *triggers.PendingRun {
	t.Helper()
	p, err := w.svc.Pending(w.scoped, w.org, id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (w *settleWorld) deliveries(t *testing.T, id uuid.UUID) []triggers.Delivery {
	t.Helper()
	ds, err := w.svc.Deliveries(w.scoped, w.org, id, 100)
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

// TestSettleCollectsDeliveriesIntoOneRunWithTheLastValues: three
// deliveries inside the quiet window start one run, with the last
// delivery's values, untrusted; every delivery is logged as collected and,
// once the run started, as collapsed into it.
func TestSettleCollectsDeliveriesIntoOneRunWithTheLastValues(t *testing.T) {
	w := newSettleWorld(t, "settle-collect")
	c := w.create(t, 10, nil)
	t0 := w.clock.Now()
	var pendingID uuid.UUID
	for i, ref := range []string{"v1", "v2", "v3"} {
		if i > 0 {
			w.clock.Add(3 * time.Second)
		}
		res := w.deliver(t, c, ref)
		if res.Outcome != triggers.OutcomeCollected || res.PendingID == nil || res.RunID != nil {
			t.Fatalf("delivery %d = %+v", i, res)
		}
		if i == 0 {
			pendingID = *res.PendingID
		} else if *res.PendingID != pendingID {
			t.Fatalf("delivery %d opened another pending run", i)
		}
		w.tick(t)
	}
	if n := len(w.started()); n != 0 {
		t.Fatalf("%d runs started inside the window", n)
	}
	p := w.pending(t, c.Trigger.ID)
	if p == nil || p.ID != pendingID || p.Deliveries != 3 || !p.FirstAt.Equal(t0) || !p.LastAt.Equal(t0.Add(6*time.Second)) ||
		!p.DueAt.Equal(t0.Add(16*time.Second)) || !p.DeadlineAt.Equal(t0.Add(100*time.Second)) {
		t.Fatalf("pending = %+v", p)
	}
	for _, d := range w.deliveries(t, c.Trigger.ID) {
		if d.Outcome != triggers.OutcomeCollected || d.PendingID == nil || *d.PendingID != pendingID || d.JobGroupID != nil {
			t.Fatalf("delivery while pending = %+v", d)
		}
	}

	w.clock.Add(10 * time.Second)
	w.tick(t)
	w.tick(t)
	runs := w.started()
	if len(runs) != 1 {
		t.Fatalf("%d runs, want 1", len(runs))
	}
	req := runs[0]
	if req.Trigger != jobs.TriggerWebhook || req.TriggerRef == nil || *req.TriggerRef != c.Trigger.ID || req.Overrides == nil ||
		!req.Overrides.Untrusted || req.Overrides.ExtraVars["release"] != "v3" || req.IdempotencyKey == "" {
		t.Fatalf("run request = %+v (overrides %+v)", req, req.Overrides)
	}
	if p := w.pending(t, c.Trigger.ID); p != nil {
		t.Fatalf("pending after the run = %+v", p)
	}
	if len(w.audit.events) != 1 || w.audit.events[0].Action != "webhook.run" || w.audit.events[0].Meta["deliveries"] != 3 ||
		w.audit.events[0].Meta["pending_id"] != pendingID.String() || *w.audit.events[0].ActorID != c.Trigger.ID {
		t.Fatalf("audit = %+v", w.audit.events)
	}
	ds := w.deliveries(t, c.Trigger.ID)
	if len(ds) != 3 {
		t.Fatalf("%d deliveries", len(ds))
	}
	for _, d := range ds {
		if d.Outcome != triggers.OutcomeCollected || d.JobGroupID == nil || *d.JobGroupID != w.runs.groups[0] || *d.PendingID != pendingID {
			t.Fatalf("delivery after the run = %+v", d)
		}
	}
}

// TestSettleQuietWindowExtends: each delivery moves the start to settle
// seconds after it.
func TestSettleQuietWindowExtends(t *testing.T) {
	w := newSettleWorld(t, "settle-extend")
	c := w.create(t, 5, nil)
	w.deliver(t, c, "v1")
	w.clock.Add(4 * time.Second)
	w.deliver(t, c, "v2")
	w.clock.Add(4 * time.Second) // 8 s after the first, 4 s after the last
	w.tick(t)
	if n := len(w.started()); n != 0 {
		t.Fatalf("started %d runs before the window was quiet", n)
	}
	w.clock.Add(time.Second)
	w.tick(t)
	if runs := w.started(); len(runs) != 1 || runs[0].Overrides.ExtraVars["release"] != "v2" {
		t.Fatalf("runs = %+v", runs)
	}
}

// TestSettleMaxWaitFiresDespiteASteadyStream: deliveries that never leave a
// quiet window still start the run max wait after the first one.
func TestSettleMaxWaitFiresDespiteASteadyStream(t *testing.T) {
	w := newSettleWorld(t, "settle-maxwait")
	c := w.create(t, 5, intp(12))
	first := w.deliver(t, c, "v0")
	for i := 1; i <= 3; i++ {
		w.clock.Add(3 * time.Second)
		w.tick(t)
		if n := len(w.started()); n != 0 {
			t.Fatalf("run started %d s after the first delivery", 3*i)
		}
		w.deliver(t, c, "v"+string(rune('0'+i)))
	}
	w.clock.Add(3 * time.Second) // 12 s after the first delivery
	w.tick(t)
	if runs := w.started(); len(runs) != 1 || runs[0].Overrides.ExtraVars["release"] != "v3" {
		t.Fatalf("runs = %+v", runs)
	}
	next := w.deliver(t, c, "v4")
	if next.Outcome != triggers.OutcomeCollected || *next.PendingID == *first.PendingID {
		t.Fatalf("a delivery after the run joined the started window: %+v", next)
	}
}

// insertJob puts a job of the template into the run history, started by
// the trigger ref (nil: a manual run), in status.
func (w *settleWorld) insertJob(t *testing.T, ref *uuid.UUID, status string) uuid.UUID {
	t.Helper()
	var zone uuid.UUID
	err := w.d.Pool().QueryRow(ctx, `SELECT id FROM zones WHERE org_id = $1 AND slug = 'default'`, w.org).Scan(&zone)
	if errors.Is(err, pgx.ErrNoRows) {
		zone = uuid.Must(uuid.NewV7())
		if _, err := w.d.Pool().Exec(ctx, `INSERT INTO zones (id, org_id, slug, name) VALUES ($1, $2, 'default', 'Default')`, zone, w.org); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	id := uuid.Must(uuid.NewV7())
	trigger := jobs.TriggerManual
	if ref != nil {
		trigger = jobs.TriggerWebhook
	}
	if _, err := w.d.Pool().Exec(ctx, `INSERT INTO jobs (id, org_id, zone_id, kind, template_id, status, run_group_id, trigger, trigger_ref)
		VALUES ($1, $2, $3, 'run', $4, $5, $6, $7, $8)`, id, w.org, zone, w.tmpl, status, uuid.Must(uuid.NewV7()), trigger, ref); err != nil {
		t.Fatal(err)
	}
	return id
}

func (w *settleWorld) setJobStatus(t *testing.T, id uuid.UUID, status string) {
	t.Helper()
	if _, err := w.d.Pool().Exec(ctx, `UPDATE jobs SET status = $2 WHERE id = $1`, id, status); err != nil {
		t.Fatal(err)
	}
}

// TestSettleWaitsForAnActiveRunOfTheTrigger is owner decision 3: when the
// window ends while a run this trigger started is still active, the
// collected run waits for it, takes the deliveries meanwhile, and starts
// exactly once afterwards with the newest values. A manual run of the same
// template does not hold it.
func TestSettleWaitsForAnActiveRunOfTheTrigger(t *testing.T) {
	w := newSettleWorld(t, "settle-active")
	c := w.create(t, 5, nil)
	ref := c.Trigger.ID
	active := w.insertJob(t, &ref, "running")
	w.insertJob(t, nil, "running")

	first := w.deliver(t, c, "v1")
	w.clock.Add(6 * time.Second)
	w.tick(t)
	if n := len(w.started()); n != 0 {
		t.Fatalf("started %d runs while the trigger's run was active", n)
	}
	second := w.deliver(t, c, "v2")
	if *second.PendingID != *first.PendingID {
		t.Fatal("a delivery while waiting opened another pending run")
	}
	w.clock.Add(time.Hour) // far past the deadline
	w.tick(t)
	if n := len(w.started()); n != 0 {
		t.Fatalf("started %d runs while the trigger's run was active", n)
	}
	w.setJobStatus(t, active, "succeeded")
	w.tick(t)
	w.tick(t)
	if runs := w.started(); len(runs) != 1 || runs[0].Overrides.ExtraVars["release"] != "v2" {
		t.Fatalf("runs = %+v", runs)
	}
}

// TestSettleDropsThePendingRunOfADisabledOrDeletedTrigger: a disabled
// trigger's collected run is dropped at the next tick, due or not, and its
// deliveries say so; a deleted trigger takes its pending run with it.
func TestSettleDropsThePendingRunOfADisabledOrDeletedTrigger(t *testing.T) {
	w := newSettleWorld(t, "settle-disabled")
	off := w.create(t, 60, nil)
	gone := w.create(t, 60, nil)
	w.deliver(t, off, "v1")
	w.deliver(t, gone, "v1")
	if _, err := w.svc.Disable(w.scoped, w.org, off.Trigger.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.Delete(w.scoped, w.org, gone.Trigger.ID); err != nil {
		t.Fatal(err)
	}
	w.tick(t)
	if p := w.pending(t, off.Trigger.ID); p != nil {
		t.Fatalf("pending of a disabled trigger = %+v", p)
	}
	var left int
	if err := w.d.Pool().QueryRow(ctx, `SELECT count(*) FROM trigger_pending_runs`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d pending runs left, %v", left, err)
	}
	ds := w.deliveries(t, off.Trigger.ID)
	if len(ds) != 1 || ds[0].Outcome != triggers.OutcomeCollected || ds[0].JobGroupID != nil || !strings.Contains(ds[0].Reason, "disabled") {
		t.Fatalf("deliveries = %+v", ds)
	}
	w.clock.Add(time.Hour)
	w.tick(t)
	if n := len(w.started()); n != 0 {
		t.Fatalf("started %d runs", n)
	}
}

// TestSettleTurnedOffFiresThePendingRunAtTheNextTick: a trigger whose
// settle delay is set to 0 does not leave its collected run waiting for a
// window it no longer has.
func TestSettleTurnedOffFiresThePendingRunAtTheNextTick(t *testing.T) {
	w := newSettleWorld(t, "settle-off")
	c := w.create(t, 600, nil)
	ref := c.Trigger.ID
	w.insertJob(t, &ref, "running") // a settle-0 leftover does not wait for it
	w.deliver(t, c, "v1")
	in := w.input(0, nil)
	in.Name = c.Trigger.Name
	if _, err := w.svc.Update(w.scoped, w.org, c.Trigger.ID, c.Trigger.Revision, in); err != nil {
		t.Fatal(err)
	}
	w.tick(t)
	if runs := w.started(); len(runs) != 1 || runs[0].Overrides.ExtraVars["release"] != "v1" {
		t.Fatalf("runs = %+v", runs)
	}
	// From now on a delivery starts its run at once again.
	if res := w.deliver(t, c, "v2"); res.Outcome != triggers.OutcomeTriggered {
		t.Fatalf("delivery with settle off = %+v", res)
	}
}
