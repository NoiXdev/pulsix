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
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// TTL is how long a request stays approvable.
const TTL = time.Hour

// MaxReasonLength caps the assistant's one-line reason.
const MaxReasonLength = 300

// Kind is what a request asks for.
type Kind string

// The request kinds.
const (
	KindRun         Kind = "run"
	KindRetryFailed Kind = "retry_failed"
)

// Status is a request's state. StatusExpired is never stored: a pending
// request past its expiry reads as expired.
type Status string

// The statuses.
const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
	StatusExpired  Status = "expired"
)

// Sentinel errors the transport maps.
var (
	ErrNotFound     = errors.New("run request not found")
	ErrExpired      = errors.New("the request expired")
	ErrDecided      = errors.New("the request was already decided")
	ErrChanged      = errors.New("changed since you looked at it; review it again")
	ErrInvalidInput = errors.New("invalid run request")
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
	ExpiresAt           time.Time
	CreatedAt           time.Time
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
}

// Store persists requests.
type Store interface {
	Insert(ctx context.Context, r Request) error
	Get(ctx context.Context, orgID, id uuid.UUID) (Request, error)
	// List returns every request of orgID, newest first.
	List(ctx context.Context, orgID uuid.UUID) ([]Request, error)
	// Decide moves a pending request to to; false when it was not pending.
	// An approval records fingerprint and sets the run group to the
	// request's own id, in the same write.
	Decide(ctx context.Context, orgID, id uuid.UUID, to Status, by uuid.UUID, at time.Time, fingerprint string) (bool, error)
	// RevertToPending undoes an approval whose enqueue failed; the run
	// group stays, the fingerprint goes.
	RevertToPending(ctx context.Context, orgID, id uuid.UUID) error
	// ByRunGroup finds the request whose approval created group.
	ByRunGroup(ctx context.Context, orgID, group uuid.UUID) (Request, error)
}

// Pictures resolves what a request would run now.
type Pictures interface {
	Picture(ctx context.Context, orgID uuid.UUID, r Request) (Picture, error)
}

// Enqueuer starts what an approval allows, through the existing run paths.
type Enqueuer interface {
	// Run and RetryFailed enqueue into the run group group.
	Run(ctx context.Context, orgID, templateID uuid.UUID, onlyHost *uuid.UUID, actor uuid.UUID, idempotencyKey string, group uuid.UUID, followUps []uuid.UUID) error
	RetryFailed(ctx context.Context, orgID, jobID, actor, group uuid.UUID, followUps []uuid.UUID) error
}

// Validator checks a request the way the run and retry routes would check
// the run itself, so a request that could never run is refused early.
type Validator interface {
	ValidateRun(ctx context.Context, orgID, templateID uuid.UUID, onlyHost *uuid.UUID) (revision int64, err error)
	ValidateRetry(ctx context.Context, orgID, jobID uuid.UUID) (templateID uuid.UUID, revision int64, err error)
}

// Service is the run request service.
type Service struct {
	store     Store
	pictures  Pictures
	enqueuer  Enqueuer
	validator Validator
	now       func() time.Time
	newID     func() uuid.UUID
}

// NewService wires the service.
func NewService(store Store, pictures Pictures, enqueuer Enqueuer, validator Validator, now func() time.Time, newID func() uuid.UUID) *Service {
	return &Service{store: store, pictures: pictures, enqueuer: enqueuer, validator: validator, now: now, newID: newID}
}

