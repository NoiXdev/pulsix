package chain

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/jobs"
)

// The starter's limits: a chain is at most maxDepth runs long, a start is
// tried maxAttempts times, and a retry waits 2^attempts ticks, at most
// maxBackoff.
const (
	maxDepth    = 10
	maxAttempts = 5
	maxBackoff  = 5 * time.Minute
)

// Reasons the starter records on a follow-up.
const (
	ReasonTemplateDeleted = "the template was deleted"
	ReasonChainTooLong    = "the chain is too long"
	ReasonAlreadyInChain  = "the template is already in this chain"
	ReasonNotInScope      = "none of the hosts is in this template's scope"
	ReasonAllInWindow     = "every host is in a maintenance window"
	ReasonInvalidGuard    = "could not start: the schedule's maintenance guard is invalid"
	reasonWaiting         = "waiting for the active run of %s"
	reasonSomeInWindow    = "%d host(s) in a maintenance window"
	reasonLeftOut         = "%d host(s) left out for maintenance"
	reasonCouldNotStart   = "could not start: %s"
)

// The maintenance guard modes of a schedule; "" reads as ignore.
const (
	maintenanceIgnore  = "ignore"
	maintenanceSkip    = "skip"
	maintenanceExclude = "exclude_hosts"
)

// Wiring gaps fail closed: a scheduled chain is never started unguarded.
var (
	errNoScheduleGuards = errors.New("no schedule guard source is wired")
	errNoMaintenance    = errors.New("no maintenance checker is wired")
)

// decision is an Outcome plus what auditing and notifying it needs.
type decision struct {
	Outcome
	name   string
	notice *SkipNotice
}

// decide applies the spec's per-row rules in order: depth, cycle, no host,
// an earlier start whose write-back was lost, deleted template, active run,
// maintenance, then the enqueue. The active-run check and the enqueue run
// under the batch's lock on the template, so two managers never both see
// no active run and start it twice.
func (s *Starter) decide(ctx context.Context, batch DueBatch, f FollowUp, now time.Time) decision {
	scoped := db.WithOrgScope(ctx, f.OrgID)
	if f.Depth >= maxDepth {
		return s.skip(scoped, f, now, "", ReasonChainTooLong, NoticeFailure)
	}
	if slices.Contains(f.Path, f.TemplateID) {
		return s.skip(scoped, f, now, "", ReasonAlreadyInChain, NoticeFailure)
	}
	if len(f.HostIDs) == 0 {
		// OnlyHosts empty would mean unrestricted; a due row without hosts
		// must never start.
		return s.skip(scoped, f, now, "", ReasonNoHostPlayed, "")
	}
	if d, ok := s.replayLostStart(scoped, f, now); ok {
		return d
	}
	name, err := s.names.TemplateName(scoped, f.OrgID, f.TemplateID)
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		return s.skip(scoped, f, now, "", ReasonTemplateDeleted, NoticeFailure)
	case err != nil:
		return s.retry(scoped, f, now, "", fmt.Errorf("read the template: %w", err))
	}
	locked, err := batch.TryLockTemplate(ctx, f.OrgID, f.TemplateID)
	if err != nil {
		return s.retry(scoped, f, now, name, err)
	}
	active := !locked // another manager is starting it right now
	if locked {
		if active, err = s.runs.HasActiveRun(scoped, f.OrgID, f.TemplateID); err != nil {
			return s.retry(scoped, f, now, name, fmt.Errorf("check for an active run: %w", err))
		}
	}
	if active {
		// Never counted, never timed out: an active run always ends.
		return decision{Outcome: Outcome{State: StateDue, Reason: fmt.Sprintf(reasonWaiting, name), At: now, NextAttemptAt: now.Add(s.tick)}, name: name}
	}
	exclude, reason, stop, err := s.maintenance(scoped, f, now, name)
	switch {
	case err != nil:
		return s.retry(scoped, f, now, name, err)
	case stop != nil:
		return *stop
	}
	created, err := s.runs.EnqueueRunWith(scoped, f.OrgID, f.TemplateID, s.request(f, exclude))
	switch {
	case errors.Is(err, jobs.ErrEmptyScope), errors.Is(err, jobs.ErrHostNotInScope):
		return s.skip(scoped, f, now, name, ReasonNotInScope, "")
	case errors.Is(err, jobs.ErrAllHostsExcluded):
		// Only the maintenance check sets ExcludeHosts: every host the
		// template's scope kept is in a window.
		return s.skip(scoped, f, now, name, ReasonAllInWindow, NoticeSkipped)
	case errors.Is(err, jobs.ErrNotFound):
		return s.skip(scoped, f, now, name, ReasonTemplateDeleted, NoticeFailure)
	case err != nil:
		d := s.retry(scoped, f, now, name, err)
		if d.State == StateSkipped && len(created) > 0 {
			// Giving up on a start that reached some zones: those run.
			return started(now, name, created[0].RunGroupID, reason)
		}
		return d
	}
	return started(now, name, created[0].RunGroupID, reason)
}

