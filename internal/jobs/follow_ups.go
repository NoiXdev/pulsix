// Job chaining on the enqueue side (backlog 2.116): the chain trigger, the
// context a chained run carries, and the port the enqueue paths call to plan
// a run's follow-ups. The planner itself lives in internal/chain; this
// package only knows when to call it.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
)

const (
	// TriggerChain marks a run a finished run's follow-up started.
	TriggerChain = "chain"
	// MaxFollowUps caps the follow-ups of one template and of one run override.
	MaxFollowUps = 10
)

// ChainContext is where in a chain a run sits: what started the root run,
// how deep this run is and which templates came before it.
type ChainContext struct {
	RootTrigger string
	RootRef     *uuid.UUID
	// Depth is the number of runs in the chain up to and including this one.
	Depth int
	// Path lists the templates of the chain up to and including this run's.
	Path         []uuid.UUID
	ConfiguredBy *uuid.UUID
}

// FollowUpPlanner records the follow-ups a run group will start once it
// finishes. Both calls must be idempotent: an idempotent enqueue replay
// plans the same group again.
type FollowUpPlanner interface {
	// PlanFollowUps writes the follow-ups of templateID for groupID: the
	// override when given, else the template's own list. c is the chain
	// context of the run being planned for.
	PlanFollowUps(ctx context.Context, orgID, groupID, templateID uuid.UUID, override *[]uuid.UUID, c ChainContext) error
	// CopyFollowUps gives toGroupID the follow-ups fromGroupID was planned
	// with, as a retry of the original run.
	CopyFollowUps(ctx context.Context, orgID, fromGroupID, toGroupID uuid.UUID, configuredBy *uuid.UUID) error
}

// SetFollowUpPlanner wires the planner; unwired, runs plan no follow-ups.
func (s *Service) SetFollowUpPlanner(p FollowUpPlanner) { s.followUps = p }

// planFollowUps validates a run's follow-up override and hands the run to
// the planner. It runs before the run's jobs exist, so a job can never
// finish before its follow-ups are recorded; a group that never gets jobs
// is resolved by the planner's sweep.
func (s *Service) planFollowUps(ctx context.Context, orgID, groupID uuid.UUID, tmpl TemplateRow, req RunRequest) error {
	if s.followUps == nil {
		return nil
	}
	if req.FollowUps != nil {
		if err := s.checkFollowUpOverride(ctx, orgID, tmpl.ID, *req.FollowUps); err != nil {
			return err
		}
	}
	c := ChainContext{RootTrigger: req.Trigger, RootRef: req.TriggerRef, Depth: 1, Path: []uuid.UUID{tmpl.ID}, ConfiguredBy: req.Actor}
	if req.Chain != nil {
		c = *req.Chain
		c.Depth++
		c.Path = append(slices.Clone(req.Chain.Path), tmpl.ID)
	}
	if err := s.followUps.PlanFollowUps(ctx, orgID, groupID, tmpl.ID, req.FollowUps, c); err != nil {
		return fmt.Errorf("plan follow-ups: %w", err)
	}
	return nil
}

// TemplateExistence is the optional cheap existence check the override
// validation prefers over TemplateLookup, which also opens secrets and
// refuses a template whose playbook is not registered yet. NewService
// resolves it from the TemplateSource's own type.
type TemplateExistence interface {
	TemplateExists(ctx context.Context, orgID, id uuid.UUID) (bool, error)
}

// checkFollowUpOverride validates an override like a template's list:
// at most MaxFollowUps, no self, no duplicates, every template exists.
// Whether a follow-up can start is the starter's concern, later.
func (s *Service) checkFollowUpOverride(ctx context.Context, orgID, self uuid.UUID, ids []uuid.UUID) error {
	if len(ids) > MaxFollowUps {
		return fmt.Errorf("%w: at most %d follow-ups per run", ErrInvalidInput, MaxFollowUps)
	}
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		switch {
		case id == self:
			return fmt.Errorf("%w: a template cannot follow itself", ErrInvalidInput)
		case seen[id]:
			return fmt.Errorf("%w: follow-up template %s is listed twice", ErrInvalidInput, id)
		}
		seen[id] = true
	}
	for _, id := range ids {
		exists, err := s.followUpExists(ctx, orgID, id)
		if err != nil {
			return fmt.Errorf("check follow-up template %s: %w", id, err)
		}
		if !exists {
			return fmt.Errorf("%w: follow-up template %s does not exist", ErrInvalidInput, id)
		}
	}
	return nil
}

func (s *Service) followUpExists(ctx context.Context, orgID, id uuid.UUID) (bool, error) {
	if s.templateExists != nil {
		return s.templateExists.TemplateExists(ctx, orgID, id)
	}
	_, err := s.templates.TemplateLookup(ctx, orgID, id)
	switch {
	case err == nil, errors.Is(err, ErrTemplateNotReady):
		return true, nil
	case errors.Is(err, ErrNotFound):
		return false, nil
	}
	return false, err
}
