package api

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/runrequests"
)

// changeActor is who makes a change through scope: the template and
// repository services pause what runs unattended after an assistant
// token's run-relevant change (spec 2026-11-25 C2); the decision is
// theirs, this only says who is asking.
func changeActor(scope Scope) assistantchange.Actor {
	return assistantchange.Actor{UserID: scope.UserID, TokenID: scope.TokenID, Assistant: scope.TokenKind == auth.TokenKindAssistant}
}

// latestReviews is the newest review request of kind of each of ids, for
// the schedule and trigger pages' "needs review" link. A failed lookup
// leaves the links out: the item's reason still says it waits for review.
func latestReviews(ctx context.Context, deps Deps, orgID uuid.UUID, kind runrequests.Kind, ids []uuid.UUID) map[uuid.UUID]uuid.UUID {
	if len(ids) == 0 || deps.RunRequests == nil {
		return nil
	}
	out, err := deps.RunRequests.LatestReviews(ctx, orgID, kind, ids)
	if err != nil {
		return nil
	}
	return out
}

// reviewApprovalSummary is the audit summary of a review's approval (spec
// 2026-11-25 C2).
func reviewApprovalSummary(a runrequests.Approved) string {
	pr := a.Review
	summary := "approved review request (" + pr.Item + " " + pr.ItemID
	if !pr.PausedForReview {
		summary += "; it was no longer paused for review"
	}
	return summary + ")"
}

// auditAppliedReview records what an approved review did to its item in
// the item's own feed, as the resume and update routes would, attributed
// to the approver: nothing when the item was no longer paused for review,
// an acceptance (no resume) when a person had stopped it before.
func auditAppliedReview(ctx context.Context, deps Deps, scope Scope, a runrequests.Approved) {
	pr := a.Review
	suffix := " (approved review " + a.Request.ID.String() + ")"
	switch {
	case !pr.PausedForReview:
	case pr.PausedBefore != nil && pr.Schedule != nil:
		// Accepted, not resumed: no resume action, so who had paused it
		// stays the answer for the next review (re-review N4).
		recordOrgEvent(ctx, deps, scope.OrgID, scope.UserID, http.MethodPost+" acceptScheduleReview", "schedule", pr.ItemID,
			"accepted the assistant's changes to schedule "+pr.Schedule.Name+"; it stays paused"+suffix)
	case pr.PausedBefore != nil && pr.Trigger != nil:
		recordOrgEvent(ctx, deps, scope.OrgID, scope.UserID, http.MethodPost+" acceptTriggerReview", "trigger", pr.ItemID,
			"accepted the assistant's changes to webhook trigger "+pr.Trigger.Name+"; it stays disabled"+suffix)
	case pr.Schedule != nil:
		recordOrgEvent(ctx, deps, scope.OrgID, scope.UserID, http.MethodPost+" resumeSchedule", "schedule", pr.ItemID,
			"resumed schedule "+pr.Schedule.Name+suffix)
	case pr.Trigger != nil:
		recordOrgEvent(ctx, deps, scope.OrgID, scope.UserID, http.MethodPost+" enableTrigger", "trigger", pr.ItemID,
			"enabled webhook trigger "+pr.Trigger.Name+suffix)
	}
}
