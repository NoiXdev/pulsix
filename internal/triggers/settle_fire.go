package triggers

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/jobs"
)

// fireTicked is the settle ticker's FireFunc: a start nobody asked for.
func (s *Service) fireTicked(ctx context.Context, r DueRun) Firing { return s.fire(ctx, r, nil) }

// fire starts a locked pending run: one run (or sync) with the collected
// values, untrusted exactly as a delivery's own, or nothing when the
// trigger is disabled. actor is the person who asked for it ("run now"),
// nil for the ticker. A refused start is recorded on the deliveries; an
// interrupted one (the context ended) is kept for the next try.
//
// The run's group is the window's id. A template run is enqueued under
// SettleIdempotencyKey, a sync through jobs.EnqueueSyncOnceWith, so a
// window whose run was started before and whose bookkeeping was then lost
// (a manager died between the enqueue's commit and the pending row's
// write-back) gets that run back instead of a second one: such a replay
// writes no second webhook.run audit, and the deliveries recorded after
// the run's first start — whose values it never saw — are not counted into
// it but left in a new window that starts on its own (reopen). A settled
// sync joins the repository's queued sync when there is one.
func (s *Service) fire(ctx context.Context, r DueRun, actor *uuid.UUID) (f Firing) {
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
	req := jobs.RunRequest{Trigger: jobs.TriggerWebhook, TriggerRef: &ref, Actor: actor, RunGroupID: r.Pending.ID}
	var started []jobs.Job
	var replay, joined bool
	var err error
	objectType, objectID := "repo", ""
	if t.Kind == KindRepoSync {
		objectID = t.RepoID.String()
		var j jobs.Job
		var existing bool
		if j, existing, err = s.d.Runs.EnqueueSyncOnceWith(ctx, t.OrgID, *t.RepoID, req); err == nil {
			started = []jobs.Job{j}
			replay = existing && j.RunGroupID == r.Pending.ID
			joined = existing && !replay
		}
	} else {
		objectType, objectID = "job_template", t.TemplateID.String()
		ov, oerr := s.openPending(r)
		if oerr != nil {
			s.d.Log.ErrorContext(ctx, "collected webhook values could not be read", "trigger", t.ID, "org", t.OrgID, "err", oerr)
			return Firing{Outcome: OutcomeFailed, Note: "the collected values could not be read (see the manager log)"}
		}
		req.Overrides = webhookOverrides(ov)
		req.IdempotencyKey = SettleIdempotencyKey(r.Pending.ID)
		var prior *uuid.UUID
		if prior, err = s.d.Runs.StartedRunGroup(ctx, t.OrgID, req.IdempotencyKey); err == nil {
			replay = prior != nil
			started, err = s.d.Runs.EnqueueRunWith(ctx, t.OrgID, *t.TemplateID, req)
		}
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Firing{Keep: true}
		}
		s.d.Log.ErrorContext(ctx, "collected webhook run could not be enqueued", "trigger", t.ID, "org", t.OrgID, "err", err)
		return Firing{Outcome: OutcomeFailed, Note: failureReason(err)}
	}
	group, runID := started[0].RunGroupID, started[0].ID
	f = Firing{Outcome: OutcomeCollected, JobGroupID: &group, RunID: &runID}
	switch {
	case replay:
		// The split compares the first job's created_at (the jobs clock of
		// the manager that enqueued it) with the deliveries' at (the trigger
		// clock of the managers that received them). It assumes the
		// managers' clocks agree: with a receiving manager's clock ahead, a
		// delivery recorded before the crashed start can count as later and
		// reopen a window whose values the first run already used. That
		// needs a crash between enqueue and write-back plus clock skew;
		// accepted (final review 2), managers are expected to run NTP.
		first := started[0].CreatedAt
		for _, j := range started[1:] {
			if j.CreatedAt.Before(first) {
				first = j.CreatedAt
			}
		}
		f.reopen = &reopen{after: first, id: s.newID(), settle: t.Settle(), maxWait: t.MaxWait()}
	case joined:
		f.Note = "joined the repository's queued sync"
	default:
		s.auditCollected(ctx, t, r.Pending, group, objectType, objectID, actor)
	}
	return f
}

