package triggers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/templates"
)

// A trigger with a settle delay (backlog 2.137) does not start a run per
// delivery. Each delivery that passes every check a run would — address,
// signature, rate limit, mapping — joins the trigger's pending run
// (OutcomeCollected): the run starts SettleSeconds after the last delivery,
// at the latest MaxWait after the first, with the last delivery's mapped
// values (owner decisions 1 and 2). When that moment comes while a run the
// trigger started is still active, the pending run waits for it and starts
// once afterwards, taking the deliveries that arrive meanwhile (owner
// decision 3); on_active does not apply to it. The manager's SettleTicker
// starts due pending runs.

// PendingRun is a trigger's collected run while it waits.
type PendingRun struct {
	// ID names the window: its deliveries' PendingID.
	ID, OrgID, TriggerID uuid.UUID
	FirstAt, LastAt      time.Time
	// DueAt is when it starts, unless more deliveries arrive (or a run
	// the trigger started is still active then); DeadlineAt is the
	// latest it starts at, the active-run wait aside.
	DueAt, DeadlineAt time.Time
	Deliveries        int
	LastDeliveryID    uuid.UUID
}

// Collect is one delivery joining its trigger's pending run.
type Collect struct {
	// ID is the window opened when none is pending; an open window keeps
	// its own.
	ID               uuid.UUID
	OrgID, TriggerID uuid.UUID
	At               time.Time
	// Due is At plus the settle delay; Deadline is At plus the maximum
	// wait, used only by the delivery that opens the window.
	Due, Deadline time.Time
	// Overrides are the delivery's mapped values, sealed with PendingAAD
	// (nil for none). They replace the window's.
	Overrides []byte
}

// DueRun is a pending run locked for starting, with its trigger as it is
// now.
type DueRun struct {
	Pending PendingRun
	// Overrides are the sealed collected values (nil for none).
	Overrides []byte
	Trigger   Trigger
}

// Firing is what became of a locked pending run. Unless Keep, the pending
// run is deleted and its deliveries get Outcome, the started run's
// JobGroupID (nil for none) and Note appended to their reason.
type Firing struct {
	// Keep leaves the pending run as it is, to be tried again (the start
	// was interrupted, not refused).
	Keep       bool
	Outcome    Outcome
	JobGroupID *uuid.UUID
	// RunID is the first job of the started run.
	RunID *uuid.UUID
	Note  string
}

// FireFunc decides a locked pending run.
type FireFunc func(ctx context.Context, r DueRun) Firing

// PendingStore keeps the pending runs. Collect and the firing of one run
// are serialised by the pending row's lock: a delivery never joins a
// window that is being started, and a window is started at most once.
type PendingStore interface {
	// Collect joins (or opens) the trigger's pending run and records d,
	// as collected into it, in one transaction.
	Collect(ctx context.Context, c Collect, d Delivery) (PendingRun, error)
	// Pending is the trigger's pending run, nil when there is none.
	Pending(ctx context.Context, orgID, triggerID uuid.UUID) (*PendingRun, error)
	// FirePending locks the trigger's pending run, waiting for a ticker
	// that is starting it, and applies fire's decision
	// (ErrNothingPending).
	FirePending(ctx context.Context, orgID, triggerID uuid.UUID, fire FireFunc) (Firing, error)
	// DiscardPending deletes the trigger's pending run and appends note
	// to its deliveries' reason (ErrNothingPending).
	DiscardPending(ctx context.Context, orgID, triggerID uuid.UUID, note string) error
	// ProcessDue locks up to limit pending runs, across organizations,
	// that are due with no run of their trigger active, whose trigger's
	// settle delay was turned off, or whose trigger is disabled — skipping
	// rows another manager holds — and applies fire's decision to each. It
	// returns how many it applied.
	ProcessDue(ctx context.Context, now time.Time, limit int, fire FireFunc) (int, error)
}

// PendingAAD is the associated data a pending run's collected values are
// sealed with.
func PendingAAD(orgID, triggerID uuid.UUID) []byte {
	return []byte("trigger-pending:" + orgID.String() + ":" + triggerID.String())
}

// settleIdempotencyKey is the key a collected run is enqueued under: a
// retried start of the same window (its bookkeeping lost after the enqueue
// committed) replays the run instead of starting another.
func settleIdempotencyKey(pendingID uuid.UUID) string { return "trigger-settle:" + pendingID.String() }

