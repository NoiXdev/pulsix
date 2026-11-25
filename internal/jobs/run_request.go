// Triggered runs: the entry point schedules and webhook triggers enqueue
// through (spec 2026-09-18 §2 step 4, §3). Same validation, zone split and
// host-key checks as a manual run — enqueueRunScope is shared — plus the
// trigger kind and reference and the sealed per-run overrides.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Trigger kinds beyond manual and system.
const (
	TriggerSchedule = "schedule"
	TriggerWebhook  = "webhook"
)

// RunOverrides is the plaintext override a run carries — sealed with
// JobAAD in the job's payload, merged over the template at claim time.
type RunOverrides struct {
	Limit     *string        `json:"limit,omitempty"`
	ExtraVars map[string]any `json:"extra_vars,omitempty"`
	// Untrusted marks values an unauthenticated sender supplied (a webhook
	// payload) or an assistant asked for (a run request's extra vars, spec
	// 2026-11-25 A): the claim marks every string in them unsafe for Ansible and
	// re-checks the limit (ansiblevars.Inert, CheckUntrustedLimit). It is
	// sealed together with the values, so it cannot be dropped on its own.
	Untrusted bool `json:"untrusted,omitempty"`
	// Check runs the play in Ansible's check mode (a playbook dry run, spec
	// 2026-11-22 §1); Diff, Tags and Playbook belong to such a run. Diff
	// asks for --diff, Tags for --tags. Playbook is an editor playbook's
	// unsaved text, run instead of the stored one, and is refused without
	// Check: a draft never runs for real (checkRunRequest).
	Check    bool     `json:"check,omitempty"`
	Diff     bool     `json:"diff,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Playbook *string  `json:"playbook,omitempty"`
}

func (o *RunOverrides) empty() bool {
	return o == nil || (o.Limit == nil && len(o.ExtraVars) == 0 && !o.Check && !o.Diff && len(o.Tags) == 0 && o.Playbook == nil)
}

// check reports whether the overrides ask for a check-mode run.
func (o *RunOverrides) check() bool { return o != nil && o.Check }

// ErrAllHostsExcluded means every host of the run's scope was excluded
// by the request (a schedule's maintenance guard, spec §6): nothing is
// left to run. Schedules record it as skipped_maintenance.
var ErrAllHostsExcluded = errors.New("every targeted host is excluded from this run")

// RunRequest says who or what starts a run and with which overrides.
type RunRequest struct {
	Trigger    string
	TriggerRef *uuid.UUID
	Actor      *uuid.UUID
	Overrides  *RunOverrides
	// ExcludeHosts drops these hosts from the resolved scope (a schedule's
	// exclude_hosts maintenance guard). When none is left the enqueue
	// fails with ErrAllHostsExcluded.
	ExcludeHosts []uuid.UUID
	// OnlyHost narrows the run to this host (backlog 7.76): it must be
	// managed (else ErrNotFound) and among the template's resolved targets
	// under the effective limit (else ErrHostNotInScope). The run gets one
	// job targeting only this host, and its sealed limit intersects the
	// effective limit with the host name (narrowLimit).
	OnlyHost *uuid.UUID
	// OnlyHosts keeps just these hosts of the resolved targets (a chained
	// run plays only on the hosts its parent played on). Whatever the scope
	// does not select is dropped; when nothing is left the enqueue fails
	// with ErrHostNotInScope. Empty means unrestricted: a caller that means
	// "no hosts" must not enqueue at all (the follow-up starter skips).
	OnlyHosts []uuid.UUID
	// FollowUps overrides the template's follow-ups for this run (manual
	// runs only); nil keeps the template's own list, an empty list none.
	FollowUps *[]uuid.UUID
	// Chain is where a chained run sits in its chain (chain runs only).
	Chain *ChainContext
	// IdempotencyKey, when not empty, makes this enqueue a no-op replay of
	// whatever run group an earlier call with the same (org, key) already
	// created (migration 00067, backlog 2.60): the schedule ticker sets one
	// naming the schedule and the due instant it fires for, so a decision
	// retried after a write-back failure — the enqueue already committed,
	// only schedule_runs and next_run_at never advanced — finds that run
	// already claimed instead of enqueueing it a second time. Empty for
	// every other trigger: a manual run, a webhook delivery and a retry are
	// each their own occurrence, with no natural key to dedupe repeats
	// against.
	//
	// The key dedupes RETRIES, not CONCURRENT callers. The claim and the
	// job creation it guards are separate statements: a second caller that
	// loses the claim lists the winner's jobs to replay them, and while the
	// winner is still mid-insert that list can come back short, so the
	// loser creates the zones it believes are missing. The schedule ticker
	// is safe from that because it never has a concurrent twin — several
	// managers tick at once, but LockDueSchedules' FOR NO KEY UPDATE SKIP
	// LOCKED (schedules.sql) hands each schedule row to exactly one of
	// them, so the same occurrence is only ever decided one at a time, and
	// the losing retry always runs after the winner has committed or died.
	// A future caller without that kind of serialization — a webhook
	// delivered twice in the same instant — needs its own, or needs the
	// claim and the inserts moved into one transaction.
	IdempotencyKey string
	// RunGroupID, when not zero, is the run group the jobs join instead of
	// a new one (and the candidate an IdempotencyKey reserves). An approved
	// run request passes its own id, so a claim of any of its jobs finds
	// the request (spec 2026-11-16 assistant access).
	RunGroupID uuid.UUID
	// PlaybookID, when set, is the playbook the template must run, else the
	// enqueue fails with ErrInvalidDryRun: a dry run started from that
	// playbook's page (spec 2026-11-22 §1).
	PlaybookID *uuid.UUID
	// PinnedCommit, when set, is the commit the run's jobs run instead of
	// the repository's branch tip (spec 2026-11-25 A, decision D3): an
	// approved run request passes the commit its approver saw; a chained
	// run inherits its parent's (chainPin). Only a manual or chained run of
	// a repository playbook takes one (checkPin).
	PinnedCommit string
	// pinnedRepo is the repository PinnedCommit belongs to, set by the
	// enqueue from the template it resolved (Job.PinnedRepoID).
	pinnedRepo *uuid.UUID
	// EnrollHostIDs, when not nil, is the list of hosts a signed-in person
	// confirmed the run enrols agents on (spec 2026-11-25 B): the approval
	// of a run request, or the person who started a manual run or retry.
	// It is stored on every job of the run (Job.EnrollHostIDs); an empty
	// list is a confirmed list with no host on it. Only a manual run takes
	// one (checkEnrollList): schedules, webhooks and chained follow-ups
	// never carry one, so they mint nothing.
	EnrollHostIDs []uuid.UUID
}

// ApprovedRun is what an approval (or, for EnrollHostIDs, the person who
// started a retry) stored on a run's jobs; the claim guard compares it
// with what an approval stored on its request: the commit (spec
// 2026-11-25 A) and the hosts it enrols agents on (B).
type ApprovedRun struct {
	PinnedCommit  string
	EnrollHostIDs []uuid.UUID
}

// checkEnrollList refuses an enroll list on a run no approval started.
func checkEnrollList(req RunRequest) error {
	if req.EnrollHostIDs != nil && req.Trigger != TriggerManual {
		return fmt.Errorf("%w: only a run a person started or approved enrols agents on a confirmed list of hosts, not a %s run", ErrInvalidInput, req.Trigger)
	}
	return nil
}

// validate checks the trigger kind and that TriggerRef fits it: schedule
// and webhook runs always name the schedule or trigger that started them,
// manual and system runs never carry one (a mismatch here is a wiring
// slip in a caller — Tasks 6/9/10 — not something an operator can cause).
func (r RunRequest) validate() error {
	switch r.Trigger {
	case TriggerManual, TriggerSystem:
		if r.TriggerRef != nil {
			return fmt.Errorf("%w: trigger %q carries no trigger reference", ErrInvalidInput, r.Trigger)
		}
	case TriggerSchedule, TriggerWebhook:
		if r.TriggerRef == nil {
			return fmt.Errorf("%w: trigger %q requires a trigger reference", ErrInvalidInput, r.Trigger)
		}
	case TriggerChain:
		if r.TriggerRef == nil || r.Chain == nil {
			return fmt.Errorf("%w: trigger %q requires a trigger reference and a chain context", ErrInvalidInput, r.Trigger)
		}
	default:
		return fmt.Errorf("%w: unknown trigger %q", ErrInvalidInput, r.Trigger)
	}
	if r.FollowUps != nil && r.Trigger != TriggerManual {
		return fmt.Errorf("%w: only a manual run takes follow-ups", ErrInvalidInput)
	}
	if r.Chain != nil && r.Trigger != TriggerChain {
		return fmt.Errorf("%w: only a chain run carries a chain context", ErrInvalidInput)
	}
	return nil
}

// replayListLimit bounds the jobs fetched back for an idempotent replay
// (one per zone): an organization realistically has far fewer zones than
// this, and ListJobs always returns a page, never "all of them".
const replayListLimit = 200

// EnqueueRunWith is EnqueueRun with an explicit trigger and overrides.
func (s *Service) EnqueueRunWith(ctx context.Context, orgID, templateID uuid.UUID, req RunRequest) ([]Job, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	if err := checkRunRequest(req); err != nil {
		return nil, err
	}
	if err := checkEnrollList(req); err != nil {
		return nil, err
	}
	group := s.newID()
	if req.RunGroupID != uuid.Nil {
		group = req.RunGroupID
	}
	group, alreadyDone, err := s.reserveRunGroup(ctx, orgID, req.IdempotencyKey, group)
	if err != nil {
		return nil, err
	}
	tmpl, err := s.templates.TemplateLookup(ctx, orgID, templateID)
	if err != nil {
		return nil, err
	}
	if err := checkRunTemplate(tmpl, req); err != nil {
		return nil, err
	}
	if req.PinnedCommit, err = s.chainPin(ctx, orgID, tmpl, req); err != nil {
		return nil, err
	}
	req.pinnedRepo = tmpl.PlaybookRepo
	if err := checkPin(tmpl, req); err != nil {
		return nil, err
	}
	scope, err := parseScope(tmpl.Scope)
	if err != nil {
		return nil, err
	}
	gate := effectiveLimit(tmpl.Limit, req.Overrides)
	if req.OnlyHost != nil {
		if req, err = s.narrowToHost(ctx, orgID, gate, req); err != nil {
			return nil, err
		}
	}
	if !req.Overrides.check() {
		// A dry run starts nothing after it, whatever the template's own
		// follow-ups say (spec 2026-11-22 §1).
		if err := s.planFollowUps(ctx, orgID, group, tmpl, req); err != nil {
			return nil, err
		}
	}
	return s.enqueueRunScope(ctx, orgID, tmpl.ID, scope, gate, req, group, alreadyDone)
}

// reserveRunGroup claims key for group (RunRequest.IdempotencyKey): it
// returns the run group the jobs join and, when an earlier call already
// claimed the key, that call's jobs. An empty key reserves nothing.
func (s *Service) reserveRunGroup(ctx context.Context, orgID uuid.UUID, key string, group uuid.UUID) (uuid.UUID, []Job, error) {
	if key == "" {
		return group, nil, nil
	}
	claimed, won, err := s.store.ReserveRunGroup(ctx, orgID, key, group)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("reserve run group: %w", err)
	}
	if won {
		return claimed, nil, nil
	}
	// An earlier call already claimed this occurrence. enqueueRunScope
	// still resolves the scope and still runs every guard, but
	// skips creating a job for any zone the returned jobs already cover —
	// which is every zone, for the ordinary retry this key exists to
	// catch (an earlier call that fully enqueued, then the write-back
	// that should have recorded it failed): nothing new is created,
	// and these are returned as-is.
	//
	// The empty-zone-list case (the earlier call claimed the key but
	// never created a single job — it failed before reaching
	// enqueueRunScope at all: ErrEmptyScope, a dead template lookup, a
	// transient store error) and the PARTIAL case (it created jobs for
	// some zones but was interrupted — e.g. a manager shutdown —
	// before reaching the rest) both fall out of the same mechanism:
	// enqueueRunScope creates jobs only for the zones the returned jobs
	// do not already cover, which is every zone in the first case and the
	// missing ones in the second. Short-circuiting here on "any
	// existing job at all" would silently leave the second case's
	// un-enqueued zones un-enqueued forever.
	//
	// A job refused at its first claim because its approval was withdrawn
	// (withdrawnAtClaim, claim_withdrawn.go) is left out: it ran nothing on
	// any host, so its zone gets a new job. That is a request approved
	// again after its first enqueue failed partway: the job the first
	// attempt did create was claimed while the request was pending again
	// and refused. Every other job still covers its zone — queued,
	// running, finished, failed after it started, or refused for another
	// reason (an errored run the group's outcome counts) — so nothing runs
	// twice and nothing the outcome counts is replaced.
	existing, _, err := s.store.ListJobs(ctx, orgID, Filter{RunGroupID: &claimed}, "", replayListLimit)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("list reserved run group: %w", err)
	}
	covering := existing[:0:0]
	for _, j := range existing {
		if !withdrawnAtClaim(j) {
			covering = append(covering, j)
		}
	}
	return claimed, covering, nil
}

// EnqueueSyncWith is EnqueueSync with an explicit trigger (a repo_sync
// webhook). A sync takes no overrides.
func (s *Service) EnqueueSyncWith(ctx context.Context, orgID, repoID uuid.UUID, req RunRequest) (Job, error) {
	if err := req.validate(); err != nil {
		return Job{}, err
	}
	if !req.Overrides.empty() {
		return Job{}, fmt.Errorf("%w: a repository sync takes no overrides", ErrInvalidInput)
	}
	return s.enqueueSync(ctx, orgID, repoID, req)
}

// HasActiveRun reports whether a job of the template is not terminal yet.
func (s *Service) HasActiveRun(ctx context.Context, orgID, templateID uuid.UUID) (bool, error) {
	return s.store.HasActiveTemplateJob(ctx, orgID, templateID)
}

// StartedRunGroup returns the run group an earlier enqueue with this
// idempotency key started (at least one run job exists), or nil. The
// follow-up starter asks it to recognise a start whose bookkeeping was lost.
func (s *Service) StartedRunGroup(ctx context.Context, orgID uuid.UUID, idempotencyKey string) (*uuid.UUID, error) {
	return s.store.StartedRunGroup(ctx, orgID, idempotencyKey)
}

// HasActiveSync reports whether a sync of the repository is not terminal.
func (s *Service) HasActiveSync(ctx context.Context, orgID, repoID uuid.UUID) (bool, error) {
	return s.store.HasActiveRepoSync(ctx, orgID, repoID)
}

// runPayload seals the overrides for one job; nil when there are none.
func (s *Service) runPayload(j Job, ov *RunOverrides) (json.RawMessage, error) {
	if ov.empty() {
		return nil, nil
	}
	raw, err := json.Marshal(ov)
	if err != nil {
		return nil, fmt.Errorf("encode run overrides: %w", err)
	}
	return s.sealPayload(j, raw)
}
