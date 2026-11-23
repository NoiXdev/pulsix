package runrequests

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// Approved is the outcome of an approval: the decided request, the
// template revision and the follow-ups block of the picture it approved,
// and the direct follow-ups the approval started, in start order. The
// revision is the approved picture's, which differs from the request's
// own TemplateRevision when the template changed after the request and
// the person reviewed the change.
type Approved struct {
	Request          Request
	TemplateRevision int64
	FollowUps        *PictureFollowUps
	Started          []PictureFollowUp
}

// Approve enqueues what the request asks for, if fingerprint still matches
// the request's current run picture. followUps is the person's selection of
// the picture's direct follow-ups; nil means the default (all startable
// ones when the organization allows follow-ups, none otherwise). The
// selection is checked against the picture just recomputed, before the
// decision, so a refused one leaves the request pending. The decision is
// recorded first, with the fingerprint and the run group (the request's
// id), so a concurrent second approval loses and every job's claim finds
// the request; the enqueue carries an idempotency key for runs; a failing
// enqueue puts the request back to pending. What runs stays bound to
// fingerprint until the claim: see CheckRunClaim.
func (s *Service) Approve(ctx context.Context, orgID, id, by uuid.UUID, fingerprint string, followUps *[]uuid.UUID) (Approved, error) {
	r, p, current, err := s.Get(ctx, orgID, id)
	if err != nil {
		return Approved{}, err
	}
	switch r.Status {
	case StatusExpired:
		return Approved{}, ErrExpired
	case StatusPending:
	default:
		return Approved{}, ErrDecided
	}
	if fingerprint != current {
		return Approved{}, ErrChanged
	}
	started, err := selectFollowUps(p.FollowUps, followUps)
	if err != nil {
		return Approved{}, err
	}
	list := make([]uuid.UUID, len(started))
	for i, n := range started {
		list[i] = uuid.MustParse(n.TemplateID)
	}
	at := s.now()
	ok, err := s.store.Decide(ctx, orgID, id, StatusApproved, by, at, fingerprint)
	if err != nil {
		return Approved{}, fmt.Errorf("decide run request: %w", err)
	}
	if !ok {
		return Approved{}, ErrDecided
	}
	group := r.ID
	if r.Kind == KindRetryFailed {
		err = s.enqueuer.RetryFailed(ctx, orgID, *r.JobID, by, idempotencyKey(r.ID), group, list)
	} else {
		err = s.enqueuer.Run(ctx, orgID, r.TemplateID, r.HostID, by, idempotencyKey(r.ID), group, list)
	}
	if err != nil {
		if rerr := s.store.RevertToPending(ctx, orgID, id); rerr != nil {
			slog.ErrorContext(ctx, "run request: revert after a failed enqueue", "err", rerr, "request_id", id)
		}
		return Approved{}, err
	}
	r.Status, r.DecidedBy, r.DecidedAt, r.RunGroupID, r.ApprovedFingerprint = StatusApproved, &by, &at, &group, fingerprint
	return Approved{Request: r, TemplateRevision: p.TemplateRevision, FollowUps: p.FollowUps, Started: started}, nil
}

// ErrChangedAfterApproval fails the claim of an approved run whose run
// picture no longer matches the one the person approved.
var ErrChangedAfterApproval = errors.New("what the run uses changed after it was approved; ask for the run again")

// errRequestNotApproved fails the claim of a job whose run group belongs
// to a request that is not approved (a failed enqueue put it back).
var errRequestNotApproved = errors.New("the run's request is not approved")

// CheckRunClaim is the claim guard of approved runs (jobs.ClaimGuard): a
// run group that belongs to no request passes; one that belongs to a
// request passes only while that request is approved and its run picture
// still has the fingerprint the person approved. Anything else, a failed
// lookup included, refuses the claim, and the queue fails the job with the
// reason.
func (s *Service) CheckRunClaim(ctx context.Context, orgID, runGroupID uuid.UUID) error {
	r, err := s.store.ByRunGroup(ctx, orgID, runGroupID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("look up the run's request: %w", err)
	}
	if r.Status != StatusApproved || r.ApprovedFingerprint == "" {
		return errRequestNotApproved
	}
	p, err := s.pictures.Picture(ctx, orgID, r)
	if err != nil {
		return fmt.Errorf("resolve the approved run: %w", err)
	}
	if Fingerprint(r.ID, p) != r.ApprovedFingerprint {
		return ErrChangedAfterApproval
	}
	return nil
}

// idempotencyKey is the key an approval enqueues with, a run or a retry
// alike, so approving a request again after a failed enqueue replays what
// the first attempt created instead of enqueueing it twice.
func idempotencyKey(id uuid.UUID) string { return "run-request:" + id.String() }
