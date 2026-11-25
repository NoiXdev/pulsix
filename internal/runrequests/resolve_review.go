package runrequests

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/triggers"
)

// errScheduleUnderReview and errTriggerUnderReview refuse an assistant's
// resume of a schedule, or enabling of a trigger, that an assistant's
// change stopped (spec 2026-11-25 C2): the review request resumes it once
// a person approves it.
var (
	errScheduleUnderReview = fmt.Errorf("%w: an assistant's change paused this schedule for review; a person approves its review request on the approvals page to resume it", ErrInvalidInput)
	errTriggerUnderReview  = fmt.Errorf("%w: an assistant's change disabled this trigger for review; a person approves its review request on the approvals page to enable it", ErrInvalidInput)
	// errReviewGone refuses the approval of a review whose schedule or
	// trigger was deleted.
	errReviewGone = fmt.Errorf("%w: the schedule or trigger of this review no longer exists", ErrInvalidInput)
)

// reviewPicture is the picture of a review request: the item's template
// (none for a repository sync trigger), the schedule or trigger as it is
// now and whether it is still stopped for the review, and the changes.
func (r *Resolver) reviewPicture(ctx context.Context, orgID uuid.UUID, req Request) (Picture, error) {
	if req.Review == nil || req.TargetID == nil {
		return Picture{}, fmt.Errorf("review %s has no changes or target", req.ID)
	}
	p := Picture{Targets: []PictureHost{}, VaultIdentities: []PictureVault{}, ExtraVars: map[string]any{}}
	templateName := ""
	if req.TemplateID != uuid.Nil {
		p.TemplateID, templateName = req.TemplateID.String(), req.TemplateID.String()
		if tmpl, err := r.src.Templates.Get(ctx, orgID, req.TemplateID); err == nil {
			p.TemplateName, p.TemplateRevision, templateName = tmpl.Name, tmpl.Revision, tmpl.Name
		}
	}
	pr := &PictureReview{ItemID: req.TargetID.String(), Changes: changedObjects(req.Review.Changes)}
	if pb := req.Review.PausedBefore; pb != nil {
		pr.PausedBefore = &PicturePausedBefore{ByAssistantToken: pb.AssistantToken}
		if pb.By != nil {
			pr.PausedBefore.ByUserID = pb.By.String()
		}
	}
	switch req.Kind {
	case KindScheduleReview:
		pr.Item = ReviewItemSchedule
		if r.src.Schedules == nil {
			return Picture{}, errNoSchedules
		}
		cur, err := r.src.Schedules.Get(ctx, orgID, *req.TargetID)
		switch {
		case errors.Is(err, schedules.ErrNotFound):
			pr.Gone = true
		case err != nil:
			return Picture{}, err
		default:
			name := templateName
			if cur.TemplateID != req.TemplateID {
				name = r.templateName(ctx, orgID, cur.TemplateID)
			}
			state := scheduleState(specOf(cur), name)
			state.Paused, state.Revision = cur.Paused, cur.Revision
			pr.Schedule, pr.PausedForReview = &state, cur.PausedReason == schedules.PausedForReview
		}
	case KindTriggerReview:
		pr.Item = ReviewItemTrigger
		if r.src.Triggers == nil {
			return Picture{}, errNoTriggers
		}
		cur, err := r.src.Triggers.Get(ctx, orgID, *req.TargetID)
		switch {
		case errors.Is(err, triggers.ErrNotFound):
			pr.Gone = true
		case err != nil:
			return Picture{}, err
		default:
			state, found, err := r.triggerState(ctx, orgID, triggerSpecOf(cur))
			if err != nil {
				return Picture{}, err
			}
			state.Revision = cur.Revision
			pr.Trigger, pr.Gone = &state, !found
			pr.PausedForReview = cur.DisabledReason == triggers.DisabledForReview
		}
	default:
		return Picture{}, fmt.Errorf("%w: request %s is no review", ErrInvalidInput, req.ID)
	}
	p.Review = pr
	return p, nil
}

// ApplyReview implements Automation: the approved review's schedule is
// resumed, or its trigger enabled, at the revision the person saw (one
// changed since is ErrChanged); an item a person had stopped before the
// change (PausedBefore) only loses the reason and stays stopped as that
// person left it; an item no longer stopped for the review (a person
// resumed it, or stopped it for another reason) is left as it is. The
// approval runs it in the transaction that records the decision.
func (r *Resolver) ApplyReview(ctx context.Context, orgID uuid.UUID, req Request, p PictureReview, _ uuid.UUID) error {
	if req.TargetID == nil {
		return fmt.Errorf("review %s has no target", req.ID)
	}
	if p.Gone {
		return errReviewGone
	}
	if !p.PausedForReview {
		return nil
	}
	switch {
	case p.Schedule != nil:
		if r.src.Schedules == nil {
			return errNoSchedules
		}
		apply := r.src.Schedules.ResumeAt
		if p.PausedBefore != nil {
			apply = r.src.Schedules.AcceptReviewAt
		}
		if _, err := apply(ctx, orgID, *req.TargetID, p.Schedule.Revision); err != nil {
			if errors.Is(err, schedules.ErrNotFound) {
				return errReviewGone
			}
			return scheduleErr(err)
		}
	case p.Trigger != nil:
		if r.src.Triggers == nil {
			return errNoTriggers
		}
		apply := r.src.Triggers.EnableAt
		if p.PausedBefore != nil {
			apply = r.src.Triggers.AcceptReviewAt
		}
		if _, err := apply(ctx, orgID, *req.TargetID, p.Trigger.Revision); err != nil {
			if errors.Is(err, triggers.ErrNotFound) {
				return errReviewGone
			}
			return triggerErr(err)
		}
	default:
		return errReviewGone
	}
	return nil
}
