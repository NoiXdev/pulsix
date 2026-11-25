package runrequests

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/triggers"
)

// errReviewNotAllowed and errRepoReviewNotAllowed refuse the approval of a
// review by a person who may not write its item.
var (
	errReviewNotAllowed     = fmt.Errorf("%w: approving the review of a schedule or webhook trigger needs the templates.write permission", ErrNotAllowed)
	errRepoReviewNotAllowed = fmt.Errorf("%w: approving the review of a repository sync trigger needs the repos.write permission", ErrNotAllowed)
)

// approveReview is Approve for a review request (r with its current
// picture p, spec 2026-11-25 C2): only a person who may write its item
// approves it (templates.write; repos.write for a repository sync
// trigger), the picture must still be the one the person saw and its item
// must still exist. The resume (enable) and the decision are one
// transaction.
// A rejection (Reject) leaves the item stopped.
func (s *Service) approveReview(ctx context.Context, orgID uuid.UUID, r Request, p Picture, by uuid.UUID, fingerprint, current string, followUps *[]uuid.UUID, may Approver) (Approved, error) {
	if p.Review == nil {
		return Approved{}, fmt.Errorf("review %s has no review picture", r.ID)
	}
	if p.Review.Trigger != nil && p.Review.Trigger.Kind == string(triggers.KindRepoSync) {
		if !may.WriteRepos {
			return Approved{}, errRepoReviewNotAllowed
		}
	} else if !may.WriteTemplates {
		return Approved{}, errReviewNotAllowed
	}
	if fingerprint != current {
		return Approved{}, ErrChanged
	}
	if followUps != nil {
		return Approved{}, fmt.Errorf("%w: a review starts no follow-ups", ErrInvalidInput)
	}
	if p.Review.Gone {
		return Approved{}, errReviewGone
	}
	at := s.now()
	// The item first, then the decision: the order every writer of an
	// item and its review row takes (a person's resume, a delete, the
	// reviewer), so two of them never wait on each other. A concurrent
	// second approval waits on the item and then finds its revision moved
	// (ErrChanged). Schedule and trigger *request* approvals lock their
	// request row, then the item, the other way round, but they never
	// touch a review row and nothing locks an item and then a request
	// row, so no cycle forms.
	err := s.store.InTx(ctx, orgID, func(ctx context.Context) error {
		if err := s.auto.ApplyReview(ctx, orgID, r, *p.Review, by); err != nil {
			return err
		}
		ok, err := s.store.Decide(ctx, orgID, r.ID, StatusApproved, by, at, fingerprint, jobs.ApprovedRun{})
		if err != nil {
			return fmt.Errorf("decide review: %w", err)
		}
		if !ok {
			return ErrDecided
		}
		return nil
	})
	if err != nil {
		return Approved{}, err
	}
	r.Status, r.DecidedBy, r.DecidedAt, r.ApprovedFingerprint = StatusApproved, &by, &at, fingerprint
	return Approved{Request: r, TemplateRevision: p.TemplateRevision, Review: p.Review}, nil
}
