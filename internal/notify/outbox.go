package notify

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/jobs"
)

// Outbox turns facts into queued deliveries inside the transaction that
// records the fact (a transactional outbox): a finished run group inside
// the job's terminal transaction, a schedule decision inside the ticker's
// row savepoint, the weekly digest inside the digest ticker's. Each event
// has a key — run:<group>, decision:<schedule run>, digest:<org>:<date> —
// and (org, key, channel) is unique, so a retried source never enqueues
// twice. Every write runs in its own savepoint: a notification failure
// rolls back only the notification, never the fact.
//
// Two limits of that guarantee, neither of them stated plainly anywhere
// else (backlog 2.87):
//
// A run group completed by a CANCEL is the one source that is NOT inside
// its fact's transaction. CancelJob commits a queued job's
// queued -> cancelled transition as a standalone statement, and
// SQLQueue.recordCancelledRunGroup then opens its own write transaction to
// notice that the cancel completed the group (internal/jobs/run_group.go).
// A crash between the two loses that run.finished with nothing to retry
// it: the job is already terminal, so no later path revisits the group.
// Merging them is not a matter of wrapping the caller — db.beginOrg always
// begins a fresh pool transaction rather than joining one bound to the
// context, so a nested WithWriteTx would take a second connection and its
// claimRunGroupReport UPDATE could block on the row lock the outer
// transaction already holds on the same job.
//
// run.finished is AT-MOST-ONCE, not at-least-once, by an explicit ruling
// and not by accident: the report claim (claimRunGroupReport) commits with
// the job's terminal transition, while a sink failure rolls back only the
// sink's savepoint. So a group is reported to the sink at most once, and
// when that one call fails the notification is logged and dropped rather
// than retried. The alternative — failing the finish — wedges the job in
// running and, for a scheduled template, makes the overlap guard skip
// every later occurrence. Losing one notification is the cheaper loss.
type Outbox struct {
	db        *db.DB
	publicURL string
	now       func() time.Time
	rand      io.Reader
	log       *slog.Logger
}

// NewOutbox wires the outbox; nil now, rand and log take defaults. rand is
// the entropy for delivery ids, and it defaults rather than staying nil
// because a nil reader would panic inside the caller's own transaction —
// the one place a notification must never reach.
func NewOutbox(d *db.DB, publicURL string, now func() time.Time, rand io.Reader, log *slog.Logger) *Outbox {
	if now == nil {
		now = time.Now
	}
	if rand == nil {
		rand = cryptorand.Reader
	}
	if log == nil {
		log = slog.Default()
	}
	return &Outbox{db: d, publicURL: strings.TrimRight(publicURL, "/"), now: now, rand: rand, log: log}
}

// Decision is a schedule decision the ticker recorded.
type Decision struct {
	OrgID, ScheduleID, RunID uuid.UUID
	Outcome, Reason          string
	DueAt, DecidedAt         time.Time
}

// FollowUpDecision is a follow-up of a scheduled run that was decided
// without starting a run (spec: job chaining, "Notifications"). The
// schedule is the chain's root schedule; Category is skipped or failure.
type FollowUpDecision struct {
	OrgID, ScheduleID, RowID, TemplateID, ParentTemplateID uuid.UUID
	Category                                               Category
	Reason                                                 string
	DecidedAt                                              time.Time
}

// eventContext holds the names an event carries, read when it is enqueued.
// ParentTemplateName is empty for an event that is not a follow-up's.
type eventContext struct {
	OrgSlug, OrgName, ScheduleName string
	TemplateID                     uuid.UUID
	TemplateName                   string
	ParentTemplateName             string
}

// RunGroupFinished implements jobs.RunGroupSink: a scheduled run group
// that finished is reported to the channels its schedule's rules name for
// the run's categories. ctx carries the finishing transaction.
func (o *Outbox) RunGroupFinished(ctx context.Context, r jobs.RunGroupResult) error {
	if r.Trigger == jobs.TriggerChain {
		return o.chainedRunFinished(ctx, r)
	}
	if r.Trigger != jobs.TriggerSchedule || r.TriggerRef == nil {
		return nil
	}
	sp, err := o.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin notification savepoint: %w", err)
	}
	return o.inSavepoint(ctx, sp, func(q *gen.Queries) error {
		return o.queueRunFinished(ctx, q, r, *r.TriggerRef, nil)
	})
}

