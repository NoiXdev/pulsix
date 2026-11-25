// Package runrequests implements run requests (spec docs/superpowers/specs/
// 2026-11-16-assistant-access-design.md, "Run requests"): an assistant
// asks for a run of a job template, or for a retry of a job's failed hosts,
// and only a person's approval enqueues it. An approval carries the
// fingerprint of the run picture the person looked at; the service
// recomputes the picture and refuses a mismatch, so nothing that changed in
// between is ever approved unseen.
package runrequests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

// TTL is how long a request stays approvable unless it asks for another
// expiry (CreateInput.ExpiresInMinutes).
const TTL = time.Hour

// MaxReasonLength caps the assistant's one-line reason.
const MaxReasonLength = 300

// MaxPendingPerToken caps the pending, unexpired requests of one token
// (backlog 2.120), so an assistant cannot bury the approvers' queue.
const MaxPendingPerToken = 20

// Kind is what a request asks for: a run, a retry, or (spec 2026-11-25
// assistant API gaps, C) a schedule or a webhook trigger. Every kind shares the lifecycle, the
// expiry, the per-token cap and the approvals page; the picture and what
// an approval does are the kind's. The table also admits the later kinds
// schedule_review and trigger_review (migration 00129).
type Kind string

// The request kinds.
const (
	KindRun         Kind = "run"
	KindRetryFailed Kind = "retry_failed"
	// KindSchedule creates, changes or resumes a schedule (ScheduleChange).
	KindSchedule Kind = "schedule"
	// KindTrigger creates, changes or rotates the secret of a webhook
	// trigger (TriggerChange). A repository sync trigger's request has no
	// template (TemplateID is uuid.Nil).
	KindTrigger Kind = "trigger"
)

// Status is a request's state. StatusExpired is never stored: a pending
// request past its expiry reads as expired. A request made with sync_first
// waits for its repository's sync (StatusWaitingForSync), then becomes
// pending, or failed when the sync does not let it (spec 2026-11-25 A).
type Status string

// The statuses.
const (
	StatusPending        Status = "pending"
	StatusApproved       Status = "approved"
	StatusRejected       Status = "rejected"
	StatusExpired        Status = "expired"
	StatusWaitingForSync Status = "waiting_for_sync"
	StatusFailed         Status = "failed"
)

// Sentinel errors the transport maps.
var (
	ErrNotFound     = errors.New("run request not found")
	ErrExpired      = errors.New("the request expired")
	ErrDecided      = errors.New("the request was already decided")
	ErrChanged      = errors.New("changed since you looked at it; review it again")
	ErrInvalidInput = errors.New("invalid run request")
	// ErrWaitingForSync refuses the approval of a request still waiting
	// for its sync; ErrSyncFailed that of one whose sync failed.
	ErrWaitingForSync = errors.New("the request waits for its repository's sync; approve it once the sync has finished")
	ErrSyncFailed     = errors.New("the request failed: the repository's sync it waited for did not succeed; ask for the run again")
	// ErrTooManyPending refuses a request of a token that already has
	// MaxPendingPerToken pending ones.
	ErrTooManyPending = fmt.Errorf("this token already has %d pending run requests; wait until a person decides on them, or they expire, before asking again", MaxPendingPerToken)
)

// Request is one stored run request.
type Request struct {
	ID               uuid.UUID
	OrgID            uuid.UUID
	Kind             Kind
	TemplateID       uuid.UUID
	TemplateRevision int64
	HostID           *uuid.UUID
	JobID            *uuid.UUID
	Reason           string
	RequestedByUser  uuid.UUID
	RequestedByToken *uuid.UUID
	Status           Status
	DecidedBy        *uuid.UUID
	DecidedAt        *time.Time
	RunGroupID       *uuid.UUID
	// ApprovedFingerprint is the fingerprint the approving person saw;
	// empty unless the request is approved. The claim of one of its jobs
	// recomputes the fingerprint and compares (CheckRunClaim).
	ApprovedFingerprint string
	// Overrides is what a run request changes about the run; nil for none
	// and always nil for a retry.
	Overrides *Overrides
	// PinnedCommit is the commit the approval pinned the run to (spec
	// 2026-11-25 A): stored with the approval, "" for a run of no
	// repository, a request not approved, and one approved before pins.
	PinnedCommit string
	// EnrollHostIDs are the hosts the approval enrols agents on (spec
	// 2026-11-25 B): stored with the approval from the picture's
	// AgentEnrollment; nil for a template without agent enrollment, a
	// request not approved and one approved before the column, an empty
	// list for an approved one that enrols no host (a dry run, every
	// target with an agent).
	EnrollHostIDs []uuid.UUID
	// ExpiresAt is when a pending request expires; zero while the request
	// waits for its sync, whose success starts the expiry.
	ExpiresAt time.Time
	// ExpiresInMinutes is the expiry the request asked for (0: TTL), which
	// a waiting request starts once it becomes pending.
	ExpiresInMinutes int
	// SyncJobID is the sync a request made with sync_first waits or waited
	// for; nil for any other request.
	SyncJobID *uuid.UUID
	// SyncError is why a failed request's sync did not let it become
	// pending; empty for any other request.
	SyncError string
	// SyncStarted is the sync Create started or attached to for a
	// sync_first request, for the caller's audit; set by Create only and
	// never stored.
	SyncStarted *SyncStart
	CreatedAt   time.Time
	// Schedule is what a KindSchedule request does (the payload column);
	// nil for every other kind.
	Schedule *ScheduleChange
	// Trigger is what a KindTrigger request does (the payload column); nil
	// for every other kind. It never holds a secret.
	Trigger *TriggerChange
	// TargetID is the object a request of a kind other than run and retry
	// acts on: for a schedule or trigger request the schedule or trigger it
	// creates (its id taken when the request is made), changes, resumes or
	// rotates.
	TargetID *uuid.UUID
	// TokenName and TokenState describe RequestedByToken as it is now,
	// filled on every read (see TokenState); both empty without a token.
	TokenName  string
	TokenState TokenState
}