func (s *Service) auditCollected(ctx context.Context, t Trigger, p PendingRun, group uuid.UUID, objectType, objectID string, actor *uuid.UUID) {
	if s.d.Audit == nil {
		return
	}
	orgID, actorKind, actorID := t.OrgID, audit.ActorKindWebhook, t.ID
	summary := fmt.Sprintf("webhook %q started a run for %d collected deliveries", t.Name, p.Deliveries)
	if actor != nil {
		actorKind, actorID = audit.ActorKindUser, *actor
		summary = fmt.Sprintf("started the run webhook %q collected from %d deliveries", t.Name, p.Deliveries)
	}
	if err := s.d.Audit.Record(ctx, audit.Event{
		OrgID: &orgID, ActorKind: actorKind, ActorID: &actorID, Action: "webhook.run",
		ObjectType: objectType, ObjectID: objectID, Summary: summary,
		Meta: map[string]any{"trigger_id": t.ID.String(), "job_group_id": group.String(), "pending_id": p.ID.String(),
			"deliveries": p.Deliveries},
	}); err != nil {
		s.d.Log.ErrorContext(ctx, "audit webhook run", "trigger", t.ID, "err", err)
	}
}

// supersede takes a leftover pending run out of a trigger whose settle
// delay is off before the delivery starts its own run, so the leftover's
// older values never start after the delivery's newer ones (review fix 1:
// discard, not fold, because folding would still start the older run
// first). When a ticker is starting the leftover right now, this waits for
// it and finds nothing: the older run started first. Nil when there was
// none (always, for a trigger that never had a settle delay).
func (s *Service) supersede(ctx context.Context, t Trigger) (*uuid.UUID, error) {
	if s.d.Pending == nil {
		return nil, nil
	}
	window, err := s.d.Pending.Supersede(ctx, t.OrgID, t.ID)
	if err != nil {
		return nil, fmt.Errorf("supersede the pending run: %w", err)
	}
	return window, nil
}

// noteSuperseded tells the superseded window's deliveries which run took
// their place (best effort, like recording a delivery).
func (s *Service) noteSuperseded(ctx context.Context, t Trigger, window *uuid.UUID, res Result, d Delivery) {
	if window == nil {
		return
	}
	note := "superseded by a later delivery"
	if res.Outcome == OutcomeTriggered && d.JobGroupID != nil {
		note = supersededByRun + d.JobGroupID.String()
	}
	if err := s.d.Pending.NoteWindow(ctx, t.OrgID, t.ID, *window, note); err != nil {
		s.d.Log.ErrorContext(ctx, "note superseded deliveries", "trigger", t.ID, "org", t.OrgID, "err", err)
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

// RunPending starts the trigger's collected run now for the person actor,
// who is recorded as the run's actor and in its audit event, without
// waiting for the deliveries to settle or for an active run of the trigger
// to end: OutcomeTriggered with the run, OutcomeFailed when the jobs
// service refused it (its deliveries say why), OutcomeDisabled when the
// trigger is disabled (the run is dropped). ErrNothingPending when none
// waits, ErrNotFound for an unknown trigger. A run that started although
// writing back its pending run failed is OutcomeTriggered (logged).
func (s *Service) RunPending(ctx context.Context, orgID, id, actor uuid.UUID) (Result, error) {
	if _, err := s.d.Store.Get(ctx, orgID, id); err != nil {
		return Result{}, err
	}
	if s.d.Pending == nil {
		return Result{}, ErrNothingPending
	}
	f, err := s.d.Pending.FirePending(ctx, orgID, id, func(ctx context.Context, r DueRun) Firing { return s.fire(ctx, r, &actor) })
	switch {
	case err != nil && f.RunID != nil:
		// The run's enqueue committed; only the pending row's write-back
		// failed. The row stays, and the ticker replays the same run group
		// (no second run), so the person is told the run started (final
		// review 7).
		s.d.Log.ErrorContext(ctx, "collected run started, but its pending run could not be written back", "trigger", id, "org", orgID, "err", err)
		return Result{Outcome: OutcomeTriggered, RunID: f.RunID}, nil
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