// chainedRunFinished reports a follow-up's run group under the chain's root
// schedule, when the chain was started by a schedule that still exists. A
// chain started by hand, by a webhook or by the system notifies nothing,
// as its first run does not. The row is found by the run's parent group
// (its trigger_ref) and template, never by child_group_id, which the
// starter may not have committed yet when a quick child run finishes.
func (o *Outbox) chainedRunFinished(ctx context.Context, r jobs.RunGroupResult) error {
	if r.TriggerRef == nil || r.TemplateID == nil {
		return nil
	}
	sp, err := o.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin notification savepoint: %w", err)
	}
	return o.inSavepoint(ctx, sp, func(q *gen.Queries) error {
		row, err := q.GetFollowUpOfChainedRun(ctx, gen.GetFollowUpOfChainedRunParams{OrgID: r.OrgID, ParentGroupID: *r.TriggerRef, TemplateID: *r.TemplateID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read follow-up of run group: %w", err)
		}
		if row.RootTrigger != jobs.TriggerSchedule || !row.RootRef.Valid || len(row.Path) == 0 {
			return nil
		}
		return o.queueRunFinished(ctx, q, r, uuid.UUID(row.RootRef.Bytes), &followUpRef{template: row.TemplateID, parent: row.Path[len(row.Path)-1]})
	})
}

// followUpRef names the templates of a chained run.
type followUpRef struct{ template, parent uuid.UUID }

// queueRunFinished queues the run.finished event of r under scheduleID's
// rules; fu is set for a chained run.
func (o *Outbox) queueRunFinished(ctx context.Context, q *gen.Queries, r jobs.RunGroupResult, scheduleID uuid.UUID, fu *followUpRef) error {
	result := "succeeded"
	switch r.Status {
	case jobs.StatusFailed, jobs.StatusError:
		result = "failed"
	case jobs.StatusCancelled:
		result = "cancelled"
	}
	return o.enqueueSchedule(ctx, q, r.OrgID, scheduleID, "run:"+r.GroupID.String(), runCategories(result, r.HostsChanged), fu,
		func(sc eventContext, cats []Category) Event {
			run := &RunSummary{
				ID: r.FirstJobID, Result: result, Hosts: r.Hosts, HostsOK: r.HostsOK(), HostsChanged: r.HostsChanged,
				HostsFailed: r.HostsFailed, HostsNoResult: r.HostsNoResult, HostsExcluded: r.HostsExcluded,
				StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
			}
			if r.StartedAt != nil && r.FinishedAt != nil {
				run.DurationSeconds = int64(r.FinishedAt.Sub(*r.StartedAt).Round(time.Second) / time.Second)
			}
			at := o.now()
			if r.FinishedAt != nil {
				at = *r.FinishedAt
			}
			return Event{
				Kind: EventRunFinished, At: at, Org: OrgRef{ID: r.OrgID, Slug: sc.OrgSlug, Name: sc.OrgName}, Categories: cats,
				Schedule: &NamedRef{ID: scheduleID, Name: sc.ScheduleName}, Template: &NamedRef{ID: sc.TemplateID, Name: sc.TemplateName},
				FollowUpOf: sc.followUpOf(fu),
				Run:        run, URL: o.publicURL + "/o/" + sc.OrgSlug + "/runs/" + r.FirstJobID.String(),
			}
		})
}

// EnqueueDecision reports a decision that started no run (skipped,
// missed, failed) inside tx, the ticker's row savepoint. A triggered
// decision is reported when its run group finishes, so it enqueues
// nothing here.
func (o *Outbox) EnqueueDecision(ctx context.Context, tx pgx.Tx, d Decision) error {
	cat, kind, ok := decisionCategory(d.Outcome)
	if !ok {
		return nil
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin notification savepoint: %w", err)
	}
	return o.inSavepoint(ctx, sp, func(q *gen.Queries) error {
		return o.enqueueSchedule(ctx, q, d.OrgID, d.ScheduleID, "decision:"+d.RunID.String(), []Category{cat}, nil,
			func(sc eventContext, cats []Category) Event {
				return Event{
					Kind: kind, At: d.DecidedAt, Org: OrgRef{ID: d.OrgID, Slug: sc.OrgSlug, Name: sc.OrgName}, Categories: cats,
					Schedule: &NamedRef{ID: d.ScheduleID, Name: sc.ScheduleName}, Template: &NamedRef{ID: sc.TemplateID, Name: sc.TemplateName},
					Decision: &DecisionSummary{Outcome: d.Outcome, Reason: d.Reason, DueAt: d.DueAt},
					URL:      o.publicURL + "/o/" + sc.OrgSlug + "/schedules?id=" + d.ScheduleID.String(),
				}
			})
	})
}