// Create records a pending request.
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, in CreateInput) (Request, error) {
	reason := strings.TrimSpace(in.Reason)
	if reason == "" || utf8.RuneCountInString(reason) > MaxReasonLength {
		return Request{}, fmt.Errorf("%w: a reason of 1 to %d characters is required", ErrInvalidInput, MaxReasonLength)
	}
	r := Request{
		ID: s.newID(), OrgID: orgID, Kind: in.Kind, Reason: reason,
		RequestedByUser: in.RequestedByUser, RequestedByToken: in.RequestedByToken,
		Status: StatusPending, CreatedAt: s.now(),
	}
	r.ExpiresAt = r.CreatedAt.Add(TTL)
	switch in.Kind {
	case KindRun:
		rev, err := s.validator.ValidateRun(ctx, orgID, in.TemplateID, in.HostID)
		if err != nil {
			return Request{}, err
		}
		r.TemplateID, r.HostID, r.TemplateRevision = in.TemplateID, in.HostID, rev
	case KindRetryFailed:
		if in.JobID == nil {
			return Request{}, fmt.Errorf("%w: a retry names its job", ErrInvalidInput)
		}
		tpl, rev, err := s.validator.ValidateRetry(ctx, orgID, *in.JobID)
		if err != nil {
			return Request{}, err
		}
		r.TemplateID, r.JobID, r.TemplateRevision = tpl, in.JobID, rev
	default:
		return Request{}, fmt.Errorf("%w: unknown kind %q", ErrInvalidInput, in.Kind)
	}
	if err := s.store.Insert(ctx, r); err != nil {
		return Request{}, fmt.Errorf("store run request: %w", err)
	}
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
	p, err := s.pictures.Picture(ctx, orgID, r)
	if err != nil {
		return Request{}, Picture{}, "", fmt.Errorf("resolve run picture: %w", err)
	}
	return r, p, Fingerprint(r.ID, p), nil
}

// List returns orgID's requests, newest first; status, when given, keeps
// only those whose effective status matches.
func (s *Service) List(ctx context.Context, orgID uuid.UUID, status *Status) ([]Request, error) {
	rows, err := s.store.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, r := range rows {
		r.Status = s.effective(r)
		if status == nil || r.Status == *status {
			out = append(out, r)
		}
	}
	return out, nil
}

// Approve enqueues what the request asks for, if fingerprint still matches
// the request's current run picture. The decision is recorded first, with
// the fingerprint and the run group (the request's id), so a concurrent
// second approval loses and every job's claim finds the request; the
// enqueue carries an idempotency key for runs; a failing enqueue puts the
// request back to pending. What runs stays bound to fingerprint until the
// claim: see CheckRunClaim.
func (s *Service) Approve(ctx context.Context, orgID, id, by uuid.UUID, fingerprint string) (Request, error) {
	r, _, current, err := s.Get(ctx, orgID, id)
	if err != nil {
		return Request{}, err
	}
	switch r.Status {
	case StatusExpired:
		return Request{}, ErrExpired
	case StatusPending:
	default:
		return Request{}, ErrDecided
	}
	if fingerprint != current {
		return Request{}, ErrChanged
	}
	at := s.now()
	ok, err := s.store.Decide(ctx, orgID, id, StatusApproved, by, at, fingerprint)
	if err != nil {
		return Request{}, fmt.Errorf("decide run request: %w", err)
	}
	if !ok {
		return Request{}, ErrDecided
	}
	group := r.ID
	if r.Kind == KindRetryFailed {
		err = s.enqueuer.RetryFailed(ctx, orgID, *r.JobID, by, group, []uuid.UUID{})
	} else {
		err = s.enqueuer.Run(ctx, orgID, r.TemplateID, r.HostID, by, "run-request:"+r.ID.String(), group, []uuid.UUID{})
	}
	if err != nil {
		if rerr := s.store.RevertToPending(ctx, orgID, id); rerr != nil {
			slog.ErrorContext(ctx, "run request: revert after a failed enqueue", "err", rerr, "request_id", id)
		}
		return Request{}, err
	}
	r.Status, r.DecidedBy, r.DecidedAt, r.RunGroupID, r.ApprovedFingerprint = StatusApproved, &by, &at, &group, fingerprint
	return r, nil
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

// Reject refuses a pending request.
func (s *Service) Reject(ctx context.Context, orgID, id, by uuid.UUID) (Request, error) {
	r, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return Request{}, err
	}
	switch s.effective(r) {
	case StatusExpired:
		return Request{}, ErrExpired
	case StatusPending:
	default:
		return Request{}, ErrDecided
	}
	at := s.now()
	ok, err := s.store.Decide(ctx, orgID, id, StatusRejected, by, at, "")
	if err != nil {
		return Request{}, fmt.Errorf("decide run request: %w", err)
	}
	if !ok {
		return Request{}, ErrDecided
	}
	r.Status, r.DecidedBy, r.DecidedAt = StatusRejected, &by, &at
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
