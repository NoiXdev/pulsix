// Agent enrollment inside a run (spec 2026-11-25 assistant API gaps, B):
// the claim of a run whose person confirmed the hosts it enrols
// (Job.EnrollHostIDs) mints one host-bound single-use enrollment token per
// listed host of the job that still runs no agent, and hands each only to
// its own host, as inventory host variables the customer's install role
// reads (pulsix_agent_enroll_token, and an empty pulsix_agent_enroll_tokens
// so no per-host dictionary from the inventory can win). The token is a
// secret: it is named in the claim's redact list (render.Inventory.Secrets)
// and never reaches the job record, an event, the activity log or a
// response. A requeue, a re-claim and every terminal path revoke the job's
// unused tokens in their own transaction.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/inventory/render"
)

// enrollTokenLifetime is how long a minted token stays redeemable: a run
// that has not enrolled its host by then has long ended, and its end has
// revoked it anyway.
const enrollTokenLifetime = 24 * time.Hour

// The host variables the claim sets for a host it hands a token
// (the install role's contract; docs: "What Pulsix sets").
const (
	enrollTokenVar  = "pulsix_agent_enroll_token"
	enrollTokensVar = "pulsix_agent_enroll_tokens"
)

// EnrollTarget is one host a run may enrol: its id and inventory name.
type EnrollTarget struct {
	HostID uuid.UUID
	Name   string
}

// EnrollToken is one minted token: the host it is bound to and the printed
// agent token. A secret; never log it.
type EnrollToken struct {
	HostID uuid.UUID
	Name   string
	Token  string
}

// EnrollmentTokens is the slice of the agent registry the claim and the
// job's ends need (agents.Service, adapted in internal/app). Both run on
// the caller's transaction (the ctx a db.WithWriteTx or WithInstanceTx
// bound).
type EnrollmentTokens interface {
	// MintJobTokens mints, in target order, a host-bound single-use token
	// in zoneID linked to jobID for every target that runs no agent.
	MintJobTokens(ctx context.Context, orgID, zoneID, jobID uuid.UUID, targets []EnrollTarget, expiresAt time.Time) ([]EnrollToken, error)
	// RevokeJobTokens deletes jobID's unused tokens and says how many.
	RevokeJobTokens(ctx context.Context, orgID, jobID uuid.UUID) (int, error)
}

// ActivityRecorder writes an activity log entry (audit.Recorder).
type ActivityRecorder interface {
	Record(ctx context.Context, e audit.Event) error
}

// SetEnrollment wires the agent registry the enrolling claim mints through
// and the activity log it reports to (post-construction, like the other
// sinks: the agents service is built after the queue). Unwired, a claim
// that would mint fails its job; revocation has nothing to revoke.
func (q *SQLQueue) SetEnrollment(tokens EnrollmentTokens, activity ActivityRecorder) {
	q.enroll, q.activity = tokens, activity
}

// errClaimMoved: the job left the claim that was minting for it (a
// requeue, a cancel's end) before the tokens were minted.
var errClaimMoved = errors.New("the job's claim moved on")

// enrols reports whether the claim of job (a run of tmpl) hands out
// enrollment tokens: only a run whose person confirmed a list, of a
// template that still enrols agents, started by a person (a webhook,
// schedule or chained run never carries a list), and no dry run.
func enrols(job Job, tmpl TemplateRow) bool {
	return job.Kind == KindRun && job.EnrollHostIDs != nil && tmpl.AgentEnrollment && !job.CheckMode && job.Trigger == TriggerManual
}

