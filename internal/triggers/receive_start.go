package triggers

import (
	"context"
	"errors"
	"fmt"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/secretshadow"
	"github.com/noixdev/pulsix/internal/templates"
)

// The receive path's last step: a delivery that passed every check starts
// its run or sync, or joins the trigger's collected run (settle.go).

func (s *Service) runTemplate(ctx context.Context, t Trigger, d Delivery, body []byte) (Result, Delivery, error) {
	ov, field, err := s.mapPayload(ctx, t, body)
	if err != nil {
		if errors.Is(err, errMappingCompile) {
			s.d.Log.ErrorContext(ctx, "webhook trigger's stored mapping failed to compile", "trigger", t.ID, "org", t.OrgID, "err", err)
			return s.finish(ctx, d, Result{Outcome: OutcomeFailed}, "the trigger's mapping is invalid (see the manager log)")
		}
		if field == "" {
			return Result{}, d, err
		}
		reason := "invalid value at " + field
		if errors.Is(err, secretshadow.ErrShadow) {
			// Names the variable and the kind of secret, never a value;
			// the sender's answer names only the field.
			reason += ": " + err.Error()
		}
		return s.finish(ctx, d, Result{Outcome: OutcomeInvalidPayload, Field: field}, reason)
	}
	if t.SettleSeconds > 0 {
		return s.collect(ctx, t, d, ov)
	}
	// The leftover collected run (settle turned off while it waited) is
	// taken out BEFORE this delivery's own start, on purpose: older values
	// must never run after newer ones. So even when the start below is
	// then skipped (on_active skip) or refused, the leftover stays
	// discarded and neither value set runs; its deliveries say "superseded
	// by a later delivery" (final review 1).
	window, err := s.supersede(ctx, t)
	if err != nil {
		return Result{}, d, err
	}
	res, d, err := s.startTemplate(ctx, t, d, ov)
	s.noteSuperseded(ctx, t, window, res, d)
	return res, d, err
}

// startTemplate starts the delivery's own run (no settle delay).
func (s *Service) startTemplate(ctx context.Context, t Trigger, d Delivery, ov templates.Overrides) (Result, Delivery, error) {
	// Check-then-enqueue, deliberately not serialised (backlog 2.62).
	// Two deliveries landing in the same instant — or one racing the
	// schedule ticker's own overlap=skip for the same template — can
	// both read "nothing active" and both enqueue. Closing that means
	// holding a per-template advisory lock across the active check AND
	// the whole enqueue, and the enqueue is not one transaction: it
	// resolves the template's scope against the inventory and creates a
	// job per zone. Serialising all of that behind one lock to turn a
	// rare duplicate into no duplicate is a poor trade — the guard
	// exists to stop runs PILING UP, which it still does, and a run
	// that does slip through is one extra run, visible in the log with
	// its own trigger.
	if t.OnActive == OnActiveSkip {
		active, err := s.d.Runs.HasActiveRun(ctx, t.OrgID, *t.TemplateID)
		if err != nil {
			return Result{}, d, err
		}
		if active {
			return s.finish(ctx, d, Result{Outcome: OutcomeSkippedActive}, "a run of the template is still active")
		}
	}
	ref := t.ID
	created, err := s.d.Runs.EnqueueRunWith(ctx, t.OrgID, *t.TemplateID, jobs.RunRequest{Trigger: jobs.TriggerWebhook, TriggerRef: &ref, Overrides: webhookOverrides(ov)})
	if err != nil {
		s.d.Log.ErrorContext(ctx, "webhook run could not be enqueued", "trigger", t.ID, "org", t.OrgID, "err", err)
		return s.finish(ctx, d, Result{Outcome: OutcomeFailed}, failureReason(err))
	}
	runID := created[0].ID
	d.JobGroupID = &created[0].RunGroupID
	s.auditRun(ctx, t, d, "job_template", t.TemplateID.String())
	return s.finish(ctx, d, Result{Outcome: OutcomeTriggered, RunID: &runID}, "")
}

func (s *Service) syncRepo(ctx context.Context, t Trigger, d Delivery) (Result, Delivery, error) {
	if t.SettleSeconds > 0 {
		return s.collect(ctx, t, d, templates.Overrides{})
	}
	// Superseded before the start on purpose, as in runTemplate.
	window, err := s.supersede(ctx, t)
	if err != nil {
		return Result{}, d, err
	}
	res, d, err := s.startSync(ctx, t, d)
	s.noteSuperseded(ctx, t, window, res, d)
	return res, d, err
}

// startSync starts the delivery's own sync (no settle delay).
func (s *Service) startSync(ctx context.Context, t Trigger, d Delivery) (Result, Delivery, error) {
	if t.OnActive == OnActiveSkip {
		active, err := s.d.Runs.HasActiveSync(ctx, t.OrgID, *t.RepoID)
		if err != nil {
			return Result{}, d, err
		}
		if active {
			return s.finish(ctx, d, Result{Outcome: OutcomeSkippedActive}, "a sync of the repository is still active")
		}
	}
	ref := t.ID
	j, err := s.d.Runs.EnqueueSyncWith(ctx, t.OrgID, *t.RepoID, jobs.RunRequest{Trigger: jobs.TriggerWebhook, TriggerRef: &ref})
	if err != nil {
		s.d.Log.ErrorContext(ctx, "webhook sync could not be enqueued", "trigger", t.ID, "org", t.OrgID, "err", err)
		return s.finish(ctx, d, Result{Outcome: OutcomeFailed}, failureReason(err))
	}
	runID := j.ID
	d.JobGroupID = &j.RunGroupID
	s.auditRun(ctx, t, d, "repo", t.RepoID.String())
	return s.finish(ctx, d, Result{Outcome: OutcomeTriggered, RunID: &runID}, "")
}

func (s *Service) auditRun(ctx context.Context, t Trigger, d Delivery, objectType, objectID string) {
	if s.d.Audit == nil {
		return
	}
	orgID, actor := t.OrgID, t.ID
	meta := map[string]any{"trigger_id": t.ID.String(), "source_ip": d.SourceIP}
	if d.JobGroupID != nil {
		meta["job_group_id"] = d.JobGroupID.String()
	}
	if d.Reason == "test call" {
		meta["test_call"] = true
	}
	if err := s.d.Audit.Record(ctx, audit.Event{
		OrgID: &orgID, ActorKind: audit.ActorKindWebhook, ActorID: &actor, Action: "webhook.run",
		ObjectType: objectType, ObjectID: objectID, Summary: fmt.Sprintf("webhook %q started a run", t.Name), Meta: meta,
	}); err != nil {
		s.d.Log.ErrorContext(ctx, "audit webhook run", "trigger", t.ID, "err", err)
	}
}

// failureReason is the generic reason a failed delivery records.
func failureReason(err error) string {
	switch {
	case errors.Is(err, jobs.ErrEmptyScope):
		return "the template's scope resolves to no host"
	case errors.Is(err, jobs.ErrHostKeyChanged):
		return "every targeted host has a changed host key"
	case errors.Is(err, jobs.ErrNoZone):
		return "the organization has no default zone"
	// schedules/service.go's own case, same sentinel, same reasoning: a
	// projection of an installation-owned definition (spec v0.2f) whose
	// playbook has not synced in this organization yet.
	case errors.Is(err, jobs.ErrTemplateNotReady):
		return "the template's playbook has not been registered in this organization yet"
	}
	return "could not enqueue (see the manager log)"
}