// FollowUpDecision reports a follow-up of a scheduled run that was decided
// without starting a run, inside tx (the chain starter's row savepoint):
// skipped or failure under the chain's root schedule's rules. Any other
// category queues nothing.
func (o *Outbox) FollowUpDecision(ctx context.Context, tx pgx.Tx, d FollowUpDecision) error {
	var kind EventKind
	var outcome string
	switch d.Category {
	case CategorySkipped:
		kind, outcome = EventScheduleSkipped, "follow_up_skipped"
	case CategoryFailure:
		kind, outcome = EventScheduleFailed, "follow_up_failed"
	default:
		return nil
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin notification savepoint: %w", err)
	}
	return o.inSavepoint(ctx, sp, func(q *gen.Queries) error {
		return o.enqueueSchedule(ctx, q, d.OrgID, d.ScheduleID, "chain:"+d.RowID.String(), []Category{d.Category},
			&followUpRef{template: d.TemplateID, parent: d.ParentTemplateID},
			func(sc eventContext, cats []Category) Event {
				return Event{
					Kind: kind, At: d.DecidedAt, Org: OrgRef{ID: d.OrgID, Slug: sc.OrgSlug, Name: sc.OrgName}, Categories: cats,
					Schedule: &NamedRef{ID: d.ScheduleID, Name: sc.ScheduleName}, Template: &NamedRef{ID: sc.TemplateID, Name: sc.TemplateName},
					FollowUpOf: &NamedRef{ID: d.ParentTemplateID, Name: sc.ParentTemplateName},
					Decision:   &DecisionSummary{Outcome: outcome, Reason: d.Reason, DueAt: d.DecidedAt},
					URL:        o.publicURL + "/o/" + sc.OrgSlug + "/schedules?id=" + d.ScheduleID.String(),
				}
			})
	})
}

// inSavepoint runs fn on sp and releases it, or rolls it back on error.
func (o *Outbox) inSavepoint(ctx context.Context, sp pgx.Tx, fn func(q *gen.Queries) error) error {
	if err := fn(gen.New(sp)); err != nil {
		_ = sp.Rollback(context.WithoutCancel(ctx))
		return err
	}
	if err := sp.Commit(ctx); err != nil {
		return fmt.Errorf("release notification savepoint: %w", err)
	}
	return nil
}

// enqueueSchedule queues ev for every enabled channel the schedule's
// rules route any of cats to. A schedule deleted meanwhile (its rules go
// with it) enqueues nothing. fu is set for a chain's follow-up: the names
// then come from the follow-up and parent templates, not the schedule's own.
func (o *Outbox) enqueueSchedule(ctx context.Context, q *gen.Queries, orgID, scheduleID uuid.UUID, key string, cats []Category, fu *followUpRef,
	build func(sc eventContext, cats []Category) Event) error {
	names := make([]string, len(cats))
	for i, c := range cats {
		names[i] = string(c)
	}
	channels, err := q.NotifyRuleChannels(ctx, gen.NotifyRuleChannelsParams{OrgID: orgID, ScheduleID: scheduleID, Categories: names})
	if err != nil {
		return fmt.Errorf("read notification rules: %w", err)
	}
	if len(channels) == 0 {
		return nil
	}
	sc, err := o.loadContext(ctx, q, orgID, scheduleID, fu)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read notification context: %w", err)
	}
	return o.insert(ctx, q, orgID, key, build(sc, cats), channels)
}