// CreateInput is what Create takes.
type CreateInput struct {
	Kind             Kind
	TemplateID       uuid.UUID  // KindRun
	HostID           *uuid.UUID // KindRun, optional
	JobID            *uuid.UUID // KindRetryFailed
	Reason           string
	RequestedByUser  uuid.UUID
	RequestedByToken *uuid.UUID
	// Overrides are KindRun only; a retry with any is refused.
	Overrides OverridesInput
	// ExpiresInMinutes is the request's time to live; zero is TTL.
	ExpiresInMinutes int
	// SyncFirst syncs the template's repository first (KindRun only): the
	// request waits for the sync and becomes pending once it succeeded.
	SyncFirst bool
	// Schedule is what a KindSchedule request asks for.
	Schedule *ScheduleInput
	// Trigger is what a KindTrigger request asks for.
	Trigger *TriggerInput
}

// Store persists requests.
type Store interface {
	Insert(ctx context.Context, r Request) error
	// InsertCapped inserts a token's request unless the token already has
	// limit pending requests at now (ErrTooManyPending); the count and the
	// insert are atomic against concurrent calls for the same token.
	InsertCapped(ctx context.Context, r Request, limit int, now time.Time) error
	Get(ctx context.Context, orgID, id uuid.UUID) (Request, error)
	// List returns orgID's requests, newest first, at most a capped page;
	// status, when given, keeps those whose effective status at now
	// matches, before the cap.
	List(ctx context.Context, orgID uuid.UUID, status *Status, now time.Time) ([]Request, error)
	// CountPending counts orgID's requests pending and unexpired at now;
	// token, when given, only that token's, and then also those waiting
	// for their sync, which count against the token's cap.
	CountPending(ctx context.Context, orgID uuid.UUID, token *uuid.UUID, now time.Time) (int, error)
	// Tokens reads the tokens of ids that still exist (TokenRecord).
	Tokens(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]TokenRecord, error)
	// Decide moves a pending request to to, and a rejection also one
	// waiting for its sync (its expiry becomes at); false when it was
	// neither. An approval records fingerprint and the commit it pins (""
	// for none) and sets the run group to the request's own id, in the same
	// write.
	Decide(ctx context.Context, orgID, id uuid.UUID, to Status, by uuid.UUID, at time.Time, fingerprint string, approved jobs.ApprovedRun) (bool, error)
	// InTx runs fn in one transaction: every store call made with the
	// context fn receives (this store's and any other store built on the
	// same database) commits with the others or not at all.
	InTx(ctx context.Context, orgID uuid.UUID, fn func(ctx context.Context) error) error
	// RevertToPending undoes an approval whose enqueue failed; the run
	// group stays, the fingerprint and the pinned commit go.
	RevertToPending(ctx context.Context, orgID, id uuid.UUID) error
	// ByRunGroup finds the request whose approval created group.
	ByRunGroup(ctx context.Context, orgID, group uuid.UUID) (Request, error)
	// ListWaitingFor lists the requests waiting for the sync syncJob, with
	// the sync job's state.
	ListWaitingFor(ctx context.Context, syncJob uuid.UUID) ([]Waiting, error)
	// ListDueWaiting lists the waiting requests the sweep has to look at:
	// those whose sync ended or is gone, and those waiting since before
	// staleBefore. An unscoped context sees every organization's.
	ListDueWaiting(ctx context.Context, staleBefore time.Time) ([]Waiting, error)
	// EndWait moves a waiting request to to (pending or failed) with
	// expiresAt and, for a failure, syncError; false when it no longer
	// waits.
	EndWait(ctx context.Context, orgID, id uuid.UUID, to Status, expiresAt time.Time, syncError string) (bool, error)
}

