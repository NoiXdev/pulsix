package runrequests

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

// ErrNotAllowed refuses an approval the approving person lacks a
// permission for; the message names it.
var ErrNotAllowed = errors.New("not allowed")

// errScheduleNotAllowed refuses the approval of a schedule request by a
// person who may not write schedules.
var errScheduleNotAllowed = fmt.Errorf("%w: approving a schedule request needs the templates.write permission", ErrNotAllowed)

// errScheduleGone refuses the approval of an update or resume whose
// schedule was deleted.
var errScheduleGone = fmt.Errorf("%w: the schedule no longer exists", ErrInvalidInput)

// Approver is what the approving person may do beyond approving runs
// (jobs.run, which the transport checks): Enrol, mint agent enrollment
// tokens (agents.write, spec 2026-11-25 B); WriteTemplates, write
// schedules (templates.write, the schedule routes' permission).
type Approver struct {
	Enrol          bool
	WriteTemplates bool
}

// scheduleRequest fills r for a schedule request: the target (a create
// takes a fresh id, so the picture and the approval name the same
// schedule), the change as the resolver checked it, and the template.
func (s *Service) scheduleRequest(ctx context.Context, orgID uuid.UUID, in *ScheduleInput, r *Request) error {
	if in == nil {
		return fmt.Errorf("%w: a schedule request names what it does", ErrInvalidInput)
	}
	var target uuid.UUID
	switch in.Action {
	case ScheduleCreate:
		if in.Spec == nil {
			return fmt.Errorf("%w: a create names the schedule", ErrInvalidInput)
		}
		target = s.newID()
	case ScheduleUpdate:
		if in.Patch == nil {
			return fmt.Errorf("%w: an update names what changes", ErrInvalidInput)
		}
		target = in.ScheduleID
	case ScheduleResume:
		target = in.ScheduleID
	default:
		return fmt.Errorf("%w: unknown schedule action %q", ErrInvalidInput, in.Action)
	}
	if target == uuid.Nil {
		return fmt.Errorf("%w: an update or resume names its schedule", ErrInvalidInput)
	}
	change, tmpl, rev, err := s.auto.ValidateSchedule(ctx, orgID, target, *in)
	if err != nil {
		return err
	}
	r.Schedule, r.TargetID, r.TemplateID, r.TemplateRevision = &change, &target, tmpl, rev
	return nil
}

// approveSchedule is Approve for a schedule request (r with its current
// picture p): only a person who may write schedules approves it; the
// picture must still be the one the person saw, and its schedule must
// still exist. The decision and the schedule's write are one
// transaction: the decision first, so a concurrent second approval
// loses, then the schedule created, changed or resumed exactly as
// pictured, as by. A failure of either leaves the request pending and the
// schedule as it was.
func (s *Service) approveSchedule(ctx context.Context, orgID uuid.UUID, r Request, p Picture, by uuid.UUID, fingerprint, current string, followUps *[]uuid.UUID, may Approver) (Approved, error) {
	if !may.WriteTemplates {
		return Approved{}, errScheduleNotAllowed
	}
	if fingerprint != current {
		return Approved{}, ErrChanged
	}
	if followUps != nil {
		return Approved{}, fmt.Errorf("%w: a schedule request starts no follow-ups", ErrInvalidInput)
	}
	if p.Schedule == nil {
		return Approved{}, fmt.Errorf("schedule request %s has no schedule picture", r.ID)
	}
	if p.Schedule.Gone {
		return Approved{}, errScheduleGone
	}
	at := s.now()
	err := s.store.InTx(ctx, orgID, func(ctx context.Context) error {
		ok, err := s.store.Decide(ctx, orgID, r.ID, StatusApproved, by, at, fingerprint, jobs.ApprovedRun{})
		if err != nil {
			return fmt.Errorf("decide schedule request: %w", err)
		}
		if !ok {
			return ErrDecided
		}
		return s.auto.ApplySchedule(ctx, orgID, r, *p.Schedule, by)
	})
	if err != nil {
		return Approved{}, err
	}
	r.Status, r.DecidedBy, r.DecidedAt, r.ApprovedFingerprint = StatusApproved, &by, &at, fingerprint
	return Approved{Request: r, TemplateRevision: p.TemplateRevision, Schedule: p.Schedule}, nil
}