// enrollTargets is the hosts of the job's confirmed list that the claim
// rendered (the job's own hosts, minus any the claim dropped), sorted by
// name.
func enrollTargets(list []uuid.UUID, conns map[string]render.Connection) []EnrollTarget {
	var out []EnrollTarget
	for name, c := range conns {
		if slices.Contains(list, c.HostID) {
			out = append(out, EnrollTarget{HostID: c.HostID, Name: name})
		}
	}
	slices.SortFunc(out, func(a, b EnrollTarget) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// handOverEnrollTokens mints the claim's tokens in one transaction —
// revoking what an earlier claim of the job left, minting, counting and
// reporting — and writes each into its own host's variables in inv and
// into inv.Secrets. The transaction first finds the job still on this
// claim (LockClaimedJob); a requeue or an end that got there first leaves
// nothing minted. Any failure rolls the whole hand-out back: nothing
// reaches the inventory.
func (q *SQLQueue) handOverEnrollTokens(ctx context.Context, job Job, inv *render.Inventory) error {
	if q.enroll == nil {
		return errors.New("mint enrollment tokens: no agent registry wired")
	}
	targets := enrollTargets(job.EnrollHostIDs, inv.Connections)
	var minted []EnrollToken
	err := q.db.WithWriteTx(ctx, job.OrgID, func(ctx context.Context) error {
		n, err := gen.New(q.db).LockClaimedJob(ctx, gen.LockClaimedJobParams{ID: job.ID, Attempts: int32(job.Attempts)}) //nolint:gosec // a small claim count
		if err != nil {
			return fmt.Errorf("lock the claimed job: %w", err)
		}
		if n == 0 {
			return errClaimMoved
		}
		if err := q.revokeEnrollTokens(ctx, job.OrgID, job.ID); err != nil {
			return err
		}
		minted, err = q.enroll.MintJobTokens(ctx, job.OrgID, job.ZoneID, job.ID, targets, q.now().Add(enrollTokenLifetime))
		if err != nil {
			return err
		}
		if len(minted) == 0 {
			return nil
		}
		if err := gen.New(q.db).AddEnrollTokensMinted(ctx, gen.AddEnrollTokensMintedParams{ID: job.ID, Minted: int32(len(minted))}); err != nil { //nolint:gosec // one per host
			return fmt.Errorf("count minted tokens: %w", err)
		}
		names := make([]string, len(minted))
		for i, m := range minted {
			names[i] = m.Name
		}
		return q.recordActivity(ctx, job.OrgID, job.ID, "job.enrollment_tokens_minted",
			fmt.Sprintf("enrollment tokens minted for %d hosts by job %s: %s", len(minted), job.ID, strings.Join(names, ", ")),
			map[string]any{"hosts": names, "count": len(minted)})
	})
	if err != nil {
		return fmt.Errorf("mint enrollment tokens: %w", err)
	}
	for _, m := range minted {
		if err := inv.SetHostVar(m.Name, enrollTokenVar, m.Token); err != nil {
			return fmt.Errorf("hand over the enrollment token of host %s: %w", m.Name, err)
		}
		if err := inv.SetHostVar(m.Name, enrollTokensVar, map[string]any{}); err != nil {
			return fmt.Errorf("hand over the enrollment token of host %s: %w", m.Name, err)
		}
		inv.Secrets = append(inv.Secrets, m.Token)
	}
	return nil
}

// revokeEnrollTokens revokes jobID's unused tokens on ctx's transaction
// and reports a revocation to the activity log. Without a registry wired
// nothing was minted, so there is nothing to revoke.
func (q *SQLQueue) revokeEnrollTokens(ctx context.Context, orgID, jobID uuid.UUID) error {
	if q.enroll == nil {
		return nil
	}
	n, err := q.enroll.RevokeJobTokens(ctx, orgID, jobID)
	if err != nil {
		return fmt.Errorf("revoke enrollment tokens: %w", err)
	}
	if n == 0 {
		return nil
	}
	return q.recordActivity(ctx, orgID, jobID, "job.enrollment_tokens_revoked",
		fmt.Sprintf("revoked %d unused enrollment tokens of job %s", n, jobID), map[string]any{"count": n})
}

// recordActivity writes one system entry about the job, on ctx's
// transaction. Counts and host names only: never a token.
func (q *SQLQueue) recordActivity(ctx context.Context, orgID, jobID uuid.UUID, action, summary string, meta map[string]any) error {
	if q.activity == nil {
		return nil
	}
	org := orgID
	if err := q.activity.Record(ctx, audit.Event{
		OrgID: &org, ActorKind: audit.ActorKindSystem, Action: action, ObjectType: "job", ObjectID: jobID.String(),
		Summary: summary, Meta: meta,
	}); err != nil {
		return fmt.Errorf("record activity: %w", err)
	}
	return nil
}