// Notes a collected delivery's reason gets when its window did not start.
const (
	noteDisabled  = "dropped: the trigger was disabled before the collected run started"
	noteDiscarded = "discarded before the collected run started"
)

// webhookOverrides are a delivery's mapped values as run overrides: nil
// when there are none, untrusted otherwise. A payload is untrusted input:
// the claim marks every mapped string unsafe for Ansible, so a
// "{{ lookup(…) }}" in a branch name stays text instead of running on the
// worker (final review C1).
func webhookOverrides(ov templates.Overrides) *jobs.RunOverrides {
	if ov.Empty() {
		return nil
	}
	return &jobs.RunOverrides{Limit: ov.Limit, ExtraVars: ov.ExtraVars, Untrusted: true}
}

// collect joins d to t's pending run instead of starting one.
func (s *Service) collect(ctx context.Context, t Trigger, d Delivery, ov templates.Overrides) (Result, Delivery, error) {
	if s.d.Pending == nil {
		return Result{}, d, errors.New("the trigger has a settle delay, but no pending run store is wired")
	}
	sealed, err := s.sealPending(t, ov)
	if err != nil {
		return Result{}, d, err
	}
	d.Outcome = OutcomeCollected
	p, err := s.d.Pending.Collect(ctx, Collect{
		ID: s.newID(), OrgID: t.OrgID, TriggerID: t.ID, At: d.At,
		Due: d.At.Add(t.Settle()), Deadline: d.At.Add(t.MaxWait()), Overrides: sealed,
	}, d)
	if err != nil {
		// Nothing was collected or recorded (one transaction), so a 500
		// here is honest: a sender's retry collects it once.
		return Result{}, d, fmt.Errorf("collect delivery: %w", err)
	}
	d.PendingID = &p.ID
	return Result{Outcome: OutcomeCollected, PendingID: &p.ID, DueAt: &p.DueAt}, d, nil
}

func (s *Service) sealPending(t Trigger, ov templates.Overrides) ([]byte, error) {
	if ov.Empty() {
		return nil, nil
	}
	plain, err := json.Marshal(ov)
	if err != nil {
		return nil, fmt.Errorf("encode collected values: %w", err)
	}
	sealed, err := s.d.Box.SealAAD(plain, PendingAAD(t.OrgID, t.ID))
	if err != nil {
		return nil, fmt.Errorf("seal collected values: %w", err)
	}
	return sealed, nil
}

func (s *Service) openPending(r DueRun) (templates.Overrides, error) {
	if r.Overrides == nil {
		return templates.Overrides{}, nil
	}
	plain, err := s.d.Box.OpenAAD(r.Overrides, PendingAAD(r.Trigger.OrgID, r.Trigger.ID))
	if err != nil {
		return templates.Overrides{}, fmt.Errorf("open collected values: %w", err)
	}
	// UseNumber keeps a number exactly as the payload sent it.
	dec := json.NewDecoder(bytes.NewReader(plain))
	dec.UseNumber()
	var ov templates.Overrides
	if err := dec.Decode(&ov); err != nil {
		return templates.Overrides{}, fmt.Errorf("decode collected values: %w", err)
	}
	return ov, nil
}

