package runrequests

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/triggers"
)

// errTriggerNotAllowed and errRepoTriggerNotAllowed refuse the approval of
// a trigger request by a person who may not write that kind of trigger.
var (
	errTriggerNotAllowed     = fmt.Errorf("%w: approving a webhook trigger request needs the templates.write permission", ErrNotAllowed)
	errRepoTriggerNotAllowed = fmt.Errorf("%w: approving a repository sync trigger request needs the repos.write permission", ErrNotAllowed)
)

// errTriggerGone refuses the approval of a request whose trigger, or the
// template or repository the trigger would start, was deleted.
var errTriggerGone = fmt.Errorf("%w: the trigger, or what it starts, no longer exists", ErrInvalidInput)

// triggerRequest fills r for a trigger request: the target (a create takes
// a fresh id, so the picture's URL and the approval name the same
// trigger), the change as the resolver checked it, and the template (none
// for a repository sync trigger).
func (s *Service) triggerRequest(ctx context.Context, orgID uuid.UUID, in *TriggerInput, r *Request) error {
	if in == nil {
		return fmt.Errorf("%w: a trigger request names what it does", ErrInvalidInput)
	}
	var target uuid.UUID
	switch in.Action {
	case TriggerCreate:
		if in.Spec == nil {
			return fmt.Errorf("%w: a create names the trigger", ErrInvalidInput)
		}
		target = s.newID()
	case TriggerUpdate:
		if in.Patch == nil {
			return fmt.Errorf("%w: an update names what changes", ErrInvalidInput)
		}
		target = in.TriggerID
	case TriggerRotate:
		target = in.TriggerID
	default:
		return fmt.Errorf("%w: unknown trigger action %q", ErrInvalidInput, in.Action)
	}
	if target == uuid.Nil {
		return fmt.Errorf("%w: an update or rotation names its trigger", ErrInvalidInput)
	}
	change, tmpl, rev, err := s.auto.ValidateTrigger(ctx, orgID, target, *in)
	if err != nil {
		return err
	}
	r.Trigger, r.TargetID, r.TemplateID, r.TemplateRevision = &change, &target, tmpl, rev
	return nil
}

// approveTrigger is Approve for a trigger request (r with its current
// picture p): only a person who may write that kind of trigger approves it
// (templates.write; repos.write for a repository sync trigger); the picture
// must still be the one the person saw, and its trigger (and what the
// trigger starts) must still exist. The decision and the trigger's write
// are one transaction, the decision first, so a concurrent second approval
// loses. The trigger's new secret (a create or a rotation) comes back in
// Approved only.
func (s *Service) approveTrigger(ctx context.Context, orgID uuid.UUID, r Request, p Picture, by uuid.UUID, fingerprint, current string, followUps *[]uuid.UUID, may Approver) (Approved, error) {
	if r.Trigger == nil {
		return Approved{}, fmt.Errorf("trigger request %s has no trigger", r.ID)
	}
	if r.Trigger.Kind == triggers.KindRepoSync {
		if !may.WriteRepos {
			return Approved{}, errRepoTriggerNotAllowed
		}
	} else if !may.WriteTemplates {
		return Approved{}, errTriggerNotAllowed
	}
	if fingerprint != current {
		return Approved{}, ErrChanged
	}
	if followUps != nil {
		return Approved{}, fmt.Errorf("%w: a trigger request starts no follow-ups", ErrInvalidInput)
	}
	if p.Trigger == nil {
		return Approved{}, fmt.Errorf("trigger request %s has no trigger picture", r.ID)
	}
	if p.Trigger.Gone {
		return Approved{}, errTriggerGone
	}
	at := s.now()
	var secret string
	err := s.store.InTx(ctx, orgID, func(ctx context.Context) error {
		ok, err := s.store.Decide(ctx, orgID, r.ID, StatusApproved, by, at, fingerprint, jobs.ApprovedRun{})
		if err != nil {
			return fmt.Errorf("decide trigger request: %w", err)
		}
		if !ok {
			return ErrDecided
		}
		secret, err = s.auto.ApplyTrigger(ctx, orgID, r, *p.Trigger, by)
		return err
	})
	if err != nil {
		return Approved{}, err
	}
	r.Status, r.DecidedBy, r.DecidedAt, r.ApprovedFingerprint = StatusApproved, &by, &at, fingerprint
	return Approved{Request: r, TemplateRevision: p.TemplateRevision, Trigger: p.Trigger, TriggerSecret: secret}, nil
}