// replayLostStart recognises a start whose write-back was lost (a failed
// row write or commit, a manager restart): the row's idempotency key already
// names a run group with jobs. Without this the row would wait on its own
// child run and stay unlinked from it. The replay enqueue creates whatever
// zones the earlier one did not reach, under the same maintenance decision
// as a first start: hosts in a window are left out, and a guard that stops
// the follow-up stops the replay, so no zone in a window gets a job. The
// row is started with that group whatever the replay answers, because the
// group's jobs exist.
func (s *Starter) replayLostStart(ctx context.Context, f FollowUp, now time.Time) (decision, bool) {
	key := s.request(f, nil).IdempotencyKey
	group, err := s.runs.StartedRunGroup(ctx, f.OrgID, key)
	if err != nil {
		return s.retry(ctx, f, now, "", fmt.Errorf("look up an earlier start: %w", err)), true
	}
	if group == nil {
		return decision{}, false
	}
	exclude, reason, stop, err := s.maintenance(ctx, f, now, "")
	switch {
	case err != nil:
		d := s.retry(ctx, f, now, "", err)
		if d.State == StateSkipped {
			// Giving up on completing the group: the jobs it has run.
			return started(now, "", *group, ""), true
		}
		return d, true
	case stop != nil:
		// The group's jobs exist and run; the zones it lacks stay out.
		return started(now, "", *group, stop.Reason), true
	}
	if _, err := s.runs.EnqueueRunWith(ctx, f.OrgID, f.TemplateID, s.request(f, exclude)); err != nil {
		s.log.WarnContext(ctx, "follow-up replay could not complete its run group; recording the start that exists", "follow_up", f.ID, "org", f.OrgID, "group", *group, "err", err)
	}
	return started(now, "", *group, reason), true
}

// request is the chained run of f. Chain carries the row's own depth and
// path; the enqueue extends them for the child's follow-ups.
func (s *Starter) request(f FollowUp, exclude []uuid.UUID) jobs.RunRequest {
	parent := f.ParentGroupID
	return jobs.RunRequest{
		Trigger: jobs.TriggerChain, TriggerRef: &parent, OnlyHosts: f.HostIDs, ExcludeHosts: exclude,
		Chain: &jobs.ChainContext{
			RootTrigger: f.RootTrigger, RootRef: f.RootRef, Depth: f.Depth, Path: f.Path, ConfiguredBy: f.ConfiguredBy,
		},
		IdempotencyKey: "chain:" + f.ID.String(),
	}
}

// maintenance applies the root schedule's guard to f's hosts. It returns
// the hosts to leave out and the reason saying so, or a skip decision.
func (s *Starter) maintenance(ctx context.Context, f FollowUp, now time.Time, name string) ([]uuid.UUID, string, *decision, error) {
	if f.RootTrigger != jobs.TriggerSchedule || f.RootRef == nil {
		return nil, "", nil, nil
	}
	if s.guards == nil {
		return nil, "", nil, errNoScheduleGuards
	}
	mode, found, err := s.guards.MaintenanceMode(ctx, f.OrgID, *f.RootRef)
	if err != nil {
		return nil, "", nil, fmt.Errorf("read the schedule's maintenance guard: %w", err)
	}
	if !found {
		return nil, "", nil, nil
	}
	switch mode {
	case "", maintenanceIgnore:
		return nil, "", nil, nil
	case maintenanceSkip, maintenanceExclude:
	default:
		s.log.ErrorContext(ctx, "follow-up's root schedule has an unrecognised maintenance guard; skipping it", "follow_up", f.ID, "schedule", *f.RootRef)
		d := s.skip(ctx, f, now, name, ReasonInvalidGuard, NoticeFailure)
		return nil, "", &d, nil
	}
	if s.maint == nil {
		return nil, "", nil, errNoMaintenance
	}
	covered, err := s.maint.HostsInMaintenance(ctx, f.OrgID, f.HostIDs, now)
	if err != nil {
		return nil, "", nil, fmt.Errorf("check maintenance windows: %w", err)
	}
	var in []uuid.UUID
	for _, h := range f.HostIDs {
		if covered[h] {
			in = append(in, h)
		}
	}
	var d decision
	switch {
	case len(in) == 0:
		return nil, "", nil, nil
	case mode == maintenanceSkip:
		d = s.skip(ctx, f, now, name, fmt.Sprintf(reasonSomeInWindow, len(in)), NoticeSkipped)
	case len(in) == len(f.HostIDs):
		d = s.skip(ctx, f, now, name, ReasonAllInWindow, NoticeSkipped)
	default:
		return in, fmt.Sprintf(reasonLeftOut, len(in)), nil, nil
	}
	return nil, "", &d, nil
}