// fire starts a locked pending run: one run (or sync) with the collected
// values, untrusted exactly as a delivery's own, or nothing when the
// trigger is disabled. A refused start is recorded on the deliveries; an
// interrupted one (the context ended) is kept for the next try.
func (s *Service) fire(ctx context.Context, r DueRun) (f Firing) {
	defer func() {
		if rec := recover(); rec != nil {
			s.d.Log.ErrorContext(context.WithoutCancel(ctx), "starting a collected webhook run panicked", "trigger", r.Trigger.ID, "org", r.Trigger.OrgID,
				"panic", rec, "stack", string(debug.Stack()))
			f = Firing{Outcome: OutcomeFailed, Note: "could not enqueue (see the manager log)"}
		}
	}()
	t := r.Trigger
	if !t.Enabled {
		return Firing{Outcome: OutcomeCollected, Note: noteDisabled}
	}
	ctx = db.WithOrgScope(ctx, t.OrgID)
	ref := t.ID
	req := jobs.RunRequest{Trigger: jobs.TriggerWebhook, TriggerRef: &ref}
	var group, runID uuid.UUID
	var err error
	objectType, objectID := "repo", ""
	if t.Kind == KindRepoSync {
		objectID = t.RepoID.String()
		var j jobs.Job
		if j, err = s.d.Runs.EnqueueSyncWith(ctx, t.OrgID, *t.RepoID, req); err == nil {
			group, runID = j.RunGroupID, j.ID
		}
	} else {
		objectType, objectID = "job_template", t.TemplateID.String()
		ov, oerr := s.openPending(r)
		if oerr != nil {
			s.d.Log.ErrorContext(ctx, "collected webhook values could not be read", "trigger", t.ID, "org", t.OrgID, "err", oerr)
			return Firing{Outcome: OutcomeFailed, Note: "the collected values could not be read (see the manager log)"}
		}
		req.Overrides = webhookOverrides(ov)
		req.IdempotencyKey = settleIdempotencyKey(r.Pending.ID)
		var created []jobs.Job
		if created, err = s.d.Runs.EnqueueRunWith(ctx, t.OrgID, *t.TemplateID, req); err == nil {
			group, runID = created[0].RunGroupID, created[0].ID
		}
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Firing{Keep: true}
		}
		s.d.Log.ErrorContext(ctx, "collected webhook run could not be enqueued", "trigger", t.ID, "org", t.OrgID, "err", err)
		return Firing{Outcome: OutcomeFailed, Note: failureReason(err)}
	}
	s.auditCollected(ctx, t, r.Pending, group, objectType, objectID)
	return Firing{Outcome: OutcomeCollected, JobGroupID: &group, RunID: &runID}
}

func (s *Service) auditCollected(ctx context.Context, t Trigger, p PendingRun, group uuid.UUID, objectType, objectID string) {
	if s.d.Audit == nil {
		return
	}
	orgID, actor := t.OrgID, t.ID
	if err := s.d.Audit.Record(ctx, audit.Event{
		OrgID: &orgID, ActorKind: audit.ActorKindWebhook, ActorID: &actor, Action: "webhook.run",
		ObjectType: objectType, ObjectID: objectID,
		Summary: fmt.Sprintf("webhook %q started a run for %d collected deliveries", t.Name, p.Deliveries),
		Meta: map[string]any{"trigger_id": t.ID.String(), "job_group_id": group.String(), "pending_id": p.ID.String(),
			"deliveries": p.Deliveries},
	}); err != nil {
		s.d.Log.ErrorContext(ctx, "audit webhook run", "trigger", t.ID, "err", err)
	}
}

// Pending is the trigger's collected run while it waits; nil when there is
// none (ErrNotFound for an unknown trigger).
func (s *Service) Pending(ctx context.Context, orgID, id uuid.UUID) (*PendingRun, error) {
	if _, err := s.d.Store.Get(ctx, orgID, id); err != nil {
		return nil, err
	}
	if s.d.Pending == nil {
		return nil, nil
	}
	return s.d.Pending.Pending(ctx, orgID, id)
}

// RunPending starts the trigger's collected run now, without waiting for
// the deliveries to settle or for an active run of the trigger to end:
// OutcomeTriggered with the run, OutcomeFailed when the jobs service
// refused it (its deliveries say why), OutcomeDisabled when the trigger is
// disabled (the run is dropped). ErrNothingPending when none waits,
// ErrNotFound for an unknown trigger.
func (s *Service) RunPending(ctx context.Context, orgID, id uuid.UUID) (Result, error) {
	if _, err := s.d.Store.Get(ctx, orgID, id); err != nil {
		return Result{}, err
	}
	if s.d.Pending == nil {
		return Result{}, ErrNothingPending
	}
	f, err := s.d.Pending.FirePending(ctx, orgID, id, s.fire)
	switch {
	case err != nil:
		return Result{}, err
	case f.Keep:
		return Result{}, errors.New("starting the collected run was interrupted; it is still pending")
	case f.RunID != nil:
		return Result{Outcome: OutcomeTriggered, RunID: f.RunID}, nil
	case f.Outcome == OutcomeFailed:
		return Result{Outcome: OutcomeFailed}, nil
	}
	return Result{Outcome: OutcomeDisabled}, nil
}

// DiscardPending drops the trigger's collected run; its deliveries say so.
// ErrNothingPending when none waits, ErrNotFound for an unknown trigger.
func (s *Service) DiscardPending(ctx context.Context, orgID, id uuid.UUID) error {
	if _, err := s.d.Store.Get(ctx, orgID, id); err != nil {
		return err
	}
	if s.d.Pending == nil {
		return ErrNothingPending
	}
	return s.d.Pending.DiscardPending(ctx, orgID, id, noteDiscarded)
}