// loadContext reads the names an event carries; pgx.ErrNoRows when the
// schedule is gone.
func (o *Outbox) loadContext(ctx context.Context, q *gen.Queries, orgID, scheduleID uuid.UUID, fu *followUpRef) (eventContext, error) {
	if fu == nil {
		sc, err := q.NotifyScheduleContext(ctx, gen.NotifyScheduleContextParams{OrgID: orgID, ScheduleID: scheduleID})
		return eventContext{OrgSlug: sc.OrgSlug, OrgName: sc.OrgName, ScheduleName: sc.ScheduleName, TemplateID: sc.TemplateID, TemplateName: sc.TemplateName}, err
	}
	sc, err := q.NotifyFollowUpContext(ctx, gen.NotifyFollowUpContextParams{OrgID: orgID, ScheduleID: scheduleID, TemplateID: fu.template, ParentTemplateID: fu.parent})
	return eventContext{OrgSlug: sc.OrgSlug, OrgName: sc.OrgName, ScheduleName: sc.ScheduleName, TemplateID: fu.template, TemplateName: sc.TemplateName, ParentTemplateName: sc.ParentTemplateName}, err
}

// followUpOf is the event's FollowUpOf for fu: nil for a plain run.
func (c eventContext) followUpOf(fu *followUpRef) *NamedRef {
	if fu == nil {
		return nil
	}
	return &NamedRef{ID: fu.parent, Name: c.ParentTemplateName}
}

// EnqueueDigest queues a weekly digest for the organization's enabled
// digest channels inside tx (the digest ticker's row savepoint).
func (o *Outbox) EnqueueDigest(ctx context.Context, tx pgx.Tx, d DueDigest, sum DigestSummary) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin notification savepoint: %w", err)
	}
	return o.inSavepoint(ctx, sp, func(q *gen.Queries) error {
		channels, err := q.DigestChannelsForSend(ctx, d.OrgID)
		if err != nil {
			return fmt.Errorf("read digest channels: %w", err)
		}
		if len(channels) == 0 {
			return nil
		}
		org, err := q.NotifyOrgContext(ctx, d.OrgID)
		if err != nil {
			return fmt.Errorf("read organization: %w", err)
		}
		base := o.publicURL + "/o/" + org.Slug
		for i := range sum.Slowest {
			sum.Slowest[i].URL = base + "/runs/" + sum.Slowest[i].RunID.String()
		}
		ev := Event{Kind: EventDigest, At: d.Occurrence, Org: OrgRef{ID: d.OrgID, Slug: org.Slug, Name: org.Name}, Digest: &sum, URL: base + "/schedules"}
		return o.insert(ctx, q, d.OrgID, d.Key(), ev, channels)
	})
}

// Enqueue is the exported door onto insert, for callers outside this
// package that have already decided who should be told. It adds no policy
// of its own: resolution, grouping and deduplication of events belong to
// the caller (internal/alertroutes), and delivery, retries and the log
// stay here. Like EnqueueDecision and EnqueueDigest, it runs in its own
// savepoint nested inside tx, so a notification failure rolls back only
// the notification, never the caller's own transaction.
func (o *Outbox) Enqueue(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, key string, ev Event, channels []uuid.UUID) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin notification savepoint: %w", err)
	}
	return o.inSavepoint(ctx, sp, func(q *gen.Queries) error {
		return o.insert(ctx, q, orgID, key, ev, channels)
	})
}

// insert queues one delivery per channel; the unique event key makes a
// repeated insert a no-op.
func (o *Outbox) insert(ctx context.Context, q *gen.Queries, orgID uuid.UUID, key string, ev Event, channels []uuid.UUID) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("encode notification event: %w", err)
	}
	now := o.now()
	for _, ch := range channels {
		id, err := uuid.NewV7FromReader(o.rand)
		if err != nil {
			return fmt.Errorf("delivery id: %w", err)
		}
		if _, err := q.InsertNotificationDelivery(ctx, gen.InsertNotificationDeliveryParams{
			ID: id, OrgID: orgID, ChannelID: ch, EventKey: key, EventKind: string(ev.Kind), Payload: payload,
			Status: string(DeliveryPending), NextAttemptAt: now, CreatedAt: now,
		}); err != nil {
			return fmt.Errorf("queue notification delivery: %w", err)
		}
	}
	return nil
}