func started(now time.Time, name string, child uuid.UUID, reason string) decision {
	return decision{Outcome: Outcome{State: StateStarted, Reason: reason, ChildGroupID: child, At: now}, name: name}
}

// retry counts a failed start and tries again after the backoff, or gives
// up at maxAttempts.
func (s *Starter) retry(ctx context.Context, f FollowUp, now time.Time, name string, cause error) decision {
	attempts := f.Attempts + 1
	reason := fmt.Sprintf(reasonCouldNotStart, cause)
	s.log.WarnContext(ctx, "follow-up could not start", "follow_up", f.ID, "org", f.OrgID, "attempt", attempts, "err", cause)
	if attempts >= maxAttempts {
		d := s.skip(ctx, f, now, name, reason, NoticeFailure)
		d.CountAttempt = true
		return d
	}
	return decision{Outcome: Outcome{State: StateDue, Reason: reason, At: now, NextAttemptAt: now.Add(s.backoff(attempts)), CountAttempt: true}, name: name}
}

// backoff is 2^attempts ticks, at most maxBackoff.
func (s *Starter) backoff(attempts int) time.Duration {
	d := s.tick
	for range attempts {
		if d *= 2; d >= maxBackoff {
			return maxBackoff
		}
	}
	return d
}

// skip decides f is never started; category, when set, notifies the root
// schedule's rules if the chain began on a schedule that still exists.
func (s *Starter) skip(ctx context.Context, f FollowUp, now time.Time, name, reason, category string) decision {
	d := decision{Outcome: Outcome{State: StateSkipped, Reason: reason, At: now}, name: name}
	if category == "" || s.notifier == nil || s.guards == nil || f.RootTrigger != jobs.TriggerSchedule || f.RootRef == nil || len(f.Path) == 0 {
		return d
	}
	_, found, err := s.guards.MaintenanceMode(ctx, f.OrgID, *f.RootRef)
	if err != nil {
		s.log.ErrorContext(ctx, "look up a follow-up's root schedule; its skip is not notified", "follow_up", f.ID, "org", f.OrgID, "err", err)
		return d
	}
	if found {
		d.notice = &SkipNotice{
			OrgID: f.OrgID, ScheduleID: *f.RootRef, RowID: f.ID, TemplateID: f.TemplateID, ParentTemplateID: f.Path[len(f.Path)-1],
			Category: category, Reason: reason, DecidedAt: now,
		}
	}
	return d
}

// auditOutcome records a started or skipped follow-up as chain.run. An audit
// failure is logged; the outcome stands.
func (s *Starter) auditOutcome(ctx context.Context, f FollowUp, d decision) {
	if s.audit == nil {
		return
	}
	scoped := db.WithOrgScope(ctx, f.OrgID)
	name := d.name
	if name == "" && d.Reason != ReasonTemplateDeleted {
		name, _ = s.names.TemplateName(scoped, f.OrgID, f.TemplateID)
	}
	if name == "" {
		name = f.TemplateID.String()
	}
	meta := map[string]any{
		"parent_group_id": f.ParentGroupID.String(), "root_trigger": f.RootTrigger, "depth": f.Depth,
		"outcome": string(d.State), "reason": d.Reason,
	}
	summary := fmt.Sprintf("skipped follow-up %s: %s", name, d.Reason)
	if d.State == StateStarted {
		meta["child_group_id"] = d.ChildGroupID.String()
		summary = fmt.Sprintf("started follow-up %s of run %s", name, f.ParentGroupID)
	}
	if f.RootRef != nil {
		meta["root_ref"] = f.RootRef.String()
	}
	if f.ConfiguredBy != nil {
		meta["configured_by"] = f.ConfiguredBy.String()
	}
	org, actor := f.OrgID, f.ParentGroupID
	if err := s.audit.Record(scoped, audit.Event{
		OrgID: &org, ActorKind: audit.ActorKindChain, ActorID: &actor, Action: "chain.run",
		ObjectType: "job_template", ObjectID: f.TemplateID.String(), Summary: summary, Meta: meta,
	}); err != nil {
		s.log.ErrorContext(ctx, "audit follow-up start", "follow_up", f.ID, "org", f.OrgID, "err", err)
	}
}
