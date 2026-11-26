package runrequests

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
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
	// Schedule is the approved picture of a schedule request; nil for a
	// run or a retry.
	Schedule *PictureSchedule
	// Trigger is the approved picture of a trigger request, and
	// TriggerSecret the trigger's new secret after a create or a rotation:
	// the approval's response shows it to the approving person once; it is
	// stored nowhere in the clear and never in the request.
	Trigger       *PictureTrigger
	TriggerSecret string
	// Review is the approved picture of a review request (spec
	// 2026-11-25 C2); nil for every other kind.
	Review *PictureReview
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
//
// A run of a repository playbook is pinned to the picture's commit (spec
// 2026-11-25 A, D3): while the picture says why that commit cannot be
// pinned (CommitBlockedError: the repository must be synced first, or a
// sync of it is running), the approval is refused, before the fingerprint
// check, so the person learns what to wait for. The commit is stored with
// the decision and passed to the enqueue.
//
// A run of a template with agent enrollment (spec 2026-11-25 B: the
// picture has AgentEnrollment, a dry run's included) is approved only by
// a person who may enrol agents (mayEnrol, agents.write, which the REST
// layer checks); otherwise ErrEnrollmentNotAllowed, before anything else
// about the picture. The hosts it enrols are stored with the decision and
// passed to the enqueue like the commit.
func (s *Service) Approve(ctx context.Context, orgID, id, by uuid.UUID, fingerprint string, followUps *[]uuid.UUID, mayEnrol bool) (Approved, error) {
	return s.ApproveWith(ctx, orgID, id, by, fingerprint, followUps, Approver{Enrol: mayEnrol})
}