// Waiting is a request waiting for its sync, with what the queue says
// about that sync.
type Waiting struct {
	OrgID            uuid.UUID
	ID               uuid.UUID
	CreatedAt        time.Time
	ExpiresInMinutes int
	SyncJobID        uuid.UUID
	// SyncStatus is the sync job's status; empty when the job is gone.
	SyncStatus jobs.Status
	SyncError  string
}

// Service is the run request service.
type Service struct {
	store     Store
	pictures  Pictures
	enqueuer  Enqueuer
	validator Validator
	syncer    Syncer
	auto      Automation
	now       func() time.Time
	newID     func() uuid.UUID
}

// NewService wires the service.
func NewService(store Store, pictures Pictures, enqueuer Enqueuer, validator Validator, syncer Syncer, auto Automation, now func() time.Time, newID func() uuid.UUID) *Service {
	return &Service{store: store, pictures: pictures, enqueuer: enqueuer, validator: validator, syncer: syncer, auto: auto, now: now, newID: newID}
}

// Create records a pending request. A token's request is refused with
// ErrTooManyPending while the token has MaxPendingPerToken pending ones
// (waiting ones included). A request made with SyncFirst starts its sync
// (syncFirst) and is stored waiting for it, without an expiry; a template
// with nothing to sync makes it pending at once.
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, in CreateInput) (Request, error) {
	reason := strings.TrimSpace(in.Reason)
	if reason == "" || utf8.RuneCountInString(reason) > MaxReasonLength {
		return Request{}, fmt.Errorf("%w: a reason of 1 to %d characters is required", ErrInvalidInput, MaxReasonLength)
	}
	ttl, err := expiresIn(in.ExpiresInMinutes)
	if err != nil {
		return Request{}, err
	}
	ov, err := in.Overrides.overrides()
	if err != nil {
		return Request{}, err
	}
	r := Request{
		ID: s.newID(), OrgID: orgID, Kind: in.Kind, Reason: reason,
		RequestedByUser: in.RequestedByUser, RequestedByToken: in.RequestedByToken,
		Status: StatusPending, CreatedAt: s.now(), ExpiresInMinutes: in.ExpiresInMinutes,
	}
	r.ExpiresAt = r.CreatedAt.Add(ttl)
	if in.Kind != KindSchedule && in.Schedule != nil {
		return Request{}, fmt.Errorf("%w: only a schedule request takes a schedule", ErrInvalidInput)
	}
	if in.Kind != KindTrigger && in.Trigger != nil {
		return Request{}, fmt.Errorf("%w: only a trigger request takes a trigger", ErrInvalidInput)
	}
	switch in.Kind {
	case KindTrigger:
		if in.SyncFirst || ov != nil || in.HostID != nil || in.JobID != nil {
			return Request{}, fmt.Errorf("%w: a trigger request takes no run options (sync_first, check_mode, hosts, limit or extra vars of a run)", ErrInvalidInput)
		}
		if err := s.triggerRequest(ctx, orgID, in.Trigger, &r); err != nil {
			return Request{}, err
		}
	case KindSchedule:
		if in.SyncFirst || ov != nil || in.HostID != nil || in.JobID != nil {
			return Request{}, fmt.Errorf("%w: a schedule request takes no run options (sync_first, check_mode, hosts, limit or extra vars of a run)", ErrInvalidInput)
		}
		if err := s.scheduleRequest(ctx, orgID, in.Schedule, &r); err != nil {
			return Request{}, err
		}
	case KindRun:
		if in.HostID != nil && ov != nil && len(ov.HostIDs) > 0 {
			return Request{}, fmt.Errorf("%w: give host_id or host_ids, not both", ErrInvalidInput)
		}
		rev, err := s.validator.ValidateRun(ctx, orgID, in.TemplateID, in.HostID, ov)
		if err != nil {
			return Request{}, err
		}
		r.TemplateID, r.HostID, r.TemplateRevision, r.Overrides = in.TemplateID, in.HostID, rev, ov
	case KindRetryFailed:
		if in.SyncFirst {
			return Request{}, fmt.Errorf("%w: a retry takes no sync_first; it runs its original run's commit", ErrInvalidInput)
		}
		if ov != nil {
			return Request{}, fmt.Errorf("%w: a retry takes no overrides; it repeats its run on the failed hosts", ErrInvalidInput)
		}
		if in.JobID == nil {
			return Request{}, fmt.Errorf("%w: a retry names its job", ErrInvalidInput)
		}
		tpl, group, rev, err := s.validator.ValidateRetry(ctx, orgID, *in.JobID)
		if err != nil {
			return Request{}, err
		}
		if r.Overrides, err = s.repeatedOverrides(ctx, orgID, group); err != nil {
			return Request{}, err
		}
		// What it repeats is checked now as at the approval, so a retry
		// whose vars are no longer allowed is refused, not left to fail
		// every approval.
		if _, err := s.validator.RetryRunOverrides(ctx, orgID, *in.JobID, r.Overrides); err != nil {
			return Request{}, err
		}
		r.TemplateID, r.JobID, r.TemplateRevision = tpl, in.JobID, rev
	default:
		return Request{}, fmt.Errorf("%w: unknown kind %q", ErrInvalidInput, in.Kind)
	}
	var started *SyncStart
	if in.SyncFirst {
		if started, err = s.syncFirst(ctx, &r); err != nil {
			return Request{}, err
		}
	}
	// A token's request is capped; the store counts and inserts atomically.
	// A request without a token is not capped.
	if r.RequestedByToken == nil {
		err = s.store.Insert(ctx, r)
	} else {
		err = s.store.InsertCapped(ctx, r, MaxPendingPerToken, s.now())
	}
	if errors.Is(err, ErrTooManyPending) {
		return Request{}, err
	}
	if err != nil {
		return Request{}, fmt.Errorf("store run request: %w", err)
	}
	if r.Status == StatusWaitingForSync {
		s.endWaitAtCreate(ctx, &r)
	}
	r.SyncStarted = started
	s.withTokensAfterWrite(ctx, orgID, &r)
	return r, nil
}

