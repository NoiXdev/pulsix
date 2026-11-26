package triggers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

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
	// reopen, on a replay (the window's run had already been started and
	// its bookkeeping was lost), moves the deliveries recorded after that
	// start into a new window instead of counting them into the run.
	reopen *reopen
}

// reopen is the new window a replay leaves the later deliveries in.
type reopen struct {
	after           time.Time
	id              uuid.UUID
	settle, maxWait time.Duration
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
	// Supersede deletes the trigger's pending run, waiting for a ticker
	// that is starting it, and returns its id (nil when there was none).
	Supersede(ctx context.Context, orgID, triggerID uuid.UUID) (*uuid.UUID, error)
	// NoteWindow appends note to the reason of the window's deliveries.
	NoteWindow(ctx context.Context, orgID, triggerID, windowID uuid.UUID, note string) error
	// ProcessDue claims, one at a time and each in its own transaction, up
	// to limit pending runs across organizations that are due with no run
	// of their trigger active, whose trigger's settle delay was turned off,
	// or whose trigger is disabled — skipping rows another manager holds —
	// and applies fire's decision to each. It returns how many it applied.
	ProcessDue(ctx context.Context, now time.Time, limit int, fire FireFunc) (int, error)
}

// PendingAAD is the associated data a pending run's collected values are
// sealed with.
func PendingAAD(orgID, triggerID uuid.UUID) []byte {
	return []byte("trigger-pending:" + orgID.String() + ":" + triggerID.String())
}

// SettleIdempotencyKey is the key a collected template run is enqueued
// under: a retried start of the same window (its bookkeeping lost after
// the enqueue committed) replays the run instead of starting another.
func SettleIdempotencyKey(pendingID uuid.UUID) string { return "trigger-settle:" + pendingID.String() }

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