// ApproveWith is Approve for a request of any kind, with what the
// approving person may do: a run or a retry is approved as Approve
// describes; a schedule request needs may.WriteTemplates
// (approveSchedule); a trigger request may.WriteTemplates or, for a
// repository sync trigger, may.WriteRepos (approveTrigger); a review the
// permission its item needs (approveReview).
func (s *Service) ApproveWith(ctx context.Context, orgID, id, by uuid.UUID, fingerprint string, followUps *[]uuid.UUID, may Approver) (Approved, error) {
	r, p, current, err := s.Get(ctx, orgID, id)
	if err != nil {
		return Approved{}, err
	}
	switch r.Status {
	case StatusExpired:
		return Approved{}, ErrExpired
	case StatusWaitingForSync:
		return Approved{}, ErrWaitingForSync
	case StatusFailed:
		return Approved{}, ErrSyncFailed
	case StatusPending:
	default:
		return Approved{}, ErrDecided
	}
	switch r.Kind {
	case KindSchedule:
		return s.approveSchedule(ctx, orgID, r, p, by, fingerprint, current, followUps, may)
	case KindTrigger:
		return s.approveTrigger(ctx, orgID, r, p, by, fingerprint, current, followUps, may)
	case KindScheduleReview, KindTriggerReview:
		return s.approveReview(ctx, orgID, r, p, by, fingerprint, current, followUps, may)
	case KindRun, KindRetryFailed:
	default:
		return Approved{}, fmt.Errorf("%w: requests of kind %q cannot be approved by this version", ErrInvalidInput, r.Kind)
	}
	if p.AgentEnrollment != nil && !may.Enrol {
		return Approved{}, ErrEnrollmentNotAllowed
	}
	pin := ""
	if p.Commit != nil {
		if len(p.Commit.Blocked) > 0 {
			return Approved{}, &CommitBlockedError{Reasons: p.Commit.Blocked}
		}
		pin = p.Commit.SHA
	}
	if fingerprint != current {
		return Approved{}, ErrChanged
	}
	started, list, err := selectFollowUps(p.FollowUps, followUps)
	if err != nil {
		return Approved{}, err
	}
	enrol, err := enrollList(p)
	if err != nil {
		return Approved{}, err
	}
	approved := jobs.ApprovedRun{PinnedCommit: pin, EnrollHostIDs: enrol}
	at := s.now()
	ok, err := s.store.Decide(ctx, orgID, id, StatusApproved, by, at, fingerprint, approved)
	if err != nil {
		return Approved{}, fmt.Errorf("decide run request: %w", err)
	}
	if !ok {
		return Approved{}, ErrDecided
	}
	group := r.ID
	r.PinnedCommit, r.EnrollHostIDs = pin, enrol
	if r.Kind == KindRetryFailed {
		err = s.enqueuer.RetryFailed(ctx, orgID, r, by, idempotencyKey(r.ID), group, list, approved)
	} else {
		err = s.enqueuer.Run(ctx, orgID, r, by, idempotencyKey(r.ID), group, list)
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

// ErrEnrollmentNotAllowed refuses the approval of a run that enrols agents
// by a person who may not enrol them (agents.write).
var ErrEnrollmentNotAllowed = errors.New("this run enrols agents on its hosts; approving it needs the agents.write permission")

// ErrChangedAfterApproval fails the claim of an approved run whose run
// picture no longer matches the one the person approved.
var ErrChangedAfterApproval = errors.New("what the run uses changed after it was approved; ask for the run again")

// errRequestNotApproved fails the claim of a job whose run group belongs
// to a request that is not approved (a failed enqueue put it back). It is
// the queue's jobs.ErrApprovalWithdrawn: a job refused with it ran nothing
// for an approval that no longer stands, so a re-approval enqueues its
// zone again and its run group's outcome leaves it out.
var errRequestNotApproved = jobs.ErrApprovalWithdrawn

// errNoApprovedPicture fails the claim of a run approved without a
// recorded fingerprint (an approval older than the fingerprint column):
// there is nothing to check the run against. Unlike errRequestNotApproved
// the approval stands, so the refused job counts as an errored run.
var errNoApprovedPicture = errors.New("the run's approval recorded no run picture; ask for the run again")

// CheckRunClaim is the claim guard of approved runs (jobs.ClaimGuard): a
// run group that belongs to no request passes; one that belongs to a
// request passes only while that request is approved, the job carries
// what the approval stored (run is the job's: its pinned commit and its
// enroll list), and its run picture still has the fingerprint the person
// approved, and every host the job renders (run.HostIDs, frozen at its
// enqueue) is among that picture's targets (checkApprovedHosts). The
// picture of an approved request shows its stored commit
// (Resolver.commit) and enroll list (Resolver.agentEnrollment), so a sync
// or an agent enrolled after the approval does not fail the run. Anything else, a failed
// lookup included, refuses the claim, and the queue fails the job with the
// reason.
func (s *Service) CheckRunClaim(ctx context.Context, orgID, runGroupID uuid.UUID, run jobs.ApprovedRun) error {
	r, err := s.store.ByRunGroup(ctx, orgID, runGroupID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("look up the run's request: %w", err)
	}
	if r.Status != StatusApproved {
		return errRequestNotApproved
	}
	if r.ApprovedFingerprint == "" {
		return errNoApprovedPicture
	}
	if run.PinnedCommit != r.PinnedCommit {
		return fmt.Errorf("%w (the job is pinned to %s, the approval to %s)", ErrChangedAfterApproval, commitOrNone(run.PinnedCommit), commitOrNone(r.PinnedCommit))
	}
	if !sameEnrollList(run.EnrollHostIDs, r.EnrollHostIDs) {
		return fmt.Errorf("%w (the job enrols agents on other hosts than the approval stored)", ErrChangedAfterApproval)
	}
	p, err := s.pictures.Picture(ctx, orgID, r)
	if err != nil {
		return fmt.Errorf("resolve the approved run: %w", err)
	}
	if Fingerprint(r.ID, p) != r.ApprovedFingerprint {
		return ErrChangedAfterApproval
	}
	return checkApprovedHosts(p, run.HostIDs)
}

// ErrHostsChangedAfterApproval fails the claim of a run job that renders a
// host the approved run picture does not hold (backlog 2.141).
var ErrHostsChangedAfterApproval = errors.New("the run's hosts changed after it was approved; ask for the run again")

// checkApprovedHosts refuses a job host that is not among p's targets. p
// is the live picture, and its fingerprint equals the approved one, so
// its targets are the approved ones. A job froze its host list when it
// was enqueued, after the approval read the picture: a host that joined
// the template's hosts in between and left again before the claim is in
// the job, but in neither picture, and must not be rendered — let alone
// played on — without having been approved. Fewer hosts than approved is
// fine (one left, or the enqueue excluded it).
func checkApprovedHosts(p Picture, hostIDs []uuid.UUID) error {
	approved := make(map[string]bool, len(p.Targets))
	for _, h := range p.Targets {
		approved[h.ID] = true
	}
	for _, id := range hostIDs {
		if !approved[id.String()] {
			return fmt.Errorf("%w (host %s was not part of the approved run)", ErrHostsChangedAfterApproval, id)
		}
	}
	return nil
}

// ConfirmWithdrawn is the claim guard's second look before a job refused
// with errRequestNotApproved is failed (jobs.ClaimGuard): it reports
// whether the run group's request is still not approved, reading it under
// a share lock that the transaction ctx carries — the one that writes the
// job's failure — holds to its commit. A re-approval's Decide waits for
// that commit, so its replay of the run group sees the job failed and
// enqueues its zone again; or the read waited for the re-approval and sees
// it approved, and the job runs (backlog 2.127). A run group no request
// owns is not approved.
func (s *Service) ConfirmWithdrawn(ctx context.Context, orgID, runGroupID uuid.UUID) (bool, error) {
	st, err := s.store.LockStatusByRunGroup(ctx, orgID, runGroupID)
	if errors.Is(err, ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("confirm the run's request is not approved: %w", err)
	}
	return st != StatusApproved, nil
}

// idempotencyKey is the key an approval enqueues with, a run or a retry
// alike, so approving a request again after a failed enqueue replays what
// the first attempt created instead of enqueueing it twice. A job of that
// attempt the claim guard refused in between (the request was pending
// again) ran nothing, and the replay enqueues its zone anew.
func idempotencyKey(id uuid.UUID) string { return "run-request:" + id.String() }

// commitOrNone names a pin in a refusal.
func commitOrNone(c string) string {
	if c == "" {
		return "no commit"
	}
	return "commit " + c
}