// Get returns a request with its current run picture and the picture's
// fingerprint, which an approval sends back.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Request, Picture, string, error) {
	r, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return Request{}, Picture{}, "", err
	}
	r.Status = s.effective(r)
	if err := s.withTokens(ctx, orgID, []*Request{&r}); err != nil {
		return Request{}, Picture{}, "", err
	}
	p, err := s.pictures.Picture(ctx, orgID, r)
	if err != nil {
		return Request{}, Picture{}, "", fmt.Errorf("resolve run picture: %w", err)
	}
	return r, p, Fingerprint(r.ID, p), nil
}

// List returns orgID's requests, newest first; status, when given, keeps
// only those whose effective status matches. The store filters before its
// cap, so decided requests never hide a pending one.
func (s *Service) List(ctx context.Context, orgID uuid.UUID, status *Status) ([]Request, error) {
	rows, err := s.store.List(ctx, orgID, status, s.now())
	if err != nil {
		return nil, err
	}
	refs := make([]*Request, len(rows))
	for i := range rows {
		rows[i].Status = s.effective(rows[i])
		refs[i] = &rows[i]
	}
	if err := s.withTokens(ctx, orgID, refs); err != nil {
		return nil, err
	}
	return rows, nil
}

// Reject refuses a pending request, or one waiting for its sync.
func (s *Service) Reject(ctx context.Context, orgID, id, by uuid.UUID) (Request, error) {
	r, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return Request{}, err
	}
	switch s.effective(r) {
	case StatusExpired:
		return Request{}, ErrExpired
	case StatusPending, StatusWaitingForSync:
	default:
		return Request{}, ErrDecided
	}
	at := s.now()
	ok, err := s.store.Decide(ctx, orgID, id, StatusRejected, by, at, "", jobs.ApprovedRun{})
	if err != nil {
		return Request{}, fmt.Errorf("decide run request: %w", err)
	}
	if !ok {
		return Request{}, ErrDecided
	}
	r.Status, r.DecidedBy, r.DecidedAt = StatusRejected, &by, &at
	if r.ExpiresAt.IsZero() {
		r.ExpiresAt = at
	}
	s.withTokensAfterWrite(ctx, orgID, &r)
	return r, nil
}

// CreatedByToken reports whether the run group was created by approving a
// request that tokenID made, which lets that token cancel the run.
func (s *Service) CreatedByToken(ctx context.Context, orgID, runGroup, tokenID uuid.UUID) (bool, error) {
	r, err := s.store.ByRunGroup(ctx, orgID, runGroup)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return r.RequestedByToken != nil && *r.RequestedByToken == tokenID, nil
}

func (s *Service) effective(r Request) Status {
	if r.Status == StatusPending && !s.now().Before(r.ExpiresAt) {
		return StatusExpired
	}
	return r.Status
}
